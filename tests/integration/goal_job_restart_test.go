package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"personal-ai-os/internal/app/store"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/supervisor"
	"personal-ai-os/migrations"
)

func TestGoalJobPersistsAcrossCoreRestart(t *testing.T) {
	pool, repository := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	goalItem, err := repository.Create(ctx, goal.Goal{OwnerID: testOwnerID, Title: uniqueLabel("job-restart"), Priority: 3, Status: goal.Active})
	if err != nil {
		t.Fatalf("create Goal: %v", err)
	}
	goalIDs := []string{goalItem.ID}
	t.Cleanup(func() { cleanupIntegrationRows(t, context.Background(), pool, goalIDs, nil, nil, nil, nil, nil) })
	jobs := store.NewGoalJobStore(pool)
	job, err := jobs.CreateOrWake(ctx, goalItem.ID, 10)
	if err != nil {
		t.Fatalf("create Goal Job: %v", err)
	}
	job, err = jobs.Pause(ctx, testOwnerID, job.ID)
	if err != nil || job.Status != supervisor.JobPaused {
		t.Fatalf("pause Goal Job: %+v err=%v", job, err)
	}

	service := startCoreService(t, ctx, os.Getenv("PAIOS_TEST_DATABASE_URL"))
	assertJobVisible := func() {
		t.Helper()
		var response struct {
			Items []supervisor.GoalJob `json:"items"`
		}
		service.request(t, "GET", "/api/v1/goals/"+goalItem.ID+"/jobs", nil, 200, &response)
		if len(response.Items) != 1 || response.Items[0].ID != job.ID || response.Items[0].Status != supervisor.JobPaused {
			t.Fatalf("restarted Core Goal Job view=%+v, want the same paused durable Job", response.Items)
		}
	}
	assertJobVisible()
	service.stop(t)
	service = startCoreService(t, ctx, os.Getenv("PAIOS_TEST_DATABASE_URL"))
	defer service.stop(t)
	assertJobVisible()
}
