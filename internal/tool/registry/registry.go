// Package registry owns immutable, versioned Tool metadata and runtime registration.
// 中文：registry 包管理不可变且带版本的 Tool 元数据及运行状态注册。
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Source identifies who supplied a Tool definition; it never grants execution permission.
// 中文：Source 标识 Tool 定义的提供方，但不授予执行权限。
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceMCP     Source = "mcp"
	SourceRemote  Source = "remote"
)

// Kind classifies a Tool's effect boundary.
// 中文：Kind 用于划分 Tool 的副作用边界。
type Kind string

const (
	KindReadOnly Kind = "read_only"
	KindMutation Kind = "mutation"
	KindExternal Kind = "external"
	KindHighRisk Kind = "high_risk"
)

// Idempotency describes the provider's externally-enforced replay contract.
// 中文：Idempotency 描述提供方强制执行的重放契约。
type Idempotency string

const (
	IdempotencyNone      Idempotency = "none"
	IdempotencyKeyed     Idempotency = "keyed"
	IdempotencyIntrinsic Idempotency = "intrinsic"
)

type Health string

const (
	HealthUnknown   Health = "unknown"
	HealthHealthy   Health = "healthy"
	HealthUnhealthy Health = "unhealthy"
)

// Identity uniquely names a published version. Tool IDs are provider-qualified.
// 中文：Identity 唯一标识一个发布版本；Tool ID 必须带 Provider 命名空间。
type Identity struct {
	ProviderID string `json:"provider_id"`
	ToolID     string `json:"tool_id"`
	Version    string `json:"version"`
}

// Definition is immutable after registration; mutable operational state lives in Entry.
// 中文：注册后 Definition 不可变；可变的运行状态保存在 Entry 中。
type Definition struct {
	Identity             Identity        `json:"identity"`
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	Source               Source          `json:"source"`
	Kind                 Kind            `json:"kind"`
	RiskLevel            int             `json:"risk_level"`
	Idempotency          Idempotency     `json:"idempotency"`
	Timeout              time.Duration   `json:"timeout"`
	MaxInputBytes        int             `json:"max_input_bytes"`
	MaxOutputBytes       int             `json:"max_output_bytes"`
	ExecutionEnvironment string          `json:"execution_environment"`
	InputSchema          json.RawMessage `json:"input_schema"`
	OutputSchema         json.RawMessage `json:"output_schema"`
}

type Entry struct {
	Definition Definition `json:"definition"`
	Enabled    bool       `json:"enabled"`
	Health     Health     `json:"health"`
}

type Handler interface {
	Execute(context.Context, json.RawMessage) (json.RawMessage, error)
}

// InvocationContext contains request identity copied by the Tool Gateway after validation.
// Tool arguments cannot override these fields.
// 中文：InvocationContext 保存 Gateway 校验后复制的请求身份；Tool 参数不能覆盖这些字段。
type InvocationContext struct {
	GoalID      string
	TaskID      string
	ExecutionID string
	StepID      string
	AgentID     string
}

// ContextualHandler is optional for Tools that must bind durable records to the verified invocation.
// 中文：需要将持久化记录绑定到已验证调用的 Tool 可实现 ContextualHandler。
type ContextualHandler interface {
	ExecuteWithContext(context.Context, InvocationContext, json.RawMessage) (json.RawMessage, error)
}

type Registered struct {
	Entry   Entry
	Handler Handler
}

