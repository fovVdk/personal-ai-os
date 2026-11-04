// English: These tests exercise Worker Pool task claiming, checkpoint callbacks, cancellation, and graceful shutdown without database coupling.
// 中文：本测试在不依赖数据库的情况下验证 Worker Pool 认领、Checkpoint 回调、取消和优雅关闭。
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestWorkerPoolClaimsRunsAndPersistsCheckpointAndResult(t *testing.T) {
	repository := newFakeRuntimeRepository(Run{ID: "run-success", TaskID: "task-success", GoalID: "goal-success", AgentID: "agent", Status: StatusQueued, Attempt: 1, MaxAttempts: 2, RetrySafe: true})
	executor := checkpointExecutor{}
	pool := newTestPool(t, repository, executor, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()
	waitUntil(t, time.Second, func() bool { return repository.status("run-success") == StatusSucceeded })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker pool returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker pool did not shut down")
	}
	repository.mu.Lock()
	claims, checkpointCount, completions := repository.claims, repository.checkpointCount, repository.completions
	taskStatus := repository.tasks["task-success"]
	repository.mu.Unlock()
	if claims != 1 || checkpointCount != 1 || completions != 1 {
		t.Fatalf("claims=%d checkpoints=%d completions=%d", claims, checkpointCount, completions)
	}
	if taskStatus != "completed" {
		t.Fatalf("task status=%s", taskStatus)
	}
}

func TestWorkerPoolCancelsInFlightWorkAndPersistsUnknown(t *testing.T) {
	repository := newFakeRuntimeRepository(Run{ID: "run-cancel", TaskID: "task-cancel", GoalID: "goal", AgentID: "agent", Status: StatusQueued, Attempt: 1, MaxAttempts: 3, RetrySafe: false})
	executor := blockingExecutor{started: make(chan struct{})}
	pool := newTestPool(t, repository, executor, 1)
	poolCtx, cancelPool := context.WithCancel(context.Background())
	poolDone := make(chan error, 1)
	go func() { poolDone <- pool.Run(poolCtx) }()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("executor did not start")
	}
	cancelPool()
	select {
	case err := <-poolDone:
		if err != nil {
			t.Fatalf("worker pool shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker pool did not cancel in-flight work")
	}
	if repository.status("run-cancel") != StatusUnknown {
		t.Fatalf("cancelled non-retry-safe run status=%s want unknown", repository.status("run-cancel"))
	}
	if repository.status("run-cancel") == StatusRetryWait {
		t.Fatal("uncertain external work was automatically retried")
	}
}

func TestWorkerPoolCancelsInflightWorkAtExecutionTimeout(t *testing.T) {
	repository := newFakeRuntimeRepository(Run{ID: "run-timeout", TaskID: "task-timeout", GoalID: "goal", AgentID: "agent", Status: StatusQueued, Attempt: 1, MaxAttempts: 3, RetrySafe: false})
	executor := blockingExecutor{started: make(chan struct{})}
	config := testWorkerConfig(1)
	config.ExecutionTimeout = 30 * time.Millisecond
	pool, err := NewWorkerPool(repository, executor, config, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("timed executor did not start")
	}
	waitUntil(t, time.Second, func() bool { return repository.status("run-timeout") == StatusUnknown })
	cancel()
	<-done
}

