// English: These tests validate Ollama chat response and token usage mapping.
// 中文：本测试验证 Ollama 对话响应及 Token 用量映射。
package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"personal-ai-os/internal/model/gateway"
)

func TestOllamaNonStreamingChatResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"tiny","message":{"role":"assistant","content":"hello"},"prompt_eval_count":4,"eval_count":2}`))
	}))
	defer server.Close()
	response, err := (Ollama{BaseURL: server.URL}).Complete(context.Background(), gateway.Request{Model: "tiny", Messages: []gateway.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "hello" || response.Usage.TotalTokens != 6 || response.Provider != "ollama" {
		t.Fatalf("response=%+v", response)
	}
}

func TestOllamaSerializesToolCallRoundTripMessages(t *testing.T) {
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
			if function["name"] != "tool_abcd" || function["arguments"].(map[string]any)["query"] != "one" {
				t.Errorf("encoded call=%+v", call)
			}
		}
		tool := request.Messages[2]
		if tool["role"] != "tool" || tool["tool_call_id"] != "call-1" || tool["tool_name"] != "tool_abcd" || tool["content"] != "one" {
			t.Errorf("tool result message=%+v", tool)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"tiny","message":{"role":"assistant","content":"done"},"prompt_eval_count":1,"eval_count":1}`))
	}))
	defer server.Close()
	response, err := (Ollama{BaseURL: server.URL}).Complete(context.Background(), gateway.Request{Model: "tiny", Messages: []gateway.Message{
		{Role: "user", Content: "query"},
		{Role: "assistant", ToolCalls: []gateway.ToolCall{{ID: "call-1", Name: "tool_abcd", Arguments: json.RawMessage(`{"query":"one"}`)}}},
		{Role: "tool", ToolCallID: "call-1", ToolName: "tool_abcd", Content: "one"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "done" {
		t.Fatalf("response=%+v", response)
	}
}
