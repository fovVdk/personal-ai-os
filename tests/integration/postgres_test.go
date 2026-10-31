// English: These opt-in tests exercise migrations and durable Goal/Task persistence against a disposable PostgreSQL database.
// 中文：这些显式启用的集成测试使用一次性 PostgreSQL 数据库验证迁移及 Goal/Task 持久化。
package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"personal-ai-os/internal/app/database"
	"personal-ai-os/internal/app/store"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/task"
	"personal-ai-os/migrations"
)

func TestPostgresMigrationsGoalTaskAndEvidenceFlow(t *testing.T) {
	databaseURL := os.Getenv("PAIOS_TEST_DATABASE_URL")
	if databaseURL == "" { t.Skip("set PAIOS_TEST_DATABASE_URL to run PostgreSQL integration tests") }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil { t.Fatalf("open test database: %v", err) }
	defer pool.Close()
	if err := migrations.Apply(ctx, pool); err != nil { t.Fatalf("apply migrations: %v", err) }

	repository := store.NewPostgres(pool)
	item, err := repository.Create(ctx, goal.Goal{OwnerID: store.LocalOwnerID, Title: "integration goal", Priority: 3, Status: goal.Draft})
	if err != nil { t.Fatalf("create goal: %v", err) }
	loaded, err := repository.Get(ctx, store.LocalOwnerID, item.ID)
	if err != nil || loaded.ID != item.ID { t.Fatalf("get goal=%+v err=%v", loaded, err) }

	tasks := repository.Tasks()
	createdTask, err := tasks.Create(ctx, task.Task{GoalID: item.ID, Title: "integration task", Priority: 3, Status: task.Created})
	if err != nil { t.Fatalf("create task: %v", err) }
		defer func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM tasks WHERE id=$1`, createdTask.ID)
			_, _ = pool.Exec(context.Background(), `DELETE FROM goals WHERE id=$1`, item.ID)
		}()
	if _, err := tasks.Transition(ctx, createdTask.ID, task.Ready); err != nil { t.Fatalf("ready task: %v", err) }
	started, err := tasks.Transition(ctx, createdTask.ID, task.Running)
	if err != nil || started.AttemptCount != 1 { t.Fatalf("start task=%+v err=%v", started, err) }
	if _, err := tasks.Transition(ctx, createdTask.ID, task.Completed); err != nil { t.Fatalf("complete task: %v", err) }
	listed, err := tasks.ListByGoal(ctx, item.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].Status != task.Completed { t.Fatalf("list tasks=%+v err=%v", listed, err) }
}
