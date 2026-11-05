// English: This failure-injection acceptance kills a real Worker after an external effect but before its response is known.
// 中文：本故障注入验收在真实外部效果发生但响应未知时强杀 Worker。
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"personal-ai-os/internal/app/database"
	"personal-ai-os/internal/app/store"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/policy"
	"personal-ai-os/internal/task"
	"personal-ai-os/internal/tool/gateway"
	"personal-ai-os/internal/tool/registry"
	"personal-ai-os/migrations"
)

const unknownHelperEnv = "PAIOS_UNKNOWN_INVOCATION_HELPER"

func TestPostgresUnknownExternalEffectWorkerCrashAndCoreRestart(t *testing.T) {
	pool, repositories := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS test_external_effect_ledger (
		id bigserial PRIMARY KEY,test_id text NOT NULL,operation_id text NOT NULL,observed_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create test-only external effect ledger: %v", err)
	}
	testID := uniqueLabel("unknown-effect-ledger")
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM test_external_effect_ledger WHERE test_id=$1", testID) })
	goalItem, err := repositories.Create(ctx, goal.Goal{OwnerID: testOwnerID, Title: uniqueLabel("unknown-effect-crash"), Priority: 3, Status: goal.Active})
	if err != nil {
		t.Fatal(err)
	}
	taskItem, err := repositories.Tasks().Create(ctx, task.Task{GoalID: goalItem.ID, Title: uniqueLabel("unknown-external-effect"), Priority: 3, Status: task.Ready})
	if err != nil {
		t.Fatal(err)
	}
	goalIDs, taskIDs := []string{goalItem.ID}, []string{taskItem.ID}
	t.Cleanup(func() { cleanupIntegrationRows(t, context.Background(), pool, goalIDs, taskIDs, nil, nil, nil, nil) })

	var externalEffects atomic.Int32
	received := make(chan string, 1)
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			OperationID string `json:"operation_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.OperationID == "" {
			http.Error(w, "invalid operation", http.StatusBadRequest)
			return
		}
		if _, err := pool.Exec(r.Context(), "INSERT INTO test_external_effect_ledger(test_id,operation_id) VALUES($1,$2)", testID, request.OperationID); err != nil {
			http.Error(w, "external effect ledger unavailable", http.StatusInternalServerError)
			return
		}
		externalEffects.Add(1) // This is the external effect; it occurs before the response is sent.
		select {
		case received <- request.OperationID:
		default:
		}
		<-r.Context().Done() // Keep the response unresolved until the Worker process is killed.
	}))
	defer external.Close()

	durable := store.NewDurableExecutionStore(pool)
	enqueue := newExecutionEnqueueRequest(taskItem.ID, "unknown-effect-agent", "attempt-0", []byte(`{"operation_id":"once-only-42"}`), 2, false)
	run, replayed, err := durable.Enqueue(ctx, enqueue)
	if err != nil || replayed {
		t.Fatalf("enqueue non-idempotent external effect: run=%+v replay=%v err=%v", run, replayed, err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestUnknownInvocationWorkerProcessHelper$", "-test.v")
	child.Env = serviceEnvironment(os.Environ(), map[string]string{
		unknownHelperEnv: "run", "PAIOS_UNKNOWN_DATABASE_URL": os.Getenv("PAIOS_TEST_DATABASE_URL"),
		"PAIOS_UNKNOWN_RUN_ID": run.ID, "PAIOS_UNKNOWN_EFFECT_URL": external.URL,
	})
	child.Stdout, child.Stderr = io.Discard, io.Discard
	if err := child.Start(); err != nil {
		t.Fatalf("start isolated durable Worker process: %v", err)
	}
	childAlive := true
	t.Cleanup(func() {
		if childAlive && child.Process != nil {
			_ = child.Process.Kill()
			_, _ = child.Process.Wait()
		}
	})
	select {
	case operationID := <-received:
		if operationID != "once-only-42" {
			t.Fatalf("external service received operation %q", operationID)
		}
	case <-ctx.Done():
		t.Fatalf("external side effect was not observed before Worker crash: %v", ctx.Err())
	}
	beforeKill, err := durable.Get(ctx, run.ID)
	if err != nil || beforeKill.Status != execution.StatusRunning || beforeKill.WorkerID == "" {
		t.Fatalf("Worker was not in-flight after external effect: run=%+v err=%v", beforeKill, err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatalf("kill Worker after effect and before response: %v", err)
	}
	childAlive = false
	if err := child.Wait(); err == nil {
		t.Fatal("SIGKILLed Worker unexpectedly exited successfully")
	}

	// Start a fresh Core process. Its durable recovery scanner must classify the
	// lost Tool/Execution outcome as unknown, never success/failure or replay.
	// 中文：启动全新的 Core 进程；持久恢复扫描器必须将丢失响应的 Tool/Execution 判为 unknown，不能伪装成成功、失败或重放。
	core := startCoreService(t, ctx, os.Getenv("PAIOS_TEST_DATABASE_URL"))
	defer core.stop(t)
	var recovered execution.Run
	for {
		recovered, err = durable.Get(ctx, run.ID)
		if err != nil {
			t.Fatalf("load recovered Execution: %v", err)
		}
		if recovered.Status == execution.StatusUnknown {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("fresh Core did not recover expired execution to unknown: %+v", recovered)
		case <-time.After(25 * time.Millisecond):
		}
	}
	var invocationStatus, invocationReason string
	if err := pool.QueryRow(ctx, `SELECT status,failure_reason FROM tool_invocations WHERE execution_id=$1`, run.ID).Scan(&invocationStatus, &invocationReason); err != nil {
		t.Fatalf("load recovered Tool Invocation: %v", err)
	}
	if invocationStatus != "unknown" || invocationReason == "" || recovered.FailureClass != execution.FailureUncertain {
		t.Fatalf("uncertain external effect was reclassified: invocation=%s reason=%q run=%+v", invocationStatus, invocationReason, recovered)
	}
	var taskStatus string
	if err := pool.QueryRow(ctx, "SELECT status FROM tasks WHERE id=$1", taskItem.ID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != string(task.WaitingExternal) {
		t.Fatalf("unknown result Task status=%q, want waiting_external", taskStatus)
	}
	if got := externalEffects.Load(); got != 1 {
		t.Fatalf("non-idempotent external effect count=%d, want exactly one and no automatic replay", got)
	}
	var durableEffectCount int
	var durableOperationID string
	if err := pool.QueryRow(ctx, "SELECT count(*),min(operation_id) FROM test_external_effect_ledger WHERE test_id=$1", testID).Scan(&durableEffectCount, &durableOperationID); err != nil || durableEffectCount != 1 || durableOperationID != "once-only-42" {
		t.Fatalf("external effect was not durably recorded exactly once: count=%d operation=%q err=%v", durableEffectCount, durableOperationID, err)
	}
	// Verify the fresh Core leaves uncertainty durable and does not issue another request.
	// 中文：验证新 Core 持久保留不确定状态，且没有再次发起外部请求。
	active, found, err := durable.ActiveForTask(ctx, taskItem.ID)
	if err != nil || !found || active.Status != execution.StatusUnknown {
		t.Fatalf("unknown Execution was not retained for reconciliation: run=%+v found=%v err=%v", active, found, err)
	}
	var historyCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM execution_state_history WHERE execution_run_id=$1 AND to_status='unknown'`, run.ID).Scan(&historyCount); err != nil || historyCount != 1 {
		t.Fatalf("unknown outcome history count=%d err=%v", historyCount, err)
	}
	if got := externalEffects.Load(); got != 1 {
		t.Fatalf("Core restart replayed uncertain external effect; count=%d", got)
	}
	core.stop(t)
}

