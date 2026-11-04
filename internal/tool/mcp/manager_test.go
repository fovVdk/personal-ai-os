// English: These tests use the official SDK client and server over real Streamable HTTP.
// 中文：本测试通过真实 Streamable HTTP 使用官方 SDK Client/Server 验证 MCP 适配器。
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/policy"
	toolgateway "personal-ai-os/internal/tool/gateway"
	"personal-ai-os/internal/tool/registry"
)

const echoSchema = `{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false}`

func newTestMCPServer(t *testing.T, schema string, toolError bool, block bool, badOutput ...bool) (*httptest.Server, *sdk.Server, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	server := sdk.NewServer(&sdk.Implementation{Name: "mcp-fixture", Version: "1.0.0"}, nil)
	server.AddTool(&sdk.Tool{Name: "echo", Description: "echo fixture", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}, InputSchema: json.RawMessage(schema), OutputSchema: json.RawMessage(`{"type":"object","properties":{"echo":{"type":"string"}},"required":["echo"],"additionalProperties":false}`)}, func(ctx context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls.Add(1)
		if block {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "eventually returned"}}}, nil
			}
		}
		if len(badOutput) > 0 && badOutput[0] {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "unexpected"}}, StructuredContent: map[string]string{"unexpected": "shape"}}, nil
		}
		if toolError {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "remote error"}}}, nil
		}
		var input struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &input); err != nil {
			return nil, err
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: input.Message}}, StructuredContent: map[string]string{"echo": input.Message}}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return httptest.NewServer(handler), server, calls
}

func testServerConfig(t *testing.T, endpoint string, enabled bool, allowlist ...string) ServerConfig {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return ServerConfig{ID: "fixture", Name: "Fixture MCP", Transport: TransportStreamableHTTP, Endpoint: endpoint, AllowedHosts: []string{parsed.Hostname()}, AllowedTools: allowlist, Enabled: enabled}
}

func waitConnected(t *testing.T, manager *Manager, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		items := manager.Status()
		if len(items) == 1 && items[0].ConnectionStatus == want && items[0].ToolCount > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("MCP server did not reach %q: %+v", want, manager.Status())
}