type Registry struct {
	mu      sync.RWMutex
	entries map[Identity]Registered
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

var (
	ErrExists       = errors.New("tool version is already registered")
	ErrNotFound     = errors.New("tool version is not registered")
	ErrUnavailable  = errors.New("tool is disabled or unhealthy")
	ErrInvalidInput = errors.New("tool input does not match registered schema")
)

func New() *Registry { return &Registry{entries: make(map[Identity]Registered)} }

func (r *Registry) Register(definition Definition, handler Handler) error {
	if r == nil || handler == nil {
		return errors.New("tool registry and handler are required")
	}
	if err := validateDefinition(&definition); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[definition.Identity]; ok {
		return ErrExists
	}
	definition.InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
	definition.OutputSchema = append(json.RawMessage(nil), definition.OutputSchema...)
	health := HealthUnknown
	if definition.Source == SourceBuiltin {
		health = HealthHealthy
	}
	r.entries[definition.Identity] = Registered{Entry: Entry{Definition: definition, Enabled: true, Health: health}, Handler: handler}
	return nil
}

func (r *Registry) Get(identity Identity) (Entry, error) {
	if r == nil {
		return Entry{}, ErrNotFound
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	registered, ok := r.entries[identity]
	if !ok {
		return Entry{}, ErrNotFound
	}
	return cloneEntry(registered.Entry), nil
}

func (r *Registry) Resolve(identity Identity) (Registered, error) {
	if r == nil {
		return Registered{}, ErrNotFound
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	registered, ok := r.entries[identity]
	if !ok {
		return Registered{}, ErrNotFound
	}
	if !registered.Entry.Enabled || registered.Entry.Health != HealthHealthy {
		return Registered{}, ErrUnavailable
	}
	registered.Entry = cloneEntry(registered.Entry)
	return registered, nil
}

func (r *Registry) List() []Entry {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]Entry, 0, len(r.entries))
	for _, registered := range r.entries {
		items = append(items, cloneEntry(registered.Entry))
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := items[i].Definition.Identity, items[j].Definition.Identity
		if left.ProviderID != right.ProviderID {
			return left.ProviderID < right.ProviderID
		}
		if left.ToolID != right.ToolID {
			return left.ToolID < right.ToolID
		}
		return left.Version < right.Version
	})
	return items
}

