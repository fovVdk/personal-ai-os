// English: These tests validate Plan Proposal DAG mappings, budgets, and stable fingerprints.
// 中文：本测试验证 Plan Proposal 的标准映射、任务预算和指纹确定性。
package planner

import (
	"testing"
	"time"

	"personal-ai-os/internal/goal"
)

func TestPlanProposalRequiresMappingEveryRequiredCriterion(t *testing.T) {
	criteria := []goal.SuccessCriterion{{ID: "sources", Name: "Sources", Type: goal.CriterionVerifiedEvidenceCount, Required: true, ValidationMethod: goal.CriterionMethodVerifiedExecutionEvidence, MinEvidence: 2, Version: 1}}
	proposal := PlanProposal{GoalID: "goal-1", BasePlanVersion: 0, ProposedVersion: 1, Trigger: "initial", Reason: "criteria unmet", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Plan: Plan{GoalID: "goal-1", Summary: "plan", Tasks: []TaskSpec{{Key: "research", Title: "Research sources"}}}}
	if err := proposal.Validate(criteria, 0, 10); err == nil {
		t.Fatal("proposal without required criterion mapping was accepted")
	}
	proposal.Plan.Tasks[0].CriterionIDs = []string{"sources"}
	proposal.Plan.Tasks[0].EvidenceRequirements = []string{"verified source snapshots"}
	if err := proposal.Validate(criteria, 0, 10); err != nil {
		t.Fatalf("valid mapped proposal rejected: %v", err)
	}
}

func TestPlanProposalRejectsUnknownMappingAndVersionConflict(t *testing.T) {
	proposal := PlanProposal{GoalID: "goal-1", BasePlanVersion: 3, ProposedVersion: 5, Trigger: "replan", Reason: "stale", Plan: Plan{GoalID: "goal-1", Summary: "plan", Tasks: []TaskSpec{{Key: "task", Title: "Task"}}}}
	if err := proposal.Validate(nil, 0, 10); err == nil {
		t.Fatal("non-monotonic Plan Version was accepted")
	}
	proposal.ProposedVersion = 4
	proposal.Plan.Tasks[0].CriterionIDs = []string{"ghost"}
	if err := proposal.Validate(nil, 0, 10); err == nil {
		t.Fatal("unknown criterion mapping was accepted")
	}
}

func TestPlanProposalFingerprintIsDeterministic(t *testing.T) {
	proposal := PlanProposal{GoalID: "goal-1", BasePlanVersion: 1, Trigger: "failure", Reason: "task failed", Plan: Plan{GoalID: "goal-1", Summary: "revised", Tasks: []TaskSpec{{Key: "retry", Title: "Retry safely"}}}}
	first, err := proposal.ComputeFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	second, err := proposal.ComputeFingerprint()
	if err != nil || first == "" || first != second {
		t.Fatalf("fingerprint=%q second=%q err=%v", first, second, err)
	}
}

func TestPlanProposalCanRetainExistingMappingsAndAddGapTask(t *testing.T) {
	criteria := []goal.SuccessCriterion{
		{ID: "sources", Name: "Sources", Type: goal.CriterionVerifiedEvidenceCount, Required: true, ValidationMethod: goal.CriterionMethodVerifiedExecutionEvidence, MinEvidence: 2, Source: goal.CriterionSourceUser, Confirmed: true, Version: 1},
		{ID: "report", Name: "Report", Type: goal.CriterionArtifactExists, Required: true, ValidationMethod: goal.CriterionMethodArtifactInStorage, ArtifactPath: "reports/final.md", MaxArtifactBytes: 4096, Source: goal.CriterionSourceUser, Confirmed: true, Version: 1},
	}
	proposal := PlanProposal{GoalID: "goal-1", BasePlanVersion: 1, ProposedVersion: 2, Trigger: "insufficient_evidence", Reason: "missing one source", RetainedTaskIDs: []string{"task-done"}, Plan: Plan{
		GoalID: "goal-1", Summary: "add one source and verify final artifact", Tasks: []TaskSpec{
			{Key: "research", Title: "Collect sources", CriterionIDs: []string{"sources"}, EvidenceRequirements: []string{"persisted source snapshots"}},
			{Key: "final-check", Title: "Check report", DependsOn: []string{"research"}, CriterionIDs: []string{"report"}, EvidenceRequirements: []string{"Core ArtifactVerifier hash and section results"}},
		},
	}}
	if err := proposal.Validate(criteria, 1, 10); err != nil {
		t.Fatalf("valid incremental proposal rejected: %v", err)
	}
}
