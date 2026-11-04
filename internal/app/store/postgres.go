// Package store provides PostgreSQL repository adapters for core domains.
// 中文：store 包为核心领域提供 PostgreSQL 仓储适配器。
// English: This adapter implements PostgreSQL-backed Goal/Task repositories and mutation journaling.
// 中文：本适配器实现 PostgreSQL Goal/Task 仓储及事务性事件、审计记录写入。
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/task"
	paierrors "personal-ai-os/pkg/errors"
)

const LocalOwnerID = "00000000-0000-4000-8000-000000000001"

// Postgres implements the core Goal and Task repositories.
// 中文：Postgres 实现核心 Goal 与 Task 仓储。
type ArtifactFreshnessVerifier interface {
	VerifyArtifactHash(context.Context, string, string) error
}

type Postgres struct {
	pool             *pgxpool.Pool
	artifactVerifier ArtifactFreshnessVerifier
}

func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (p *Postgres) SetArtifactFreshnessVerifier(verifier ArtifactFreshnessVerifier) {
	p.artifactVerifier = verifier
}

func (p *Postgres) Create(ctx context.Context, item goal.Goal) (goal.Goal, error) {
	if err := goal.ValidateSuccessCriteria(item.Criteria); err != nil {
		return goal.Goal{}, fmt.Errorf("validate structured goal criteria: %w", err)
	}
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
	structuredCriteria, err := json.Marshal(item.Criteria)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("encode structured goal criteria: %w", err)
	}
	if item.Criteria == nil {
		structuredCriteria = []byte("[]")
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

	var parentID any
	if item.ParentGoalID != nil {
		parentID = *item.ParentGoalID
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("begin goal transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	row := tx.QueryRow(ctx, `INSERT INTO goals
		(owner_id,parent_goal_id,title,description,priority,success_criteria,structured_success_criteria,constraints,autonomy_policy,review_policy,status,progress)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id::text,created_at,updated_at`, item.OwnerID, parentID, item.Title, item.Description,
		item.Priority, criteria, structuredCriteria, constraints, autonomy, review, item.Status, item.Progress)
	if err := row.Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return goal.Goal{}, fmt.Errorf("insert goal: %w", err)
	}
	if err := recordMutation(ctx, tx, item.OwnerID, "goal.created", "goals", item.ID, item); err != nil {
		return goal.Goal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return goal.Goal{}, fmt.Errorf("commit goal creation: %w", err)
	}
	return item, nil
}

func (p *Postgres) Get(ctx context.Context, ownerID, id string) (goal.Goal, error) {
	var item goal.Goal
	var parentID *string
	err := p.pool.QueryRow(ctx, `SELECT id::text,title,description,owner_id::text,parent_goal_id::text,priority,
		success_criteria,structured_success_criteria,constraints,autonomy_policy,review_policy,status,progress,created_at,updated_at,completed_at
		FROM goals WHERE owner_id=$1 AND id=$2`, ownerID, id).Scan(
		&item.ID, &item.Title, &item.Description, &item.OwnerID, &parentID, &item.Priority,
		&item.SuccessCriteria, &item.Criteria, &item.Constraints, &item.AutonomyPolicy, &item.ReviewPolicy, &item.Status, &item.Progress,
		&item.CreatedAt, &item.UpdatedAt, &item.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return goal.Goal{}, ErrNotFound
	}
	if err != nil {
		return goal.Goal{}, fmt.Errorf("get goal: %w", err)
	}
	item.ParentGoalID = parentID
	item.Evidence, err = p.loadGoalEvidence(ctx, item.ID)
	if err != nil {
		return goal.Goal{}, err
	}
	return item, nil
}

func (p *Postgres) List(ctx context.Context, ownerID string, limit int) ([]goal.Goal, error) {
	rows, err := p.pool.Query(ctx, `SELECT id::text,title,description,owner_id::text,parent_goal_id::text,priority,
		success_criteria,structured_success_criteria,constraints,autonomy_policy,review_policy,status,progress,created_at,updated_at,completed_at
		FROM goals WHERE owner_id=$1 ORDER BY priority DESC,created_at DESC LIMIT $2`, ownerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list goals: %w", err)
	}
	defer rows.Close()
	items := make([]goal.Goal, 0)
	for rows.Next() {
		var item goal.Goal
		var parentID *string
		if err := rows.Scan(&item.ID, &item.Title, &item.Description, &item.OwnerID, &parentID, &item.Priority,
			&item.SuccessCriteria, &item.Criteria, &item.Constraints, &item.AutonomyPolicy, &item.ReviewPolicy, &item.Status, &item.Progress,
			&item.CreatedAt, &item.UpdatedAt, &item.CompletedAt); err != nil {
			return nil, fmt.Errorf("scan goal: %w", err)
		}
		item.ParentGoalID = parentID
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate goals: %w", err)
	}
	return items, nil
}

func (p *Postgres) CreateTask(ctx context.Context, item task.Task) (task.Task, error) {
	criteria, err := json.Marshal(item.SuccessCriteria)
	if err != nil {
		return task.Task{}, fmt.Errorf("encode task criteria: %w", err)
	}
	if item.SuccessCriteria == nil {
		criteria = []byte("[]")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return task.Task{}, fmt.Errorf("begin task transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockGoalShared(ctx, tx, item.GoalID); err != nil && !errors.Is(err, ErrNotFound) {
		return task.Task{}, fmt.Errorf("lock Goal before Task creation: %w", err)
	}
	criterionIDs, err := json.Marshal(item.CriterionIDs)
	if err != nil {
		return task.Task{}, fmt.Errorf("encode task criterion mapping: %w", err)
	}
	if item.CriterionIDs == nil {
		criterionIDs = []byte("[]")
	}
	if err := tx.QueryRow(ctx, `INSERT INTO tasks(goal_id,title,description,priority,status,success_criteria,attempt_count,plan_key,criterion_ids)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text,created_at,updated_at`,
		item.GoalID, item.Title, item.Description, item.Priority, item.Status, criteria, item.AttemptCount, item.PlanKey, criterionIDs).Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return task.Task{}, fmt.Errorf("insert task: %w", err)
	}
	if err := recordMutation(ctx, tx, LocalOwnerID, "task.created", "tasks", item.ID, item); err != nil {
		return task.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return task.Task{}, fmt.Errorf("commit task creation: %w", err)
	}
	return item, nil
}

func recordMutation(ctx context.Context, tx pgx.Tx, actor, eventType, resource, resourceID string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", eventType, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO event_outbox(event_type,aggregate_type,aggregate_id,payload) VALUES($1,$2,$3,$4)`, eventType, resource, resourceID, encoded); err != nil {
		return fmt.Errorf("write %s outbox event: %w", eventType, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_records(actor,action,resource,resource_id,result,metadata) VALUES($1,$2,$3,$4,'success',$5)`, actor, eventType, resource, resourceID, encoded); err != nil {
		return fmt.Errorf("write %s audit record: %w", eventType, err)
	}
	return nil
}

func (p *Postgres) GetTask(ctx context.Context, id string) (task.Task, error) {
	var item task.Task
	err := p.pool.QueryRow(ctx, `SELECT id::text,goal_id::text,title,description,priority,status,attempt_count,success_criteria,plan_key,criterion_ids,
		scheduled_at,started_at,completed_at,created_at,updated_at FROM tasks WHERE id=$1`, id).Scan(
		&item.ID, &item.GoalID, &item.Title, &item.Description, &item.Priority, &item.Status, &item.AttemptCount, &item.SuccessCriteria,
		&item.PlanKey, &item.CriterionIDs,
		&item.ScheduledAt, &item.StartedAt, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return task.Task{}, ErrNotFound
	}
	if err != nil {
		return task.Task{}, fmt.Errorf("get task: %w", err)
	}
	item.DependsOn, err = p.taskDependencies(ctx, item.ID)
	if err != nil {
		return task.Task{}, err
	}
	return item, nil
}

func (p *Postgres) taskDependencies(ctx context.Context, taskID string) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT depends_on_task_id::text FROM task_dependencies WHERE task_id=$1 ORDER BY depends_on_task_id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("load task dependencies: %w", err)
	}
	defer rows.Close()
	dependencies := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan task dependency: %w", err)
		}
		dependencies = append(dependencies, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task dependencies: %w", err)
	}
	return dependencies, nil
}

func (p *Postgres) ListTasksByGoal(ctx context.Context, goalID string, limit int) ([]task.Task, error) {
	rows, err := p.pool.Query(ctx, `SELECT t.id::text,t.goal_id::text,t.title,t.description,t.priority,t.status,t.attempt_count,t.success_criteria,t.plan_key,t.criterion_ids,
		t.scheduled_at,t.started_at,t.completed_at,t.created_at,t.updated_at,COALESCE(array_agg(d.depends_on_task_id::text) FILTER (WHERE d.depends_on_task_id IS NOT NULL), ARRAY[]::text[])
		FROM tasks t LEFT JOIN task_dependencies d ON d.task_id=t.id WHERE t.goal_id=$1
		GROUP BY t.id ORDER BY t.priority DESC,t.created_at LIMIT $2`, goalID, limit)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	items := make([]task.Task, 0)
	for rows.Next() {
		var item task.Task
		if err := rows.Scan(&item.ID, &item.GoalID, &item.Title, &item.Description, &item.Priority, &item.Status, &item.AttemptCount, &item.SuccessCriteria, &item.PlanKey, &item.CriterionIDs,
			&item.ScheduledAt, &item.StartedAt, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt, &item.DependsOn); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	return items, nil
}

var ErrNotFound = paierrors.ErrNotFound

var _ goal.Repository = (*Postgres)(nil)
var _ task.Repository = taskRepository{Postgres: nil}

type taskRepository struct{ *Postgres }

func (r taskRepository) Create(ctx context.Context, item task.Task) (task.Task, error) {
	return r.CreateTask(ctx, item)
}
func (r taskRepository) CreateMany(ctx context.Context, items []task.Task) ([]task.Task, error) {
	return r.CreateTasks(ctx, items)
}
func (r taskRepository) Get(ctx context.Context, id string) (task.Task, error) {
	return r.GetTask(ctx, id)
}
func (r taskRepository) ListByGoal(ctx context.Context, goalID string, limit int) ([]task.Task, error) {
	return r.ListTasksByGoal(ctx, goalID, limit)
}
func (r taskRepository) Transition(ctx context.Context, id string, next task.Status) (task.Task, error) {
	return r.TransitionTask(ctx, id, next)
}
func (r taskRepository) Recoverable(ctx context.Context, limit int) ([]task.Task, error) {
	return r.Postgres.Recoverable(ctx, limit)
}

// Tasks returns the Task repository view backed by the same pool.
// 中文：Tasks 返回共享同一连接池的 Task 仓储视图。
func (p *Postgres) Tasks() task.Repository { return taskRepository{Postgres: p} }
