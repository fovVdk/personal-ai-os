package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"personal-ai-os/internal/app/store"
	"personal-ai-os/internal/goal"
	"personal-ai-os/migrations"
)

func TestPostgresOutboxDispatcherRetriesAndDeadLetters(t *testing.T) {
	pool, repository := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	goalItem, err := repository.Create(ctx, goal.Goal{OwnerID: testOwnerID, Title: uniqueLabel("outbox-deadletter"), Priority: 3, Status: goal.Active})
	if err != nil {
		t.Fatalf("create Goal: %v", err)
	}
	goalIDs := []string{goalItem.ID}
	t.Cleanup(func() { cleanupIntegrationRows(t, context.Background(), pool, goalIDs, nil, nil, nil, nil, nil) })
	if _, err := store.NewGoalJobStore(pool).CreateOrWake(ctx, goalItem.ID, 10); err != nil {
		t.Fatalf("create Goal Job: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE event_outbox SET published_at=now() WHERE published_at IS NULL"); err != nil {
		t.Fatalf("mark unrelated setup events delivered: %v", err)
	}
	var eventID string
	if err := pool.QueryRow(ctx, `INSERT INTO event_outbox(event_type,aggregate_type,aggregate_id,payload)
		VALUES('goal.updated','goals',$1,'{}') RETURNING id::text`, goalItem.ID).Scan(&eventID); err != nil {
		t.Fatalf("insert test event: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION test_fail_goal_job_wakeup() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected Goal Job wake failure'; END; $$`); err != nil {
		t.Fatalf("install wake failure trigger: %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE TRIGGER test_fail_goal_job_wakeup BEFORE UPDATE ON goal_jobs FOR EACH ROW EXECUTE FUNCTION test_fail_goal_job_wakeup()"); err != nil {
		t.Fatalf("install wake failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS test_fail_goal_job_wakeup ON goal_jobs")
		_, _ = pool.Exec(context.Background(), "DROP FUNCTION IF EXISTS test_fail_goal_job_wakeup()")
	})

	dispatcher := store.NewOutboxDispatcherStore(pool)
	if _, err := dispatcher.DispatchBatch(ctx, "dispatcher-failure-a", 1, time.Second, 2); err == nil || !strings.Contains(err.Error(), "injected Goal Job wake failure") {
		t.Fatalf("first injected delivery error=%v", err)
	}
	var attempts int
	var deadLettered *time.Time
	if err := pool.QueryRow(ctx, "SELECT attempts,dead_lettered_at FROM event_outbox WHERE id=$1", eventID).Scan(&attempts, &deadLettered); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || deadLettered != nil {
		t.Fatalf("first failure should be retryable: attempts=%d dead_lettered=%v", attempts, deadLettered)
	}
	if _, err := pool.Exec(ctx, "UPDATE event_outbox SET next_attempt_at=now() WHERE id=$1", eventID); err != nil {
		t.Fatal(err)
	}
	_, secondDispatchErr := dispatcher.DispatchBatch(ctx, "dispatcher-failure-b", 1, time.Second, 2)
	if secondDispatchErr == nil {
		t.Fatal("second delivery failure was not reported")
	}
	if err := pool.QueryRow(ctx, "SELECT attempts,dead_lettered_at FROM event_outbox WHERE id=$1", eventID).Scan(&attempts, &deadLettered); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || deadLettered == nil {
		t.Fatalf("retry exhaustion did not dead-letter event: attempts=%d dead_lettered=%v dispatch_err=%v", attempts, deadLettered, secondDispatchErr)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_records WHERE action='outbox.dead_letter' AND resource_id=$1", eventID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("dead-letter audit count=%d err=%v", auditCount, err)
	}
}
