// English: These tests ensure policy, approvals, lease checks, schema validation, and uncertain outcomes gate dispatch.
// 中文：本测试验证策略、审批、租约、Schema 和不确定执行结果均能约束工具调用。
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/policy"
	"personal-ai-os/internal/tool/registry"
)

type handlerFunc func(context.Context, json.RawMessage) (json.RawMessage, error)

func (f handlerFunc) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	return f(ctx, args)
}

type contextualTestHandler struct{ received registry.InvocationContext }

func (h *contextualTestHandler) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("contextual execution path was not used")
}
func (h *contextualTestHandler) ExecuteWithContext(_ context.Context, call registry.InvocationContext, _ json.RawMessage) (json.RawMessage, error) {
	h.received = call
	return json.RawMessage(`{"value":"ok"}`), nil
}

type testAuthorizer struct{ decision policy.Decision }

func (a testAuthorizer) Authorize(context.Context, Request, registry.Definition) (Authorization, error) {
	return Authorization{Decision: a.decision}, nil
}

type testLeaseVerifier struct{ err error }

func (v testLeaseVerifier) Verify(context.Context, Request) error { return v.err }

type testStore struct {
	pending        int
	begins         int
	completes      int
	unknown        int
	reservation    Reservation
	approvalStatus string
}

func (s *testStore) RequestApproval(context.Context, Request, registry.Definition, string) (InvocationResult, error) {
	s.pending++
	status := s.approvalStatus
	if status == "" {
		status = "waiting_approval"
	}
	return InvocationResult{InvocationID: "inv-1", Status: status, ApprovalID: "approval-1"}, nil
}
func (s *testStore) Begin(context.Context, Request, registry.Definition, string) (Reservation, error) {
	s.begins++
	return s.reservation, nil
}
func (s *testStore) Complete(context.Context, Reservation, json.RawMessage) error {
	s.completes++
	return nil
}
func (s *testStore) Fail(context.Context, Reservation, string, string) error { return nil }
func (s *testStore) MarkUnknown(context.Context, Reservation, string) error  { s.unknown++; return nil }

func TestGatewayContinuesOnlyWithPersistedApprovedInvocation(t *testing.T) {
	store := &testStore{approvalStatus: "approved", reservation: Reservation{InvocationID: "inv-1", Status: "running"}}
	gateway, calls := buildGateway(t, policy.RequireApproval, store, nil, nil)
	result, err := gateway.Execute(context.Background(), validRequest())
	if err != nil || result.Status != "succeeded" || store.pending != 1 || store.begins != 1 || *calls != 1 {
		t.Fatalf("result=%+v err=%v pending=%d begins=%d calls=%d", result, err, store.pending, store.begins, *calls)
	}
}

