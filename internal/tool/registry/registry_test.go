// English: These tests cover stable Tool identity, immutable metadata, validation, and operational state.
// 中文：本测试覆盖稳定 Tool 身份、不可变元数据、Schema 校验及运行状态管理。
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type testHandler struct{}

func (testHandler) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}

func validDefinition(provider, tool, version string) Definition {
	return Definition{
		Identity: Identity{ProviderID: provider, ToolID: tool, Version: version},
		Name:     "Search", Source: SourceBuiltin, Kind: KindReadOnly, RiskLevel: 0,
		Idempotency: IdempotencyIntrinsic, Timeout: time.Second,
		MaxInputBytes: 4096, MaxOutputBytes: 8192, ExecutionEnvironment: "local",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
	}
}

func TestRegistryUsesProviderQualifiedVersionIdentity(t *testing.T) {
	registry := New()
	if err := registry.Register(validDefinition("builtin", "search", "v1"), testHandler{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(validDefinition("mcp-server", "search", "v1"), testHandler{}); err != nil {
		t.Fatalf("same tool name from another provider: %v", err)
	}
	if err := registry.Register(validDefinition("builtin", "search", "v2"), testHandler{}); err != nil {
		t.Fatalf("new tool version: %v", err)
	}
	if err := registry.Register(validDefinition("builtin", "search", "v1"), testHandler{}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate version error=%v", err)
	}
	if got := len(registry.List()); got != 3 {
		t.Fatalf("registered entries=%d, want 3", got)
	}
}

func TestRegistryCopiesDefinitionsAndSchemaBytes(t *testing.T) {
	registry := New()
	definition := validDefinition("builtin", "search", "v1")
	if err := registry.Register(definition, testHandler{}); err != nil {
		t.Fatal(err)
	}
	definition.InputSchema[0] = 'x'
	entry, err := registry.Get(definition.Identity)
	if err != nil {
		t.Fatal(err)
	}
	entry.Definition.InputSchema[0] = 'x'
	again, err := registry.Get(definition.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Definition.InputSchema)[:1] != "{" {
		t.Fatal("registered schema was mutated through caller-owned bytes")
	}
}

func TestRegistryOperationalStateDoesNotGrantAccess(t *testing.T) {
	registry := New()
	definition := validDefinition("builtin", "search", "v1")
	if err := registry.Register(definition, testHandler{}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(definition.Identity); err != nil {
		t.Fatalf("registered built-in handler should start healthy: %v", err)
	}
	if err := registry.SetOperationalState(definition.Identity, false, HealthHealthy); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(definition.Identity); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disabled tool resolve error=%v", err)
	}
	if err := registry.SetOperationalState(definition.Identity, true, HealthUnhealthy); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(definition.Identity); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unhealthy tool resolve error=%v", err)
	}
}

func TestValidateEnforcesStrictObjectSchema(t *testing.T) {
	schema := validDefinition("builtin", "search", "v1").InputSchema
	for _, input := range []string{`{"query":"hello"}`, `{"query":"hello","extra":true}`, `{"query":1}`, `{}`, `{"query":"hello"} {}`} {
		err := Validate(schema, json.RawMessage(input))
		if input == `{"query":"hello"}` && err != nil {
			t.Fatalf("valid input rejected: %v", err)
		}
		if input != `{"query":"hello"}` && !errors.Is(err, ErrInvalidInput) {
			t.Errorf("invalid input %s error=%v", input, err)
		}
	}
}

func TestDefinitionRejectsUnsafeBoundsAndHighRiskMismatch(t *testing.T) {
	definition := validDefinition("builtin", "door", "v1")
	definition.Kind = KindHighRisk
	if err := New().Register(definition, testHandler{}); err == nil {
		t.Fatal("high-risk definition with non-A3 risk must be rejected")
	}
	definition = validDefinition("builtin", "search", "v1")
	definition.Timeout = 24 * time.Hour
	if err := New().Register(definition, testHandler{}); err == nil {
		t.Fatal("unbounded timeout must be rejected")
	}
}

func TestDefinitionRejectsUnsupportedSchemaConstraints(t *testing.T) {
	definition := validDefinition("builtin", "search", "v1")
	definition.InputSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","enum":["approved"]}},"required":["query"],"additionalProperties":false}`)
	if err := New().Register(definition, testHandler{}); err == nil {
		t.Fatal("unsupported property constraints must be rejected rather than ignored")
	}
}
