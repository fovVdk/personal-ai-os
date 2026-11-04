// Package execution owns bounded retry primitives and durable execution worker contracts.
// 中文：execution 包定义有界重试，以及持久化执行运行时和 Worker 契约。
package execution

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrNoExecution      = errors.New("no queued execution available")
	ErrLeaseLost        = errors.New("execution lease lost or fencing token is stale")
	ErrIdempotencyReuse = errors.New("idempotency key reused with different input")
	ErrUncertainOutcome = errors.New("execution outcome is uncertain and requires reconciliation")
)

type RunStatus string

const (
	StatusPending         RunStatus = "pending"
	StatusQueued          RunStatus = "queued"
	StatusClaimed         RunStatus = "claimed"
	StatusRunning         RunStatus = "running"
	StatusWaitingApproval RunStatus = "waiting_approval"
	StatusWaitingExternal RunStatus = "waiting_external"
	StatusRetryWait       RunStatus = "retry_wait"
	StatusSucceeded       RunStatus = "succeeded"
	StatusFailed          RunStatus = "failed"
	StatusCancelled       RunStatus = "cancelled"
	StatusUnknown         RunStatus = "unknown"
)

type FailureClass string

const (
	FailureRetryable      FailureClass = "retryable"
	FailurePermanent      FailureClass = "permanent"
	FailureUncertain      FailureClass = "uncertain"
	FailureCancelled      FailureClass = "cancelled"
	FailureApprovalNeeded FailureClass = "approval_required"
	FailureRecovered      FailureClass = "recovered"
	FailureSafeRetry      FailureClass = "safe_retry"
	FailureSafeResume     FailureClass = "safe_resume"
)

type Run struct {
	ID                  string
	TaskID              string
	GoalID              string
	AgentID             string
	WorkerID            string
	IdempotencyScope    string
	IdempotencyKey      string
	InputHash           string
	Payload             []byte
	Result              []byte
	Status              RunStatus
	Attempt             int
	MaxAttempts         int
	FencingToken        int64
	LeaseUntil          time.Time
	HeartbeatAt         time.Time
	AvailableAt         time.Time
	RetrySafe           bool
	Idempotent          bool
	CancellationRequest bool
	FailureClass        FailureClass
	FailureReason       string
	StateReason         string
	ErrorClass          string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type Checkpoint struct {
	ExecutionID    string    `json:"execution_id"`
	StepID         string    `json:"step_id"`
	Sequence       int64     `json:"sequence"`
	Progress       []byte    `json:"progress"`
	CompletedSteps []string  `json:"completed_steps"`
	StateReference string    `json:"state_reference,omitempty"`
	SchemaVersion  int       `json:"schema_version"`
	FencingToken   int64     `json:"fencing_token"`
	CreatedAt      time.Time `json:"created_at"`
}

type EnqueueRequest struct {
	TaskID           string
	AgentID          string
	IdempotencyScope string
	IdempotencyKey   string
	InputHash        string
	Payload          []byte
	MaxAttempts      int
	RetrySafe        bool
	Idempotent       bool
}

type Lease struct {
	ExecutionID string
	WorkerID    string
	Fencing     int64
	LeaseUntil  time.Time
}

type Repository interface {
	Enqueue(context.Context, EnqueueRequest) (Run, bool, error)
	Claim(context.Context, string, time.Duration) (Run, error)
	Start(context.Context, Lease) error
	Heartbeat(context.Context, Lease, time.Duration) (Lease, error)
	SaveCheckpoint(context.Context, Lease, Checkpoint) (Checkpoint, error)
	LatestCheckpoint(context.Context, string) (Checkpoint, error)
	ClaimStep(context.Context, Lease, string, string, string) (bool, error)
	Complete(context.Context, Lease, []byte, []Evidence, bool) error
	Fail(context.Context, Lease, FailureClass, string, time.Duration) error
	RequestCancel(context.Context, string) error
	CancelClaimed(context.Context, Lease, string) error
	RecoverExpired(context.Context, int, time.Duration) (int, error)
	Get(context.Context, string) (Run, error)
	VerifiedTaskIDs(context.Context, string) ([]string, error)
}

type Evidence struct {
	Source      string
	Description string
	Reference   string
	ObservedAt  time.Time
}

type Executor interface {
	Execute(context.Context, Run, *Checkpoint, Progress) (ExecutionResult, error)
}

type Progress interface {
	SaveCheckpoint(context.Context, Checkpoint) error
	ClaimStep(context.Context, string, string, string) (bool, error)
}

type ExecutionResult struct {
	Result         []byte
	Evidence       []Evidence
	VerifiedResult bool
}

type Failure struct {
	Class FailureClass
	Err   error
}

func (f Failure) Error() string {
	if f.Err == nil {
		return string(f.Class)
	}
	return f.Err.Error()
}
func (f Failure) Unwrap() error { return f.Err }

func ClassifyError(err error) FailureClass {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrIdempotencyReuse) {
		return FailurePermanent
	}
	var classified Failure
	if errors.As(err, &classified) && classified.Class != "" {
		return classified.Class
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return FailureUncertain
	}
	return FailureUncertain
}

