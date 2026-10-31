// English: These tests verify Mock responses and provider fallback without external credentials.
// 中文：本测试无需外部凭据即可验证 Mock 响应和 Provider 回退。
// 中文：本测试无需真实凭据即可验证 Mock 响应与 Provider 回退。
package gateway_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"personal-ai-os/internal/model/gateway"
	"personal-ai-os/internal/model/provider"
)

func TestMockProviderAndFallback(t *testing.T) {
	mock := provider.Mock{}
	response, err := (gateway.Default{Providers: []gateway.Provider{mock}}).Complete(context.Background(), gateway.Request{Model: "mock", Messages: []gateway.Message{{Role: "user", Content: "hello"}}})
	if err != nil || !strings.Contains(response.Content, "Mock model response") || response.Provider != "mock" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("authorization header missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"fake","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))
	}))
	defer server.Close()
	client := provider.OpenAICompatible{BaseURL: server.URL, APIKey: "test-secret", ProviderName: "test"}
	result, err := client.Complete(context.Background(), gateway.Request{Model: "fake", Messages: []gateway.Message{{Role: "user", Content: "hi"}}})
	if err != nil || result.Content != "ok" || result.Usage.TotalTokens != 3 {
		t.Fatalf("response=%+v err=%v", result, err)
	}
}

func TestOpenAIProviderRejectsProviderErrorWithoutLeakingBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("credential-bearing server body"))
	}))
	defer server.Close()
	_, err := (provider.OpenAICompatible{BaseURL: server.URL, APIKey: "secret"}).Complete(context.Background(), gateway.Request{Model: "fake", Messages: []gateway.Message{{Role: "user", Content: "hi"}}})
	if err == nil || strings.Contains(err.Error(), "credential-bearing") {
		t.Fatalf("unsafe provider error: %v", err)
	}
	if _, err := (gateway.Default{}).Complete(context.Background(), gateway.Request{}); !errors.Is(err, gateway.ErrNoProvider) {
		t.Fatalf("expected no-provider error, got %v", err)
	}
}
