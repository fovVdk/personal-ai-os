// English: These tests verify OpenAI-compatible assistant Tool Calls and correlated Tool results serialize correctly.
// 中文：本测试验证 OpenAI 兼容 Provider 能正确序列化 Assistant Tool Call 和关联 Tool Result。
package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"personal-ai-os/internal/model/gateway"
)

func TestOpenAICompatibleSerializesToolCallRoundTripMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(request.Messages) != 3 {
			t.Errorf("messages=%+v", request.Messages)
			http.Error(w, "bad message count", 400)
			return
		}
		assistant := request.Messages[1]
		calls, ok := assistant["tool_calls"].([]any)
		if assistant["role"] != "assistant" || !ok || len(calls) != 1 {
			t.Errorf("assistant tool call message=%+v", assistant)
		} else {
			call := calls[0].(map[string]any)
			function := call["function"].(map[string]any)
			if call["id"] != "call-1" || call["type"] != "function" || function["name"] != "tool_abcd" || function["arguments"] != `{"query":"one"}` {
				t.Errorf("encoded call=%+v", call)
			}
		}
		tool := request.Messages[2]
		if tool["role"] != "tool" || tool["tool_call_id"] != "call-1" || tool["content"] != "one" {
			t.Errorf("tool result message=%+v", tool)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()
	response, err := (OpenAICompatible{BaseURL: server.URL, APIKey: "test-key"}).Complete(context.Background(), gateway.Request{Model: "test", Messages: []gateway.Message{
		{Role: "user", Content: "query"},
		{Role: "assistant", Content: "", ToolCalls: []gateway.ToolCall{{ID: "call-1", Name: "tool_abcd", Arguments: json.RawMessage(`{"query":"one"}`)}}},
		{Role: "tool", ToolCallID: "call-1", ToolName: "tool_abcd", Content: "one"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "done" {
		t.Fatalf("response=%+v", response)
	}
}
