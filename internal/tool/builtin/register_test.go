// English: These tests exercise built-in read-only Tools and filesystem/SSRF security boundaries.
// 中文：本测试验证内置只读工具以及文件路径和 SSRF 防护边界。
package builtin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestHTTPFetchRejectsPrivateAddressesAndRedirectTargets(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("should not be reached"))
	}))
	defer server.Close()
	parsedHost := strings.TrimPrefix(strings.TrimPrefix(server.URL, "http://"), "https://")
	allowed := map[string]struct{}{strings.Split(parsedHost, ":")[0]: {}}
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
