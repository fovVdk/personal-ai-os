// English: These tests validate durable Outbox delivery and idempotent Goal Job wakeups.
// 中文：本测试验证持久 Outbox 投递和幂等 Goal Job 唤醒。
package integration

import (
	"context"
	"testing"
	"time"

	"personal-ai-os/internal/approval"
	"personal-ai-os/internal/app/store"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/supervisor"
	"personal-ai-os/internal/task"
	"personal-ai-os/migrations"
)

func TestPostgresOutboxDispatcherWakesExistingJobsIdempotently(t *testing.T) {
	pool, repository := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	goalItem, err := repository.Create(ctx, goal.Goal{OwnerID: testOwnerID, Title: uniqueLabel("outbox-wakeup"), Priority: 3, Status: goal.Active})
	if err != nil {
		t.Fatalf("create Goal: %v", err)
	}
	goalIDs := []string{goalItem.ID}
	var taskIDs, approvalIDs []string
	t.Cleanup(func() { cleanupIntegrationRows(t, context.Background(), pool, goalIDs, taskIDs, nil, nil, nil, approvalIDs) })

	jobs := store.NewGoalJobStore(pool)
	job, err := jobs.CreateOrWake(ctx, goalItem.ID, 10)
	if err != nil {
		t.Fatalf("create explicit Goal Job: %v", err)
	}
	_, lease, err := jobs.Claim(ctx, "dispatcher-test-worker", time.Second)
	if err != nil {
		t.Fatalf("claim Goal Job: %v", err)
	}

	// A persisted Goal update is a real Outbox source. While running it must only
	// coalesce a wake signal; it must not start a second Supervisor loop.
	// 中文：持久化 Goal 更新是实际 Outbox 来源；Job 运行中只合并唤醒请求，不能启动第二个 Supervisor 循环。
	goalItem.Description = "changed to generate a durable update event"
	if _, err := repository.Update(ctx, testOwnerID, goalItem); err != nil {
		t.Fatalf("update Goal: %v", err)
	}
	var eventID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM event_outbox WHERE aggregate_type='goals' AND aggregate_id=$1 AND event_type='goal.updated' ORDER BY occurred_at DESC LIMIT 1`, goalItem.ID).Scan(&eventID); err != nil {
		t.Fatalf("load Goal update event: %v", err)
	}
	// Simulate a Dispatcher crash after the durable claim but before consumer work.
	// 中文：模拟 Dispatcher 持久认领事件后、执行消费者逻辑前发生崩溃。
	if _, err := pool.Exec(ctx, "UPDATE event_outbox SET dispatch_owner='crashed-dispatcher',dispatch_lease_until=now()-interval '1 second' WHERE id=$1", eventID); err != nil {
		t.Fatalf("prepare expired dispatch lease: %v", err)
	}
	dispatcher := store.NewOutboxDispatcherStore(pool)
	processed, err := dispatcher.DispatchBatch(ctx, "dispatcher-a", 50, time.Second, 5)
	if err != nil || processed == 0 {
		t.Fatalf("dispatch real Goal event: processed=%d err=%v", processed, err)
	}
	woken, err := jobs.Get(ctx, testOwnerID, job.ID)
	if err != nil || woken.Status != supervisor.JobRunning || !woken.WakeRequested {
		t.Fatalf("running Job wake was not coalesced: %+v err=%v", woken, err)
	}

	// Simulate redelivery after a dispatcher crash between consumer processing
	// and acknowledgement. The consumer receipt must make this harmless.
	// 中文：模拟消费者处理后、确认事件前崩溃造成的重复投递；消费回执必须使重放无害。
	if _, err := pool.Exec(ctx, "UPDATE event_outbox SET published_at=NULL,dispatch_owner='',dispatch_lease_until=NULL WHERE id=$1", eventID); err != nil {
		t.Fatalf("reset event for redelivery: %v", err)
	}
	if _, err := dispatcher.DispatchBatch(ctx, "dispatcher-b", 50, time.Second, 5); err != nil {
		t.Fatalf("redeliver Goal event: %v", err)
	}
	var receipts int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM goal_job_wake_events WHERE event_id=$1", eventID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("duplicate event receipts=%d err=%v, want exactly one", receipts, err)
	}

	if _, err := pool.Exec(ctx, "UPDATE goal_jobs SET wake_requested=false WHERE id=$1", job.ID); err != nil {
		t.Fatalf("reset consumed test wake signal: %v", err)
	}
	if _, err := jobs.Finish(ctx, lease, supervisor.JobWaitingApproval, "awaiting human decision", time.Time{}, false, 5); err != nil {
		t.Fatalf("finish Job into approval wait: %v", err)
	}

	approvalTask, err := repository.Tasks().Create(ctx, task.Task{GoalID: goalItem.ID, Title: uniqueLabel("approval-wakeup-task"), Priority: 2, Status: task.Ready})
	if err != nil {
		t.Fatalf("create approval Task: %v", err)
	}
	taskIDs = append(taskIDs, approvalTask.ID)
	now := time.Now().UTC()
	approvalRequest, err := approval.New(testOwnerID, approvalTask.ID, "high-risk.test", "perform", map[string]string{"scope": "exact"}, 3, now.Add(time.Hour), now)
	if err != nil {
		t.Fatalf("create approval request value: %v", err)
	}
	approvalRepo := repository.Approvals()
	approvalRequest, err = approvalRepo.Create(ctx, approvalRequest)
	if err != nil {
		t.Fatalf("persist approval request: %v", err)
	}
	approvalIDs = append(approvalIDs, approvalRequest.ID)
	if _, err := approvalRepo.Decide(ctx, testOwnerID, approvalRequest.ID, approval.Approved, time.Now().UTC()); err != nil {
		t.Fatalf("approve request: %v", err)
	}
	if processed, err := dispatcher.DispatchBatch(ctx, "dispatcher-c", 50, time.Second, 5); err != nil || processed == 0 {
		t.Fatalf("dispatch Approval event for waiting Job: processed=%d err=%v", processed, err)
	}
	woken, err = jobs.Get(ctx, testOwnerID, job.ID)
	if err != nil || woken.Status != supervisor.JobPending {
		t.Fatalf("approval event did not resume waiting Job: %+v err=%v", woken, err)
	}

	// A terminal Goal is re-read from PostgreSQL; an obsolete event cannot revive
	// its cancelled Job.
	// 中文：终态 Goal 必须从 PostgreSQL 重新读取；过期事件不能复活已取消的 Job。
	if _, err := jobs.Cancel(ctx, testOwnerID, job.ID); err != nil {
		t.Fatalf("cancel Goal Job: %v", err)
	}
	if _, err := repository.Transition(ctx, testOwnerID, goalItem.ID, goal.Cancelled, nil); err != nil {
		t.Fatalf("cancel Goal: %v", err)
	}
	if _, err := dispatcher.DispatchBatch(ctx, "dispatcher-d", 50, time.Second, 5); err != nil {
		t.Fatalf("dispatch terminal Goal event: %v", err)
	}
	final, err := jobs.Get(ctx, testOwnerID, job.ID)
	if err != nil || final.Status != supervisor.JobCancelled {
		t.Fatalf("terminal Goal wake changed cancelled Job: %+v err=%v", final, err)
	}
	if _, _, err := jobs.Claim(ctx, "cancelled-job-worker", time.Second); err == nil || err != execution.ErrNoExecution {
		t.Fatalf("cancelled Job became claimable: %v", err)
	}
}
