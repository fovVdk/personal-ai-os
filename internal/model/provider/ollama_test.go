// English: These tests validate Ollama chat response and token usage mapping.
// 中文：本测试验证 Ollama 对话响应及 Token 用量映射。
package provider

import (
	"context"
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