func buildGateway(t *testing.T, decision policy.Decision, store *testStore, handler registry.Handler, leaseErr error) (*Gateway, *int) {
	t.Helper()
	calls := new(int)
	registrySet := registry.New()
	definition := registry.Definition{
		Identity: registry.Identity{ProviderID: "builtin", ToolID: "test.read", Version: "v1"},
		Name:     "test", Source: registry.SourceBuiltin, Kind: registry.KindReadOnly, RiskLevel: 0,
		Idempotency: registry.IdempotencyIntrinsic, Timeout: time.Second, MaxInputBytes: 4096, MaxOutputBytes: 4096,
		ExecutionEnvironment: "local",
		InputSchema:          json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
		OutputSchema:         json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`),
	}
	if handler == nil {
		handler = handlerFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
			*calls++
			return json.RawMessage(`{"value":"ok"}`), nil
		})
	}
	if err := registrySet.Register(definition, handler); err != nil {
		t.Fatal(err)
	}
	return &Gateway{Registry: registrySet, Authorizer: testAuthorizer{decision: decision}, Leases: testLeaseVerifier{err: leaseErr}, Store: store}, calls
}

func validRequest() Request {
	return Request{GoalID: "goal", TaskID: "task", ExecutionID: "execution", StepID: "step-1", AgentID: "agent",
		Identity: registry.Identity{ProviderID: "builtin", ToolID: "test.read", Version: "v1"}, IdempotencyKey: "key-1",
		Lease:     execution.Lease{ExecutionID: "execution", WorkerID: "worker", Fencing: 1, LeaseUntil: time.Now().Add(time.Minute)},
		Arguments: json.RawMessage(`{"query":"hello"}`)}
}

func TestGatewayDenialAndApprovalNeverDispatch(t *testing.T) {
	store := &testStore{reservation: Reservation{InvocationID: "inv-1", Status: "running"}}
	gateway, calls := buildGateway(t, policy.Deny, store, nil, nil)
	if _, err := gateway.Execute(context.Background(), validRequest()); !errors.Is(err, ErrDenied) {
		t.Fatalf("denial error=%v", err)
	}
	if *calls != 0 || store.begins != 0 {
		t.Fatal("denied call reached reservation or handler")
	}

	gateway.Authorizer = testAuthorizer{decision: policy.RequireApproval}
	result, err := gateway.Execute(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "waiting_approval" || store.pending != 1 || *calls != 0 {
		t.Fatalf("unexpected approval result=%+v calls=%d", result, *calls)
	}
}

func TestGatewayRequiresTrustedLeaseAndStrictSchema(t *testing.T) {
	store := &testStore{reservation: Reservation{InvocationID: "inv-1", Status: "running"}}
	gateway, calls := buildGateway(t, policy.Allow, store, nil, errors.New("stale fence"))
	if _, err := gateway.Execute(context.Background(), validRequest()); err == nil {
		t.Fatal("stale lease must fail")
	}
	request := validRequest()
	request.Arguments = json.RawMessage(`{"query":"hello","unexpected":true}`)
	gateway.Leases = testLeaseVerifier{}
	if _, err := gateway.Execute(context.Background(), request); !errors.Is(err, registry.ErrInvalidInput) {
		t.Fatalf("schema error=%v", err)
	}
	if *calls != 0 || store.begins != 0 {
		t.Fatal("invalid request reached reservation or handler")
	}
}

func TestGatewayPersistsSuccessAndUnknownOutcomes(t *testing.T) {
	store := &testStore{reservation: Reservation{InvocationID: "inv-1", Status: "running"}}
	gateway, calls := buildGateway(t, policy.Allow, store, nil, nil)
	result, err := gateway.Execute(context.Background(), validRequest())
	if err != nil || result.Status != "succeeded" || store.completes != 1 || *calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, *calls)
	}

	store = &testStore{reservation: Reservation{InvocationID: "inv-2", Status: "running"}}
	gateway, calls = buildGateway(t, policy.Allow, store, handlerFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		*calls++
		return nil, errors.New("remote timed out")
	}), nil)
	result, err = gateway.Execute(context.Background(), validRequest())
	if !errors.Is(err, ErrInvocationUnknown) || result.Status != "unknown" || store.unknown != 1 {
		t.Fatalf("uncertain result=%+v err=%v unknown=%d", result, err, store.unknown)
	}
}

func TestGatewayPassesOnlyValidatedInvocationIdentityToContextualHandler(t *testing.T) {
	store := &testStore{reservation: Reservation{InvocationID: "inv-context", Status: "running"}}
	handler := &contextualTestHandler{}
	gateway, _ := buildGateway(t, policy.Allow, store, handler, nil)
	request := validRequest()
	if _, err := gateway.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	want := registry.InvocationContext{GoalID: request.GoalID, TaskID: request.TaskID, ExecutionID: request.ExecutionID, StepID: request.StepID, AgentID: request.AgentID}
	if handler.received != want {
		t.Fatalf("trusted invocation context=%+v, want %+v", handler.received, want)
	}
}

func TestGatewayRequiresPersistedApprovalBeforeContextualExternalExport(t *testing.T) {
	store := &testStore{reservation: Reservation{InvocationID: "export-inv", Status: "running"}}
	gateway, _ := buildGateway(t, policy.RequireApproval, store, nil, nil)
	request := validRequest()
	result, err := gateway.Execute(context.Background(), request)
	if err != nil || result.Status != "waiting_approval" || store.pending != 1 || store.begins != 0 {
		t.Fatalf("unapproved export result=%+v err=%v approval_requests=%d begins=%d", result, err, store.pending, store.begins)
	}
	store.approvalStatus = "approved"
	calls := 0
	approvedHandler := &countingContextualHandler{calls: &calls}
	identity := registry.Identity{ProviderID: "builtin", ToolID: "test.external", Version: "v1"}
	definition := registry.Definition{
		Identity: identity, Name: "test external export", Source: registry.SourceBuiltin,
		Kind: registry.KindExternal, RiskLevel: 2, Idempotency: registry.IdempotencyNone,
		Timeout: time.Second, MaxInputBytes: 4096, MaxOutputBytes: 4096, ExecutionEnvironment: "local",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`),
	}
	tools := registry.New()
	if err := tools.Register(definition, approvedHandler); err != nil {
		t.Fatal(err)
	}
	gateway.Registry = tools
	request.Identity = identity
	result, err = gateway.Execute(context.Background(), request)
	if err != nil || result.Status != "succeeded" || calls != 1 || store.begins != 1 {
		t.Fatalf("approved exact export result=%+v err=%v calls=%d begins=%d", result, err, calls, store.begins)
	}
	if approvedHandler.received.GoalID != request.GoalID || approvedHandler.received.TaskID != request.TaskID || approvedHandler.received.ExecutionID != request.ExecutionID || approvedHandler.received.StepID != request.StepID {
		t.Fatalf("approved export did not receive trusted invocation context: %+v", approvedHandler.received)
	}
}

type countingContextualHandler struct {
	calls    *int
	received registry.InvocationContext
}

func (h *countingContextualHandler) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("contextual execution path was not used")
}
func (h *countingContextualHandler) ExecuteWithContext(_ context.Context, call registry.InvocationContext, _ json.RawMessage) (json.RawMessage, error) {
	*h.calls++
	h.received = call
	return json.RawMessage(`{"value":"ok"}`), nil
}
