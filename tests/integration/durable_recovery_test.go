// English: This acceptance test kills a real Worker process after a persisted checkpoint, proves fenced takeover, executes a five-Task dependency chain, and verifies state after Core restart.
// 中文：本验收测试在 Checkpoint 持久化后强杀真实 Worker 进程，验证 fencing 接管、五个依赖任务续跑及 Core 重启后的持久状态。
package integration

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/app/database"
	"personal-ai-os/internal/app/store"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/supervisor"
	"personal-ai-os/internal/task"
	"personal-ai-os/migrations"
)

func TestDurableWorkerCrashAndCheckpointRecoveryAcceptance(t *testing.T) {
	pool, repositories := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations for durable worker acceptance: %v", err)
	}

	goalItem, err := repositories.Create(ctx, goal.Goal{OwnerID: testOwnerID, Title: uniqueLabel("worker-crash-acceptance-goal"), Description: "five dependent tasks", Priority: 4, Status: goal.Active})
	if err != nil {
		t.Fatalf("create acceptance Goal: %v", err)
	}
	goalIDs := []string{goalItem.ID}
	taskIDs := make([]string, 0, 5)
	runIDs := make([]string, 0, 5)
	expectedRunStatuses := make(map[string]execution.RunStatus)
	t.Cleanup(func() {
		allRunIDs := append([]string(nil), runIDs...)
		cleanupIntegrationRows(t, context.Background(), pool, goalIDs, taskIDs, nil, nil, nil, allRunIDs)
	})

	taskSpecs := make([]task.Task, 5)
	for i := range taskSpecs {
		key := fmt.Sprintf("phase-step-%d", i+1)
		taskSpecs[i] = task.Task{GoalID: goalItem.ID, PlanKey: key, Title: fmt.Sprintf("Acceptance task %d", i+1), Description: "ordered durable recovery task", Priority: 3, Status: task.Created, SuccessCriteria: []string{fmt.Sprintf("task %d has verified evidence", i+1)}}
		if i > 0 {
			taskSpecs[i].DependsOn = []string{fmt.Sprintf("phase-step-%d", i)}
		}
	}
	createdTasks, err := repositories.CreateTasks(ctx, taskSpecs)
	if err != nil {
		t.Fatalf("create five-task dependency DAG: %v", err)
	}
	if len(createdTasks) != 5 {
		t.Fatalf("created tasks=%d, want five", len(createdTasks))
	}
	for _, item := range createdTasks {
		taskIDs = append(taskIDs, item.ID)
	}

	tasks := repositories.Tasks()
	rootTask, err := tasks.Transition(ctx, createdTasks[0].ID, task.Ready)
	if err != nil {
		t.Fatalf("make root Task ready: %v", err)
	}
	rootRequest := newExecutionEnqueueRequest(rootTask.ID, "crash-checkpoint-worker", "attempt-0", []byte(`{"acceptance":"crash-after-checkpoint"}`), 3, true)
	rootRun, replayed, err := store.NewDurableExecutionStore(pool).Enqueue(ctx, rootRequest)
	if err != nil || replayed {
		t.Fatalf("enqueue root execution=%+v replay=%v err=%v", rootRun, replayed, err)
	}
	runIDs = append(runIDs, rootRun.ID)
	expectedRunStatuses[rootRun.ID] = execution.StatusSucceeded

	child, childLines := startCrashWorkerProcess(t, ctx, rootRun.ID)
	childAlive := true
	t.Cleanup(func() {
		if childAlive && child.Process != nil {
			_ = child.Process.Kill()
			_, _ = child.Process.Wait()
		}
	})
	waitForChildMarker(t, ctx, childLines, "WORKER_CHECKPOINT_READY "+rootRun.ID)

	// A second independent Worker Pool is active while the first process owns the unexpired lease.
	durable := store.NewDurableExecutionStore(pool)
	claimSignals := make(chan string, 128)
	observedRepository := &claimObservingRepository{Repository: durable, claims: claimSignals}
	resumed := make(chan checkpointObservation, 8)
	workerPool, err := execution.NewWorkerPool(observedRepository, &acceptanceCheckpointExecutor{rootRunID: rootRun.ID, resumed: resumed}, recoveryWorkerConfig("acceptance-worker-b", 2), nil)
	if err != nil {
		t.Fatalf("create replacement Worker Pool: %v", err)
	}
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() { workerDone <- workerPool.Run(workerCtx) }()
	replacementClaimObserved := false
	claimDeadline := time.NewTimer(2 * time.Second)
	for !replacementClaimObserved {
		select {
		case workerID := <-claimSignals:
			if strings.HasPrefix(workerID, "acceptance-worker-b-") {
				replacementClaimObserved = true
			}
		case <-claimDeadline.C:
			t.Fatal("second Worker Pool did not poll/compete while the first Worker held its lease")
		case <-ctx.Done():
			t.Fatalf("waiting for second Worker claim attempt: %v", ctx.Err())
		}
	}
	claimDeadline.Stop()

	beforeKill, err := durable.Get(ctx, rootRun.ID)
	if err != nil || beforeKill.Status != execution.StatusRunning || beforeKill.WorkerID != "acceptance-worker-a-01" || beforeKill.FencingToken != 1 || !beforeKill.LeaseUntil.After(time.Now()) {
		t.Fatalf("first Worker did not hold one live claim before forced termination: run=%+v err=%v", beforeKill, err)
	}
	assertCount(t, ctx, pool, "SELECT count(*) FROM execution_state_history WHERE execution_run_id=$1 AND to_status='claimed'", rootRun.ID, 1)
	assertZero(t, ctx, pool, "SELECT count(*) FROM execution_evidence WHERE execution_run_id=$1", rootRun.ID)
	assertZero(t, ctx, pool, "SELECT count(*) FROM audit_records WHERE resource_id=$1 AND action='execution.succeeded'", rootRun.ID)

	if err := child.Process.Kill(); err != nil {
		t.Fatalf("force-kill first Worker process: %v", err)
	}
	childAlive = false
	if err := child.Wait(); err == nil {
		t.Fatal("force-killed Worker unexpectedly exited successfully")
	}
	var recoveredCheckpoint checkpointObservation
	select {
	case recoveredCheckpoint = <-resumed:
	case <-ctx.Done():
		t.Fatalf("replacement Worker did not resume from checkpoint: %v", ctx.Err())
	}
	if recoveredCheckpoint.RunID != rootRun.ID || recoveredCheckpoint.Sequence != 1 || !containsString(recoveredCheckpoint.Completed, "step-1") {
		t.Fatalf("replacement Worker did not receive the last valid checkpoint: %+v", recoveredCheckpoint)
	}
	rootResult := waitForRun(t, ctx, durable, rootRun.ID, execution.StatusSucceeded)
	if rootResult.FencingToken != 2 || rootResult.Attempt != 2 {
		t.Fatalf("reclaimed execution fencing/attempt=%d/%d, want 2/2", rootResult.FencingToken, rootResult.Attempt)
	}
	var takeoverAt time.Time
	if err := pool.QueryRow(ctx, "SELECT claimed_at FROM execution_runs WHERE id=$1", rootRun.ID).Scan(&takeoverAt); err != nil {
		t.Fatal(err)
	}
	if takeoverAt.Before(beforeKill.LeaseUntil) {
		t.Fatalf("second Worker reclaimed before the first lease expired: takeover=%s old_lease=%s", takeoverAt, beforeKill.LeaseUntil)
	}
	assertCount(t, ctx, pool, "SELECT count(*) FROM execution_state_history WHERE execution_run_id=$1 AND to_status='claimed'", rootRun.ID, 2)
	assertCount(t, ctx, pool, "SELECT count(*) FROM execution_step_idempotency WHERE execution_run_id=$1 AND step_id='step-1'", rootRun.ID, 1)
	assertCount(t, ctx, pool, "SELECT count(*) FROM execution_step_idempotency WHERE execution_run_id=$1 AND step_id='step-2'", rootRun.ID, 1)
	assertCount(t, ctx, pool, "SELECT count(*) FROM execution_evidence WHERE execution_run_id=$1", rootRun.ID, 1)
	assertCount(t, ctx, pool, "SELECT count(*) FROM audit_records WHERE resource_id=$1 AND action='execution.succeeded'", rootRun.ID, 1)
	assertCount(t, ctx, pool, "SELECT count(*) FROM audit_records WHERE resource_id=$1 AND action='task.completed'", rootTask.ID, 1)
	latestRootCheckpoint, err := durable.LatestCheckpoint(ctx, rootRun.ID)
	if err != nil || latestRootCheckpoint.Sequence != 2 || !containsString(latestRootCheckpoint.CompletedSteps, "step-1") || !containsString(latestRootCheckpoint.CompletedSteps, "step-2") {
		t.Fatalf("resumed execution did not persist ordered completion checkpoints: %+v err=%v", latestRootCheckpoint, err)
	}

	// Schedule each successor only after its predecessor completed with persisted Evidence.
	for i := 1; i < len(createdTasks)-1; i++ {
		previous, err := tasks.Get(ctx, createdTasks[i-1].ID)
		if err != nil || previous.Status != task.Completed {
			t.Fatalf("dependency predecessor %d is not complete: %+v err=%v", i, previous, err)
		}
		ready, err := tasks.Transition(ctx, createdTasks[i].ID, task.Ready)
		if err != nil {
			t.Fatalf("make dependency Task %d ready: %v", i+1, err)
		}
		request := newExecutionEnqueueRequest(ready.ID, "checkpoint-worker", "attempt-0", []byte(fmt.Sprintf(`{"task_id":%q,"sequence":%d}`, ready.ID, i+1)), 2, true)
		run, replay, err := durable.Enqueue(ctx, request)
		if err != nil || replay {
			t.Fatalf("enqueue dependency Task %d: run=%+v replay=%v err=%v", i+1, run, replay, err)
		}
		runIDs = append(runIDs, run.ID)
		expectedRunStatuses[run.ID] = execution.StatusSucceeded
		finished := waitForRun(t, ctx, durable, run.ID, execution.StatusSucceeded)
		if finished.FencingToken != 1 {
			t.Fatalf("successor Task %d fencing token=%d, want 1", i+1, finished.FencingToken)
		}
		assertCount(t, ctx, pool, "SELECT count(*) FROM execution_evidence WHERE execution_run_id=$1", run.ID, 1)
		assertCount(t, ctx, pool, "SELECT count(*) FROM audit_records WHERE resource_id=$1 AND action='execution.succeeded'", run.ID, 1)
	}
	stopWorkers()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatalf("replacement Worker Pool shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replacement Worker Pool did not stop")
	}

	for i, item := range createdTasks[:len(createdTasks)-1] {
		loaded, err := tasks.Get(ctx, item.ID)
		if err != nil || loaded.Status != task.Completed {
			t.Fatalf("task %d did not finish through the dependency chain: %+v err=%v", i+1, loaded, err)
		}
		if i > 0 && (len(loaded.DependsOn) != 1 || loaded.DependsOn[0] != createdTasks[i-1].ID) {
			t.Fatalf("task %d dependency=%v, predecessor=%s", i+1, loaded.DependsOn, createdTasks[i-1].ID)
		}
	}
	lastTask, err := tasks.Get(ctx, createdTasks[len(createdTasks)-1].ID)
	if err != nil || lastTask.Status != task.Created {
		t.Fatalf("last dependent Task should remain ready to resume after Core restart: %+v err=%v", lastTask, err)
	}
	if err := pool.QueryRow(ctx, "SELECT status FROM goals WHERE id=$1", goalItem.ID).Scan(&goalItem.Status); err != nil {
		t.Fatal(err)
	}
	if goalItem.Status == goal.Completed {
		t.Fatal("Goal was marked complete without a separate Goal-level evidence decision")
	}
	assertZero(t, ctx, pool, "SELECT count(*) FROM goal_evidence WHERE goal_id=$1", goalItem.ID)
	for _, id := range taskIDs[:len(taskIDs)-1] {
		var successAudit, taskAudit int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_records WHERE resource_id=$1 AND action='task.completed'", id).Scan(&taskAudit); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM execution_runs e JOIN tasks t ON t.id=e.task_id WHERE t.id=$1 AND e.status='succeeded'", id).Scan(&successAudit); err != nil {
			t.Fatal(err)
		}
		if taskAudit != 1 || successAudit != 1 {
			t.Errorf("task %s audit/succeeded records=%d/%d, want 1/1", id, taskAudit, successAudit)
		}
	}

	// Restart the actual Core with one dependency-ready Task. Its Mock Agent must safely refuse false success.
	core := startCoreService(t, ctx, os.Getenv("PAIOS_TEST_DATABASE_URL"))
	var coreTasks struct {
		Items []task.Task `json:"items"`
	}
	core.request(t, http.MethodGet, "/api/v1/tasks?goal_id="+goalItem.ID, nil, http.StatusOK, &coreTasks)
	if len(coreTasks.Items) != 5 {
		t.Fatalf("restarted Core returned %d tasks, want five", len(coreTasks.Items))
	}
	coreTaskByID := make(map[string]task.Task, len(coreTasks.Items))
	for _, item := range coreTasks.Items {
		coreTaskByID[item.ID] = item
	}
	for i, item := range createdTasks[:4] {
		if got := coreTaskByID[item.ID]; got.Status != task.Completed {
			t.Fatalf("restarted Core lost completed Task %d state: %+v", i+1, got)
		}
	}
	if got := coreTaskByID[createdTasks[4].ID]; got.Status != task.Created && got.Status != task.Ready {
		t.Fatalf("restarted Core lost or invalidated the pending dependent Task: %+v", got)
	}
	turnBody, _ := json.Marshal(map[string]string{"goal_id": goalItem.ID})
	turnRequest, err := http.NewRequest(http.MethodPost, core.baseURL+"/api/v1/supervisor/turn", strings.NewReader(string(turnBody)))
	if err != nil {
		t.Fatal(err)
	}
	turnRequest.Header.Set("Content-Type", "application/json")
	turnResponse, err := (&http.Client{Timeout: 45 * time.Second}).Do(turnRequest)
	if err != nil {
		t.Fatalf("restart Core Supervisor turn: %v", err)
	}
	turnResponseBody, readErr := io.ReadAll(turnResponse.Body)
	_ = turnResponse.Body.Close()
	if readErr != nil {
		t.Fatalf("read restarted Core Supervisor response: %v", readErr)
	}
	if turnResponse.StatusCode != http.StatusConflict {
		var taskStatus string
		_ = pool.QueryRow(ctx, "SELECT status FROM tasks WHERE id=$1", createdTasks[4].ID).Scan(&taskStatus)
		var runStatuses string
		_ = pool.QueryRow(ctx, "SELECT COALESCE(string_agg(status||':'||failure_reason, ';' ORDER BY started_at),'') FROM execution_runs WHERE task_id=$1", createdTasks[4].ID).Scan(&runStatuses)
		var plans, proposals string
		_ = pool.QueryRow(ctx, "SELECT COALESCE(string_agg(version::text||':'||status,','),'') FROM plan_versions WHERE goal_id=$1", goalItem.ID).Scan(&plans)
		_ = pool.QueryRow(ctx, "SELECT COALESCE(string_agg(status||':'||reason,','),'') FROM plan_proposals WHERE goal_id=$1", goalItem.ID).Scan(&proposals)
		t.Fatalf("Core Supervisor turn status=%d want=%d body=%s task=%s runs=%s plans=%s proposals=%s logs=%s", turnResponse.StatusCode, http.StatusConflict, turnResponseBody, taskStatus, runStatuses, plans, proposals, core.logs.String())
	}
	lastTask, err = tasks.Get(ctx, createdTasks[4].ID)
	if err != nil || lastTask.Status != task.Failed {
		t.Fatalf("Core restart did not resume the pending Task safely: %+v err=%v", lastTask, err)
	}
	var coreFailedRunID string
	if err := pool.QueryRow(ctx, "SELECT id::text FROM execution_runs WHERE task_id=$1 AND status='failed' ORDER BY started_at DESC LIMIT 1", lastTask.ID).Scan(&coreFailedRunID); err != nil {
		t.Fatalf("find Core restart execution attempt: %v", err)
	}
	runIDs = append(runIDs, coreFailedRunID)
	expectedRunStatuses[coreFailedRunID] = execution.StatusFailed
	assertZero(t, ctx, pool, "SELECT count(*) FROM execution_evidence WHERE execution_run_id=$1", coreFailedRunID)
	assertZero(t, ctx, pool, "SELECT count(*) FROM audit_records WHERE resource_id=$1 AND action='execution.succeeded'", coreFailedRunID)
	assertCount(t, ctx, pool, "SELECT count(*) FROM audit_records WHERE resource_id=$1 AND action='execution.failed'", coreFailedRunID, 1)
	core.stop(t)

	// The failed unverified attempt is explicitly retryable by a later trusted Worker after Core restart.
	lastTask, err = tasks.Transition(ctx, lastTask.ID, task.Retrying)
	if err != nil {
		t.Fatalf("schedule verified Worker retry after Core restart: %v", err)
	}
	finalRequest := newExecutionEnqueueRequest(lastTask.ID, "checkpoint-worker-after-core-restart", "attempt-1", []byte(fmt.Sprintf(`{"task_id":%q,"sequence":5,"verified_by":"integration-harness"}`, lastTask.ID)), 2, true)
	finalRun, replay, err := durable.Enqueue(ctx, finalRequest)
	if err != nil || replay {
		t.Fatalf("enqueue trusted final Task attempt: run=%+v replay=%v err=%v", finalRun, replay, err)
	}
	runIDs = append(runIDs, finalRun.ID)
	finalPool, err := execution.NewWorkerPool(durable, &acceptanceCheckpointExecutor{resumed: make(chan checkpointObservation, 1)}, recoveryWorkerConfig("acceptance-worker-after-core-restart", 1), nil)
	if err != nil {
		t.Fatalf("create post-Core-restart Worker Pool: %v", err)
	}
	finalCtx, stopFinalWorker := context.WithCancel(ctx)
	finalDone := make(chan error, 1)
	go func() { finalDone <- finalPool.Run(finalCtx) }()
	if finished := waitForRun(t, ctx, durable, finalRun.ID, execution.StatusSucceeded); finished.FencingToken < 1 {
		t.Fatalf("final dependency Task has invalid fencing token=%d", finished.FencingToken)
	}
	stopFinalWorker()
	select {
	case workerErr := <-finalDone:
		if workerErr != nil {
			t.Fatalf("post-Core-restart Worker Pool shutdown: %v", workerErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("post-Core-restart Worker Pool did not stop")
	}
	expectedRunStatuses[finalRun.ID] = execution.StatusSucceeded
	assertCount(t, ctx, pool, "SELECT count(*) FROM execution_evidence WHERE execution_run_id=$1", finalRun.ID, 1)
	assertCount(t, ctx, pool, "SELECT count(*) FROM audit_records WHERE resource_id=$1 AND action='execution.succeeded'", finalRun.ID, 1)

	for _, item := range createdTasks {
		loaded, err := tasks.Get(ctx, item.ID)
		if err != nil || loaded.Status != task.Completed {
			t.Fatalf("task %s did not finish through the dependency chain: %+v err=%v", item.ID, loaded, err)
		}
	}
	if err := pool.QueryRow(ctx, "SELECT status FROM goals WHERE id=$1", goalItem.ID).Scan(&goalItem.Status); err != nil {
		t.Fatal(err)
	}
	if goalItem.Status == goal.Completed {
		t.Fatal("Goal was marked complete without a separate Goal-level evidence decision")
	}
	assertZero(t, ctx, pool, "SELECT count(*) FROM goal_evidence WHERE goal_id=$1", goalItem.ID)

	// Restart Core again after all Tasks settle and verify every persisted execution via the HTTP API.
	core = startCoreService(t, ctx, os.Getenv("PAIOS_TEST_DATABASE_URL"))
	defer core.stop(t)
	core.request(t, http.MethodGet, "/api/v1/tasks?goal_id="+goalItem.ID, nil, http.StatusOK, &coreTasks)
	if len(coreTasks.Items) != 5 {
		t.Fatalf("final restarted Core returned %d tasks, want five", len(coreTasks.Items))
	}
	var coreGoal goal.Goal
	core.request(t, http.MethodGet, "/api/v1/goals/"+goalItem.ID, nil, http.StatusOK, &coreGoal)
	if coreGoal.Status == goal.Completed || len(coreGoal.Evidence) != 0 {
		t.Fatalf("restarted Core incorrectly reported Goal completion: %+v", coreGoal)
	}
	for _, runID := range runIDs {
		var durableRun execution.Run
		core.request(t, http.MethodGet, "/api/v1/executions/"+runID, nil, http.StatusOK, &durableRun)
		if durableRun.Status != expectedRunStatuses[runID] {
			t.Fatalf("restarted Core returned incorrect Execution state: got=%s want=%s run=%+v", durableRun.Status, expectedRunStatuses[runID], durableRun)
		}
	}
	core.stop(t)
}

func TestDurableWorkerProcessHelper(t *testing.T) {
	if os.Getenv("PAIOS_TEST_WORKER_HELPER") != "crash-after-checkpoint" {
		t.Skip("subprocess helper is launched by TestDurableWorkerCrashAndCheckpointRecoveryAcceptance")
	}
	pool, err := database.Open(context.Background(), os.Getenv("PAIOS_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("open subprocess Worker database: %v", err)
	}
	defer pool.Close()
	if err := migrations.Apply(context.Background(), pool); err != nil {
		t.Fatalf("apply subprocess Worker migrations: %v", err)
	}
	workerID := os.Getenv("PAIOS_TEST_WORKER_ID")
	runID := os.Getenv("PAIOS_TEST_WORKER_RUN_ID")
	workerPool, err := execution.NewWorkerPool(store.NewDurableExecutionStore(pool), processCheckpointExecutor{runID: runID}, recoveryWorkerConfig(workerID, 1), nil)
	if err != nil {
		t.Fatalf("create subprocess Worker Pool: %v", err)
	}
	if err := workerPool.Run(context.Background()); err != nil {
		t.Fatalf("run subprocess Worker Pool: %v", err)
	}
}

type claimObservingRepository struct {
	execution.Repository
	claims chan<- string
}

func (repository *claimObservingRepository) Claim(ctx context.Context, workerID string, lease time.Duration) (execution.Run, error) {
	select {
	case repository.claims <- workerID:
	default:
	}
	return repository.Repository.Claim(ctx, workerID, lease)
}

type checkpointObservation struct {
	RunID     string
	Sequence  int64
	Completed []string
}

type acceptanceCheckpointExecutor struct {
	rootRunID string
	resumed   chan<- checkpointObservation
}

func (executor *acceptanceCheckpointExecutor) Execute(ctx context.Context, run execution.Run, checkpoint *execution.Checkpoint, progress execution.Progress) (execution.ExecutionResult, error) {
	completed := make(map[string]bool)
	if checkpoint != nil {
		for _, step := range checkpoint.CompletedSteps {
			completed[step] = true
		}
	}
	if run.ID == executor.rootRunID {
		if checkpoint == nil || checkpoint.Sequence != 1 || !completed["step-1"] {
			return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: errors.New("takeover did not resume from the last completed step checkpoint")}
		}
		select {
		case executor.resumed <- checkpointObservation{RunID: run.ID, Sequence: checkpoint.Sequence, Completed: append([]string(nil), checkpoint.CompletedSteps...)}:
		default:
		}
	}
	for _, stepID := range []string{"step-1", "step-2"} {
		if completed[stepID] {
			continue
		}
		fingerprint := sha256.Sum256([]byte(run.TaskID + "/" + stepID))
		claimed, err := progress.ClaimStep(ctx, stepID, run.TaskID+":"+stepID, hex.EncodeToString(fingerprint[:]))
		if err != nil {
			return execution.ExecutionResult{}, err
		}
		if !claimed {
			return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: fmt.Errorf("step %s was claimed but has no completed checkpoint", stepID)}
		}
		completed[stepID] = true
		completedSteps := make([]string, 0, len(completed))
		for _, candidate := range []string{"step-1", "step-2"} {
			if completed[candidate] {
				completedSteps = append(completedSteps, candidate)
			}
		}
		progressBytes, _ := json.Marshal(map[string]string{"task_id": run.TaskID, "last_step": stepID})
		if err := progress.SaveCheckpoint(ctx, execution.Checkpoint{StepID: stepID, Progress: progressBytes, CompletedSteps: completedSteps, SchemaVersion: 1}); err != nil {
			return execution.ExecutionResult{}, err
		}
	}
	completedAt := time.Now().UTC()
	result, err := json.Marshal(supervisor.Execution{TaskID: run.TaskID, AgentID: run.AgentID, Summary: "independently verified integration artifact", Outcome: agentruntime.OutcomeSucceeded, Verified: true, VerificationMethod: "integration-harness-verifier/v1", Evidence: []supervisor.Evidence{{TaskID: run.TaskID, Source: "durable-recovery-acceptance", Description: "all declared steps and dependency checks completed", ObservedAt: completedAt}}, CompletedAt: completedAt})
	if err != nil {
		return execution.ExecutionResult{}, err
	}
	return execution.ExecutionResult{Result: result, Evidence: []execution.Evidence{{Source: "durable-recovery-acceptance", Description: "all declared steps and dependency checks completed", ObservedAt: completedAt}}, VerifiedResult: true}, nil
}

