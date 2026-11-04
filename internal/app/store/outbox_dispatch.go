// English: This adapter claims Outbox events with leases and atomically wakes Goal Jobs.
// 中文：本适配器使用持久租约认领 Outbox，并原子唤醒 Goal Job。
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"personal-ai-os/internal/event"
)

type OutboxDispatcherStore struct{ pool *pgxpool.Pool }

type outboxEvent struct {
	ID            string
	EventType     string
	AggregateType string
	AggregateID   string
	Attempts      int
}

func NewOutboxDispatcherStore(pool *pgxpool.Pool) *OutboxDispatcherStore {
	return &OutboxDispatcherStore{pool: pool}
}

func (s *OutboxDispatcherStore) DispatchBatch(ctx context.Context, workerID string, limit int, lease time.Duration, maxAttempts int) (int, error) {
	if strings.TrimSpace(workerID) == "" || limit < 1 || limit > 500 || lease < time.Second || maxAttempts < 1 {
		return 0, fmt.Errorf("Outbox dispatch claim is invalid")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin Outbox claim: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(ctx, `WITH picked AS (
		SELECT id FROM event_outbox WHERE published_at IS NULL AND dead_lettered_at IS NULL AND next_attempt_at<=now()
		AND (dispatch_lease_until IS NULL OR dispatch_lease_until<=now())
		ORDER BY occurred_at,id FOR UPDATE SKIP LOCKED LIMIT $1
		)
		UPDATE event_outbox e SET dispatch_owner=$2,dispatch_lease_until=now()+$3::interval,attempts=attempts+1
		FROM picked p WHERE e.id=p.id
		RETURNING e.id::text,e.event_type,e.aggregate_type,e.aggregate_id::text,e.attempts`, limit, workerID, lease.String())
	if err != nil {
		return 0, fmt.Errorf("claim Outbox events: %w", err)
	}
	batch := make([]outboxEvent, 0)
	for rows.Next() {
		var item outboxEvent
		if err := rows.Scan(&item.ID, &item.EventType, &item.AggregateType, &item.AggregateID, &item.Attempts); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan claimed Outbox event: %w", err)
		}
		batch = append(batch, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate claimed Outbox events: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit Outbox claims: %w", err)
	}
	processed := 0
	var failures []error
	for _, item := range batch {
		if err := s.dispatchOne(ctx, workerID, item); err != nil {
			failures = append(failures, fmt.Errorf("dispatch Outbox %s (%s): %w", item.ID, item.EventType, err))
			if failErr := s.failOne(context.WithoutCancel(ctx), workerID, item, err, maxAttempts); failErr != nil {
				failures = append(failures, failErr)
			}
			continue
		}
		processed++
	}
	return processed, errors.Join(failures...)
}

func (s *OutboxDispatcherStore) dispatchOne(ctx context.Context, workerID string, item outboxEvent) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin event dispatch: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var owner string
	var published *time.Time
	if err := tx.QueryRow(ctx, `SELECT dispatch_owner,published_at FROM event_outbox WHERE id=$1 AND dead_lettered_at IS NULL FOR UPDATE`, item.ID).Scan(&owner, &published); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("lock Outbox event: %w", err)
	}
	if published != nil {
		return tx.Commit(ctx)
	}
	if owner != workerID {
		return fmt.Errorf("Outbox lease belongs to another dispatcher")
	}
	goalID, err := s.resolveWakeGoal(ctx, tx, item)
	if err != nil {
		return err
	}
	if goalID != "" {
		var inserted string
		err := tx.QueryRow(ctx, `INSERT INTO goal_job_wake_events(event_id,goal_job_id,goal_id)
			SELECT $1,j.id,j.goal_id FROM goal_jobs j WHERE j.goal_id=$2 AND j.status IN ('pending','running','waiting_approval','waiting_external','retry_wait','paused')
			ORDER BY j.created_at DESC LIMIT 1 ON CONFLICT(event_id) DO NOTHING RETURNING event_id::text`, item.ID, goalID).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) {
			// Events wake explicitly-created jobs; they do not silently opt every
			// Goal into autonomous execution.
		} else if err != nil {
			return fmt.Errorf("record Goal Job wake receipt: %w", err)
		} else {
			if err := s.wakeExistingJobTx(ctx, tx, goalID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE event_outbox SET published_at=now(),dispatch_owner='',dispatch_lease_until=NULL,last_error=''
		WHERE id=$1 AND dispatch_owner=$2 AND published_at IS NULL`, item.ID, workerID); err != nil {
		return fmt.Errorf("mark Outbox event delivered: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *OutboxDispatcherStore) resolveWakeGoal(ctx context.Context, tx pgx.Tx, item outboxEvent) (string, error) {
	switch item.AggregateType {
	case "goals":
		if item.EventType != "goal.created" && item.EventType != "goal.updated" && item.EventType != "goal.completed" {
			return "", nil
		}
		var status string
		err := tx.QueryRow(ctx, "SELECT id::text,status FROM goals WHERE id=$1", item.AggregateID).Scan(&item.AggregateID, &status)
		if errors.Is(err, pgx.ErrNoRows) || status == "completed" || status == "cancelled" || status == "archived" {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("verify Goal event state: %w", err)
		}
		return item.AggregateID, nil
	case "tasks":
		if !strings.HasPrefix(item.EventType, "task.") {
			return "", nil
		}
		var goalID string
		err := tx.QueryRow(ctx, "SELECT goal_id::text FROM tasks WHERE id=$1", item.AggregateID).Scan(&goalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return goalID, err
	case "approvals":
		if item.EventType != "approval.approved" && item.EventType != "approval.rejected" && item.EventType != "approval.expired" {
			return "", nil
		}
		var goalID string
		err := tx.QueryRow(ctx, "SELECT t.goal_id::text FROM approvals a JOIN tasks t ON t.id=a.task_id WHERE a.id=$1", item.AggregateID).Scan(&goalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return goalID, err
	case "execution_runs":
		if item.EventType != "execution.succeeded" && item.EventType != "execution.failed" && item.EventType != "execution.cancelled" && item.EventType != "execution.unknown" && item.EventType != "execution.retry_wait" && item.EventType != "execution.waiting_external" {
			return "", nil
		}
		var goalID string
		err := tx.QueryRow(ctx, "SELECT t.goal_id::text FROM execution_runs e JOIN tasks t ON t.id=e.task_id WHERE e.id=$1", item.AggregateID).Scan(&goalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return goalID, err
	case "tool_invocations":
		if !strings.HasPrefix(item.EventType, "tool.invocation_") {
			return "", nil
		}
		var goalID string
		err := tx.QueryRow(ctx, "SELECT t.goal_id::text FROM tool_invocations i JOIN tasks t ON t.id=i.task_id WHERE i.id=$1", item.AggregateID).Scan(&goalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return goalID, err
	case "plan_versions":
		if item.EventType != "plan.accepted" {
			return "", nil
		}
		var goalID string
		err := tx.QueryRow(ctx, "SELECT goal_id::text FROM plan_versions WHERE id=$1", item.AggregateID).Scan(&goalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return goalID, err
	case "source_snapshots":
		if item.EventType != "source.snapshot.created" {
			return "", nil
		}
		var goalID string
		err := tx.QueryRow(ctx, "SELECT goal_id::text FROM source_snapshots WHERE id=$1", item.AggregateID).Scan(&goalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return goalID, err
	default:
		return "", nil
	}
}

func (s *OutboxDispatcherStore) wakeExistingJobTx(ctx context.Context, tx pgx.Tx, goalID string) error {
	_, err := tx.Exec(ctx, `UPDATE goal_jobs SET status=CASE WHEN status='running' THEN 'running' WHEN status='paused' THEN 'paused' ELSE 'pending' END,
		wake_requested=CASE WHEN status='running' THEN true ELSE wake_requested END,
		next_run_at=CASE WHEN status IN ('running','paused') THEN next_run_at ELSE now() END,updated_at=now()
		WHERE goal_id=$1 AND status IN ('pending','running','waiting_approval','waiting_external','retry_wait','paused')`, goalID)
	if err != nil {
		return fmt.Errorf("wake existing Goal Job: %w", err)
	}
	return nil
}

func (s *OutboxDispatcherStore) failOne(ctx context.Context, workerID string, item outboxEvent, cause error, maxAttempts int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin Outbox failure update: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	dead := item.Attempts >= maxAttempts
	delay := time.Second << min(item.Attempts-1, 8)
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	var tag pgconn.CommandTag
	if dead {
		tag, err = tx.Exec(ctx, `UPDATE event_outbox SET dead_lettered_at=now(),dispatch_owner='',dispatch_lease_until=NULL,last_error=$3 WHERE id=$1 AND dispatch_owner=$2 AND published_at IS NULL`, item.ID, workerID, cause.Error())
	} else {
		tag, err = tx.Exec(ctx, `UPDATE event_outbox SET next_attempt_at=now()+$3::interval,dispatch_owner='',dispatch_lease_until=NULL,last_error=$4 WHERE id=$1 AND dispatch_owner=$2 AND published_at IS NULL`, item.ID, workerID, delay.String(), cause.Error())
	}
	if err != nil {
		return fmt.Errorf("persist Outbox retry/dead-letter: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if dead {
		if _, err := tx.Exec(ctx, `INSERT INTO audit_records(actor,action,resource,resource_id,result,metadata)
			VALUES($1,'outbox.dead_letter','event_outbox',$2,'failed',jsonb_build_object('error',$3::text,'attempts',$4::integer))`, workerID, item.ID, cause.Error(), item.Attempts); err != nil {
			return fmt.Errorf("audit Outbox dead letter: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit Outbox retry/dead-letter: %w", err)
	}
	return nil
}

var _ event.OutboxRepository = (*OutboxDispatcherStore)(nil)