func TestRealSDKStreamableHTTPDiscoveryGatewayApprovalAndLifecycle(t *testing.T) {
	serverHTTP, server, serverCalls := newTestMCPServer(t, echoSchema, false, false)
	defer serverHTTP.Close()
	tools := registry.New()
	manager, err := NewManager(tools, Options{CallTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), []ServerConfig{testServerConfig(t, serverHTTP.URL, true, "echo")}); err != nil {
		t.Fatal(err)
	}
	waitConnected(t, manager, "connected")
	defer manager.Close()
	items := tools.List()
	var discovered registry.Identity
	for _, item := range items {
		if item.Definition.Source == registry.SourceMCP {
			discovered = item.Definition.Identity
		}
	}
	if discovered.ToolID == "" || discovered.ProviderID != "mcp.fixture" || discovered.Version == "" {
		t.Fatalf("unexpected discovered identity: %+v", discovered)
	}
	entry, err := tools.Resolve(discovered)
	if err != nil {
		t.Fatalf("discovered Tool not healthy: %v", err)
	}
	if entry.Entry.Definition.RiskLevel != 3 || entry.Entry.Definition.Idempotency != registry.IdempotencyNone {
		t.Fatalf("untrusted MCP hints changed policy metadata: %+v", entry.Entry.Definition)
	}

	store := &gatewayStore{calls: serverCalls}
	grants := toolgateway.StaticGrants{discovered.ProviderID + "/" + discovered.ToolID + "@" + discovered.Version: {Allowed: true, Autonomy: policy.A0}}
	gate := &toolgateway.Gateway{Registry: tools, Authorizer: toolgateway.PolicyAuthorizer{Grants: grants}, Leases: gatewayLease{}, Store: store}
	request := gatewayRequest(discovered)
	gate.Authorizer = toolgateway.PolicyAuthorizer{Grants: toolgateway.StaticGrants{}}
	if _, err := gate.Execute(context.Background(), request); !errors.Is(err, toolgateway.ErrDenied) || serverCalls.Load() != 0 {
		t.Fatalf("missing operator grant was not denied: err=%v calls=%d", err, serverCalls.Load())
	}
	gate.Authorizer = toolgateway.PolicyAuthorizer{Grants: grants}
	pending, err := gate.Execute(context.Background(), request)
	if err != nil || pending.Status != "waiting_approval" || serverCalls.Load() != 0 {
		t.Fatalf("pre-approval result=%+v err=%v toolCalls=%d", pending, err, serverCalls.Load())
	}
	request.ApprovalID = pending.ApprovalID
	result, err := gate.Execute(context.Background(), request)
	if err != nil || result.Status != "succeeded" || store.completeCount != 1 || serverCalls.Load() != 1 || !strings.Contains(string(result.Result), "hello MCP") {
		t.Fatalf("Gateway MCP result=%+v err=%v toolCalls=%d", result, err, serverCalls.Load())
	}
	replayed, err := gate.Execute(context.Background(), request)
	if err != nil || !replayed.Replayed || replayed.Status != "succeeded" || serverCalls.Load() != 1 {
		t.Fatalf("repeated MCP invocation=%+v err=%v toolCalls=%d", replayed, err, serverCalls.Load())
	}
	if !manager.IsToolAllowed(discovered) {
		t.Fatal("operator-allowed MCP tool did not resolve its explicit grant")
	}

	invalid := gatewayRequest(discovered)
	invalid.Arguments = json.RawMessage(`{"message":3}`)
	if _, err := gate.Execute(context.Background(), invalid); !errors.Is(err, registry.ErrInvalidInput) {
		t.Fatalf("invalid MCP input error=%v", err)
	}
	if serverCalls.Load() != 1 {
		t.Fatalf("schema-invalid MCP invocation reached server: calls=%d", serverCalls.Load())
	}

	oldIdentity := discovered
	changedSchema := `{"type":"object","properties":{"message":{"type":"string"},"suffix":{"type":"string"}},"required":["message"],"additionalProperties":false}`
	server.AddTool(&sdk.Tool{Name: "echo", Description: "updated echo", InputSchema: json.RawMessage(changedSchema)}, func(_ context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(request.Params.Arguments)}}}, nil
	})
	if err := manager.RefreshTools(context.Background(), "fixture"); err != nil {
		t.Fatalf("rediscover changed MCP tool: %v", err)
	}
	updatedFound := false
	for _, item := range tools.List() {
		if item.Definition.Identity.ToolID == oldIdentity.ToolID && item.Definition.Identity.Version != oldIdentity.Version && item.Enabled && item.Health == registry.HealthHealthy {
			updatedFound = true
		}
	}
	if !updatedFound {
		t.Fatal("changed MCP schema was not registered under a new version")
	}
	if _, err := tools.Resolve(oldIdentity); !errors.Is(err, registry.ErrUnavailable) {
		t.Fatalf("old MCP tool version remains executable after schema change: %v", err)
	}
	if err := manager.SetEnabled("fixture", false); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Resolve(oldIdentity); !errors.Is(err, registry.ErrUnavailable) {
		t.Fatalf("disabled MCP Tool resolve error=%v", err)
	}
	if err := manager.SetEnabled("fixture", true); err != nil {
		t.Fatal(err)
	}
	waitConnected(t, manager, "connected")
	if err := manager.RemoveServer("fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.getServer("fixture"); err == nil {
		t.Fatal("removed MCP server remains registered")
	}
}

