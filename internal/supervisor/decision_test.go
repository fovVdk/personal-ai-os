// English: These tests check bounded deterministic Supervisor decisions.
// 中文：本测试验证 Supervisor 决策从 Evaluation 确定性生成并且指纹稳定。
package supervisor

import (
	"testing"
	"time"
)

func TestNewDecisionMapsWaitAndBudgetActions(t *testing.T) {
	base := Evaluation{GoalID: "goal-1", GoalUpdatedAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), Status: EvaluationBlocked, Reason: "approval", RecommendedAction: ActionWaitApproval}
	decision, err := NewDecision(base)
	if err != nil || decision.Type != DecisionWaitApproval || decision.Status != "waiting" {
		t.Fatalf("approval decision=%+v err=%v", decision, err)
	}
	base.RecommendedAction = ActionStop
	base.BudgetSummary = EvaluationBudgetSummary{ToolCallsUsed: 2, ToolCallsLimit: 2}
	decision, err = NewDecision(base)
	if err != nil || decision.Type != DecisionBudgetExhausted {
		t.Fatalf("budget decision=%+v err=%v", decision, err)
	}
}

func TestNewDecisionProgressFingerprintChangesWithFacts(t *testing.T) {
	base := Evaluation{GoalID: "goal-2", GoalUpdatedAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), Status: EvaluationInProgress, RecommendedAction: ActionContinue, Reason: "ready"}
	first, err := NewDecision(base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDecision(base)
	if err != nil || first.ProgressFingerprint != second.ProgressFingerprint {
		t.Fatalf("same facts must have same fingerprint: %+v %+v err=%v", first, second, err)
	}
	base.UnmetCriteria = []CriterionAssessment{{CriterionID: "criterion", Status: "insufficient_evidence"}}
	third, err := NewDecision(base)
	if err != nil || third.ProgressFingerprint == first.ProgressFingerprint {
		t.Fatalf("changed facts did not change fingerprint: %+v err=%v", third, err)
	}
}