// SetOperationalState changes only operator-controlled enablement and health, never metadata.
// 中文：SetOperationalState 只修改运维控制的启用状态和健康状态，不修改 Tool 元数据。
func (r *Registry) SetOperationalState(identity Identity, enabled bool, health Health) error {
	if health != HealthUnknown && health != HealthHealthy && health != HealthUnhealthy {
		return errors.New("invalid tool health state")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	registered, ok := r.entries[identity]
	if !ok {
		return ErrNotFound
	}
	registered.Entry.Enabled = enabled
	registered.Entry.Health = health
	r.entries[identity] = registered
	return nil
}

// ReplaceHandler swaps a runtime adapter for an unchanged immutable Tool version.
// 中文：ReplaceHandler 仅为语义与版本不变的 Tool 更换运行时 Adapter。
func (r *Registry) ReplaceHandler(identity Identity, handler Handler) error {
	if r == nil || handler == nil {
		return errors.New("tool registry and handler are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	registered, ok := r.entries[identity]
	if !ok {
		return ErrNotFound
	}
	registered.Handler = handler
	r.entries[identity] = registered
	return nil
}

func cloneEntry(entry Entry) Entry {
	entry.Definition.InputSchema = append(json.RawMessage(nil), entry.Definition.InputSchema...)
	entry.Definition.OutputSchema = append(json.RawMessage(nil), entry.Definition.OutputSchema...)
	return entry
}

func validateDefinition(definition *Definition) error {
	id := definition.Identity
	if !identifierPattern.MatchString(id.ProviderID) || !identifierPattern.MatchString(id.ToolID) || !identifierPattern.MatchString(id.Version) {
		return errors.New("provider id, tool id, and version must be valid stable identifiers")
	}
	if strings.TrimSpace(definition.Name) == "" || len(definition.Name) > 256 || len(definition.Description) > 4096 || strings.ContainsAny(definition.Name+definition.Description, "\x00\r") {
		return errors.New("tool name or description is invalid")
	}
	if definition.Source != SourceBuiltin && definition.Source != SourceMCP && definition.Source != SourceRemote {
		return errors.New("tool source must be builtin, mcp, or remote")
	}
	switch definition.Kind {
	case KindReadOnly, KindMutation, KindExternal, KindHighRisk:
	default:
		return errors.New("tool kind is invalid")
	}
	if definition.RiskLevel < 0 || definition.RiskLevel > 3 {
		return errors.New("tool risk level must be between 0 and 3")
	}
	if definition.Kind == KindHighRisk && definition.RiskLevel != 3 {
		return errors.New("high-risk tools must declare risk level 3")
	}
	if definition.Kind == KindReadOnly && definition.RiskLevel > 1 {
		return errors.New("read-only tools cannot declare high risk")
	}
	if definition.Idempotency != IdempotencyNone && definition.Idempotency != IdempotencyKeyed && definition.Idempotency != IdempotencyIntrinsic {
		return errors.New("tool idempotency declaration is invalid")
	}
	if definition.Timeout < time.Millisecond || definition.Timeout > 10*time.Minute || definition.MaxInputBytes < 1 || definition.MaxInputBytes > 1<<20 || definition.MaxOutputBytes < 1 || definition.MaxOutputBytes > 4<<20 {
		return errors.New("tool timeout and input/output limits are outside permitted bounds")
	}
	if !identifierPattern.MatchString(definition.ExecutionEnvironment) {
		return errors.New("execution environment must be a stable identifier")
	}
	if err := validateSchema(definition.InputSchema); err != nil {
		return fmt.Errorf("input schema: %w", err)
	}
	if err := validateSchema(definition.OutputSchema); err != nil {
		return fmt.Errorf("output schema: %w", err)
	}
	return nil
}

// validateSchema accepts the intentionally narrow JSON Schema subset supported by the Tool boundary.
// 中文：validateSchema 仅接受 Tool 边界明确支持的受限 JSON Schema 子集。
func validateSchema(raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > 256<<10 {
		return errors.New("schema must be present and no larger than 256 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var schema struct {
		Type                 string                     `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	if err := decoder.Decode(&schema); err != nil {
		return fmt.Errorf("decode schema: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("schema contains trailing JSON")
	}
	if schema.Type != "object" {
		return errors.New("root schema type must be object")
	}
	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		return errors.New("additionalProperties must be false")
	}
	if len(schema.Properties) > 256 || len(schema.Required) > 256 {
		return errors.New("schema exceeds property limits")
	}
	for key, rawProperty := range schema.Properties {
		if key == "" || len(key) > 128 {
			return errors.New("schema property name is invalid")
		}
		var property map[string]json.RawMessage
		if err := json.Unmarshal(rawProperty, &property); err != nil || len(property) != 1 {
			return errors.New("property schema is invalid")
		}
		var propertyType string
		if rawType, ok := property["type"]; !ok || json.Unmarshal(rawType, &propertyType) != nil {
			return errors.New("property schema must contain only a string type")
		}
		switch propertyType {
		case "string", "boolean", "integer", "number":
		default:
			return fmt.Errorf("unsupported property type %q", propertyType)
		}
	}
	seen := map[string]bool{}
	for _, field := range schema.Required {
		if _, ok := schema.Properties[field]; !ok || seen[field] {
			return errors.New("required fields must be unique declared properties")
		}
		seen[field] = true
	}
	return nil
}

// Validate checks a JSON object against the exact registered root and primitive property types.
// 中文：Validate 按注册的根对象和基础属性类型严格校验 JSON 输入。
func Validate(schemaRaw, valueRaw json.RawMessage) error {
	if err := validateSchema(schemaRaw); err != nil {
		return fmt.Errorf("registered schema invalid: %w", err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(valueRaw))
	decoder.UseNumber()
	var value map[string]json.RawMessage
	if err := decoder.Decode(&value); err != nil || value == nil {
		return ErrInvalidInput
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidInput
	}
	for key, raw := range value {
		propertyRaw, ok := schema.Properties[key]
		if !ok {
			return ErrInvalidInput
		}
		var property struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(propertyRaw, &property) != nil || !matchesType(property.Type, raw) {
			return ErrInvalidInput
		}
	}
	for _, required := range schema.Required {
		if _, ok := value[required]; !ok {
			return ErrInvalidInput
		}
	}
	return nil
}

func matchesType(kind string, raw json.RawMessage) bool {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return false
	}
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		n, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := n.Int64()
		return err == nil
	case "number":
		_, ok := value.(json.Number)
		return ok
	default:
		return false
	}
}
