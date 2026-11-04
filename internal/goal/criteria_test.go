// English: These tests validate typed success criteria and reject unverifiable rule shapes.
// 中文：本测试验证结构化成功标准，并拒绝不支持的核验规则形状。
package goal

import "testing"

func TestValidateSuccessCriteriaSupportedRules(t *testing.T) {
	valid := []SuccessCriterion{
		{ID: "sources", Name: "At least two cited sources", Type: CriterionVerifiedEvidenceCount, Required: true, ValidationMethod: CriterionMethodVerifiedExecutionEvidence, MinEvidence: 2, EvidenceSources: []string{"research"}, RequireReference: true, Source: CriterionSourceUser, Confirmed: true, Version: 1},
		{ID: "review", Name: "Review task completed", Type: CriterionVerifiedTaskCompletion, ValidationMethod: CriterionMethodVerifiedExecutionEvidence, TaskIDs: []string{"task-review"}, Source: CriterionSourceUser, Confirmed: true, Version: 1},
	}
	if err := ValidateSuccessCriteria(valid); err != nil {
		t.Fatalf("valid criteria rejected: %v", err)
	}
}

func TestValidateSuccessCriteriaRejectsUnsupportedAndAmbiguousRules(t *testing.T) {
	cases := map[string][]SuccessCriterion{
		"missing-required": {{ID: "optional", Name: "Optional", Type: CriterionVerifiedEvidenceCount, ValidationMethod: CriterionMethodVerifiedExecutionEvidence, MinEvidence: 1, Source: CriterionSourceUser, Version: 1}},
		"duplicate-id": {
			{ID: "same", Name: "First", Type: CriterionVerifiedEvidenceCount, Required: true, ValidationMethod: CriterionMethodVerifiedExecutionEvidence, MinEvidence: 1, Source: CriterionSourceUser, Version: 1},
			{ID: "same", Name: "Second", Type: CriterionVerifiedEvidenceCount, ValidationMethod: CriterionMethodVerifiedExecutionEvidence, MinEvidence: 1, Source: CriterionSourceUser, Version: 1},
		},
		"unknown-type":       {{ID: "unknown", Name: "Unknown", Type: "model_says_done", Required: true, ValidationMethod: CriterionMethodVerifiedExecutionEvidence, Source: CriterionSourceUser, Version: 1}},
		"invalid-method":     {{ID: "method", Name: "Method", Type: CriterionVerifiedEvidenceCount, Required: true, ValidationMethod: "llm_confidence", MinEvidence: 1, Source: CriterionSourceUser, Version: 1}},
		"bad-evidence-count": {{ID: "count", Name: "Count", Type: CriterionVerifiedEvidenceCount, Required: true, ValidationMethod: CriterionMethodVerifiedExecutionEvidence, MinEvidence: 0, Source: CriterionSourceUser, Version: 1}},
		"empty-task-list":    {{ID: "tasks", Name: "Tasks", Type: CriterionVerifiedTaskCompletion, Required: true, ValidationMethod: CriterionMethodVerifiedExecutionEvidence, Source: CriterionSourceUser, Version: 1}},
		"artifact-traversal": {{ID: "artifact", Name: "Artifact", Type: CriterionArtifactExists, Required: true, ValidationMethod: CriterionMethodArtifactInStorage, ArtifactPath: "../outside.md", MaxArtifactBytes: 1024, Source: CriterionSourceUser, Confirmed: true, Version: 1}},
	}
	for name, criteria := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSuccessCriteria(criteria); err == nil {
				t.Fatal("expected invalid success criteria to be rejected")
			}
		})
	}
}
