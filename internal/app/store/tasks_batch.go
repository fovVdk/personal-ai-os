// English: This adapter atomically creates a planned task DAG and its UUID dependencies.
// 中文：本适配器以原子事务创建计划任务 DAG 及其 UUID 依赖关系。
// 中文：本适配器以单个事务批量创建计划任务 DAG 及其 UUID 依赖关系。
package store

import (
	"context"
	"encoding/json"
	"fmt"

	"personal-ai-os/internal/task"
)

func (p *Postgres) CreateTasks(ctx context.Context, items []task.Task) ([]task.Task, error) {
	if len(items) == 0 {
		return []task.Task{}, nil
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin task plan transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	created := make([]task.Task, len(items))
	ids := make(map[string]string, len(items))
	for i, item := range items {
		if item.PlanKey == "" {
			return nil, fmt.Errorf("planned task %d is missing a plan key", i)
		}
		if _, exists := ids[item.PlanKey]; exists {
			return nil, fmt.Errorf("duplicate planned task key %q", item.PlanKey)
		}
		if err := item.Validate(); err != nil {
			return nil, fmt.Errorf("validate planned task %q: %w", item.PlanKey, err)
		}
		criteria, err := json.Marshal(item.SuccessCriteria)
		if err != nil {
			return nil, fmt.Errorf("encode task criteria: %w", err)
		}
		if item.SuccessCriteria == nil {
			criteria = []byte("[]")
		}
		err = tx.QueryRow(ctx, `INSERT INTO tasks(goal_id,title,description,priority,status,success_criteria,attempt_count)
			VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text,created_at,updated_at`, item.GoalID, item.Title, item.Description, item.Priority, item.Status, criteria, item.AttemptCount).Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("insert planned task: %w", err)
		}
		ids[item.PlanKey] = item.ID
		created[i] = item
	}
	for i := range created {
		created[i].DependsOn = append([]string(nil), items[i].DependsOn...)
		for j, dependencyKey := range created[i].DependsOn {
			if resolvedID, ok := ids[dependencyKey]; ok {
				created[i].DependsOn[j] = resolvedID
			}
		}
	}
	for i, spec := range items {
		for _, dependencyID := range spec.DependsOn {
			resolved, ok := ids[dependencyID]
			if !ok {
				return nil, fmt.Errorf("planned task %q depends on unknown key %q", spec.PlanKey, dependencyID)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO task_dependencies(task_id,depends_on_task_id) VALUES($1,$2)`, created[i].ID, resolved); err != nil {
				return nil, fmt.Errorf("insert task dependency: %w", err)
			}
		}
		if err := recordMutation(ctx, tx, LocalOwnerID, "task.created", "tasks", created[i].ID, created[i]); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit task plan: %w", err)
	}
	return created, nil
}
