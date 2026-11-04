// English: GoalJob defines the durable background wakeup and lease boundary for autonomous Goal work.
// 中文：GoalJob 定义自主 Goal 后台唤醒与租约边界。
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/goal"
	paierrors "personal-ai-os/pkg/errors"
)

var ErrGoalJobNotFound = errors.New("goal job not found")
var ErrGoalJobLeaseLost = errors.New("goal job lease lost")
var ErrGoalJobNotRunnable = errors.New("goal job is not runnable")

type JobStatus string

const (
	JobPending         JobStatus = "pending"
	JobRunning         JobStatus = "running"
	JobWaitingApproval JobStatus = "waiting_approval"
	JobWaitingExternal JobStatus = "waiting_external"
	JobRetryWait       JobStatus = "retry_wait"
	JobCompleted       JobStatus = "completed"
	JobFailed          JobStatus = "failed"
	JobCancelled       JobStatus = "cancelled"
	JobPaused          JobStatus = "paused"
)

type GoalJob struct {
	ID            string     `json:"job_id"`
	GoalID        string     `json:"goal_id"`
	Status        JobStatus  `json:"status"`
	CurrentStage  string     `json:"current_stage"`
	LeaseOwner    string     `json:"lease_owner,omitempty"`
	LeaseExpires  time.Time  `json:"lease_expires_at,omitempty"`
	HeartbeatAt   time.Time  `json:"heartbeat_at,omitempty"`
	FencingToken  int64      `json:"fencing_token"`
	NextRunAt     time.Time  `json:"next_run_at,omitempty"`
	LastRunAt     time.Time  `json:"last_run_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	RetryCount    int        `json:"retry_count"`
	RunCount      int        `json:"run_count"`
	MaxRuns       int        `json:"max_runs"`
	WakeRequested bool       `json:"wake_requested"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

type GoalJobLease struct {
	JobID        string
	GoalID       string
	WorkerID     string
	FencingToken int64
	ExpiresAt    time.Time
}

type GoalJobRepository interface {
	CreateOrWake(context.Context, string, int) (GoalJob, error)
	Get(context.Context, string, string) (GoalJob, error)
	ListByGoal(context.Context, string, string, int) ([]GoalJob, error)
	Claim(context.Context, string, time.Duration) (GoalJob, GoalJobLease, error)
	Heartbeat(context.Context, GoalJobLease, time.Duration) (GoalJobLease, error)
	Finish(context.Context, GoalJobLease, JobStatus, string, time.Time, bool, int) (GoalJob, error)
	Pause(context.Context, string, string) (GoalJob, error)
	Resume(context.Context, string, string) (GoalJob, error)
	Cancel(context.Context, string, string) (GoalJob, error)
}

func validJobStatus(status JobStatus) bool {
	switch status {
	case JobPending, JobRunning, JobWaitingApproval, JobWaitingExternal, JobRetryWait, JobCompleted, JobFailed, JobCancelled, JobPaused:
		return true
	default:
		return false
	}
}

func (j GoalJob) Validate() error {
	if j.ID == "" || j.GoalID == "" || !validJobStatus(j.Status) || j.FencingToken < 0 || j.RetryCount < 0 || j.RunCount < 0 || j.MaxRuns < 1 {
		return errors.New("goal job record is invalid")
	}
	if j.Status == JobRunning && (strings.TrimSpace(j.LeaseOwner) == "" || j.LeaseExpires.IsZero() || j.HeartbeatAt.IsZero()) {
		return errors.New("running goal job requires a live lease owner and timestamps")
	}
	return nil
}

// JobWorker runs exactly one bounded Supervisor turn per claim. It never directly invokes an Agent or Tool.
// 中文：JobWorker 每次认领只运行一个有界 Supervisor turn，不直接调用 Agent 或 Tool。
type JobWorker struct {
	Jobs           GoalJobRepository
	Goals          goal.Repository
	Supervisor     *Supervisor
	WorkerID       string
	Concurrency    int
	LeaseDuration  time.Duration
	HeartbeatEvery time.Duration
	PollInterval   time.Duration
	RetryBase      time.Duration
	MaxRetries     int
	MaxRuns        int
	Logger         interface{ Error(string, ...any) }
}

func (w *JobWorker) validate() error {
	if w.Jobs == nil || w.Goals == nil || w.Supervisor == nil || strings.TrimSpace(w.WorkerID) == "" || w.Concurrency < 1 || w.LeaseDuration < time.Second || w.HeartbeatEvery <= 0 || w.HeartbeatEvery*2 >= w.LeaseDuration || w.PollInterval <= 0 || w.RetryBase <= 0 || w.MaxRetries < 0 || w.MaxRuns < 1 {
		return errors.New("goal job worker configuration is invalid")
	}
	return nil
}

// Run starts a bounded number of claim loops and returns after context cancellation.
// 中文：Run 启动有界认领循环，并在 Context 取消后等待所有循环退出。
func (w *JobWorker) Run(ctx context.Context) error {
	if err := w.validate(); err != nil {
		return err
	}
	var group sync.WaitGroup
	for i := 0; i < w.Concurrency; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			w.runLoop(ctx, index)
		}(i)
	}
	<-ctx.Done()
	group.Wait()
	return nil
}

