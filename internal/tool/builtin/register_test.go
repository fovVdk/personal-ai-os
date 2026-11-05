// English: These tests cover native Tool registration, path security, and policy gating for report export.
// 中文：本测试覆盖原生工具注册、文件路径安全以及报告导出的策略审批门控。
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/policy"
	"personal-ai-os/internal/source"
	"personal-ai-os/internal/task"
	"personal-ai-os/internal/tool/registry"
)

func TestTimeNowToolReturnsUTC(t *testing.T) {
	tools := registry.New()
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3600))
	if err := Register(tools, Options{Now: func() time.Time { return fixed }}); err != nil {
		t.Fatal(err)
	}
	registered, err := tools.Resolve(registry.Identity{ProviderID: providerID, ToolID: "time.now", Version: toolVersion})
	if err != nil {
		t.Fatal(err)
	}
	result, err := registered.Handler.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "2026-01-02T02:04:05Z") {
		t.Fatalf("time result=%s", result)
	}
}

type testSourceFetcher struct{ snapshot source.Snapshot }

func (f testSourceFetcher) Fetch(context.Context, string) (source.Snapshot, error) {
	return f.snapshot, nil
}

type captureRepository struct{ snapshot source.Snapshot }

func (r *captureRepository) Create(_ context.Context, item source.Snapshot) (source.Snapshot, error) {
	item.ID = "persisted-source"
	r.snapshot = item
	return item, nil
}
func (r *captureRepository) Get(context.Context, string, string, string) (source.Snapshot, error) {
	return r.snapshot, nil
}
func (r *captureRepository) List(context.Context, string, string, int) ([]source.Snapshot, error) {
	return []source.Snapshot{r.snapshot}, nil
}
func (r *captureRepository) AddReference(context.Context, source.Reference) (source.Reference, error) {
	return source.Reference{}, nil
}
func (r *captureRepository) ListReferences(context.Context, string, string, string) ([]source.Reference, error) {
	return nil, nil
}

func TestSourceCaptureUsesGatewayContextAndPersistsAllowlistedSource(t *testing.T) {
	const sourceURL = "https://research.example/source"
	content := []byte("A captured source sentence.")
	repository := &captureRepository{}
	handler := sourceCapture{ownerID: "owner", fetcher: testSourceFetcher{snapshot: source.Snapshot{
		OriginalURL: sourceURL, FinalURL: sourceURL, HTTPStatus: 200, MIMEType: "text/plain",
		RetrievedAt: time.Now().UTC(), ContentHash: source.Hash(content), RawContent: content,
		NormalizedContent: string(content), FetcherVersion: source.FetcherVersion,
	}}, repository: repository}
	arguments, _ := json.Marshal(map[string]string{"url": sourceURL})
	if _, err := handler.Execute(context.Background(), arguments); err == nil {
		t.Fatal("source capture without trusted execution context was accepted")
	}
	output, err := handler.ExecuteWithContext(context.Background(), registry.InvocationContext{GoalID: "goal", TaskID: "task", ExecutionID: "execution", StepID: "step", AgentID: "agent"}, arguments)
	if err != nil {
		t.Fatalf("capture allowlisted test source: %v", err)
	}
	if repository.snapshot.ID != "persisted-source" || repository.snapshot.GoalID != "goal" || repository.snapshot.TaskID != "task" || repository.snapshot.ContentHash != source.Hash(content) {
		t.Fatalf("persisted source context/content mismatch: %+v", repository.snapshot)
	}
	if len(output) == 0 {
		t.Fatal("source capture returned empty result")
	}
}

func TestFilesystemToolsConstrainPathsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("safe contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	tools := registry.New()
	if err := Register(tools, Options{FileRoot: root}); err != nil {
		t.Fatal(err)
	}
	read, err := tools.Resolve(registry.Identity{ProviderID: providerID, ToolID: "filesystem.read", Version: toolVersion})
	if err != nil {
		t.Fatal(err)
	}
	result, err := read.Handler.Execute(context.Background(), json.RawMessage(`{"path":"note.txt"}`))
	if err != nil || !strings.Contains(string(result), "safe contents") {
		t.Fatalf("read result=%s err=%v", result, err)
	}
	for _, path := range []string{"../outside/secret.txt", "escape.txt"} {
		args, _ := json.Marshal(map[string]string{"path": path})
		if _, err := read.Handler.Execute(context.Background(), args); err == nil {
			t.Errorf("read accepted escaping path %q", path)
		}
	}
	list, err := tools.Resolve(registry.Identity{ProviderID: providerID, ToolID: "filesystem.list", Version: toolVersion})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := list.Handler.Execute(context.Background(), json.RawMessage(`{"path":"."}`))
	if err != nil || !strings.Contains(string(listed), "note.txt") {
		t.Fatalf("list result=%s err=%v", listed, err)
	}
}