func newTestPool(t *testing.T, repository *fakeRuntimeRepository, executor Executor, concurrency int) *WorkerPool {
	t.Helper()
	pool, err := NewWorkerPool(repository, executor, testWorkerConfig(concurrency), nil)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func testWorkerConfig(concurrency int) WorkerConfig {
	return WorkerConfig{WorkerID: "test-worker", Concurrency: concurrency, LeaseDuration: time.Second, ExecutionTimeout: 2 * time.Second, HeartbeatInterval: 100 * time.Millisecond, PollInterval: 5 * time.Millisecond, RetryBaseDelay: 10 * time.Millisecond, RecoveryInterval: time.Hour, RecoveryBatch: 10, ShutdownGrace: time.Second}
}

func waitUntil(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true before timeout")
}

type fakeRuntimeRepository struct {
	mu              sync.Mutex
	runs            map[string]Run
	checkpointRows  map[string]Checkpoint
	tasks           map[string]string
	claims          int
	checkpointCount int
	completions     int
}

func newFakeRuntimeRepository(runs ...Run) *fakeRuntimeRepository {
	repository := &fakeRuntimeRepository{runs: make(map[string]Run), checkpointRows: make(map[string]Checkpoint), tasks: make(map[string]string)}
	for _, run := range runs {
		repository.runs[run.ID] = run
		repository.tasks[run.TaskID] = "ready"
	}
	return repository
}

func (r *fakeRuntimeRepository) Enqueue(_ context.Context, request EnqueueRequest) (Run, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := Run{ID: request.IdempotencyKey, TaskID: request.TaskID, AgentID: request.AgentID, Status: StatusQueued, Attempt: 1, MaxAttempts: request.MaxAttempts, RetrySafe: request.RetrySafe, Payload: request.Payload}
	r.runs[run.ID] = run
	r.tasks[run.TaskID] = "ready"
	return run, false, nil
}

func (r *fakeRuntimeRepository) Claim(ctx context.Context, worker string, _ time.Duration) (Run, error) {
	if err := ctx.Err(); err != nil {
		return Run{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, run := range r.runs {
		if run.Status != StatusQueued && run.Status != StatusRetryWait {
			continue
		}
		run.Status, run.WorkerID, run.FencingToken = StatusClaimed, worker, run.FencingToken+1
		run.LeaseUntil = time.Now().Add(time.Second)
		r.runs[id] = run
		r.tasks[run.TaskID] = "running"
		r.claims++
		return run, nil
	}
	return Run{}, ErrNoExecution
}

func (r *fakeRuntimeRepository) Start(_ context.Context, lease Lease) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, err := r.fenced(lease)
	if err != nil {
		return err
	}
	run.Status = StatusRunning
	r.runs[run.ID] = run
	return nil
}

func (r *fakeRuntimeRepository) Heartbeat(_ context.Context, lease Lease, duration time.Duration) (Lease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, err := r.fenced(lease)
	if err != nil {
		return Lease{}, err
	}
	run.LeaseUntil = time.Now().Add(duration)
	r.runs[run.ID] = run
	lease.LeaseUntil = run.LeaseUntil
	return lease, nil
}

func (r *fakeRuntimeRepository) SaveCheckpoint(_ context.Context, lease Lease, checkpoint Checkpoint) (Checkpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.fenced(lease); err != nil {
		return Checkpoint{}, err
	}
	checkpoint.Sequence = int64(r.checkpointRows[lease.ExecutionID].Sequence + 1)
	checkpoint.ExecutionID = lease.ExecutionID
	checkpoint.FencingToken = lease.Fencing
	r.checkpointRows[lease.ExecutionID] = checkpoint
	r.checkpointCount++
	return checkpoint, nil
}

func (r *fakeRuntimeRepository) LatestCheckpoint(_ context.Context, id string) (Checkpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	checkpoint, ok := r.checkpointRows[id]
	if !ok {
		return Checkpoint{}, ErrNoExecution
	}
	return checkpoint, nil
}

func (r *fakeRuntimeRepository) ClaimStep(context.Context, Lease, string, string, string) (bool, error) {
	return true, nil
}

func (r *fakeRuntimeRepository) Complete(_ context.Context, lease Lease, result []byte, evidence []Evidence, verified bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, err := r.fenced(lease)
	if err != nil {
		return err
	}
	if len(evidence) == 0 || !json.Valid(result) || !verified {
		return errors.New("result/evidence required")
	}
	run.Status, run.Result = StatusSucceeded, result
	r.runs[run.ID] = run
	r.tasks[run.TaskID] = "completed"
	r.completions++
	return nil
}

func (r *fakeRuntimeRepository) Fail(_ context.Context, lease Lease, class FailureClass, reason string, delay time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, err := r.fenced(lease)
	if err != nil {
		return err
	}
	run.FailureClass, run.FailureReason = class, reason
	if class == FailureUncertain {
		run.Status = StatusUnknown
	} else if class == FailureRetryable && run.RetrySafe && run.Attempt < run.MaxAttempts {
		run.Status = StatusRetryWait
		run.AvailableAt = time.Now().Add(delay)
	} else {
		run.Status = StatusFailed
	}
	r.runs[run.ID] = run
	return nil
}

func (r *fakeRuntimeRepository) RequestCancel(context.Context, string) error        { return nil }
func (r *fakeRuntimeRepository) CancelTask(context.Context, string) (bool, error)   { return false, nil }
func (r *fakeRuntimeRepository) CancelClaimed(context.Context, Lease, string) error { return nil }
func (r *fakeRuntimeRepository) RecoverExpired(context.Context, int, time.Duration) (int, error) {
	return 0, nil
}
func (r *fakeRuntimeRepository) Get(_ context.Context, id string) (Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	if !ok {
		return Run{}, ErrNoExecution
	}
	return run, nil
}
func (*fakeRuntimeRepository) VerifiedTaskIDs(context.Context, string) ([]string, error) {
	return nil, nil
}
func (r *fakeRuntimeRepository) status(id string) RunStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[id].Status
}
func (r *fakeRuntimeRepository) fenced(lease Lease) (Run, error) {
	run, ok := r.runs[lease.ExecutionID]
	if !ok || run.WorkerID != lease.WorkerID || run.FencingToken != lease.Fencing || run.LeaseUntil.Before(time.Now()) {
		return Run{}, ErrLeaseLost
	}
	return run, nil
}

type checkpointExecutor struct{}

func (checkpointExecutor) Execute(ctx context.Context, run Run, checkpoint *Checkpoint, progress Progress) (ExecutionResult, error) {
	if err := progress.SaveCheckpoint(ctx, Checkpoint{StepID: "prepared", Progress: []byte(`{"ready":true}`), CompletedSteps: []string{"prepared"}, SchemaVersion: 1}); err != nil {
		return ExecutionResult{}, err
	}
	result, err := json.Marshal(map[string]string{"task_id": run.TaskID})
	if err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{Result: result, Evidence: []Evidence{{Source: "unit-test", Description: "worker result", ObservedAt: time.Now().UTC()}}, VerifiedResult: true}, nil
}

type blockingExecutor struct{ started chan struct{} }

func (e blockingExecutor) Execute(ctx context.Context, _ Run, _ *Checkpoint, _ Progress) (ExecutionResult, error) {
	close(e.started)
	<-ctx.Done()
	return ExecutionResult{}, ctx.Err()
}
