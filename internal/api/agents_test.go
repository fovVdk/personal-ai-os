// English: These tests verify the public agent API omits internal runtime objects.
// 中文：本测试验证公开 Agent API 不会返回内部 Runtime 对象。
// 中文：本测试验证 Agent API 不会返回内部运行时对象。
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"personal-ai-os/internal/agent/registry"
	"personal-ai-os/internal/agent/runtime"
)

func TestAgentsAPIListsProfilesWithoutRuntimeImplementation(t *testing.T) {
	agents := registry.New()
	if err := agents.Register(registry.Profile{ID: "planning", Name: "Planning", Version: "v1", Capabilities: []string{"plan"}, Agent: runtime.MockAgent{AgentID: "planning"}}); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Agents: agents}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	if got := response.Body.String(); got == "" || strings.Contains(got, "runtime") {
		t.Fatalf("unexpected public agent response: %s", got)
	}
}