type WorkerConfig struct {
	WorkerID          string
	Concurrency       int
	LeaseDuration     time.Duration
	ExecutionTimeout  time.Duration
	HeartbeatInterval time.Duration
	PollInterval      time.Duration
	RetryBaseDelay    time.Duration
	RecoveryInterval  time.Duration
	RecoveryBatch     int
	ShutdownGrace     time.Duration
}

type WorkerPool struct {
	repository Repository
	executor   Executor
	config     WorkerConfig
	logger     Logger
	stopping   atomic.Bool
	active     atomic.Int64
	startOnce  sync.Once
	stopOnce   sync.Once
	cancel     context.CancelFunc
	workers    sync.WaitGroup
}

type Logger interface {
	InfoContext(context.Context, string, ...any)
	WarnContext(context.Context, string, ...any)
	ErrorContext(context.Context, string, ...any)
}

type WorkerSnapshot struct {
	WorkerID string `json:"worker_id"`
	Active   int64  `json:"active"`
	Capacity int    `json:"capacity"`
	Stopping bool   `json:"stopping"`
}

func NewWorkerPool(repository Repository, executor Executor, config WorkerConfig, logger Logger) (*WorkerPool, error) {
	if repository == nil || executor == nil {
		return nil, errors.New("execution worker repository and executor are required")
	}
	if strings.TrimSpace(config.WorkerID) == "" {
		return nil, errors.New("execution worker id is required")
	}
	if config.Concurrency < 1 || config.Concurrency > 128 || config.LeaseDuration < time.Second || config.ExecutionTimeout <= 0 || config.HeartbeatInterval <= 0 || config.HeartbeatInterval*2 >= config.LeaseDuration || config.PollInterval <= 0 || config.RetryBaseDelay < 0 || config.RecoveryInterval <= 0 || config.RecoveryBatch < 1 || config.ShutdownGrace <= 0 {
		return nil, errors.New("execution worker configuration is invalid")
	}
	return &WorkerPool{repository: repository, executor: executor, config: config, logger: logger}, nil
}

func (p *WorkerPool) Snapshot() WorkerSnapshot {
	return WorkerSnapshot{WorkerID: p.config.WorkerID, Active: p.active.Load(), Capacity: p.config.Concurrency, Stopping: p.stopping.Load()}
}

func (p *WorkerPool) Run(ctx context.Context) error {
	p.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		p.cancel = cancel
		for i := 0; i < p.config.Concurrency; i++ {
			workerID := fmt.Sprintf("%s-%02d", p.config.WorkerID, i+1)
			p.workers.Add(1)
			go p.workerLoop(runCtx, workerID)
		}
		p.workers.Add(1)
		go p.recoveryLoop(runCtx)
	})
	if p.cancel == nil {
		return errors.New("execution worker pool already stopped")
	}
	if ctx.Err() != nil {
		p.Stop()
		return nil
	}
	<-ctx.Done()
	p.Stop()
	return nil
}

func (p *WorkerPool) Stop() {
	p.stopOnce.Do(func() {
		p.stopping.Store(true)
		if p.cancel != nil {
			p.cancel()
		}
		done := make(chan struct{})
		go func() {
			p.workers.Wait()
			close(done)
		}()
		timer := time.NewTimer(p.config.ShutdownGrace)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			if p.logger != nil {
				p.logger.WarnContext(context.Background(), "execution worker shutdown grace expired", "worker_id", p.config.WorkerID, "active", p.active.Load())
			}
		}
	})
}

