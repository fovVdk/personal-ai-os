// English: These tests ensure approved report export is hash-bound, idempotent for identical bytes, and never overwrites unrelated files.
// 中文：本测试确保审批后的报告导出绑定哈希、相同内容幂等，且不覆盖无关已有文件。
package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"personal-ai-os/internal/tool/registry"
)

func TestReportExporterRequiresExactApprovedParametersAndSeparateRoot(t *testing.T) {
	artifactRoot, exportRoot := t.TempDir(), t.TempDir()
	reader, invocation := artifactToolFixture(artifactRoot)
	content := []byte("# Summary\n## Sources\nBound report.\n")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(artifactRoot, "reports/final.md")), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "reports/final.md"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	artifactRootHandle, err := newFileRoot(artifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	exportRootHandle, err := newFileRoot(exportRoot)
	if err != nil {
		t.Fatal(err)
	}
	handler := reportExporter{reader: reader, export: exportRootHandle, maxSize: 1 << 20}
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	arguments, _ := json.Marshal(map[string]any{"criterion_id": "report", "criterion_version": 1, "artifact_path": "reports/final.md", "sha256": hash})
	if _, err := handler.Execute(context.Background(), arguments); err == nil {
		t.Fatal("report export bypassed Tool Gateway context")
	}
	output, err := handler.ExecuteWithContext(context.Background(), invocation, arguments)
	if err != nil {
		t.Fatalf("execute approved export with exact hash: %v", err)
	}
	published, err := os.ReadFile(filepath.Join(exportRoot, "reports/final.md"))
	if err != nil || string(published) != string(content) {
		t.Fatalf("published bytes=%q err=%v", published, err)
	}
	var result struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(output, &result); err != nil || result.Result == "" {
		t.Fatalf("export result=%s err=%v", output, err)
	}
	if _, err := handler.ExecuteWithContext(context.Background(), invocation, arguments); err != nil {
		t.Fatalf("same approved report bytes should be idempotently exportable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "reports/final.md"), []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ExecuteWithContext(context.Background(), invocation, arguments); err == nil {
		t.Fatal("modified content was exported against stale approved hash")
	}
	if _, err := os.Stat(filepath.Join(artifactRootHandle.root, "reports/final.md")); err != nil {
		t.Fatal(err)
	}
}

func TestReportExporterDoesNotAcceptCrossGoalInvocation(t *testing.T) {
	artifactRoot, exportRoot := t.TempDir(), t.TempDir()
	reader, invocation := artifactToolFixture(artifactRoot)
	exportRootHandle, err := newFileRoot(exportRoot)
	if err != nil {
		t.Fatal(err)
	}
	handler := reportExporter{reader: reader, export: exportRootHandle, maxSize: 1 << 20}
	invocation.GoalID = "different-goal"
	arguments, _ := json.Marshal(map[string]any{"criterion_id": "report", "criterion_version": 1, "artifact_path": "reports/final.md", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if _, err := handler.ExecuteWithContext(context.Background(), invocation, arguments); err == nil {
		t.Fatal("report exporter accepted another Goal's invocation context")
	}
	if _, err := os.Stat(filepath.Join(exportRoot, "reports/final.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid invocation wrote export content: %v", err)
	}
}

func TestReportExporterDoesNotOverwriteExistingDifferentFile(t *testing.T) {
	artifactRoot, exportRoot := t.TempDir(), t.TempDir()
	reader, invocation := artifactToolFixture(artifactRoot)
	content := []byte("# Summary\n## Sources\nApproved bytes.\n")
	if err := os.MkdirAll(filepath.Join(artifactRoot, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "reports/final.md"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(exportRoot, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	const existing = "pre-existing operator file"
	if err := os.WriteFile(filepath.Join(exportRoot, "reports/final.md"), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	exportRootHandle, err := newFileRoot(exportRoot)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	handler := reportExporter{reader: reader, export: exportRootHandle, maxSize: 1 << 20}
	args, _ := json.Marshal(map[string]any{"criterion_id": "report", "criterion_version": 1, "artifact_path": "reports/final.md", "sha256": hash})
	if _, err := handler.ExecuteWithContext(context.Background(), invocation, args); err == nil {
		t.Fatal("exporter overwrote a pre-existing different file")
	}
	actual, err := os.ReadFile(filepath.Join(exportRoot, "reports/final.md"))
	if err != nil || string(actual) != existing {
		t.Fatalf("pre-existing export target changed: %q err=%v", actual, err)
	}
}

var _ registry.ContextualHandler = reportExporter{}
