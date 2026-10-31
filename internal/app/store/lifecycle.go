// English: This adapter applies row-locked Goal/Task transitions and crash-recovery queries.
// 中文：本适配器通过行锁更新 Goal/Task 状态，并提供崩溃恢复查询。
// 中文：本适配器通过行锁持久化 Goal/Task 状态转换，并查询中断后待恢复任务。
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/task"
	paierrors "personal-ai-os/pkg/errors"
)

func (p *Postgres) Update(ctx context.Context, ownerID string, item goal.Goal) (goal.Goal, error) {
	criteria, err := json.Marshal(item.SuccessCriteria)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("encode goal criteria: %w", err)
	}
	constraints, err := json.Marshal(item.Constraints)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("encode goal constraints: %w", err)
	}
	autonomy, err := json.Marshal(item.AutonomyPolicy)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("encode goal autonomy policy: %w", err)
	}
	review, err := json.Marshal(item.ReviewPolicy)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("encode goal review policy: %w", err)
	}
	if item.SuccessCriteria == nil {
		criteria = []byte("[]")
	}
	if item.Constraints == nil {
		constraints = []byte("[]")
	}
	if item.AutonomyPolicy == nil {
		autonomy = []byte("{}")
	}
	if item.ReviewPolicy == nil {
		review = []byte("{}")
	}
	var parent any
	if item.ParentGoalID != nil {
		parent = *item.ParentGoalID
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("begin goal update: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	err = tx.QueryRow(ctx, `UPDATE goals SET parent_goal_id=$3,title=$4,description=$5,priority=$6,success_criteria=$7,constraints=$8,
		autonomy_policy=$9,review_policy=$10,updated_at=now() WHERE owner_id=$1 AND id=$2
		RETURNING updated_at`, ownerID, item.ID, parent, item.Title, item.Description, item.Priority, criteria, constraints, autonomy, review).Scan(&item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return goal.Goal{}, paierrors.ErrNotFound
	}
	if err != nil {
		return goal.Goal{}, fmt.Errorf("update goal: %w", err)
	}
	item.OwnerID = ownerID
	if err := recordMutation(ctx, tx, ownerID, "goal.updated", "goals", item.ID, item); err != nil {
		return goal.Goal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return goal.Goal{}, fmt.Errorf("commit goal update: %w", err)
	}
	return item, nil
}

func (p *Postgres) Transition(ctx context.Context, ownerID, id string, next goal.Status, evidence []goal.Evidence) (goal.Goal, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("begin goal transition: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var item goal.Goal
	var parent *string
	err = tx.QueryRow(ctx, `SELECT id::text,title,description,owner_id::text,parent_goal_id::text,priority,success_criteria,constraints,
		autonomy_policy,review_policy,status,progress,created_at,updated_at,completed_at FROM goals WHERE owner_id=$1 AND id=$2 FOR UPDATE`, ownerID, id).Scan(
		&item.ID, &item.Title, &item.Description, &item.OwnerID, &parent, &item.Priority, &item.SuccessCriteria, &item.Constraints,
		&item.AutonomyPolicy, &item.ReviewPolicy, &item.Status, &item.Progress, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return goal.Goal{}, paierrors.ErrNotFound
	}
	if err != nil {
		return goal.Goal{}, fmt.Errorf("load goal transition: %w", err)
	}
	item.ParentGoalID = parent
	now := time.Now().UTC()
	if err := item.Transition(next, evidence, now); err != nil {
		return goal.Goal{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE goals SET status=$3,progress=$4,updated_at=$5,completed_at=$6 WHERE owner_id=$1 AND id=$2`, ownerID, id, item.Status, item.Progress, item.UpdatedAt, item.CompletedAt); err != nil {
		return goal.Goal{}, fmt.Errorf("persist goal transition: %w", err)
	}
	if next == goal.Completed {
		for _, evidenceItem := range item.Evidence {
			_, err := tx.Exec(ctx, `INSERT INTO goal_evidence(goal_id,source,description,reference,observed_at) VALUES($1,$2,$3,$4,$5)`, item.ID, evidenceItem.Source, evidenceItem.Description, evidenceItem.Reference, evidenceItem.ObservedAt.UTC())
			if err != nil {
				return goal.Goal{}, fmt.Errorf("persist goal evidence: %w", err)
			}
		}
	}
	eventType := "goal.updated"
	if next == goal.Completed {
		eventType = "goal.completed"
	}
	if err := recordMutation(ctx, tx, ownerID, eventType, "goals", item.ID, item); err != nil {
		return goal.Goal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return goal.Goal{}, fmt.Errorf("commit goal transition: %w", err)
	}
	return item, nil
}