func TestUnknownInvocationWorkerProcessHelper(t *testing.T) {
	if os.Getenv(unknownHelperEnv) != "run" {
		t.Skip("subprocess helper is launched by TestPostgresUnknownExternalEffectWorkerCrashAndCoreRestart")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, os.Getenv("PAIOS_UNKNOWN_DATABASE_URL"))
	if err != nil {
		t.Fatalf("open Worker subprocess database: %v", err)
	}
	defer pool.Close()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repositories := store.NewPostgres(pool)
	durable := store.NewDurableExecutionStore(pool)
	tools := registry.New()
	definition := registry.Definition{
		Identity: registry.Identity{ProviderID: "test.external", ToolID: "effect.once", Version: "v1"},
		Name: "effect.once", Description: "Call the integration side-effect ledger exactly once",
		Source: registry.SourceBuiltin, Kind: registry.KindExternal, RiskLevel: 2,
		Idempotency: registry.IdempotencyNone, Timeout: 20 * time.Second,
		MaxInputBytes: 4096, MaxOutputBytes: 4096, ExecutionEnvironment: "local",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"operation_id":{"type":"string"}},"required":["operation_id"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"receipt":{"type":"string"}},"required":["receipt"],"additionalProperties":false}`),
	}
	if err := tools.Register(definition, externalEffectHandler{url: os.Getenv("PAIOS_UNKNOWN_EFFECT_URL")}); err != nil {
		t.Fatal(err)
	}
	grants := toolgateway.StaticGrants{"test.external/effect.once@v1": {Allowed: true, Autonomy: policy.A2}}
	toolGateway := &toolgateway.Gateway{Registry: tools, Authorizer: toolgateway.PolicyAuthorizer{Grants: grants}, Leases: repositories, Store: repositories}
	worker, err := execution.NewWorkerPool(durable, unknownInvocationExecutor{gateway: toolGateway}, execution.WorkerConfig{
		WorkerID: uniqueLabel("unknown-crash-child"), Concurrency: 1, LeaseDuration: time.Second,
		ExecutionTimeout: 25 * time.Second, HeartbeatInterval: 200 * time.Millisecond,
		PollInterval: 10 * time.Millisecond, RetryBaseDelay: 10 * time.Millisecond,
		RecoveryInterval: time.Hour, RecoveryBatch: 10, ShutdownGrace: time.Second,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("run crash-test Worker: %v", err)
	}
}

type externalEffectHandler struct{ url string }

func (h externalEffectHandler) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, strings.NewReader(string(args)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("external side-effect service returned HTTP %d: %s", response.StatusCode, body)
	}
	return body, nil
}

type unknownInvocationExecutor struct{ gateway *toolgateway.Gateway }

func (e unknownInvocationExecutor) Execute(ctx context.Context, run execution.Run, _ *execution.Checkpoint, _ execution.Progress) (execution.ExecutionResult, error) {
	args := json.RawMessage(run.Payload)
	result, err := e.gateway.Execute(ctx, toolgateway.Request{
		GoalID: run.GoalID, TaskID: run.TaskID, ExecutionID: run.ID, StepID: "effect-once", AgentID: run.AgentID,
		Identity: registry.Identity{ProviderID: "test.external", ToolID: "effect.once", Version: "v1"},
		IdempotencyKey: "one-effect-only", Lease: execution.Lease{ExecutionID: run.ID, WorkerID: run.WorkerID, Fencing: run.FencingToken, LeaseUntil: run.LeaseUntil},
		Arguments: args,
	})
	if err != nil {
		return execution.ExecutionResult{}, err
	}
	return execution.ExecutionResult{Result: []byte(`{"status":"unexpected_response"}`), VerifiedResult: false}, fmt.Errorf("side-effect service unexpectedly returned: %s", result.Status)
}

var _ execution.Executor = unknownInvocationExecutor{}
var _ registry.Handler = externalEffectHandler{}
