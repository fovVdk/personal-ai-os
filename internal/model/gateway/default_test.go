// English: These tests cover provider fallback, usage normalization, and strict tool-call validation.
// 中文：本测试覆盖 Provider 回退、用量标准化及严格工具调用校验。
// 中文：本测试覆盖供应商回退、用量标准化及严格工具调用参数校验。
package gateway_test

import (
	"context"
	"errors"
	"testing"

	"personal-ai-os/internal/model/gateway"
)

type testProvider struct {
	name  string
	err   error
	calls int
}

func (p *testProvider) Name() string { return p.name }
func (p *testProvider) Complete(context.Context, gateway.Request) (gateway.Response, error) {
	p.calls++
	if p.err != nil {
		return gateway.Response{}, p.err
	}
	return gateway.Response{Content: p.name}, nil
}

func TestGatewayFallsBackInOrder(t *testing.T) {
	first := &testProvider{name: "primary", err: errors.New("offline")}
	second := &testProvider{name: "fallback"}
	response, err := (gateway.Default{Providers: []gateway.Provider{first, second}}).Complete(context.Background(), gateway.Request{Model: "test", Messages: []gateway.Message{{Role: "user", Content: "hello"}}})
	if err != nil || response.Content != "fallback" || first.calls != 1 || second.calls != 1 {
		t.Fatalf("response=%+v calls=%d/%d err=%v", response, first.calls, second.calls, err)
	}
}

func TestGatewayNormalizesMissingTotalTokenUsage(t *testing.T) {
	provider := responseProvider{response: gateway.Response{Usage: gateway.Usage{InputTokens: 7, OutputTokens: 5}}}
	response, err := (gateway.Default{Providers: []gateway.Provider{provider}}).Complete(context.Background(), gateway.Request{Model: "test", Messages: []gateway.Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Usage.TotalTokens != 12 {
		t.Fatalf("total tokens=%d, want 12", response.Usage.TotalTokens)
	}
}

type responseProvider struct{ response gateway.Response }

func (p responseProvider) Name() string { return "response" }
func (p responseProvider) Complete(context.Context, gateway.Request) (gateway.Response, error) {
	return p.response, nil
}

func TestGatewayRejectsUndeclaredToolCall(t *testing.T) {
	request := gateway.Request{Model: "test", Messages: []gateway.Message{{Role: "user", Content: "invoke"}}, Tools: []gateway.ToolDefinition{{Name: "allowed", Parameters: []byte(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)}}}
	provider := responseProvider{response: gateway.Response{ToolCalls: []gateway.ToolCall{{Name: "not-allowed", Arguments: []byte(`{}`)}}}}
	if _, err := (gateway.Default{Providers: []gateway.Provider{provider}}).Complete(context.Background(), request); err == nil {
		t.Fatal("undeclared tool call must be rejected")
	}
}

func TestGatewayRejectsMalformedToolArguments(t *testing.T) {
	request := gateway.Request{Model: "test", Messages: []gateway.Message{{Role: "user", Content: "invoke"}}, Tools: []gateway.ToolDefinition{{Name: "allowed", Parameters: []byte(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)}}}
	provider := responseProvider{response: gateway.Response{ToolCalls: []gateway.ToolCall{{Name: "allowed", Arguments: []byte(`{} {}`)}}}}
	if _, err := (gateway.Default{Providers: []gateway.Provider{provider}}).Complete(context.Background(), request); err == nil {
		t.Fatal("trailing tool arguments must be rejected")
	}
}

func TestGatewayRejectsNonObjectToolArguments(t *testing.T) {
	request := gateway.Request{Model: "test", Messages: []gateway.Message{{Role: "user", Content: "invoke"}}, Tools: []gateway.ToolDefinition{{Name: "allowed", Parameters: []byte(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)}}}
	provider := responseProvider{response: gateway.Response{ToolCalls: []gateway.ToolCall{{Name: "allowed", Arguments: []byte(`null`)}}}}
	if _, err := (gateway.Default{Providers: []gateway.Provider{provider}}).Complete(context.Background(), request); err == nil {
		t.Fatal("null tool arguments must be rejected")
	}
}

func TestGatewayValidatesEnumsAndValueTypes(t *testing.T) {
	request := gateway.Request{Model: "test", Messages: []gateway.Message{{Role: "user", Content: "invoke"}}, Tools: []gateway.ToolDefinition{{Name: "set_mode", Parameters: []byte(`{"type":"object","properties":{"mode":{"type":"string","enum":["safe","fast"]}},"required":["mode"],"additionalProperties":false}`)}}}
	valid := responseProvider{response: gateway.Response{ToolCalls: []gateway.ToolCall{{Name: "set_mode", Arguments: []byte(`{"mode":"safe"}`)}}}}
	if _, err := (gateway.Default{Providers: []gateway.Provider{valid}}).Complete(context.Background(), request); err != nil {
		t.Fatalf("valid enum value rejected: %v", err)
	}
	invalid := responseProvider{response: gateway.Response{ToolCalls: []gateway.ToolCall{{Name: "set_mode", Arguments: []byte(`{"mode":"unsafe"}`)}}}}
	if _, err := (gateway.Default{Providers: []gateway.Provider{invalid}}).Complete(context.Background(), request); err == nil {
		t.Fatal("value outside declared enum must fail")
	}
}

func TestGatewayAcceptsStandardObjectSchemaWithoutOptionalFields(t *testing.T) {
	request := gateway.Request{Model: "test", Messages: []gateway.Message{{Role: "user", Content: "invoke"}}, Tools: []gateway.ToolDefinition{{Name: "ping", Parameters: []byte(`{"type":"object"}`)}}}
	provider := responseProvider{response: gateway.Response{ToolCalls: []gateway.ToolCall{{Name: "ping", Arguments: []byte(`{}`)}}}}
	if _, err := (gateway.Default{Providers: []gateway.Provider{provider}}).Complete(context.Background(), request); err != nil {
		t.Fatalf("valid standard object schema was rejected: %v", err)
	}
}

func TestGatewayRejectsSchemaThatAllowsAdditionalProperties(t *testing.T) {
	request := gateway.Request{Model: "test", Messages: []gateway.Message{{Role: "user", Content: "invoke"}}, Tools: []gateway.ToolDefinition{{Name: "open", Parameters: []byte(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":true}`)}}}
	provider := responseProvider{response: gateway.Response{Content: "never called"}}
	if _, err := (gateway.Default{Providers: []gateway.Provider{provider}}).Complete(context.Background(), request); err == nil {
		t.Fatal("schema that permits undeclared fields must be rejected")
	}
}