func TestArtifactAndApprovalToolsRegisterOnlyWithExplicitOperatorRoots(t *testing.T) {
	root, exportRoot := t.TempDir(), t.TempDir()
	goals := artifactGoalRepository{item: goal.Goal{ID: "goal", OwnerID: "owner"}}
	tasks := artifactTaskRepository{item: task.Task{ID: "task", GoalID: "goal", Status: task.Running}}
	withoutExport := registry.New()
	if err := Register(withoutExport, Options{FileRoot: root, OwnerID: "owner", Goals: goals, Tasks: tasks}); err != nil {
		t.Fatal(err)
	}
	if _, err := withoutExport.Resolve(registry.Identity{ProviderID: providerID, ToolID: "artifact.write", Version: toolVersion}); err != nil {
		t.Fatalf("restricted artifact writer not registered: %v", err)
	}
	if _, err := withoutExport.Resolve(registry.Identity{ProviderID: providerID, ToolID: "report.export", Version: toolVersion}); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("report export must remain disabled without an explicit export root: %v", err)
	}

	withExport := registry.New()
	if err := Register(withExport, Options{FileRoot: root, ReportExportRoot: exportRoot, OwnerID: "owner", Goals: goals, Tasks: tasks}); err != nil {
		t.Fatal(err)
	}
	entry, err := withExport.Get(registry.Identity{ProviderID: providerID, ToolID: "report.export", Version: toolVersion})
	if err != nil || entry.Definition.Kind != registry.KindExternal || entry.Definition.RiskLevel != 2 {
		t.Fatalf("report export definition=%+v err=%v", entry, err)
	}
	aliasParent := t.TempDir()
	artifactAlias := filepath.Join(aliasParent, "artifact-alias")
	if err := os.Symlink(root, artifactAlias); err != nil {
		t.Fatal(err)
	}
	if err := Register(registry.New(), Options{FileRoot: root, ReportExportRoot: artifactAlias, OwnerID: "owner", Goals: goals, Tasks: tasks}); err == nil {
		t.Fatal("symlink alias overlapping Artifact root was accepted as export root")
	}
	missingRootParent := filepath.Join(aliasParent, "not-created-yet")
	if err := os.Mkdir(missingRootParent, 0o700); err != nil {
		t.Fatal(err)
	}
	nestedExportParent := filepath.Join(missingRootParent, "export")
	if err := os.Mkdir(nestedExportParent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(nestedExportParent, "artifact-alias")); err != nil {
		t.Fatal(err)
	}
	missingArtifactAliasRoot := filepath.Join(nestedExportParent, "artifact-alias", "child")
	if err := Register(registry.New(), Options{FileRoot: missingArtifactAliasRoot, ReportExportRoot: nestedExportParent, OwnerID: "owner", Goals: goals, Tasks: tasks}); err == nil {
		t.Fatal("nested roots escaping through symlink alias were accepted")
	}
	decision, err := policy.Evaluate(policy.Request{ToolID: "paios.native/report.export", Action: "report.export", Level: policy.A2, RiskLevel: entry.Definition.RiskLevel, ToolAllowed: true, ApprovalRequired: true})
	if err != nil || decision.Decision != policy.RequireApproval {
		t.Fatalf("report export policy=%+v err=%v, want human approval", decision, err)
	}
	readDecision, err := policy.Evaluate(policy.Request{ToolID: "paios.native/source.capture", Action: "source.capture", Level: policy.A1, RiskLevel: 1, ToolAllowed: true})
	if err != nil || readDecision.Decision != policy.Allow {
		t.Fatalf("research source capture policy=%+v err=%v, want no forced Approval", readDecision, err)
	}
}

func TestHTTPFetchRejectsPrivateAddressesAndRedirectTargets(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("should not be reached"))
	}))
	defer server.Close()
	parsedHost := strings.TrimPrefix(strings.TrimPrefix(server.URL, "http://"), "https://")
	allowed := map[string]struct{}{parsedHost: {}}
	fetch, err := newHTTPFetch(allowed)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetch.Execute(context.Background(), mustJSON(t, map[string]string{"url": server.URL}))
	if err == nil {
		t.Fatal("http.fetch accepted private loopback destination")
	}
	if calls != 0 {
		t.Fatalf("private HTTP server received %d request(s)", calls)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
