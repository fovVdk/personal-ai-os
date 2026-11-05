// English: These tests launch a real MCP stdio server subprocess using the official SDK CommandTransport.
// 中文：本测试通过官方 SDK CommandTransport 启动真实 MCP stdio 子进程 Server。
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/policy"
	toolgateway "personal-ai-os/internal/tool/gateway"
	"personal-ai-os/internal/tool/registry"
)

const stdioFixtureEnv = "PAIOS_MCP_STDIO_FIXTURE"

// TestMCPStdioServerProcess is both a test-only child-process entry point and a normal no-op test.
// 中文：TestMCPStdioServerProcess 既是仅供测试使用的子进程入口，也是普通运行时的空操作测试。
func TestMCPStdioServerProcess(t *testing.T) {
	if os.Getenv(stdioFixtureEnv) != "1" {
		return
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "stdio-fixture", Version: "1.0.0"}, nil)
	server.AddTool(&sdk.Tool{Name: "echo", Description: "stdio echo fixture", InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false}`)}, func(_ context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var input struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &input); err != nil {
			return nil, err
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: input.Message}}}, nil
	})
	server.AddTool(&sdk.Tool{Name: "crash", Description: "terminates the fixture subprocess", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		os.Exit(23)
		return nil, nil
	})
	if err := server.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	// Avoid testing.T's PASS output on stdout; the stdio stream is the MCP wire protocol.
	// 中文：避免 testing.T 将 PASS 输出写入 stdout，因为 stdio 流本身承载 MCP 线协议。
	os.Exit(0)
}

func TestRealStdioSubprocessLifecycleCrashAndRecovery(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	workingDir := t.TempDir()
	config := ServerConfig{
		ID: "stdio-fixture", Name: "Real stdio fixture", Transport: TransportStdio,
		Command: executable, Args: []string{"-test.run=^TestMCPStdioServerProcess$"}, WorkingDir: workingDir,
		Environment: map[string]string{stdioFixtureEnv: "1"}, AllowedTools: []string{"echo", "crash"}, Enabled: true,
	}
	tools := registry.New()
	manager, err := NewManager(tools, Options{ConnectTimeout: 4 * time.Second, CallTimeout: 2 * time.Second, HealthInterval: time.Second, ReconnectMin: 25 * time.Millisecond, ReconnectMax: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), []ServerConfig{config}); err != nil {
		t.Fatalf("start stdio Manager: %v", err)
	}
	defer manager.Close()
	waitConnected(t, manager, "connected")
	identity := findMCPTool(t, tools, "echo")
	initialConnection := manager.Status()[0].LastConnectedAt

	if err := manager.SetEnabled("stdio-fixture", false); err != nil {
		t.Fatalf("disable stdio Server: %v", err)
	}
	waitServerStatus(t, manager, "disabled")
	if _, err := tools.Resolve(identity); !errors.Is(err, registry.ErrUnavailable) {
		t.Fatalf("disabled stdio Tool resolve error=%v", err)
	}
	if err := manager.SetEnabled("stdio-fixture", true); err != nil {
		t.Fatalf("enable stdio Server: %v", err)
	}
	waitServerConnectedAfter(t, manager, initialConnection)

	grants := make(toolgateway.StaticGrants)
	for _, entry := range tools.List() {
		if entry.Definition.Source == registry.SourceMCP {
			grants[entry.Definition.Identity.ProviderID+"/"+entry.Definition.Identity.ToolID+"@"+entry.Definition.Identity.Version] = toolgateway.Grant{Allowed: true, Autonomy: policy.A0}
		}
	}
	invocations := newStdioInvocationStore()
	gate := &toolgateway.Gateway{Registry: tools, Authorizer: toolgateway.PolicyAuthorizer{Grants: grants}, Leases: stdioLeaseVerifier{}, Store: invocations}

	crashIdentity := findMCPTool(t, tools, "crash")
	crashRequest := withApproval(t, gate, stdioRequest(crashIdentity, "crash-before-restart", "{}"))
	crashResult, crashErr := gate.Execute(context.Background(), crashRequest)
	if !errors.Is(crashErr, toolgateway.ErrInvocationUnknown) || crashResult.Status != "unknown" || invocations.unknownCount() != 1 {
		t.Fatalf("crashed stdio invocation=%+v err=%v unknown=%d", crashResult, crashErr, invocations.unknownCount())
	}

	waitServerConnectedAfter(t, manager, manager.Status()[0].LastConnectedAt)
	echoIdentity := findMCPTool(t, tools, "echo")
	echoRequest := withApproval(t, gate, stdioRequest(echoIdentity, "echo-after-restart", `{"message":"stdio recovered"}`))
	echoResult, err := gate.Execute(context.Background(), echoRequest)
	if err != nil || echoResult.Status != "succeeded" || !containsJSONText(echoResult.Result, "stdio recovered") {
		t.Fatalf("recovered stdio invocation=%+v err=%v", echoResult, err)
	}

	if err := manager.SetEnabled("stdio-fixture", false); err != nil {
		t.Fatalf("disable recovered stdio Server: %v", err)
	}
	waitServerStatus(t, manager, "disabled")
	if err := manager.SetEnabled("stdio-fixture", true); err != nil {
		t.Fatalf("re-enable stdio Server: %v", err)
	}
	waitServerConnectedAfter(t, manager, nil)
	if err := manager.RemoveServer("stdio-fixture"); err != nil {
		t.Fatalf("unregister stdio Server: %v", err)
	}
	if len(manager.Status()) != 0 {
		t.Fatalf("removed stdio Server remains in status: %+v", manager.Status())
	}
}

func findMCPTool(t *testing.T, tools *registry.Registry, remoteName string) registry.Identity {
	t.Helper()
	for _, entry := range tools.List() {
		if entry.Definition.Source != registry.SourceMCP {
			continue
		}
		if registered, err := tools.Resolve(entry.Definition.Identity); err == nil && registered.Entry.Definition.Name == remoteName {
			return entry.Definition.Identity
		}
	}
	t.Fatalf("MCP Tool %q was not discovered and enabled", remoteName)
	return registry.Identity{}
}

func waitServerStatus(t *testing.T, manager *Manager, status string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		items := manager.Status()
		if len(items) == 1 && items[0].ConnectionStatus == status {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("MCP stdio server did not reach %q: %+v", status, manager.Status())
}

func waitServerConnectedAfter(t *testing.T, manager *Manager, previous *time.Time) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		items := manager.Status()
		if len(items) == 1 && items[0].ConnectionStatus == "connected" && items[0].ToolCount == 2 && items[0].LastConnectedAt != nil && (previous == nil || items[0].LastConnectedAt.After(*previous)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("MCP stdio server did not reconnect: %+v", manager.Status())
}

func stdioRequest(identity registry.Identity, key string, args string) toolgateway.Request {
	return toolgateway.Request{GoalID: "goal", TaskID: "task", ExecutionID: "exec", StepID: "step-" + key, AgentID: "stdio-agent", Identity: identity, IdempotencyKey: key, Lease: execution.Lease{ExecutionID: "exec", WorkerID: "stdio-worker", Fencing: 1, LeaseUntil: time.Now().Add(time.Minute)}, Arguments: json.RawMessage(args)}
}

func withApproval(t *testing.T, gateway *toolgateway.Gateway, request toolgateway.Request) toolgateway.Request {
	t.Helper()
	pending, err := gateway.Execute(context.Background(), request)
	if err != nil || pending.Status != "waiting_approval" || pending.ApprovalID == "" {
		t.Fatalf("stdio approval request=%+v err=%v", pending, err)
	}
	request.ApprovalID = pending.ApprovalID
	return request
}

func containsJSONText(raw json.RawMessage, value string) bool {
	var result struct {
		Result string `json:"result"`
	}
	return json.Unmarshal(raw, &result) == nil && strings.Contains(result.Result, value)
}

type stdioLeaseVerifier struct{}

func (stdioLeaseVerifier) Verify(context.Context, toolgateway.Request) error { return nil }

type stdioInvocationStore struct {
	mu        sync.Mutex
	states    map[string]string
	approvals map[string]string
	results   map[string]json.RawMessage
	unknown   int
}

func newStdioInvocationStore() *stdioInvocationStore {
	return &stdioInvocationStore{states: map[string]string{}, approvals: map[string]string{}, results: map[string]json.RawMessage{}}
}
func (s *stdioInvocationStore) RequestApproval(_ context.Context, request toolgateway.Request, _ registry.Definition, _ string) (toolgateway.InvocationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	approvalID := "approval-" + request.IdempotencyKey
	s.approvals[request.IdempotencyKey] = approvalID
	s.states[request.IdempotencyKey] = "waiting_approval"
	return toolgateway.InvocationResult{InvocationID: "inv-" + request.IdempotencyKey, Status: "waiting_approval", ApprovalID: approvalID}, nil
}
func (s *stdioInvocationStore) Begin(_ context.Context, request toolgateway.Request, _ registry.Definition, _ string) (toolgateway.Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[request.IdempotencyKey]
	if state == "succeeded" || state == "unknown" {
		return toolgateway.Reservation{InvocationID: "inv-" + request.IdempotencyKey, ExecutionID: request.ExecutionID, WorkerID: request.Lease.WorkerID, Fencing: request.Lease.Fencing, Status: state, Result: s.results[request.IdempotencyKey], Replay: true}, nil
	}
	if request.ApprovalID == "" || request.ApprovalID != s.approvals[request.IdempotencyKey] {
		return toolgateway.Reservation{}, errors.New("stdio Approval binding mismatch")
	}
	s.states[request.IdempotencyKey] = "running"
	return toolgateway.Reservation{InvocationID: "inv-" + request.IdempotencyKey, ExecutionID: request.ExecutionID, WorkerID: request.Lease.WorkerID, Fencing: request.Lease.Fencing, Status: "running"}, nil
}
func (s *stdioInvocationStore) Complete(_ context.Context, reservation toolgateway.Reservation, result json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.TrimPrefix(reservation.InvocationID, "inv-")
	s.states[key] = "succeeded"
	s.results[key] = append(json.RawMessage(nil), result...)
	return nil
}
func (s *stdioInvocationStore) Fail(context.Context, toolgateway.Reservation, string, string) error {
	return nil
}
func (s *stdioInvocationStore) MarkUnknown(_ context.Context, reservation toolgateway.Reservation, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.TrimPrefix(reservation.InvocationID, "inv-")
	s.states[key] = "unknown"
	s.unknown++
	return nil
}
func (s *stdioInvocationStore) unknownCount() int { s.mu.Lock(); defer s.mu.Unlock(); return s.unknown }