func TestRealSDKMCPErrorAndTimeoutBecomeUnknown(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		toolError, block, badOutput, cancelCall bool
	}{{"remote error", true, false, false, false}, {"timeout", false, true, false, false}, {"parent context cancellation", false, true, false, true}, {"unexpected output", false, false, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			server, _, calls := newTestMCPServer(t, echoSchema, tc.toolError, tc.block, tc.badOutput)
			defer server.Close()
			tools := registry.New()
			callTimeout := 120 * time.Millisecond
			if tc.cancelCall {
				callTimeout = 5 * time.Second
			}
			manager, err := NewManager(tools, Options{CallTimeout: callTimeout})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Start(context.Background(), []ServerConfig{testServerConfig(t, server.URL, true, "echo")}); err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			waitConnected(t, manager, "connected")
			var identity registry.Identity
			for _, item := range tools.List() {
				if item.Definition.Source == registry.SourceMCP {
					identity = item.Definition.Identity
				}
			}
			store := &gatewayStore{calls: calls}
			grant := toolgateway.StaticGrants{identity.ProviderID + "/" + identity.ToolID + "@" + identity.Version: {Allowed: true, Autonomy: policy.A0}}
			gate := &toolgateway.Gateway{Registry: tools, Authorizer: toolgateway.PolicyAuthorizer{Grants: grant}, Leases: gatewayLease{}, Store: store}
			request := gatewayRequest(identity)
			pending, err := gate.Execute(context.Background(), request)
			if err != nil || pending.Status != "waiting_approval" {
				t.Fatalf("approval request=%+v err=%v", pending, err)
			}
			request.ApprovalID = pending.ApprovalID
			callCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type callOutcome struct {
				result toolgateway.InvocationResult
				err    error
			}
			outcomes := make(chan callOutcome, 1)
			go func() {
				result, callErr := gate.Execute(callCtx, request)
				outcomes <- callOutcome{result: result, err: callErr}
			}()
			if tc.cancelCall {
				deadline := time.Now().Add(2 * time.Second)
				for calls.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if calls.Load() == 0 {
					t.Fatal("MCP server did not receive call before cancellation")
				}
				cancel()
			}
			var result toolgateway.InvocationResult
			select {
			case outcome := <-outcomes:
				result, err = outcome.result, outcome.err
			case <-time.After(5 * time.Second):
				t.Fatal("MCP Tool call did not return after timeout/cancellation")
			}
			if !errors.Is(err, toolgateway.ErrInvocationUnknown) || result.Status != "unknown" || store.unknownCount != 1 || calls.Load() != 1 {
				t.Fatalf("result=%+v err=%v unknown=%d calls=%d", result, err, store.unknownCount, calls.Load())
			}
		})
	}
}

func TestMCPDefinitionRejectsUnknownSchemaAndUnsafeTransport(t *testing.T) {
	if _, err := sanitizeObjectSchema([]byte(`{"type":"object","properties":{"x":{"type":"string","pattern":".*"}},"additionalProperties":false}`), 4096); err == nil {
		t.Fatal("unknown property constraint must be rejected")
	}
	if err := ValidateServerConfig(ServerConfig{ID: "unsafe", Name: "unsafe", Transport: TransportStreamableHTTP, Endpoint: "http://example.test/mcp", AllowedHosts: []string{"example.test"}}); err == nil {
		t.Fatal("non-loopback MCP HTTP must require TLS")
	}
	if err := ValidateServerConfig(ServerConfig{ID: "shell", Name: "shell", Transport: TransportStdio, Command: "/bin/sh", WorkingDir: "/tmp", Enabled: true}); err == nil {
		t.Fatal("shell command must not be accepted as MCP stdio server")
	}
	if err := ValidateServerConfig(ServerConfig{ID: "bad", Name: "bad", Transport: TransportStreamableHTTP, Endpoint: "https://example.test/mcp", AllowedHosts: []string{"other.example"}}); err == nil {
		t.Fatal("MCP endpoint outside exact allowlist must be rejected")
	}
	for _, host := range []string{"*.example.com", "bad host", "-invalid.example", "example..com", "host:443"} {
		if validHost(host) {
			t.Errorf("invalid allowed host %q was accepted", host)
		}
	}
	for _, address := range []string{"100.64.0.1", "192.0.2.1", "198.18.0.1", "203.0.113.1", "64:ff9b::a00:1", "2001:db8::1", "2002::1"} {
		if publicMCPAddress(net.ParseIP(address), false) {
			t.Errorf("special-use MCP endpoint IP %s was allowed", address)
		}
	}
}

