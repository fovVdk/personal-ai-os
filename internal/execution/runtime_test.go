// English: These tests cover durable Worker Pool configuration, cancellation classification, retry bounds, and shutdown.
// 中文：本测试覆盖持久化 Worker Pool 配置、取消结果分类、重试上限和优雅关闭。
package execution

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassifyErrorTreatsCancellationAsUncertain(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if got := ClassifyError(err); got != FailureUncertain {
			t.Errorf("ClassifyError(%v)=%q want uncertain", err, got)
		}
	}
	if got := ClassifyError(Failure{Class: FailurePermanent, Err: errors.New("bad input")}); got != FailurePermanent {
		t.Fatalf("classified failure=%q want permanent", got)
	}
	if got := ClassifyError(errors.New("transient database error")); got != FailureUncertain {
		t.Fatalf("generic failure=%q want uncertain by default", got)
	}
}

func TestClassifyErrorKeepsExplicitRetryClassification(t *testing.T) {
	if got := ClassifyError(Failure{Class: FailureRetryable, Err: errors.New("temporary transport failure")}); got != FailureRetryable {
		t.Fatalf("explicit retry classification=%q", got)
	}
	if got := ClassifyError(ErrIdempotencyReuse); got != FailurePermanent {
		t.Fatalf("idempotency conflict classification=%q", got)
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	if got := retryDelay(time.Second, 1); got != time.Second {
		t.Fatalf("first retry delay=%s", got)
	}
	if got := retryDelay(time.Second, 4); got != 8*time.Second {
		t.Fatalf("fourth attempt delay=%s", got)
	}
	if got := retryDelay(time.Second, 100); got != time.Hour {
		t.Fatalf("bounded retry delay=%s want one hour", got)
	}
	if got := retryDelay(0, 3); got != 0 {
		t.Fatalf("disabled retry delay=%s", got)
	}
}

func TestNewWorkerPoolRejectsUnsafeLeaseAndConcurrency(t *testing.T) {
	base := WorkerConfig{WorkerID: "test", Concurrency: 1, LeaseDuration: 10 * time.Second, ExecutionTimeout: 5 * time.Second, HeartbeatInterval: time.Second, PollInterval: time.Second, RecoveryInterval: time.Second, RecoveryBatch: 10, ShutdownGrace: time.Second}
	for name, mutate := range map[string]func(*WorkerConfig){
		"missing identity":          func(cfg *WorkerConfig) { cfg.WorkerID = " " },
		"excessive concurrency":     func(cfg *WorkerConfig) { cfg.Concurrency = 129 },
		"heartbeat outlives lease":  func(cfg *WorkerConfig) { cfg.HeartbeatInterval = 5 * time.Second },
		"missing execution timeout": func(cfg *WorkerConfig) { cfg.ExecutionTimeout = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if _, err := NewWorkerPool(&countingIdleRepository{}, idleExecutor{}, cfg, nil); err == nil {
				t.Fatal("expected invalid worker configuration error")
			}
		})
	}
}

func TestWorkerPoolStopsClaimsOnContextCancellation(t *testing.T) {
	config := WorkerConfig{WorkerID: "shutdown-test", Concurrency: 3, LeaseDuration: 2 * time.Second, ExecutionTimeout: time.Second, HeartbeatInterval: 100 * time.Millisecond, PollInterval: time.Millisecond, RecoveryInterval: time.Millisecond, RecoveryBatch: 10, ShutdownGrace: time.Second}
	repository := &countingIdleRepository{}
	pool, err := NewWorkerPool(repository, idleExecutor{}, config, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for repository.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if repository.calls.Load() == 0 {
		t.Fatal("worker pool never attempted to claim work")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker pool shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker pool did not stop after cancellation")
	}
	if snapshot := pool.Snapshot(); !snapshot.Stopping {
		t.Fatalf("worker snapshot does not report stopping: %+v", snapshot)
	}
	stoppedCalls := repository.calls.Load()
	time.Sleep(20 * time.Millisecond)
	if calls := repository.calls.Load(); calls != stoppedCalls {
		t.Fatalf("worker pool kept claiming after stop: before=%d after=%d", stoppedCalls, calls)
	}
}

type countingIdleRepository struct{ calls atomic.Int64 }

func (*countingIdleRepository) Enqueue(context.Context, EnqueueRequest) (Run, bool, error) {
	return Run{}, false, nil
}
func (r *countingIdleRepository) Claim(ctx context.Context, _ string, _ time.Duration) (Run, error) {
	r.calls.Add(1)
	<-ctx.Done()
	return Run{}, ctx.Err()
}
func (*countingIdleRepository) Start(context.Context, Lease) error { return nil }
func (*countingIdleRepository) Heartbeat(context.Context, Lease, time.Duration) (Lease, error) {
	return Lease{}, nil
}
func (*countingIdleRepository) SaveCheckpoint(context.Context, Lease, Checkpoint) (Checkpoint, error) {
	return Checkpoint{}, nil
}
func (*countingIdleRepository) LatestCheckpoint(context.Context, string) (Checkpoint, error) {
	return Checkpoint{}, ErrNoExecution
}
func (*countingIdleRepository) ClaimStep(context.Context, Lease, string, string, string) (bool, error) {
	return true, nil
}
func (*countingIdleRepository) Complete(context.Context, Lease, []byte, []Evidence, bool) error {
	return nil
}
func (*countingIdleRepository) Fail(context.Context, Lease, FailureClass, string, time.Duration) error {
	return nil
}
func (*countingIdleRepository) RequestCancel(context.Context, string) error        { return nil }
func (*countingIdleRepository) CancelTask(context.Context, string) (bool, error)   { return false, nil }
func (*countingIdleRepository) CancelClaimed(context.Context, Lease, string) error { return nil }
func (*countingIdleRepository) RecoverExpired(context.Context, int, time.Duration) (int, error) {
	return 0, nil
}
func (*countingIdleRepository) Get(context.Context, string) (Run, error) {
	return Run{}, ErrNoExecution
}
func (*countingIdleRepository) VerifiedTaskIDs(context.Context, string) ([]string, error) {
	return nil, nil
}

type idleExecutor struct{}

func (idleExecutor) Execute(context.Context, Run, *Checkpoint, Progress) (ExecutionResult, error) {
	return ExecutionResult{}, errors.New("idle executor must never be called")
}
