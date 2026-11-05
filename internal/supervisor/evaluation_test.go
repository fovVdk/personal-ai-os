// English: These tests ensure evaluations remain conservative and evidence-based.
// 中文：本测试确保评估保持保守，并严格基于证据。
package supervisor

import (
	"context"
	"testing"
	"time"

	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/task"
)

func TestEvaluateGoalSnapshotRequiresCriteriaAndDoesNotInventProgress(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	item := goal.Goal{ID: "goal-a", Title: "Goal", Status: goal.Active, UpdatedAt: now}
	evaluation := EvaluateGoalSnapshot(item, nil, nil, UsageTotals{}, Budgets{}, now)
	if evaluation.Status != EvaluationNeedsCriteria || evaluation.RecommendedAction != ActionReplan {
		t.Fatalf("evaluation=%+v, want missing-criteria replan", evaluation)
	}
	if evaluation.CompletionPercentage != nil {
		t.Fatalf("unknown progress must not have a fabricated percentage: %d", *evaluation.CompletionPercentage)
	}
	if evaluation.PlanVersionKnown || evaluation.PlanVersion != 0 {
		t.Fatalf("legacy tasks must not claim a known plan version: %+v", evaluation)
	}
}

func TestEvaluateGoalSnapshotRecognizesApprovalFailureAndDependencies(t *testing.T) {
	item := goal.Goal{ID: "goal-b", Status: goal.Active, SuccessCriteria: []string{"criterion"}}
	tasks := []task.Task{
		{ID: "approval", GoalID: item.ID, Title: "Needs approval", Status: task.WaitingApproval},
		{ID: "failed", GoalID: item.ID, Title: "Failed", Status: task.Failed},
		{ID: "dependent", GoalID: item.ID, Title: "Dependent", Status: task.Created, DependsOn: []string{"failed"}},
	}
	evaluation := EvaluateGoalSnapshot(item, tasks, nil, UsageTotals{}, Budgets{}, time.Now())
	if evaluation.RecommendedAction != ActionWaitApproval || len(evaluation.PendingApprovals) != 1 {
		t.Fatalf("approval state not recognized: %+v", evaluation)
	}
	if len(evaluation.FailedTasks) != 1 || len(evaluation.PendingDependencies) != 1 {
		t.Fatalf("failure/dependency state not recognized: %+v", evaluation)
	}
}

func TestEvaluateGoalSnapshotLeavesLegacyFreeTextCriteriaUnknown(t *testing.T) {
	item := goal.Goal{ID: "goal-c", Status: goal.Active, SuccessCriteria: []string{"report is independently checked"}}
	tasks := []task.Task{{ID: "task-1", GoalID: item.ID, Title: "Prepare report", Status: task.Completed}}
	runs := []Execution{{
		TaskID: "task-1", Outcome: agentruntime.OutcomeSucceeded, Verified: true, VerificationMethod: "test-verifier/v1",
		Evidence: []Evidence{{TaskID: "task-1", Source: "test-verifier", Description: "task artifact checked"}},
	}}
	evaluation := EvaluateGoalSnapshot(item, tasks, runs, UsageTotals{}, Budgets{}, time.Now())
	if evaluation.RecommendedAction != ActionEscalate || evaluation.Status != EvaluationBlocked {
		t.Fatalf("verified Task execution must not satisfy legacy free-text criteria: %+v", evaluation)
	}
	if evaluation.CompletionPercentage != nil || len(evaluation.CompletedCriteria) != 0 || len(evaluation.UnmetCriteria) != 1 || evaluation.UnmetCriteria[0].Status != "unknown" {
		t.Fatalf("free-form criteria must remain unknown until verifier stage: %+v", evaluation)
	}
}

func TestEvaluateGoalSnapshotStopsAtBudgetAndRejectsUnverifiedEvidence(t *testing.T) {
	item := goal.Goal{ID: "goal-d", Status: goal.Active, SuccessCriteria: []string{"criterion"}}
	tasks := []task.Task{{ID: "task-1", GoalID: item.ID, Title: "Claimed done", Status: task.Completed}}
	unverified := []Execution{{TaskID: "task-1", Outcome: agentruntime.OutcomeSucceeded, Evidence: []Evidence{{Source: "agent", Description: "agent claims completion"}}}}
	budgetEvaluation := EvaluateGoalSnapshot(item, tasks, unverified, UsageTotals{ToolCalls: 2}, Budgets{MaxToolCalls: 2}, time.Now())
	if budgetEvaluation.RecommendedAction != ActionStop || budgetEvaluation.EvidenceSummary.VerifiedExecutions != 0 {
		t.Fatalf("budget/evidence safety failed: %+v", budgetEvaluation)
	}
}

