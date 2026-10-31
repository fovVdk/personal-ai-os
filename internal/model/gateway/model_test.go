// English: This test verifies OpenAI-compatible function-call proposals are parsed as JSON arguments.
// 中文：本测试验证 OpenAI 兼容的函数调用提议会被解析为 JSON 参数。
// 中文：本测试验证 OpenAI-compatible 工具调用提议会解析为 JSON 参数。
package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"personal-ai-os/internal/model/gateway"
	"personal-ai-os/internal/model/provider"
)

func TestOpenAIProviderMapsStructuredToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"fake","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","function":{"name":"get_time","arguments":"{}"}}]}}]}`))
	}))
	defer server.Close()
	client := provider.OpenAICompatible{BaseURL: server.URL, APIKey: "test-secret"}
	response, err := client.Complete(context.Background(), gateway.Request{Model: "fake", Messages: []gateway.Message{{Role: "user", Content: "time"}}, Tools: []gateway.ToolDefinition{{Name: "get_time", Parameters: []byte(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "get_time" || string(response.ToolCalls[0].Arguments) != `{}` {
		t.Fatalf("tool calls=%+v", response.ToolCalls)
	}
}