func (p *Postgres) TransitionTask(ctx context.Context, id string, next task.Status) (task.Task, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return task.Task{}, fmt.Errorf("begin task transition: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var item task.Task
	err = tx.QueryRow(ctx, `SELECT id::text,goal_id::text,title,description,priority,status,attempt_count,success_criteria,scheduled_at,started_at,completed_at,created_at,updated_at
		FROM tasks WHERE id=$1 FOR UPDATE`, id).Scan(&item.ID, &item.GoalID, &item.Title, &item.Description, &item.Priority, &item.Status, &item.AttemptCount, &item.SuccessCriteria,
		&item.ScheduledAt, &item.StartedAt, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return task.Task{}, paierrors.ErrNotFound
	}
	if err != nil {
		return task.Task{}, fmt.Errorf("load task transition: %w", err)
	}
	if err := item.Transition(next, time.Now().UTC()); err != nil {
		return task.Task{}, err
	}
	if next == task.Running {
		item.AttemptCount++
	}
	if _, err := tx.Exec(ctx, `UPDATE tasks SET status=$2,started_at=$3,completed_at=$4,updated_at=$5,attempt_count=$6 WHERE id=$1`, item.ID, item.Status, item.StartedAt, item.CompletedAt, item.UpdatedAt, item.AttemptCount); err != nil {
		return task.Task{}, fmt.Errorf("persist task transition: %w", err)
	}
	eventType := "task.updated"
	switch next {
	case task.Running:
		eventType = "task.started"
	case task.Completed:
		eventType = "task.completed"
	case task.Failed:
		eventType = "task.failed"
	}
	if err := recordMutation(ctx, tx, LocalOwnerID, eventType, "tasks", item.ID, item); err != nil {
		return task.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return task.Task{}, fmt.Errorf("commit task transition: %w", err)
	}
	return item, nil
}

var _ goal.Repository = (*Postgres)(nil)

func (p *Postgres) loadGoalEvidence(ctx context.Context, goalID string) ([]goal.Evidence, error) {
	rows, err := p.pool.Query(ctx, `SELECT source,description,COALESCE(reference,''),observed_at FROM goal_evidence WHERE goal_id=$1 ORDER BY observed_at`, goalID)
	if err != nil {
		return nil, fmt.Errorf("load goal evidence: %w", err)
	}
	defer rows.Close()
	items := make([]goal.Evidence, 0)
	for rows.Next() {
		var item goal.Evidence
		if err := rows.Scan(&item.Source, &item.Description, &item.Reference, &item.ObservedAt); err != nil {
			return nil, fmt.Errorf("scan goal evidence: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate goal evidence: %w", err)
	}
	return items, nil
}

func (p *Postgres) Recoverable(ctx context.Context, limit int) ([]task.Task, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, `SELECT id::text,goal_id::text,title,description,priority,status,attempt_count,success_criteria,scheduled_at,started_at,completed_at,created_at,updated_at
		FROM tasks WHERE status IN ('running','retrying') ORDER BY updated_at LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("load recoverable tasks: %w", err)
	}
	defer rows.Close()
	items := make([]task.Task, 0)
	for rows.Next() {
		var item task.Task
		if err := rows.Scan(&item.ID, &item.GoalID, &item.Title, &item.Description, &item.Priority, &item.Status, &item.AttemptCount, &item.SuccessCriteria, &item.ScheduledAt, &item.StartedAt, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan recoverable task: %w", err)
		}
		item.DependsOn, err = p.taskDependencies(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recoverable tasks: %w", err)
	}
	return items, nil
}

var _ interface {
	Recoverable(context.Context, int) ([]task.Task, error)
} = (*Postgres)(nil)