func TestEvaluateGoalSnapshotVerifiesTypedEvidenceCriteria(t *testing.T) {
	item := goal.Goal{
		ID: "goal-typed", Status: goal.Active,
		Criteria: []goal.SuccessCriterion{{
			ID: "sources", Name: "Two cited sources", Type: goal.CriterionVerifiedEvidenceCount,
			Required: true, ValidationMethod: goal.CriterionMethodVerifiedExecutionEvidence,
			MinEvidence: 2, EvidenceSources: []string{"research"}, RequireReference: true,
			Source: goal.CriterionSourceUser, Confirmed: true, Version: 1,
		}},
	}
	tasks := []task.Task{{ID: "task-1", GoalID: item.ID, Title: "Research", Status: task.Completed}}
	runs := []Execution{{
		ID: "run-1", TaskID: "task-1", Outcome: agentruntime.OutcomeSucceeded, Verified: true, VerificationMethod: "test-verifier/v1",
		Evidence: []Evidence{
			{EvidenceID: "evidence-1", CriterionID: "sources", CriterionVersion: 1, Source: "research", Description: "source one", Reference: "https://example.test/1"},
			{EvidenceID: "evidence-2", CriterionID: "sources", CriterionVersion: 1, Source: "research", Description: "source two", Reference: "https://example.test/2"},
			{EvidenceID: "evidence-3", Source: "agent", Description: "must not count", Reference: "model://claim"},
		},
	}}
	evaluation := EvaluateGoalSnapshot(item, tasks, runs, UsageTotals{}, Budgets{}, time.Now())
	if evaluation.RecommendedAction != ActionVerifyCompletion || len(evaluation.CompletedCriteria) != 1 || evaluation.CompletedCriteria[0].CriterionID != "sources" || len(evaluation.CompletedCriteria[0].Evidence) != 2 {
		t.Fatalf("typed criterion not verified against matching evidence: %+v", evaluation)
	}
	if evaluation.CompletedCriteria[0].VerifierType != "core.execution-evidence" || evaluation.CompletedCriteria[0].VerifierVersion == "" || len(evaluation.CompletedCriteria[0].EvidenceHashes) != 2 {
		t.Fatalf("verification provenance/hashes missing: %+v", evaluation.CompletedCriteria[0])
	}

	// Duplicated representations of the same persisted Evidence UUID only count once.
	// 中文：同一持久化 Evidence UUID 的重复表示只能计数一次。
	runs[0].Evidence = append(runs[0].Evidence, runs[0].Evidence[0])
	duplicate := EvaluateGoalSnapshot(item, tasks, runs, UsageTotals{}, Budgets{}, time.Now())
	if len(duplicate.CompletedCriteria) != 1 || len(duplicate.CompletedCriteria[0].Evidence) != 2 {
		t.Fatalf("duplicate Evidence UUID counted more than once: %+v", duplicate.CompletedCriteria)
	}

	runs[0].Verified = false
	insufficient := EvaluateGoalSnapshot(item, tasks, runs, UsageTotals{}, Budgets{}, time.Now())
	if len(insufficient.CompletedCriteria) != 0 || len(insufficient.UnmetCriteria) != 1 || insufficient.UnmetCriteria[0].Status != "insufficient_evidence" {
		t.Fatalf("unverified execution was accepted: %+v", insufficient)
	}
}

func TestEvaluateGoalSnapshotTaskCriterionRequiresTaskAndPersistedEvidence(t *testing.T) {
	item := goal.Goal{ID: "goal-task-rule", Status: goal.Active, Criteria: []goal.SuccessCriterion{{
		ID: "task-proof", Name: "Research task verified", Type: goal.CriterionVerifiedTaskCompletion,
		Required: true, ValidationMethod: goal.CriterionMethodVerifiedExecutionEvidence,
		TaskIDs: []string{"task-1"}, Source: goal.CriterionSourceUser, Confirmed: true, Version: 2,
	}}}
	tasks := []task.Task{{ID: "task-1", GoalID: item.ID, Title: "Research", Status: task.Completed, CriterionIDs: []string{"task-proof"}}}
	runs := []Execution{{ID: "run-1", TaskID: "task-1", Outcome: agentruntime.OutcomeSucceeded, Verified: true, VerificationMethod: "core-test-verifier/v1", Evidence: []Evidence{{EvidenceID: "ev-1", TaskID: "task-1", CriterionID: "task-proof", CriterionVersion: 2, Source: "research", Description: "verified source snapshot"}}}}
	evaluation := EvaluateGoalSnapshot(item, tasks, runs, UsageTotals{}, Budgets{}, time.Now())
	if len(evaluation.CompletedCriteria) != 1 || evaluation.CompletedCriteria[0].CriterionVersion != 2 || len(evaluation.CompletedCriteria[0].Evidence) != 1 {
		t.Fatalf("task completion criterion not satisfied by verified facts: %+v", evaluation)
	}

	tasks[0].GoalID = "other-goal"
	evaluation = EvaluateGoalSnapshot(item, tasks, runs, UsageTotals{}, Budgets{}, time.Now())
	if len(evaluation.CompletedCriteria) != 0 || evaluation.UnmetCriteria[0].Status != "insufficient_evidence" {
		t.Fatalf("cross-goal Task was accepted: %+v", evaluation)
	}
}

