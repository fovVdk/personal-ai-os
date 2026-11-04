// English: These tests verify MCP lifecycle API exposes no transport credentials and remains authenticated.
// 中文：本测试验证 MCP 生命周期 API 不泄漏传输凭据并受认证中间件保护。
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	toolmcp "personal-ai-os/internal/tool/mcp"
	"personal-ai-os/internal/tool/registry"
)

func TestMCPServerStatusAPIIsAuthenticatedAndRedactsConfig(t *testing.T) {
	tools := registry.New()
	manager, err := toolmcp.NewManager(tools, toolmcp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RegisterServer(toolmcp.ServerConfig{ID: "docs", Name: "Docs MCP", Transport: toolmcp.TransportStreamableHTTP, Endpoint: "https://mcp.example.com/rpc", AllowedHosts: []string{"mcp.example.com"}, AllowedTools: []string{"search"}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	apiKey := strings.Repeat("k", 32)
	handler := (&Server{MCPServers: manager, APIKey: apiKey}).Handler()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", response.Code, response.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers", nil)
	request.Header.Set("X-API-Key", apiKey)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "docs") || strings.Contains(response.Body.String(), "mcp.example.com") {
		t.Fatalf("authenticated status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/docs/disable", nil)
	request.Header.Set("X-API-Key", apiKey)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}
}