type processCheckpointExecutor struct{ runID string }

func (executor processCheckpointExecutor) Execute(ctx context.Context, run execution.Run, checkpoint *execution.Checkpoint, progress execution.Progress) (execution.ExecutionResult, error) {
	if run.ID != executor.runID || checkpoint != nil {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: errors.New("crash fixture expected a fresh root execution")}
	}
	fingerprint := sha256.Sum256([]byte(run.TaskID + "/step-1"))
	claimed, err := progress.ClaimStep(ctx, "step-1", run.TaskID+":step-1", hex.EncodeToString(fingerprint[:]))
	if err != nil || !claimed {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: fmt.Errorf("claim first step before crash: claimed=%v err=%w", claimed, err)}
	}
	progressBytes, _ := json.Marshal(map[string]string{"task_id": run.TaskID, "last_step": "step-1"})
	if err := progress.SaveCheckpoint(ctx, execution.Checkpoint{StepID: "step-1", Progress: progressBytes, CompletedSteps: []string{"step-1"}, SchemaVersion: 1}); err != nil {
		return execution.ExecutionResult{}, err
	}
	fmt.Fprintf(os.Stdout, "WORKER_CHECKPOINT_READY %s\n", run.ID)
	<-ctx.Done()
	return execution.ExecutionResult{}, ctx.Err()
}

