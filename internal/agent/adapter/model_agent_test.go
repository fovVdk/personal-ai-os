// English: These tests cover bounded model Tool Calls, trusted identity mapping, and exact approval checkpoint resume.
// 中文：本测试覆盖有界模型 Tool Call、可信身份映射及精确审批 Checkpoint 恢复。
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	agentruntime "personal-ai-os/internal/agent/runtime"
	modelgateway "personal-ai-os/internal/model/gateway"
	"personal-ai-os/internal/tool/registry"
)

const testArgsSchema = `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`

type queuedModel struct {
	responses []modelgateway.Response
	requests  []modelgateway.Request
	errors    []error
}

func (m *queuedModel) Complete(_ context.Context, request modelgateway.Request) (modelgateway.Response, error) {
	m.requests = append(m.requests, request)
	if len(m.errors) > 0 {
		err := m.errors[0]
		m.errors = m.errors[1:]
		if err != nil {
			return modelgateway.Response{}, err
		}
	}
	if len(m.responses) == 0 {
		return modelgateway.Response{}, errors.New("no queued response")
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, nil
}

type recordedToolCall struct{ request agentruntime.ToolCallRequest }
type approvalToolCaller struct {
	calls    []recordedToolCall
	approved bool
	result   string
}

func (t *approvalToolCaller) ExecuteTool(_ context.Context, request agentruntime.ToolCallRequest) (agentruntime.ToolCallResult, error) {
	t.calls = append(t.calls, recordedToolCall{request: request})
	if !t.approved {
		t.approved = true
		return agentruntime.ToolCallResult{InvocationID: "inv-1", Status: "waiting_approval", ApprovalID: "approval-1"}, errors.New("approval required")
	}
	if request.ApprovalID != "" && request.ApprovalID != "approval-1" {
		return agentruntime.ToolCallResult{}, errors.New("approval id was not replayed")
	}
	content := t.result
	if content == "" {
		content = "trusted gateway result"
	}
	encoded, _ := json.Marshal(map[string]string{"result": content})
	return agentruntime.ToolCallResult{InvocationID: "inv-1", Status: "succeeded", Result: encoded}, nil
}

func testBinding() ToolBinding {
	identity := registry.Identity{ProviderID: "paios.native", ToolID: "memory.search", Version: "v1"}
	alias := AliasFor(identity)
	return ToolBinding{Alias: alias, Identity: identity, Definition: modelgateway.ToolDefinition{Name: alias, Description: "approved search", Parameters: json.RawMessage(testArgsSchema)}}
}
func testAgentRequest(caller agentruntime.ToolCaller) agentruntime.Request {
	return agentruntime.Request{TaskID: "task-1", GoalID: "goal-1", Title: "Research a topic", Description: "Find relevant facts", SuccessCriteria: []string{"results reviewed"}, MaxSteps: 8, MaxModelCalls: 4, MaxToolCalls: 4, MaxTokens: 2000, Deadline: time.Now().Add(time.Minute), Tools: caller}
}

func responseWithToolCall(alias string) modelgateway.Response {
	return modelgateway.Response{ToolCalls: []modelgateway.ToolCall{{ID: "call-1", Name: alias, Arguments: json.RawMessage(`{"query":"topic"}`)}}, Usage: modelgateway.Usage{InputTokens: 10, OutputTokens: 3, TotalTokens: 13}}
}

func finalResponse() modelgateway.Response {
	return modelgateway.Response{Content: "The gateway returned data for review.", Usage: modelgateway.Usage{InputTokens: 20, OutputTokens: 5, TotalTokens: 25}}
}

func TestModelAgentRoundTripsToolResultThroughCoreCaller(t *testing.T) {
	binding := testBinding()
	model := &queuedModel{responses: []modelgateway.Response{{ToolCalls: []modelgateway.ToolCall{{ID: "call-1", Name: binding.Alias, Arguments: json.RawMessage(`{"query":"topic"}`)}}, Usage: modelgateway.Usage{InputTokens: 10, OutputTokens: 3, TotalTokens: 13}}, {Content: "The gateway returned data for review.", Usage: modelgateway.Usage{InputTokens: 20, OutputTokens: 5, TotalTokens: 25}}}}
	caller := &approvalToolCaller{approved: true}
	agent, err := NewModelAgent("research", "test-model", model, []ToolBinding{binding}, 4, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.Execute(context.Background(), testAgentRequest(caller))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != agentruntime.OutcomeUnverified || result.Verified || result.ModelCalls != 2 || result.ToolCalls != 1 || len(caller.calls) != 1 {
		t.Fatalf("Agent result=%+v calls=%d", result, len(caller.calls))
	}
	call := caller.calls[0].request
	if call.ProviderID != binding.Identity.ProviderID || call.ToolID != binding.Identity.ToolID || call.Version != binding.Identity.Version || call.StepID == "" || call.IdempotencyKey == "" {
		t.Fatalf("Gateway identity was not resolved from the trusted alias: %+v", call)
	}
	if len(model.requests) != 2 {
		t.Fatalf("model requests=%d", len(model.requests))
	}
	second := model.requests[1]
	if len(second.Messages) < 4 || second.Messages[len(second.Messages)-1].Role != "tool" || second.Messages[len(second.Messages)-1].ToolCallID != "call-1" || !strings.Contains(second.Messages[len(second.Messages)-1].Content, "trusted gateway result") {
		t.Fatalf("tool result was not correlated in follow-up messages: %+v", second.Messages)
	}
	if len(second.Tools) != 1 || second.Tools[0].Name != binding.Alias {
		t.Fatalf("model catalog leaked or changed tool alias: %+v", second.Tools)
	}
}

func TestModelAgentApprovalCheckpointResumesExactCall(t *testing.T) {
	binding := testBinding()
	model := &queuedModel{responses: []modelgateway.Response{{ToolCalls: []modelgateway.ToolCall{{ID: "stable-call", Name: binding.Alias, Arguments: json.RawMessage(`{"query":"same query"}`)}}}, {Content: "Results are ready for independent review."}}}
	caller := &approvalToolCaller{}
	agent, err := NewModelAgent("research", "test-model", model, []ToolBinding{binding}, 4, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var latest *agentruntime.Checkpoint
	save := func(_ context.Context, checkpoint agentruntime.Checkpoint) error {
		copy := checkpoint
		copy.Progress = append(json.RawMessage(nil), checkpoint.Progress...)
		latest = &copy
		return nil
	}
	request := testAgentRequest(caller)
	if _, err := agent.ExecuteResumable(context.Background(), request, nil, save, nil); err == nil {
		t.Fatal("first Tool request must stop for approval")
	}
	if latest == nil {
		t.Fatal("approval-waiting Tool request must be checkpointed")
	}
	var persisted modelAgentState
	if err := json.Unmarshal(latest.Progress, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Pending) != 1 || persisted.Pending[0].ApprovalID != "approval-1" || persisted.Pending[0].CallID != "stable-call" {
		t.Fatalf("pending call was not persisted exactly: %+v", persisted.Pending)
	}
	firstCall := caller.calls[0].request
	result, err := agent.ExecuteResumable(context.Background(), request, latest, save, nil)
	if err != nil {
		t.Fatalf("resume exact approved call: %v", err)
	}
	if len(caller.calls) != 2 {
		t.Fatalf("Gateway calls=%d, want initial request and approved continuation", len(caller.calls))
	}
	secondCall := caller.calls[1].request
	if firstCall.ProviderID != secondCall.ProviderID || firstCall.ToolID != secondCall.ToolID || firstCall.Version != secondCall.Version || firstCall.StepID != secondCall.StepID || firstCall.IdempotencyKey != secondCall.IdempotencyKey || string(firstCall.Arguments) != string(secondCall.Arguments) {
		t.Fatalf("resumed call changed approved identity/parameters: first=%+v second=%+v", firstCall, secondCall)
	}
	if secondCall.ApprovalID != "approval-1" || result.ModelCalls != 2 || result.ToolCalls != 1 || result.Verified {
		t.Fatalf("resumed result=%+v continuation=%+v", result, secondCall)
	}
}

func TestModelAgentRebuildsCompletedToolResultsWithoutCheckpointingContent(t *testing.T) {
	binding := testBinding()
	caller := &approvalToolCaller{approved: true, result: "secret tool output must not enter checkpoint"}
	firstModel := &queuedModel{responses: []modelgateway.Response{responseWithToolCall(binding.Alias)}, errors: []error{nil, errors.New("simulated model timeout after Tool completed")}}
	firstAgent, err := NewModelAgent("research", "test-model", firstModel, []ToolBinding{binding}, 4, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint *agentruntime.Checkpoint
	save := func(_ context.Context, value agentruntime.Checkpoint) error {
		copy := value
		copy.Progress = append(json.RawMessage(nil), value.Progress...)
		checkpoint = &copy
		return nil
	}
	request := testAgentRequest(caller)
	if _, err := firstAgent.ExecuteResumable(context.Background(), request, nil, save, nil); err == nil {
		t.Fatal("simulated follow-up model failure must surface")
	}
	if checkpoint == nil || strings.Contains(string(checkpoint.Progress), "secret tool output") {
		t.Fatal("tool result content must not enter durable Checkpoint")
	}
	var persisted modelAgentState
	if err := json.Unmarshal(checkpoint.Progress, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.PendingIndex != 1 || len(persisted.Pending) != 1 {
		t.Fatalf("completed Tool call reference not checkpointed: %+v", persisted)
	}
	secondModel := &queuedModel{responses: []modelgateway.Response{finalResponse()}}
	secondAgent, err := NewModelAgent("research", "test-model", secondModel, []ToolBinding{binding}, 4, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := secondAgent.ExecuteResumable(context.Background(), request, checkpoint, save, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 2 || caller.calls[0].request.IdempotencyKey != caller.calls[1].request.IdempotencyKey || len(secondModel.requests) != 1 || len(secondModel.requests[0].Messages) < 4 || !strings.Contains(secondModel.requests[0].Messages[len(secondModel.requests[0].Messages)-1].Content, "secret tool output") || result.ToolCalls != 1 {
		t.Fatalf("idempotent Tool restore result=%+v calls=%+v requests=%+v", result, caller.calls, secondModel.requests)
	}
}

func TestModelAgentRejectsUnknownAliasAndToolBudgetOverflow(t *testing.T) {
	binding := testBinding()
	request := testAgentRequest(&approvalToolCaller{approved: true})
	unknown := &queuedModel{responses: []modelgateway.Response{{ToolCalls: []modelgateway.ToolCall{{Name: "tool_not_in_catalog", Arguments: json.RawMessage(`{}`)}}}}}
	agent, err := NewModelAgent("research", "test", unknown, []ToolBinding{binding}, 2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Execute(context.Background(), request); err == nil {
		t.Fatal("unknown model tool alias must be rejected")
	}
	tooMany := &queuedModel{responses: []modelgateway.Response{{ToolCalls: []modelgateway.ToolCall{{Name: binding.Alias, Arguments: json.RawMessage(`{"query":"one"}`)}, {Name: binding.Alias, Arguments: json.RawMessage(`{"query":"two"}`)}}}}}
	request.MaxToolCalls = 1
	agent, err = NewModelAgent("research", "test", tooMany, []ToolBinding{binding}, 2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Execute(context.Background(), request); !errors.Is(err, agentruntime.ErrBudgetExceeded) {
		t.Fatalf("tool budget error=%v", err)
	}
}

func TestCatalogExposesOnlyExplicitHealthyIdentities(t *testing.T) {
	binding := testBinding()
	entry := registry.Entry{Definition: registry.Definition{Identity: binding.Identity, Name: "Memory Search", Description: "search", InputSchema: json.RawMessage(testArgsSchema)}, Enabled: true, Health: registry.HealthHealthy}
	if got := CatalogFromRegistry([]registry.Entry{entry}, map[registry.Identity]bool{}); len(got) != 0 {
		t.Fatal("ungranted registered tool entered model catalog")
	}
	got := CatalogFromRegistry([]registry.Entry{entry}, map[registry.Identity]bool{binding.Identity: true})
	if len(got) != 1 || got[0].Alias != AliasFor(binding.Identity) || got[0].Alias == binding.Identity.ToolID {
		t.Fatalf("catalog=%+v", got)
	}
	entry.Health = registry.HealthUnhealthy
	if got := CatalogFromRegistry([]registry.Entry{entry}, map[registry.Identity]bool{binding.Identity: true}); len(got) != 0 {
		t.Fatal("unhealthy tool entered model catalog")
	}
}
