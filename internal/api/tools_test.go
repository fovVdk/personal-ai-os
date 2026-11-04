// English: These tests verify authenticated metadata discovery without exposing Tool handlers.
// 中文：本测试验证工具元数据发现接口不会泄漏工具处理器。
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"personal-ai-os/internal/tool/builtin"
	"personal-ai-os/internal/tool/registry"
)

func TestToolsAPIListsMetadataOnly(t *testing.T) {
	tools := registry.New()
	if err := builtin.Register(tools, builtin.Options{}); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Tools: tools}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tools", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "time.now") || strings.Contains(response.Body.String(), "Handler") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tools/paios.native/time.now/v1", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "healthy") {
		t.Fatalf("detail status=%d body=%s", response.Code, response.Body.String())
	}
}