func TestEvaluateGoalSnapshotRequiresOwnerConfirmation(t *testing.T) {
	item := goal.Goal{ID: "goal-unconfirmed", Status: goal.Active, Criteria: []goal.SuccessCriterion{{
		ID: "criterion", Name: "Evidence criterion", Type: goal.CriterionVerifiedEvidenceCount,
		Required: true, ValidationMethod: goal.CriterionMethodVerifiedExecutionEvidence, MinEvidence: 1,
		Source: goal.CriterionSourceModel, Confirmed: false, Version: 1,
	}}}
	evaluation := EvaluateGoalSnapshot(item, nil, nil, UsageTotals{}, Budgets{}, time.Now())
	if len(evaluation.UnmetCriteria) != 1 || evaluation.UnmetCriteria[0].Status != "unknown" || evaluation.RecommendedAction == ActionVerifyCompletion {
		t.Fatalf("unconfirmed proposal was treated as an acceptance rule: %+v", evaluation)
	}
}

type evaluationStoreFake struct {
	items []Evaluation
}

type decisionStoreFake struct{ items []Decision }

func (s *decisionStoreFake) SaveDecision(_ context.Context, item Decision) (Decision, error) {
	item.ID = "decision-test"
	s.items = append(s.items, item)
	return item, nil
}
func (s *decisionStoreFake) ListDecisions(_ context.Context, _ string, limit int) ([]Decision, error) {
	if limit > len(s.items) {
		limit = len(s.items)
	}
	return append([]Decision(nil), s.items[:limit]...), nil
}

func (s *evaluationStoreFake) SaveEvaluation(_ context.Context, item Evaluation) (Evaluation, error) {
	item.ID = "evaluation-test"
	s.items = append(s.items, item)
	return item, nil
}

func (s *evaluationStoreFake) ListEvaluations(_ context.Context, _ string, limit int) ([]Evaluation, error) {
	if limit > len(s.items) {
		limit = len(s.items)
	}
	return append([]Evaluation(nil), s.items[:limit]...), nil
}

func TestEvaluateGoalPersistsSnapshotAndHistory(t *testing.T) {
	now := time.Now().UTC()
	goalRepo := &goalsRepo{item: goal.Goal{ID: "goal-eval", OwnerID: localOwner, Title: "Goal", Priority: 3, Status: goal.Active, UpdatedAt: now, SuccessCriteria: []string{"criterion"}}}
	taskRepo := &tasksRepo{items: []task.Task{{ID: "task-eval", GoalID: "goal-eval", Title: "Work", Priority: 3, Status: task.Ready}}}
	executions := &executionRepo{}
	stored := &evaluationStoreFake{}
	decisions := &decisionStoreFake{}
	service := Supervisor{Goals: goalRepo, Tasks: taskRepo, Executions: executions, Evaluations: stored, Decisions: decisions, Budgets: Budgets{MaxModelCalls: 10, MaxModelTokens: 1000, MaxToolCalls: 10}}
	evaluation, err := service.EvaluateGoal(context.Background(), goalRepo.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.ID != "evaluation-test" || evaluation.DecisionID != "decision-test" || evaluation.GoalID != goalRepo.item.ID || len(stored.items) != 1 || len(decisions.items) != 1 {
		t.Fatalf("evaluation persistence failed: result=%+v stored=%d", evaluation, len(stored.items))
	}
	history, err := service.ListEvaluations(context.Background(), goalRepo.item.ID, 10)
	if err != nil || len(history) != 1 || history[0].ID != evaluation.ID {
		t.Fatalf("evaluation history=%+v err=%v", history, err)
	}
}
