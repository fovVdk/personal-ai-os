// English: These PostgreSQL tests validate Goal Job leases, fencing, coalescing, and recovery.
// 中文：本 PostgreSQL 测试验证 Goal Job 租约、Fencing、唤醒合并与恢复。
package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"personal-ai-os/internal/app/store"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/supervisor"
	"personal-ai-os/migrations"
)

func TestPostgresGoalJobClaimFencingWakeAndControl(t *testing.T) {
	pool, repository := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	goalItem, err := repository.Create(ctx, goal.Goal{OwnerID: testOwnerID, Title: uniqueLabel("goal-job-runtime"), Priority: 3, Status: goal.Active})
	if err != nil {
		t.Fatalf("create Goal: %v", err)
	}
	goalIDs := []string{goalItem.ID}
	t.Cleanup(func() { cleanupIntegrationRows(t, context.Background(), pool, goalIDs, nil, nil, nil, nil, nil) })
	jobs := store.NewGoalJobStore(pool)
	created, err := jobs.CreateOrWake(ctx, goalItem.ID, 10)
	if err != nil || created.Status != supervisor.JobPending {
		t.Fatalf("create Goal Job=%+v err=%v", created, err)
	}

	type claimResult struct {
		job   supervisor.GoalJob
		lease supervisor.GoalJobLease
		err   error
	}
	results := make(chan claimResult, 2)
	start := make(chan struct{})
	var group sync.WaitGroup
	for _, workerID := range []string{"goal-worker-a", "goal-worker-b"} {
		group.Add(1)
		go func(id string) {
			defer group.Done()
			<-start
			job, lease, err := jobs.Claim(ctx, id, time.Second)
			results <- claimResult{job: job, lease: lease, err: err}
		}(workerID)
	}
	close(start)
	group.Wait()
	close(results)
	var first supervisor.GoalJob
	var lease supervisor.GoalJobLease
	claims := 0
	for result := range results {
		if result.err == nil {
			claims++
			first, lease = result.job, result.lease
		} else if !errors.Is(result.err, execution.ErrNoExecution) {
			t.Fatalf("competing Goal Job claim error=%v", result.err)
		}
	}
	if claims != 1 || first.Status != supervisor.JobRunning || first.FencingToken != 1 {
		t.Fatalf("concurrent claims=%d job=%+v", claims, first)
	}

	woken, err := jobs.CreateOrWake(ctx, goalItem.ID, 10)
	if err != nil || woken.ID != first.ID || !woken.WakeRequested || woken.Status != supervisor.JobRunning {
		t.Fatalf("running Job wake should coalesce: %+v err=%v", woken, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE goal_jobs SET lease_expires_at=now()-interval '1 second' WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	takeover, newLease, err := jobs.Claim(ctx, "goal-worker-replacement", time.Second)
	if err != nil || takeover.ID != first.ID || takeover.FencingToken != lease.FencingToken+1 || takeover.RunCount != 2 {
		t.Fatalf("expired Goal Job takeover=%+v lease=%+v err=%v", takeover, newLease, err)
	}
	if _, err := jobs.Heartbeat(ctx, lease, time.Second); !errors.Is(err, supervisor.ErrGoalJobLeaseLost) {
		t.Fatalf("old Worker heartbeat error=%v, want fencing rejection", err)
	}
	if _, err := jobs.Finish(ctx, lease, supervisor.JobCompleted, "stale worker", time.Time{}, false, 3); !errors.Is(err, supervisor.ErrGoalJobLeaseLost) {
		t.Fatalf("old Worker finish error=%v, want fencing rejection", err)
	}
	if _, err := jobs.CreateOrWake(ctx, goalItem.ID, 10); err != nil {
		t.Fatalf("coalesce event while replacement Worker runs: %v", err)
	}
	finished, err := jobs.Finish(ctx, newLease, supervisor.JobWaitingApproval, "approval required", time.Time{}, false, 3)
	if err != nil || finished.Status != supervisor.JobPending || finished.LeaseOwner != "" || finished.WakeRequested {
		t.Fatalf("event during execution should resume instead of sleeping for approval=%+v err=%v", finished, err)
	}
	woken, err = jobs.CreateOrWake(ctx, goalItem.ID, 10)
	if err != nil || woken.Status != supervisor.JobPending || woken.ID != finished.ID {
		t.Fatalf("approval event wake did not resume persisted Job: %+v err=%v", woken, err)
	}
	_, retryLease, err := jobs.Claim(ctx, "goal-worker-retry-wake", time.Second)
	if err != nil {
		t.Fatalf("claim Job for retry wake: %v", err)
	}
	if _, err := jobs.CreateOrWake(ctx, goalItem.ID, 10); err != nil {
		t.Fatalf("coalesce event while retrying Worker runs: %v", err)
	}
	finished, err = jobs.Finish(ctx, retryLease, supervisor.JobRetryWait, "transient error", time.Now().Add(time.Hour), true, 3)
	if err != nil || finished.Status != supervisor.JobPending || finished.NextRunAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("event during retry should bypass backoff=%+v err=%v", finished, err)
	}
	paused, err := jobs.Pause(ctx, testOwnerID, woken.ID)
	if err != nil || paused.Status != supervisor.JobPaused {
		t.Fatalf("pause Goal Job=%+v err=%v", paused, err)
	}
	resumed, err := jobs.Resume(ctx, testOwnerID, paused.ID)
	if err != nil || resumed.Status != supervisor.JobPending {
		t.Fatalf("resume Goal Job=%+v err=%v", resumed, err)
	}
	cancelled, err := jobs.Cancel(ctx, testOwnerID, resumed.ID)
	if err != nil || cancelled.Status != supervisor.JobCancelled {
		t.Fatalf("cancel Goal Job=%+v err=%v", cancelled, err)
	}
	if _, _, err := jobs.Claim(ctx, "cancelled-must-not-run", time.Second); !errors.Is(err, execution.ErrNoExecution) {
		t.Fatalf("cancelled Goal Job remained claimable: %v", err)
	}
}