func (w *JobWorker) runLoop(ctx context.Context, index int) {
	workerID := fmt.Sprintf("%s-%d", w.WorkerID, index)
	ticker := time.NewTicker(w.PollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := w.RunOne(ctx, workerID); err != nil && !errors.Is(err, execution.ErrNoExecution) && !errors.Is(err, context.Canceled) {
			if w.Logger != nil {
				w.Logger.Error("Goal Job cycle failed", "worker_id", workerID, "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *JobWorker) RunOne(ctx context.Context, workerID string) error {
	if err := w.validate(); err != nil {
		return err
	}
	job, lease, err := w.Jobs.Claim(ctx, workerID, w.LeaseDuration)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		w.heartbeatLoop(runCtx, cancel, lease)
	}()
	goalItem, goalErr := w.Goals.Get(runCtx, goalOwnerID(), job.GoalID)
	if goalErr != nil {
		cancel()
		<-heartbeatDone
		if errors.Is(goalErr, paierrors.ErrNotFound) {
			_, finishErr := w.Jobs.Finish(context.WithoutCancel(ctx), lease, JobCancelled, "Goal no longer exists", time.Time{}, false, w.MaxRetries)
			return finishErr
		}
		_, finishErr := w.Jobs.Finish(context.WithoutCancel(ctx), lease, JobRetryWait, goalErr.Error(), time.Now().UTC().Add(w.retryDelay(job.RetryCount)), true, w.MaxRetries)
		return finishErr
	}
	if goalItem.Status == goal.Completed || goalItem.Status == goal.Archived {
		cancel()
		<-heartbeatDone
		_, err := w.Jobs.Finish(context.WithoutCancel(ctx), lease, JobCompleted, "Goal is already terminal", time.Time{}, false, w.MaxRetries)
		return err
	}
	if goalItem.Status == goal.Cancelled {
		cancel()
		<-heartbeatDone
		_, err := w.Jobs.Finish(context.WithoutCancel(ctx), lease, JobCancelled, "Goal was cancelled", time.Time{}, false, w.MaxRetries)
		return err
	}
	if goalItem.Status == goal.Paused || goalItem.Status == goal.Blocked {
		cancel()
		<-heartbeatDone
		_, err := w.Jobs.Finish(context.WithoutCancel(ctx), lease, JobWaitingExternal, "Goal is paused or blocked", time.Time{}, false, w.MaxRetries)
		return err
	}
	result, turnErr := w.Supervisor.Turn(runCtx, TurnRequest{GoalID: job.GoalID})
	cancel()
	<-heartbeatDone
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(turnErr, ErrUnverifiedResult) {
		_, finishErr := w.Jobs.Finish(context.WithoutCancel(ctx), lease, JobFailed, turnErr.Error(), time.Time{}, false, w.MaxRetries)
		if finishErr != nil {
			return fmt.Errorf("persist unverified Goal Job failure: %w", finishErr)
		}
		return turnErr
	}
	if turnErr != nil {
		retry := !errors.Is(turnErr, paierrors.ErrNotFound) && !errors.Is(turnErr, ErrNoReadyTasks)
		if !retry {
			_, finishErr := w.Jobs.Finish(context.WithoutCancel(ctx), lease, JobFailed, turnErr.Error(), time.Time{}, false, w.MaxRetries)
			return finishErr
		}
		delay := time.Duration(0)
		if retry {
			delay = w.retryDelay(job.RetryCount)
		}
		_, finishErr := w.Jobs.Finish(context.WithoutCancel(ctx), lease, JobRetryWait, turnErr.Error(), time.Now().UTC().Add(delay), retry, w.MaxRetries)
		if finishErr != nil {
			return fmt.Errorf("persist Goal Job retry: %w", finishErr)
		}
		return turnErr
	}
	status, nextRun := jobStatusForTurn(result), time.Time{}
	if status == JobPending {
		nextRun = time.Now().UTC().Add(w.PollInterval)
	}
	_, err = w.Jobs.Finish(context.WithoutCancel(ctx), lease, status, "", nextRun, false, w.MaxRetries)
	if err != nil {
		return fmt.Errorf("persist Goal Job result: %w", err)
	}
	return nil
}

func jobStatusForTurn(result TurnResult) JobStatus {
	switch result.Status {
	case string(goal.Completed), string(execution.StatusSucceeded):
		return JobCompleted
	case string(goal.Cancelled):
		return JobCancelled
	case string(execution.StatusWaitingApproval):
		return JobWaitingApproval
	case string(execution.StatusWaitingExternal), string(execution.StatusUnknown):
		return JobWaitingExternal
	case "budget_exhausted", "replan_budget_exhausted", "stopped":
		return JobFailed
	case "no_progress":
		return JobFailed
	case "awaiting_verification":
		return JobWaitingExternal
	default:
		if result.Goal.Status == goal.Completed {
			return JobCompleted
		}
		if result.Goal.Status == goal.Cancelled {
			return JobCancelled
		}
		return JobPending
	}
}

func (w *JobWorker) heartbeatLoop(ctx context.Context, cancel context.CancelFunc, initial GoalJobLease) {
	lease := initial
	ticker := time.NewTicker(w.HeartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			updated, err := w.Jobs.Heartbeat(ctx, lease, w.LeaseDuration)
			if err != nil {
				cancel()
				return
			}
			lease = updated
		}
	}
}

func (w *JobWorker) retryDelay(retryCount int) time.Duration {
	delay := w.RetryBase
	for i := 0; i < retryCount && delay < time.Minute; i++ {
		delay *= 2
	}
	if delay > time.Minute {
		delay = time.Minute
	}
	return delay
}