func TestRealSDKServerRestartReconnectsAndChangesToolVersion(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	first := serveSDKServer(t, listener, "1.0.0", echoSchema)
	tools := registry.New()
	manager, err := NewManager(tools, Options{HealthInterval: time.Second, ReconnectMin: 20 * time.Millisecond, ReconnectMax: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), []ServerConfig{testServerConfig(t, "http://"+address, true, "echo")}); err != nil {
		t.Fatal(err)
	}
	waitConnected(t, manager, "connected")
	var oldIdentity registry.Identity
	for _, item := range tools.List() {
		if item.Definition.Source == registry.SourceMCP {
			oldIdentity = item.Definition.Identity
		}
	}
	first.Close()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		items := manager.Status()
		if len(items) == 1 && items[0].ConnectionStatus == "disconnected" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	listener, err = net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("rebind local MCP server endpoint: %v", err)
	}
	second := serveSDKServer(t, listener, "2.0.0", `{"type":"object","properties":{"message":{"type":"string"},"suffix":{"type":"string"}},"required":["message"],"additionalProperties":false}`)
	defer second.Close()
	defer manager.Close()
	waitConnected(t, manager, "connected")
	var newVersionFound bool
	for _, item := range tools.List() {
		if item.Definition.Identity.ToolID == oldIdentity.ToolID && item.Definition.Identity.Version != oldIdentity.Version && item.Enabled && item.Health == registry.HealthHealthy {
			newVersionFound = true
		}
	}
	if !newVersionFound {
		t.Fatalf("restarted server's changed Tool definition wasn't versioned: %+v", tools.List())
	}
	if _, err := tools.Resolve(oldIdentity); !errors.Is(err, registry.ErrUnavailable) {
		t.Fatalf("old Tool version remained enabled after server restart: %v", err)
	}
}

func serveSDKServer(t *testing.T, listener net.Listener, version, schema string) *httptest.Server {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "mcp-fixture", Version: version}, nil)
	server.AddTool(&sdk.Tool{Name: "echo", Description: "restart fixture", InputSchema: json.RawMessage(schema)}, func(_ context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(request.Params.Arguments)}}}, nil
	})
	httpServer := httptest.NewUnstartedServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	httpServer.Listener = listener
	httpServer.Start()
	return httpServer
}

type gatewayLease struct{}

func (gatewayLease) Verify(context.Context, toolgateway.Request) error { return nil }

type gatewayStore struct {
	calls                       *atomic.Int32
	completeCount, unknownCount int
	result                      json.RawMessage
}

func (s *gatewayStore) RequestApproval(context.Context, toolgateway.Request, registry.Definition, string) (toolgateway.InvocationResult, error) {
	return toolgateway.InvocationResult{InvocationID: "inv-test", Status: "waiting_approval", ApprovalID: "approval-test"}, nil
}
func (s *gatewayStore) Begin(_ context.Context, request toolgateway.Request, _ registry.Definition, _ string) (toolgateway.Reservation, error) {
	if s.completeCount > 0 {
		return toolgateway.Reservation{InvocationID: "inv-test", ExecutionID: request.ExecutionID, WorkerID: request.Lease.WorkerID, Fencing: request.Lease.Fencing, Status: "succeeded", Result: append(json.RawMessage(nil), s.result...), Replay: true}, nil
	}
	if request.ApprovalID != "approval-test" {
		return toolgateway.Reservation{}, errors.New("approval mismatch")
	}
	return toolgateway.Reservation{InvocationID: "inv-test", ExecutionID: request.ExecutionID, WorkerID: request.Lease.WorkerID, Fencing: request.Lease.Fencing, Status: "running"}, nil
}
func (s *gatewayStore) Complete(_ context.Context, _ toolgateway.Reservation, result json.RawMessage) error {
	s.completeCount++
	s.result = append(json.RawMessage(nil), result...)
	return nil
}
func (s *gatewayStore) Fail(context.Context, toolgateway.Reservation, string, string) error {
	return nil
}
func (s *gatewayStore) MarkUnknown(context.Context, toolgateway.Reservation, string) error {
	s.unknownCount++
	return nil
}
func gatewayRequest(identity registry.Identity) toolgateway.Request {
	return toolgateway.Request{GoalID: "goal", TaskID: "task", ExecutionID: "exec", StepID: "step", AgentID: "agent", Identity: identity, IdempotencyKey: "mcp-key", Lease: execution.Lease{ExecutionID: "exec", WorkerID: "worker", Fencing: 1, LeaseUntil: time.Now().Add(time.Minute)}, Arguments: json.RawMessage(`{"message":"hello MCP"}`)}
}
