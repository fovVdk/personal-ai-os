// English: This adapter implements persistent Goal Job creation, SKIP LOCKED claims, leases and fencing.
// 中文：本适配器实现持久 Goal Job、SKIP LOCKED 认领、Lease 和 Fencing。
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/supervisor"
	paierrors "personal-ai-os/pkg/errors"
)

type GoalJobStore struct{ pool *pgxpool.Pool }

func NewGoalJobStore(pool *pgxpool.Pool) *GoalJobStore { return &GoalJobStore{pool: pool} }

func (s *GoalJobStore) CreateOrWake(ctx context.Context, goalID string, maxRuns int) (supervisor.GoalJob, error) {
	if strings.TrimSpace(goalID) == "" {
		return supervisor.GoalJob{}, errors.New("goal_id is required")
	}
	if maxRuns < 1 || maxRuns > 10000 {
		maxRuns = 100
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return supervisor.GoalJob{}, fmt.Errorf("begin Goal Job create/wake: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var goalStatus string
	err = tx.QueryRow(ctx, "SELECT status FROM goals WHERE id=$1 AND owner_id=$2 FOR SHARE", goalID, LocalOwnerID).Scan(&goalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return supervisor.GoalJob{}, paierrors.ErrNotFound
	}
	if err != nil {
		return supervisor.GoalJob{}, fmt.Errorf("verify Goal for Job: %w", err)
	}
	if goalStatus == string(goal.Completed) || goalStatus == string(goal.Cancelled) || goalStatus == string(goal.Archived) {
		return supervisor.GoalJob{}, fmt.Errorf("Goal status %q cannot start a background Job", goalStatus)
	}
	job, err := scanGoalJob(tx.QueryRow(ctx, `INSERT INTO goal_jobs(goal_id,status,current_stage,next_run_at,max_runs)
		VALUES($1,'pending','evaluate',now(),$2)
		ON CONFLICT(goal_id) WHERE status IN ('pending','running','waiting_approval','waiting_external','retry_wait','paused')
		DO UPDATE SET status=CASE WHEN goal_jobs.status='running' THEN 'running' WHEN goal_jobs.status='paused' THEN 'paused' ELSE 'pending' END,
			wake_requested=CASE WHEN goal_jobs.status='running' THEN true ELSE goal_jobs.wake_requested END,
			next_run_at=CASE WHEN goal_jobs.status IN ('running','paused') THEN goal_jobs.next_run_at ELSE now() END,
			updated_at=now()
		RETURNING `+goalJobColumns, goalID, maxRuns))
	if err != nil {
		return supervisor.GoalJob{}, fmt.Errorf("create or wake Goal Job: %w", err)
	}
	if err := recordMutation(ctx, tx, LocalOwnerID, "goal_job.woken", "goal_jobs", job.ID, job); err != nil {
		return supervisor.GoalJob{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return supervisor.GoalJob{}, fmt.Errorf("commit Goal Job create/wake: %w", err)
	}
	return job, nil
}

const goalJobColumns = `id::text,goal_id::text,status,current_stage,COALESCE(lease_owner,''),COALESCE(lease_expires_at,'epoch'::timestamptz),COALESCE(heartbeat_at,'epoch'::timestamptz),fencing_token,COALESCE(next_run_at,'epoch'::timestamptz),COALESCE(last_run_at,'epoch'::timestamptz),last_error,retry_count,run_count,max_runs,wake_requested,created_at,updated_at,completed_at`
const goalJobColumnsJ = `j.id::text,j.goal_id::text,j.status,j.current_stage,COALESCE(j.lease_owner,''),COALESCE(j.lease_expires_at,'epoch'::timestamptz),COALESCE(j.heartbeat_at,'epoch'::timestamptz),j.fencing_token,COALESCE(j.next_run_at,'epoch'::timestamptz),COALESCE(j.last_run_at,'epoch'::timestamptz),j.last_error,j.retry_count,j.run_count,j.max_runs,j.wake_requested,j.created_at,j.updated_at,j.completed_at`

func scanGoalJob(row interface{ Scan(...any) error }) (supervisor.GoalJob, error) {
	var job supervisor.GoalJob
	var status string
	err := row.Scan(&job.ID, &job.GoalID, &status, &job.CurrentStage, &job.LeaseOwner, &job.LeaseExpires, &job.HeartbeatAt, &job.FencingToken, &job.NextRunAt, &job.LastRunAt, &job.LastError, &job.RetryCount, &job.RunCount, &job.MaxRuns, &job.WakeRequested, &job.CreatedAt, &job.UpdatedAt, &job.CompletedAt)
	job.Status = supervisor.JobStatus(status)
	return job, err
}

func (s *GoalJobStore) Get(ctx context.Context, ownerID, id string) (supervisor.GoalJob, error) {
	return s.get(ctx, `SELECT `+goalJobColumnsJ+` FROM goal_jobs j JOIN goals g ON g.id=j.goal_id WHERE j.id=$1 AND g.owner_id=$2`, id, ownerID)
}

func (s *GoalJobStore) get(ctx context.Context, query string, args ...any) (supervisor.GoalJob, error) {
	job, err := scanGoalJob(s.pool.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return supervisor.GoalJob{}, supervisor.ErrGoalJobNotFound
	}
	if err != nil {
		return supervisor.GoalJob{}, fmt.Errorf("load Goal Job: %w", err)
	}
	return job, nil
}

func (s *GoalJobStore) ListByGoal(ctx context.Context, ownerID string, goalID string, limit int) ([]supervisor.GoalJob, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `SELECT `+goalJobColumnsJ+` FROM goal_jobs j JOIN goals g ON g.id=j.goal_id WHERE g.owner_id=$1 AND j.goal_id=$2 ORDER BY j.created_at DESC,j.id DESC LIMIT $3`, ownerID, goalID, limit)
	if err != nil {
		return nil, fmt.Errorf("list Goal Jobs: %w", err)
	}
	defer rows.Close()
	items := make([]supervisor.GoalJob, 0)
	for rows.Next() {
		item, err := scanGoalJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan Goal Job: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Goal Jobs: %w", err)
	}
	return items, nil
}

func (s *GoalJobStore) Claim(ctx context.Context, workerID string, leaseDuration time.Duration) (supervisor.GoalJob, supervisor.GoalJobLease, error) {
	if strings.TrimSpace(workerID) == "" || leaseDuration < time.Second {
		return supervisor.GoalJob{}, supervisor.GoalJobLease{}, errors.New("worker ID and a lease of at least one second are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return supervisor.GoalJob{}, supervisor.GoalJobLease{}, fmt.Errorf("begin Goal Job claim: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `UPDATE goal_jobs SET status='failed',last_error='maximum Goal Job run budget exhausted',lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,updated_at=now()
		WHERE run_count>=max_runs AND (status IN ('pending','retry_wait') AND next_run_at<=now() OR status='running' AND lease_expires_at<=now())`); err != nil {
		return supervisor.GoalJob{}, supervisor.GoalJobLease{}, fmt.Errorf("stop exhausted Goal Jobs: %w", err)
	}
	var job supervisor.GoalJob
	var status string
	err = tx.QueryRow(ctx, `WITH candidate AS (
		SELECT j.id FROM goal_jobs j JOIN goals g ON g.id=j.goal_id
		WHERE ((j.status IN ('pending','retry_wait') AND (j.next_run_at IS NULL OR j.next_run_at<=now())) OR (j.status='running' AND j.lease_expires_at<=now()))
		AND j.run_count<j.max_runs AND g.status IN ('active','monitoring')
		ORDER BY COALESCE(j.next_run_at,j.created_at),j.created_at,j.id
		FOR UPDATE OF j SKIP LOCKED LIMIT 1
		)
		UPDATE goal_jobs j SET status='running',lease_owner=$1,lease_expires_at=now()+$2::interval,heartbeat_at=now(),
			fencing_token=fencing_token+1,run_count=run_count+1,last_run_at=now(),updated_at=now(),wake_requested=false,last_error=CASE WHEN j.status='running' THEN 'previous lease expired; reclaimed' ELSE j.last_error END
		FROM candidate c WHERE j.id=c.id RETURNING `+goalJobColumnsJ, workerID, leaseDuration.String()).Scan(&job.ID, &job.GoalID, &status, &job.CurrentStage, &job.LeaseOwner, &job.LeaseExpires, &job.HeartbeatAt, &job.FencingToken, &job.NextRunAt, &job.LastRunAt, &job.LastError, &job.RetryCount, &job.RunCount, &job.MaxRuns, &job.WakeRequested, &job.CreatedAt, &job.UpdatedAt, &job.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return supervisor.GoalJob{}, supervisor.GoalJobLease{}, execution.ErrNoExecution
	}
	if err != nil {
		return supervisor.GoalJob{}, supervisor.GoalJobLease{}, fmt.Errorf("claim Goal Job: %w", err)
	}
	job.Status = supervisor.JobRunning
	if err := recordMutation(ctx, tx, workerID, "goal_job.claimed", "goal_jobs", job.ID, job); err != nil {
		return supervisor.GoalJob{}, supervisor.GoalJobLease{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return supervisor.GoalJob{}, supervisor.GoalJobLease{}, fmt.Errorf("commit Goal Job claim: %w", err)
	}
	return job, supervisor.GoalJobLease{JobID: job.ID, GoalID: job.GoalID, WorkerID: workerID, FencingToken: job.FencingToken, ExpiresAt: job.LeaseExpires}, nil
}

func (s *GoalJobStore) Heartbeat(ctx context.Context, lease supervisor.GoalJobLease, duration time.Duration) (supervisor.GoalJobLease, error) {
	var expiry time.Time
	err := s.pool.QueryRow(ctx, `UPDATE goal_jobs SET heartbeat_at=now(),lease_expires_at=now()+$4::interval,updated_at=now()
		WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND status='running' AND lease_expires_at>now() RETURNING lease_expires_at`, lease.JobID, lease.WorkerID, lease.FencingToken, duration.String()).Scan(&expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return supervisor.GoalJobLease{}, supervisor.ErrGoalJobLeaseLost
	}
	if err != nil {
		return supervisor.GoalJobLease{}, fmt.Errorf("heartbeat Goal Job: %w", err)
	}
	lease.ExpiresAt = expiry
	return lease, nil
}

func (s *GoalJobStore) Finish(ctx context.Context, lease supervisor.GoalJobLease, status supervisor.JobStatus, message string, nextRun time.Time, retry bool, maxRetries int) (supervisor.GoalJob, error) {
	if status != supervisor.JobPending && status != supervisor.JobWaitingApproval && status != supervisor.JobWaitingExternal && status != supervisor.JobCompleted && status != supervisor.JobFailed && status != supervisor.JobCancelled && status != supervisor.JobRetryWait {
		return supervisor.GoalJob{}, errors.New("invalid Goal Job finish status")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return supervisor.GoalJob{}, fmt.Errorf("begin Goal Job finish: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if retry && maxRetries >= 0 {
		status = supervisor.JobRetryWait
	}
	var rowStatus string
	var currentRetry int
	err = tx.QueryRow(ctx, `UPDATE goal_jobs SET
		status=CASE WHEN wake_requested AND $4 IN ('pending','retry_wait','waiting_approval','waiting_external') THEN 'pending' ELSE $4 END,
		current_stage='evaluate',last_error=$5,retry_count=retry_count+CASE WHEN $6 THEN 1 ELSE 0 END,
		next_run_at=CASE WHEN wake_requested AND $4 IN ('pending','retry_wait','waiting_approval','waiting_external') THEN now() ELSE NULLIF($7,'epoch'::timestamptz) END,
		lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,updated_at=now(),
		completed_at=CASE WHEN $4 IN ('completed','failed','cancelled') THEN now() ELSE NULL END,
		wake_requested=CASE WHEN wake_requested AND $4 IN ('pending','retry_wait','waiting_approval','waiting_external') THEN false ELSE wake_requested END
		WHERE id=$1 AND goal_id=$2 AND lease_owner=$3 AND fencing_token=$8 AND status='running'
		RETURNING status,retry_count`, lease.JobID, lease.GoalID, lease.WorkerID, status, message, retry, nullableTime(nextRun), lease.FencingToken).Scan(&rowStatus, &currentRetry)
	if errors.Is(err, pgx.ErrNoRows) {
		return supervisor.GoalJob{}, supervisor.ErrGoalJobLeaseLost
	}
	if err != nil {
		return supervisor.GoalJob{}, fmt.Errorf("persist Goal Job finish: %w", err)
	}
	if retry && currentRetry > maxRetries {
		if _, err := tx.Exec(ctx, "UPDATE goal_jobs SET status='failed',completed_at=now(),next_run_at=NULL,last_error='maximum retry budget exhausted: '||last_error,updated_at=now() WHERE id=$1", lease.JobID); err != nil {
			return supervisor.GoalJob{}, fmt.Errorf("stop exhausted Goal Job retries: %w", err)
		}
	}
	job, err := scanGoalJob(tx.QueryRow(ctx, `SELECT `+goalJobColumnsJ+` FROM goal_jobs j WHERE j.id=$1`, lease.JobID))
	if err != nil {
		return supervisor.GoalJob{}, fmt.Errorf("reload finished Goal Job: %w", err)
	}
	if err := recordMutation(ctx, tx, lease.WorkerID, "goal_job.finished", "goal_jobs", job.ID, job); err != nil {
		return supervisor.GoalJob{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return supervisor.GoalJob{}, fmt.Errorf("commit Goal Job finish: %w", err)
	}
	return job, nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func (s *GoalJobStore) Pause(ctx context.Context, ownerID, jobID string) (supervisor.GoalJob, error) {
	return s.control(ctx, ownerID, jobID, supervisor.JobPaused)
}
func (s *GoalJobStore) Resume(ctx context.Context, ownerID, jobID string) (supervisor.GoalJob, error) {
	return s.control(ctx, ownerID, jobID, supervisor.JobPending)
}
func (s *GoalJobStore) Cancel(ctx context.Context, ownerID, jobID string) (supervisor.GoalJob, error) {
	return s.control(ctx, ownerID, jobID, supervisor.JobCancelled)
}

func (s *GoalJobStore) control(ctx context.Context, ownerID, jobID string, target supervisor.JobStatus) (supervisor.GoalJob, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return supervisor.GoalJob{}, fmt.Errorf("begin Goal Job control: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var current supervisor.GoalJob
	var status string
	err = tx.QueryRow(ctx, `SELECT `+goalJobColumnsJ+` FROM goal_jobs j JOIN goals g ON g.id=j.goal_id WHERE j.id=$1 AND g.owner_id=$2 FOR UPDATE OF j`, jobID, ownerID).Scan(&current.ID, &current.GoalID, &status, &current.CurrentStage, &current.LeaseOwner, &current.LeaseExpires, &current.HeartbeatAt, &current.FencingToken, &current.NextRunAt, &current.LastRunAt, &current.LastError, &current.RetryCount, &current.RunCount, &current.MaxRuns, &current.WakeRequested, &current.CreatedAt, &current.UpdatedAt, &current.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return supervisor.GoalJob{}, supervisor.ErrGoalJobNotFound
	}
	if err != nil {
		return supervisor.GoalJob{}, fmt.Errorf("load Goal Job control: %w", err)
	}
	currentStatus := supervisor.JobStatus(status)
	if currentStatus == supervisor.JobCompleted || currentStatus == supervisor.JobFailed || currentStatus == supervisor.JobCancelled {
		if target != supervisor.JobCancelled || currentStatus != supervisor.JobCancelled {
			return supervisor.GoalJob{}, supervisor.ErrGoalJobNotRunnable
		}
		return current, nil
	}
	if target == supervisor.JobPaused && currentStatus == supervisor.JobPaused {
		return current, nil
	}
	if target == supervisor.JobPending && currentStatus != supervisor.JobPaused && currentStatus != supervisor.JobWaitingApproval && currentStatus != supervisor.JobWaitingExternal {
		return supervisor.GoalJob{}, supervisor.ErrGoalJobNotRunnable
	}
	if (target == supervisor.JobPaused || target == supervisor.JobCancelled) && supervisor.JobStatus(status) == supervisor.JobRunning {
		if _, err := tx.Exec(ctx, `UPDATE goal_jobs SET status=$3,fencing_token=fencing_token+1,lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,next_run_at=NULL,updated_at=now(),completed_at=CASE WHEN $3='cancelled' THEN now() ELSE NULL END
			WHERE id=$1 AND lease_owner=$2 AND status='running'`, jobID, current.LeaseOwner, target); err != nil {
			return supervisor.GoalJob{}, fmt.Errorf("fence running Goal Job control: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE goal_jobs SET status=$2,next_run_at=CASE WHEN $2='pending' THEN now() ELSE NULL END,lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,fencing_token=fencing_token+1,updated_at=now(),completed_at=CASE WHEN $2='cancelled' THEN now() ELSE completed_at END WHERE id=$1`, jobID, target); err != nil {
			return supervisor.GoalJob{}, fmt.Errorf("persist Goal Job control: %w", err)
		}
	}
	job, err := scanGoalJob(tx.QueryRow(ctx, `SELECT `+goalJobColumnsJ+` FROM goal_jobs j WHERE j.id=$1`, jobID))
	if err != nil {
		return supervisor.GoalJob{}, fmt.Errorf("reload Goal Job after control: %w", err)
	}
	if err := recordMutation(ctx, tx, ownerID, "goal_job."+string(target), "goal_jobs", job.ID, job); err != nil {
		return supervisor.GoalJob{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return supervisor.GoalJob{}, fmt.Errorf("commit Goal Job control: %w", err)
	}
	return job, nil
}

var _ supervisor.GoalJobRepository = (*GoalJobStore)(nil)
