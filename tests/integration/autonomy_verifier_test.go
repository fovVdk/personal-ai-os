// English: This PostgreSQL acceptance verifies Artifact hashes again at Goal completion.
// 中文：本 PostgreSQL 验收验证 Goal 完成时重新检查 Artifact Hash 并拒绝陈旧证据。
package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/app/store"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/supervisor"
	"personal-ai-os/internal/source"
	"personal-ai-os/internal/task"
	"personal-ai-os/internal/verification"
	"personal-ai-os/migrations"
)

func TestPostgresArtifactVerificationHashGate(t *testing.T) {
	pool, repository := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	root := t.TempDir()
	artifactPath := "reports/comparison.md"
	content := []byte("# Summary\nSummary.\n## Options\nThree options.\n## Sources\nEvidence.\n")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, artifactPath)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, artifactPath), content, 0o600); err != nil {
		t.Fatal(err)
	}
	verifier, err := verification.NewArtifactVerifier(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	repository.SetArtifactFreshnessVerifier(verifier)
	goalItem, err := repository.Create(ctx, goal.Goal{OwnerID: testOwnerID, Title: uniqueLabel("artifact-verification-goal"), Priority: 3, Status: goal.Active, Criteria: []goal.SuccessCriterion{{
		ID: "final-report", Name: "Final report has the required structure", Type: goal.CriterionMarkdownStructure, Required: true,
		ValidationMethod: goal.CriterionMethodArtifactInStorage, ArtifactPath: artifactPath, RequiredSections: []string{"Summary", "Options", "Sources"}, MaxArtifactBytes: 1 << 20,
		Source: goal.CriterionSourceUser, Confirmed: true, Version: 1,
	}}})
	if err != nil {
		t.Fatalf("create Goal: %v", err)
	}
	taskRepo := repository.Tasks()
	item, err := taskRepo.Create(ctx, task.Task{GoalID: goalItem.ID, PlanKey: "final-report", CriterionIDs: []string{"final-report"}, Title: uniqueLabel("artifact-final-task"), Priority: 3, Status: task.Created})
	if err != nil {
		t.Fatalf("create artifact Task: %v", err)
	}
	goalIDs, taskIDs := []string{goalItem.ID}, []string{item.ID}
	t.Cleanup(func() { cleanupIntegrationRows(t, context.Background(), pool, goalIDs, taskIDs, nil, nil, nil, nil) })
	for _, next := range []task.Status{task.Ready, task.Running} {
		if _, err := taskRepo.Transition(ctx, item.ID, next); err != nil {
			t.Fatalf("transition Task to %s: %v", next, err)
		}
	}
	result, err := verifier.Verify(ctx, agentruntime.Request{GoalID: goalItem.ID, TaskID: item.ID, TaskCriterionIDs: item.CriterionIDs, GoalCriteria: goalItem.Criteria}, agentruntime.Result{Summary: "untrusted Agent claims it is complete"})
	if err != nil || !result.Verified || len(result.Evidence) != 1 {
		t.Fatalf("verify report artifact=%+v err=%v", result, err)
	}
	completedAt := time.Now().UTC()
	_, err = store.NewExecutionStore(pool).Save(ctx, supervisor.Execution{
		TaskID: item.ID, AgentID: "artifact-test-agent", Outcome: agentruntime.OutcomeSucceeded, Verified: true,
		VerificationMethod: result.Method, CompletedAt: completedAt,
		Evidence: []supervisor.Evidence{{TaskID: item.ID, CriterionID: result.Evidence[0].CriterionID, CriterionVersion: result.Evidence[0].CriterionVersion, Source: result.Evidence[0].Source, Description: result.Evidence[0].Description, Reference: result.Evidence[0].Reference, ContentHash: result.Evidence[0].ContentHash, VerifierType: result.Evidence[0].VerifierType, VerifierVersion: result.Evidence[0].VerifierVersion, ObservedAt: completedAt}},
	})
	if err != nil {
		t.Fatalf("persist Core verified artifact evidence: %v", err)
	}
	if _, err := taskRepo.Transition(ctx, item.ID, task.Completed); err != nil {
		t.Fatalf("complete independently verified Task: %v", err)
	}
	service := supervisor.Supervisor{Goals: repository, Tasks: taskRepo, Executions: store.NewExecutionStore(pool), Evaluations: store.NewEvaluationStore(pool), Decisions: store.NewDecisionStore(pool), Usage: store.NewModelUsageStore(pool), Budgets: supervisor.Budgets{MaxModelCalls: 20, MaxModelTokens: 10000, MaxToolCalls: 20}}
	evaluation, err := service.EvaluateGoal(ctx, goalItem.ID)
	if err != nil || evaluation.RecommendedAction != supervisor.ActionVerifyCompletion || len(evaluation.CompletedCriteria) != 1 || len(evaluation.CompletedCriteria[0].EvidenceHashes) != 1 || evaluation.CompletedCriteria[0].EvidenceHashes[0] != result.Evidence[0].ContentHash {
		t.Fatalf("artifact Goal Evaluation=%+v err=%v", evaluation, err)
	}
	if evaluation.CompletedCriteria[0].VerificationID == "" {
		t.Fatal("artifact criterion Verification ID was not persisted")
	}
	if err := os.WriteFile(filepath.Join(root, artifactPath), append(content, []byte("mutated after evaluation")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Transition(ctx, testOwnerID, goalItem.ID, goal.Completed, nil); !errors.Is(err, goal.ErrCriteriaUnverified) {
		t.Fatalf("stale Artifact Hash completion error=%v, want criteria verification failure", err)
	}
	if err := os.WriteFile(filepath.Join(root, artifactPath), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyArtifactHash(ctx, artifactPath, result.Evidence[0].ContentHash); err != nil {
		t.Fatalf("restored artifact does not match verified hash: %v", err)
	}
	completedGoal, err := repository.Transition(ctx, testOwnerID, goalItem.ID, goal.Completed, []goal.Evidence{{Source: "caller", Description: "untrusted claim", Reference: "model://claim"}})
	if err != nil || completedGoal.Status != goal.Completed || len(completedGoal.Evidence) != 1 || completedGoal.Evidence[0].Source != "structured-criterion-verifier" {
		t.Fatalf("artifact-verified Goal completion=%+v err=%v", completedGoal, err)
	}
	var verificationRows, finalLinkRows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM criterion_verifications WHERE goal_id=$1 AND status='satisfied' AND criterion_version=1", goalItem.ID).Scan(&verificationRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM criterion_verification_evidence link JOIN criterion_verifications v ON v.id=link.verification_id WHERE v.goal_id=$1", goalItem.ID).Scan(&finalLinkRows); err != nil {
		t.Fatal(err)
	}
	if verificationRows != 1 || finalLinkRows != 1 {
		t.Fatalf("verification history rows=%d evidence links=%d", verificationRows, finalLinkRows)
	}
}

func TestPostgresSourceReferenceFreshnessRecheckedAtGoalCompletion(t *testing.T) {
	pool, repository := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	root := t.TempDir()
	artifactPath := "reports/source-report.md"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, artifactPath)), 0o700); err != nil {
		t.Fatal(err)
	}
	verifier, err := verification.NewArtifactVerifier(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	verifier.Sources = repository.Sources()
	verifier.Now = func() time.Time { return time.Now().UTC() }
	repository.SetArtifactFreshnessVerifier(verifier)
	goalItem, err := repository.Create(ctx, goal.Goal{OwnerID: testOwnerID, Title: uniqueLabel("source-freshness-gate"), Priority: 3, Status: goal.Active, Criteria: []goal.SuccessCriterion{{
		ID: "report", Name: "Report with a current source reference", Type: goal.CriterionMarkdownStructure, Required: true,
		ValidationMethod: goal.CriterionMethodArtifactInStorage, ArtifactPath: artifactPath, RequiredSections: []string{"Research"},
		MaxArtifactBytes: 1 << 20, RequireReference: true, Source: goal.CriterionSourceUser, Confirmed: true, Version: 1,
	}}})
	if err != nil {
		t.Fatalf("create Goal: %v", err)
	}
	taskRepo := repository.Tasks()
	item, err := taskRepo.Create(ctx, task.Task{GoalID: goalItem.ID, PlanKey: "report", CriterionIDs: []string{"report"}, Title: uniqueLabel("source-report-task"), Priority: 3, Status: task.Created})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	goalIDs, taskIDs := []string{goalItem.ID}, []string{item.ID}
	t.Cleanup(func() { cleanupIntegrationRows(t, context.Background(), pool, goalIDs, taskIDs, nil, nil, nil, nil) })
	for _, next := range []task.Status{task.Ready, task.Running} {
		if _, err := taskRepo.Transition(ctx, item.ID, next); err != nil {
			t.Fatalf("transition Task to %s: %v", next, err)
		}
	}
	content := []byte("Official project notes describe the modular controller and autonomous navigation.")
	quote := "modular controller and autonomous navigation"
	retrievedAt := time.Now().UTC().Add(-source.DefaultSnapshotMaxAge - time.Hour)
	snapshot, err := repository.Sources().Create(ctx, source.Snapshot{
		OwnerID: testOwnerID, GoalID: goalItem.ID, TaskID: item.ID,
		OriginalURL: "https://research.example/project", FinalURL: "https://research.example/project",
		HTTPStatus: 200, MIMEType: "text/plain", RetrievedAt: retrievedAt, ContentHash: source.Hash(content),
		RawContent: content, NormalizedContent: string(content), FetcherVersion: source.FetcherVersion,
	})
	if err != nil {
		t.Fatalf("persist old source snapshot: %v", err)
	}
	if _, err := repository.Sources().AddReference(ctx, source.Reference{
		OwnerID: testOwnerID, GoalID: goalItem.ID, TaskID: item.ID, SnapshotID: snapshot.ID,
		ArtifactPath: artifactPath, CitationURL: snapshot.OriginalURL, Quote: quote, QuoteHash: source.Hash([]byte(quote)),
	}); err != nil {
		t.Fatalf("persist source reference: %v", err)
	}
	report := "# Research\n\n" + quote + ". [Source](source://" + snapshot.ID + ") " + snapshot.OriginalURL + "\n"
	if err := os.WriteFile(filepath.Join(root, artifactPath), []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	// Simulate a verifier that accepted the snapshot at its recorded retrieval
	// instant, then exercise the independent DB-time completion gate later.
	verifier.Now = func() time.Time { return retrievedAt.Add(time.Minute) }
	result, err := verifier.Verify(ctx, agentruntime.Request{OwnerID: testOwnerID, GoalID: goalItem.ID, TaskID: item.ID, TaskCriterionIDs: item.CriterionIDs, GoalCriteria: goalItem.Criteria}, agentruntime.Result{})
	if err != nil || !result.Verified || len(result.Evidence) != 2 {
		t.Fatalf("initial Artifact/Reference verification=%+v err=%v", result, err)
	}
	completedAt := time.Now().UTC()
	evidence := make([]supervisor.Evidence, 0, len(result.Evidence))
	for _, evidenceItem := range result.Evidence {
		evidence = append(evidence, supervisor.Evidence{TaskID: item.ID, CriterionID: evidenceItem.CriterionID, CriterionVersion: evidenceItem.CriterionVersion, Source: evidenceItem.Source, Description: evidenceItem.Description, Reference: evidenceItem.Reference, ContentHash: evidenceItem.ContentHash, VerifierType: evidenceItem.VerifierType, VerifierVersion: evidenceItem.VerifierVersion, ObservedAt: completedAt})
	}
	if _, err := store.NewExecutionStore(pool).Save(ctx, supervisor.Execution{TaskID: item.ID, AgentID: "source-test-agent", Outcome: agentruntime.OutcomeSucceeded, Verified: true, VerificationMethod: result.Method, CompletedAt: completedAt, Evidence: evidence}); err != nil {
		t.Fatalf("persist source-verified execution: %v", err)
	}
	if _, err := taskRepo.Transition(ctx, item.ID, task.Completed); err != nil {
		t.Fatalf("complete Task: %v", err)
	}
	service := supervisor.Supervisor{Goals: repository, Tasks: taskRepo, Executions: store.NewExecutionStore(pool), Evaluations: store.NewEvaluationStore(pool), Decisions: store.NewDecisionStore(pool), Usage: store.NewModelUsageStore(pool), Budgets: supervisor.Budgets{MaxModelCalls: 20, MaxModelTokens: 10000, MaxToolCalls: 20}}
	evaluation, err := service.EvaluateGoal(ctx, goalItem.ID)
	if err != nil || evaluation.RecommendedAction != supervisor.ActionVerifyCompletion {
		t.Fatalf("source Goal Evaluation=%+v err=%v", evaluation, err)
	}
	if _, err := repository.Transition(ctx, testOwnerID, goalItem.ID, goal.Completed, nil); !errors.Is(err, goal.ErrCriteriaUnverified) {
		t.Fatalf("stale source snapshot completion error=%v, want criteria verification failure", err)
	}
}
