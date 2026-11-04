// English: These tests exercise the real filesystem Artifact Verifier boundary.
// 中文：本测试使用真实临时文件系统验证 ArtifactVerifier 的根目录和内容规则。
package verification

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/goal"
)

func TestArtifactVerifierChecksMarkdownStructureAndHash(t *testing.T) {
	root := t.TempDir()
	artifact := "reports/comparison.md"
	content := "# Summary\nConclusion\n## Options\nOption A\n## Sources\n- source snapshot\n"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, artifact)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, artifact), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier, err := NewArtifactVerifier(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	criterion := goal.SuccessCriterion{ID: "report", Name: "Report structure", Type: goal.CriterionMarkdownStructure, Required: true, ValidationMethod: goal.CriterionMethodArtifactInStorage, ArtifactPath: artifact, RequiredSections: []string{"Summary", "Options", "Sources"}, MaxArtifactBytes: 4096, Confirmed: true, Version: 3}
	result, err := verifier.Verify(context.Background(), agentruntime.Request{TaskCriterionIDs: []string{"report"}, GoalCriteria: []goal.SuccessCriterion{criterion}}, agentruntime.Result{Summary: "model claims success"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.Method != "core.file-artifact/v1" || len(result.Evidence) != 1 || result.Evidence[0].CriterionID != "report" || result.Evidence[0].CriterionVersion != 3 || len(result.Evidence[0].ContentHash) != 64 || !strings.Contains(result.Evidence[0].Reference, "sha256="+result.Evidence[0].ContentHash) {
		t.Fatalf("artifact verification result=%+v", result)
	}
	if err := verifier.VerifyArtifactHash(context.Background(), artifact, result.Evidence[0].ContentHash); err != nil {
		t.Fatalf("unchanged Artifact hash rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, artifact), []byte(content+"changed after verify\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyArtifactHash(context.Background(), artifact, result.Evidence[0].ContentHash); err == nil {
		t.Fatal("mutated Artifact content passed stale hash gate")
	}
}

func TestArtifactVerifierFailsClosedOnMissingOrUnsafeArtifact(t *testing.T) {
	verifier, err := NewArtifactVerifier(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside.txt", "missing.md"} {
		criterion := goal.SuccessCriterion{ID: "report", Name: "Report", Type: goal.CriterionArtifactExists, Required: true, ValidationMethod: goal.CriterionMethodArtifactInStorage, ArtifactPath: path, MaxArtifactBytes: 100, Confirmed: true, Version: 1}
		_, err := verifier.Verify(context.Background(), agentruntime.Request{GoalCriteria: []goal.SuccessCriterion{criterion}, TaskCriterionIDs: []string{"report"}}, agentruntime.Result{Verified: true})
		if err == nil {
			t.Fatalf("path %q unexpectedly verified", path)
		}
	}
}

func TestArtifactVerifierIgnoresUnmappedAndUnconfirmedProposals(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("actual"), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier, err := NewArtifactVerifier(root, 100)
	if err != nil {
		t.Fatal(err)
	}
	criteria := []goal.SuccessCriterion{{ID: "unconfirmed", Name: "Not approved", Type: goal.CriterionArtifactExists, Required: true, ValidationMethod: goal.CriterionMethodArtifactInStorage, ArtifactPath: "file.txt", MaxArtifactBytes: 100, Confirmed: false, Version: 1}}
	result, err := verifier.Verify(context.Background(), agentruntime.Request{GoalCriteria: criteria}, agentruntime.Result{})
	if err != nil || result.Verified || len(result.Evidence) != 0 {
		t.Fatalf("unconfirmed/unmapped criterion verified: result=%+v err=%v", result, err)
	}
}