func (p *WorkerPool) workerLoop(ctx context.Context, workerID string) {
	defer p.workers.Done()
	poll := time.NewTicker(p.config.PollInterval)
	defer poll.Stop()
	for {
		if ctx.Err() != nil || p.stopping.Load() {
			return
		}
		claimCtx, cancel := context.WithTimeout(ctx, p.config.PollInterval)
		run, err := p.repository.Claim(claimCtx, workerID, p.config.LeaseDuration)
		cancel()
		if err == nil {
			p.active.Add(1)
			func() {
				defer p.active.Add(-1)
				defer func() {
					if recovered := recover(); recovered != nil && p.logger != nil {
						p.logger.ErrorContext(context.Background(), "execution worker recovered from executor panic", "execution_id", run.ID, "panic", fmt.Sprint(recovered))
					}
				}()
				p.execute(ctx, workerID, run)
			}()
			continue
		}
		if !errors.Is(err, ErrNoExecution) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && p.logger != nil {
			p.logger.ErrorContext(ctx, "claim execution failed", "worker_id", workerID, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
	}
}

func (p *WorkerPool) execute(poolCtx context.Context, workerID string, run Run) {
	lease := Lease{ExecutionID: run.ID, WorkerID: workerID, Fencing: run.FencingToken, LeaseUntil: run.LeaseUntil}
	var leaseMu sync.RWMutex
	leaseSnapshot := func() Lease {
		leaseMu.RLock()
		defer leaseMu.RUnlock()
		return lease
	}
	runCtx, cancel := context.WithTimeout(poolCtx, p.config.ExecutionTimeout)
	defer cancel()
	if err := p.repository.Start(runCtx, lease); err != nil {
		if p.logger != nil {
			p.logger.WarnContext(runCtx, "execution start rejected", "execution_id", run.ID, "error", err)
		}
		return
	}
	var checkpoint *Checkpoint
	loadedCheckpoint, err := p.repository.LatestCheckpoint(runCtx, run.ID)
	if errors.Is(err, ErrNoExecution) {
		checkpoint = nil
	} else if err != nil {
		if p.logger != nil {
			p.logger.ErrorContext(runCtx, "load execution checkpoint failed", "execution_id", run.ID, "error", err)
		}
		return
	} else {
		checkpoint = &loadedCheckpoint
	}
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(p.config.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				updated, err := p.repository.Heartbeat(runCtx, leaseSnapshot(), p.config.LeaseDuration)
				if err != nil {
					cancel()
					if p.logger != nil {
						p.logger.WarnContext(runCtx, "execution lease heartbeat failed", "execution_id", run.ID, "fencing_token", leaseSnapshot().Fencing, "error", err)
					}
					return
				}
				leaseMu.Lock()
				lease = updated
				leaseMu.Unlock()
			}
		}
	}()
	result, executeErr := p.executor.Execute(runCtx, run, checkpoint, workerProgress{
		lease:      leaseSnapshot,
		repository: p.repository,
	})
	cancel()
	<-heartbeatDone
	if executeErr == nil && !result.VerifiedResult {
		executeErr = Failure{Class: FailurePermanent, Err: errors.New("execution result is not independently verified")}
	}
	if executeErr != nil {
		class := ClassifyError(executeErr)
		backoff := time.Duration(0)
		if class == FailureRetryable {
			backoff = retryDelay(p.config.RetryBaseDelay, run.Attempt)
		}
		failureCtx, failureCancel := context.WithTimeout(context.Background(), p.config.HeartbeatInterval)
		defer failureCancel()
		if err := p.repository.Fail(failureCtx, leaseSnapshot(), class, executeErr.Error(), backoff); err != nil && p.logger != nil {
			p.logger.WarnContext(context.Background(), "execution failure update rejected", "execution_id", run.ID, "fencing_token", leaseSnapshot().Fencing, "error", err)
		}
		return
	}
	completeCtx, completeCancel := context.WithTimeout(context.Background(), p.config.HeartbeatInterval)
	defer completeCancel()
	if err := p.repository.Complete(completeCtx, leaseSnapshot(), result.Result, result.Evidence, result.VerifiedResult); err != nil && p.logger != nil {
		p.logger.WarnContext(completeCtx, "execution completion rejected", "execution_id", run.ID, "fencing_token", leaseSnapshot().Fencing, "error", err)
	}
}

type workerProgress struct {
	lease      func() Lease
	repository Repository
}

func (progress workerProgress) SaveCheckpoint(ctx context.Context, checkpoint Checkpoint) error {
	lease := progress.lease()
	checkpoint.FencingToken = lease.Fencing
	_, err := progress.repository.SaveCheckpoint(ctx, lease, checkpoint)
	return err
}

func (progress workerProgress) ClaimStep(ctx context.Context, stepID, key, inputHash string) (bool, error) {
	return progress.repository.ClaimStep(ctx, progress.lease(), stepID, key, inputHash)
}

func (p *WorkerPool) recoveryLoop(ctx context.Context) {
	defer p.workers.Done()
	ticker := time.NewTicker(p.config.RecoveryInterval)
	defer ticker.Stop()
	for {
		if _, err := p.repository.RecoverExpired(ctx, p.config.RecoveryBatch, p.config.RetryBaseDelay); err != nil && ctx.Err() == nil && p.logger != nil {
			p.logger.ErrorContext(ctx, "execution recovery scan failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func retryDelay(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 20 {
		shift = 20
	}
	delay := float64(base) * math.Pow(2, float64(shift))
	if delay > float64(time.Hour) {
		return time.Hour
	}
	return time.Duration(delay)
}
