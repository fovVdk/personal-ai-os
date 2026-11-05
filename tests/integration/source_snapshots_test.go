// English: This PostgreSQL acceptance verifies immutable Source Snapshot persistence and exact report citation linkage.
// 中文：本 PostgreSQL 验收验证不可变来源快照持久化及报告引用的精确绑定。
package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/source"
	"personal-ai-os/internal/task"
	"personal-ai-os/migrations"
)

func TestPostgresSourceSnapshotAndReferenceVerification(t *testing.T) {
	pool, repository := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	goalItem, err := repository.Create(ctx, goal.Goal{OwnerID: testOwnerID, Title: uniqueLabel("source-snapshot"), Priority: 3, Status: goal.Active})
	if err != nil {
		t.Fatalf("create Goal: %v", err)
	}
	taskItem, err := repository.Tasks().Create(ctx, task.Task{GoalID: goalItem.ID, Title: uniqueLabel("source-capture"), Priority: 2, Status: task.Ready})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	goalIDs, taskIDs := []string{goalItem.ID}, []string{taskItem.ID}
	t.Cleanup(func() { cleanupIntegrationRows(t, context.Background(), pool, goalIDs, taskIDs, nil, nil, nil, nil) })

	repositorySources := repository.Sources()
	raw := []byte("Robotics project uses a modular controller and supports autonomous navigation.")
	snapshot, err := repositorySources.Create(ctx, source.Snapshot{
		OwnerID: testOwnerID, GoalID: goalItem.ID, TaskID: taskItem.ID,
		OriginalURL: "https://research.example/project", FinalURL: "https://research.example/project",
		HTTPStatus: 200, MIMEType: "text/plain", RetrievedAt: time.Now().UTC(), ContentHash: source.Hash(raw),
		RawContent: raw, NormalizedContent: string(raw), FetcherVersion: source.FetcherVersion,
	})
	if err != nil {
		t.Fatalf("persist immutable snapshot: %v", err)
	}
	quote := "modular controller and supports autonomous navigation"
	reference, err := repositorySources.AddReference(ctx, source.Reference{
		OwnerID: testOwnerID, GoalID: goalItem.ID, TaskID: taskItem.ID, SnapshotID: snapshot.ID,
		ArtifactPath: "reports/research.md", CitationURL: snapshot.OriginalURL, Quote: quote,
		QuoteHash: source.Hash([]byte(quote)),
	})
	if err != nil {
		t.Fatalf("persist source reference: %v", err)
	}
	if _, err := repositorySources.AddReference(ctx, source.Reference{
		OwnerID: testOwnerID, GoalID: goalItem.ID, TaskID: taskItem.ID, SnapshotID: snapshot.ID,
		ArtifactPath: "reports/research.md", CitationURL: "https://unrelated.example/", Quote: quote,
		QuoteHash: source.Hash([]byte(quote)),
	}); err == nil {
		t.Fatal("reference with unrelated URL was accepted")
	}
	if _, err := repositorySources.AddReference(ctx, source.Reference{
		OwnerID: testOwnerID, GoalID: goalItem.ID, TaskID: taskItem.ID, SnapshotID: snapshot.ID,
		ArtifactPath: "reports/research.md", CitationURL: snapshot.OriginalURL, Quote: "not present in source",
		QuoteHash: source.Hash([]byte("not present in source")),
	}); err == nil {
		t.Fatal("reference with fabricated quote was accepted")
	}

	loaded, err := repositorySources.Get(ctx, testOwnerID, goalItem.ID, snapshot.ID)
	if err != nil || loaded.ContentHash != snapshot.ContentHash || string(loaded.RawContent) != string(raw) {
		t.Fatalf("reloaded snapshot=%+v err=%v", loaded, err)
	}
	listed, err := repositorySources.List(ctx, testOwnerID, goalItem.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != snapshot.ID {
		t.Fatalf("listed snapshots=%+v err=%v", listed, err)
	}
	references, err := repositorySources.ListReferences(ctx, testOwnerID, goalItem.ID, "reports/research.md")
	if err != nil || len(references) != 1 || references[0].ID != reference.ID {
		t.Fatalf("listed source references=%+v err=%v", references, err)
	}
	verifier := source.Verifier{Repository: repositorySources, Now: func() time.Time { return time.Now().UTC() }}
	results, err := verifier.Verify(ctx, testOwnerID, goalItem.ID, "reports/research.md", []source.ReferenceInput{{SnapshotID: snapshot.ID, CitationURL: snapshot.OriginalURL, Quote: reference.Quote, QuoteHash: reference.QuoteHash}})
	if err != nil || len(results) != 1 || !results[0].Verified || results[0].SemanticSupport != "requires_independent_review" {
		t.Fatalf("verified source reference=%+v err=%v", results, err)
	}
	staleVerifier := source.Verifier{Repository: repositorySources, MaxAge: time.Minute, Now: func() time.Time { return snapshot.RetrievedAt.Add(-time.Second) }}
	results, err = staleVerifier.Verify(ctx, testOwnerID, goalItem.ID, "reports/research.md", []source.ReferenceInput{{SnapshotID: snapshot.ID, CitationURL: snapshot.OriginalURL, Quote: reference.Quote, QuoteHash: reference.QuoteHash}})
	if err != nil || len(results) != 1 || results[0].Verified || results[0].SnapshotFresh {
		t.Fatalf("stale/future snapshot was not rejected: %+v err=%v", results, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE source_snapshots SET normalized_content=$2 WHERE id=$1", snapshot.ID, strings.ToUpper(string(raw))); err == nil {
		t.Fatal("immutable source snapshot update unexpectedly succeeded")
	}
}