func startCrashWorkerProcess(t *testing.T, ctx context.Context, runID string) (*exec.Cmd, <-chan string) {
	t.Helper()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDurableWorkerProcessHelper$", "-test.v")
	command.Env = serviceEnvironment(os.Environ(), map[string]string{
		"PAIOS_TEST_WORKER_HELPER": "crash-after-checkpoint", "PAIOS_TEST_WORKER_ID": "acceptance-worker-a",
		"PAIOS_TEST_WORKER_RUN_ID": runID, "PAIOS_TEST_DATABASE_URL": os.Getenv("PAIOS_TEST_DATABASE_URL"),
	})
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("open subprocess Worker stdout: %v", err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start subprocess Worker: %v", err)
	}
	lines := make(chan string, 128)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return command, lines
}

func waitForChildMarker(t *testing.T, ctx context.Context, lines <-chan string, expected string) {
	t.Helper()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("Worker subprocess exited before checkpoint marker %q", expected)
			}
			if strings.TrimSpace(line) == expected {
				return
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for Worker checkpoint marker %q: %v", expected, ctx.Err())
		}
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func recoveryWorkerConfig(workerID string, concurrency int) execution.WorkerConfig {
	return execution.WorkerConfig{WorkerID: workerID, Concurrency: concurrency, LeaseDuration: 3 * time.Second, ExecutionTimeout: 20 * time.Second,
		HeartbeatInterval: 500 * time.Millisecond, PollInterval: 10 * time.Millisecond, RetryBaseDelay: 20 * time.Millisecond,
		RecoveryInterval: 50 * time.Millisecond, RecoveryBatch: 32, ShutdownGrace: 2 * time.Second}
}
