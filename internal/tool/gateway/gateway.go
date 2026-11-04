// Package gateway is the only supported path from model/Agent Tool proposals to Tool handlers.
// 中文：gateway 是模型/Agent 工具提议到实际处理器之间唯一受支持的执行入口。
package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/policy"
	"personal-ai-os/internal/tool/registry"
)

var (
	ErrDenied             = errors.New("tool invocation denied by policy")
	ErrApprovalRequired   = errors.New("tool invocation requires human approval")
	ErrInvocationInFlight = errors.New("tool invocation is already in progress")
	ErrInvocationUnknown  = errors.New("tool invocation outcome is unknown")
)

type Request struct {
	GoalID         string            `json:"goal_id"`
	TaskID         string            `json:"task_id"`
	ExecutionID    string            `json:"execution_id"`
	StepID         string            `json:"step_id"`
	AgentID        string            `json:"agent_id"`
	Identity       registry.Identity `json:"tool"`
	IdempotencyKey string            `json:"idempotency_key"`
	ApprovalID     string            `json:"approval_id,omitempty"`
	Lease          execution.Lease   `json:"-"`
	Arguments      json.RawMessage   `json:"arguments"`
}

type InvocationResult struct {
	InvocationID string          `json:"invocation_id"`
	Status       string          `json:"status"`
	ApprovalID   string          `json:"approval_id,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	Replayed     bool            `json:"replayed,omitempty"`
}

type Authorization struct {
	Decision policy.Decision
	Reason   string
}

type Authorizer interface {
	// Authorize must resolve grants/autonomy from trusted persisted policy, not Request fields.
	Authorize(context.Context, Request, registry.Definition) (Authorization, error)
}

type LeaseVerifier interface {
	// Verify must check execution/task association, worker, fencing token, active lease and cancellation.
	Verify(context.Context, Request) error
}

type Reservation struct {
	InvocationID string
	ExecutionID  string
	WorkerID     string
	Fencing      int64
	Status       string
	Result       json.RawMessage
	Replay       bool
}

type InvocationStore interface {
	// RequestApproval persists a pending invocation and exact context-bound approval without dispatch.
	RequestApproval(context.Context, Request, registry.Definition, string) (InvocationResult, error)
	// Begin atomically reserves the unique invocation and consumes a matching one-time approval if supplied.
	Begin(context.Context, Request, registry.Definition, string) (Reservation, error)
	Complete(context.Context, Reservation, json.RawMessage) error
	Fail(context.Context, Reservation, string, string) error
	MarkUnknown(context.Context, Reservation, string) error
}

type Gateway struct {
	Registry   *registry.Registry
	Authorizer Authorizer
	Leases     LeaseVerifier
	Store      InvocationStore
}

func (g *Gateway) Execute(ctx context.Context, request Request) (InvocationResult, error) {
	if g == nil || g.Registry == nil || g.Authorizer == nil || g.Leases == nil || g.Store == nil {
		return InvocationResult{}, errors.New("tool gateway requires registry, trusted authorizer, lease verifier, and durable invocation store")
	}
	if err := validateRequest(request); err != nil {
		return InvocationResult{}, err
	}
	registered, err := g.Registry.Resolve(request.Identity)
	if err != nil {
		return InvocationResult{}, err
	}
	definition := registered.Entry.Definition
	if len(request.Arguments) > definition.MaxInputBytes {
		return InvocationResult{}, errors.New("tool arguments exceed registered input limit")
	}
	if err := registry.Validate(definition.InputSchema, request.Arguments); err != nil {
		return InvocationResult{}, err
	}
	canonical, err := canonicalize(request.Arguments)
	if err != nil {
		return InvocationResult{}, err
	}
	request.Arguments = canonical
	inputHash := RequestHash(request.Identity, canonical)
	if err := g.Leases.Verify(ctx, request); err != nil {
		return InvocationResult{}, fmt.Errorf("verify execution lease before authorization: %w", err)
	}
	authorization, err := g.Authorizer.Authorize(ctx, request, definition)
	if err != nil {
		return InvocationResult{}, fmt.Errorf("authorize tool invocation: %w", err)
	}
	switch authorization.Decision {
	case policy.Deny:
		return InvocationResult{}, fmt.Errorf("%w: %s", ErrDenied, authorization.Reason)
	case policy.RequireApproval:
		if request.ApprovalID == "" {
			approvalState, err := g.Store.RequestApproval(ctx, request, definition, inputHash)
			if err != nil {
				return InvocationResult{}, err
			}
			if approvalState.Status != "approved" {
				return approvalState, nil
			}
			request.ApprovalID = approvalState.ApprovalID
		}
	case policy.Allow:
		if request.ApprovalID != "" {
			return InvocationResult{}, errors.New("approval id supplied for an invocation that does not require approval")
		}
	default:
		return InvocationResult{}, ErrDenied
	}
	reservation, err := g.Store.Begin(ctx, request, definition, inputHash)
	if err != nil {
		return InvocationResult{}, err
	}
	if reservation.Replay {
		switch reservation.Status {
		case "succeeded":
			return InvocationResult{InvocationID: reservation.InvocationID, Status: reservation.Status, Result: append(json.RawMessage(nil), reservation.Result...), Replayed: true}, nil
		case "waiting_approval":
			return InvocationResult{InvocationID: reservation.InvocationID, Status: reservation.Status, ApprovalID: request.ApprovalID}, ErrApprovalRequired
		case "running":
			return InvocationResult{InvocationID: reservation.InvocationID, Status: reservation.Status}, ErrInvocationInFlight
		case "unknown":
			return InvocationResult{InvocationID: reservation.InvocationID, Status: reservation.Status}, ErrInvocationUnknown
		default:
			return InvocationResult{InvocationID: reservation.InvocationID, Status: reservation.Status}, errors.New("invocation is not eligible for replay")
		}
	}
	if err := g.Leases.Verify(ctx, request); err != nil {
		if persistErr := g.markUnknown(reservation, "lease lost after invocation reservation"); persistErr != nil {
			return InvocationResult{InvocationID: reservation.InvocationID, Status: "unknown"}, fmt.Errorf("lease lost before dispatch (%v); persist unknown outcome: %w", err, persistErr)
		}
		return InvocationResult{InvocationID: reservation.InvocationID, Status: "unknown"}, fmt.Errorf("verify execution lease before tool dispatch: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, definition.Timeout)
	defer cancel()
	var output json.RawMessage
	var callErr error
	if contextual, ok := registered.Handler.(registry.ContextualHandler); ok {
		output, callErr = contextual.ExecuteWithContext(callCtx, registry.InvocationContext{
			GoalID: request.GoalID, TaskID: request.TaskID, ExecutionID: request.ExecutionID,
			StepID: request.StepID, AgentID: request.AgentID,
		}, canonical)
	} else {
		output, callErr = registered.Handler.Execute(callCtx, canonical)
	}
	if callErr != nil {
		// A handler error does not prove that an external side effect did not happen.
		if err := g.markUnknown(reservation, "tool handler returned an error; external outcome is uncertain"); err != nil {
			return InvocationResult{InvocationID: reservation.InvocationID, Status: "unknown"}, fmt.Errorf("tool failed (%v) and unknown outcome persistence failed: %w", callErr, err)
		}
		return InvocationResult{InvocationID: reservation.InvocationID, Status: "unknown"}, fmt.Errorf("%w: %v", ErrInvocationUnknown, callErr)
	}
	if err := g.Leases.Verify(ctx, request); err != nil {
		if persistErr := g.markUnknown(reservation, "lease lost after tool dispatch"); persistErr != nil {
			return InvocationResult{InvocationID: reservation.InvocationID, Status: "unknown"}, fmt.Errorf("lease lost after dispatch (%v); persist unknown outcome: %w", err, persistErr)
		}
		return InvocationResult{InvocationID: reservation.InvocationID, Status: "unknown"}, fmt.Errorf("%w: lease lost after tool dispatch: %v", ErrInvocationUnknown, err)
	}
	if len(output) > definition.MaxOutputBytes {
		if err := g.markUnknown(reservation, "tool returned oversized output after dispatch"); err != nil {
			return InvocationResult{InvocationID: reservation.InvocationID, Status: "unknown"}, fmt.Errorf("oversized output; persist unknown outcome: %w", err)
		}
		return InvocationResult{InvocationID: reservation.InvocationID, Status: "unknown"}, fmt.Errorf("%w: tool output exceeds registered output limit", ErrInvocationUnknown)
	}
	if err := registry.Validate(definition.OutputSchema, output); err != nil {
		if persistErr := g.markUnknown(reservation, "tool returned schema-invalid output after dispatch"); persistErr != nil {
			return InvocationResult{InvocationID: reservation.InvocationID, Status: "unknown"}, fmt.Errorf("invalid tool output (%v); persist unknown outcome: %w", err, persistErr)
		}
		return InvocationResult{InvocationID: reservation.InvocationID, Status: "unknown"}, fmt.Errorf("%w: tool output validation: %v", ErrInvocationUnknown, err)
	}
	if err := g.Store.Complete(ctx, reservation, output); err != nil {
		if persistErr := g.markUnknown(reservation, "tool completed but durable result commit failed"); persistErr != nil {
			return InvocationResult{InvocationID: reservation.InvocationID, Status: "unknown"}, fmt.Errorf("tool result commit failed (%v); persist unknown outcome: %w", err, persistErr)
		}
		return InvocationResult{InvocationID: reservation.InvocationID, Status: "unknown"}, fmt.Errorf("tool succeeded but durable result could not be committed: %w", err)
	}
	return InvocationResult{InvocationID: reservation.InvocationID, Status: "succeeded", Result: append(json.RawMessage(nil), output...)}, nil
}

// RequestHash returns the canonical, version-bound idempotency hash used by both Step and Tool Invocation stores.
func RequestHash(identity registry.Identity, arguments json.RawMessage) string {
	canonical, err := canonicalize(arguments)
	if err != nil {
		return ""
	}
	return invocationHash(identity, canonical)
}

func (g *Gateway) markUnknown(reservation Reservation, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return g.Store.MarkUnknown(ctx, reservation, reason)
}

func validateRequest(request Request) error {
	if !validIdentifier(request.GoalID, 128) || !validIdentifier(request.TaskID, 128) || !validIdentifier(request.ExecutionID, 128) || !validIdentifier(request.StepID, 128) || !validIdentifier(request.AgentID, 128) || !validIdentifier(request.Identity.ProviderID, 128) || !validIdentifier(request.Identity.ToolID, 128) || !validIdentifier(request.Identity.Version, 128) || !validIdentifier(request.IdempotencyKey, 256) {
		return errors.New("goal, task, execution, step, agent, exact tool identity, and idempotency key are required")
	}
	if request.ApprovalID != "" && !validIdentifier(request.ApprovalID, 128) || len(request.Arguments) == 0 || !json.Valid(request.Arguments) {
		return errors.New("tool idempotency key or arguments are invalid")
	}
	if request.Lease.ExecutionID != request.ExecutionID || !validIdentifier(request.Lease.WorkerID, 256) || request.Lease.Fencing < 1 {
		return errors.New("tool request must carry the active execution lease identity")
	}
	return nil
}

func validIdentifier(value string, maxLength int) bool {
	return value != "" && len(value) <= maxLength && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func canonicalize(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode tool arguments: %w", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize tool arguments: %w", err)
	}
	return encoded, nil
}

func invocationHash(identity registry.Identity, canonical json.RawMessage) string {
	payload, _ := json.Marshal(struct {
		Identity  IdentityHash    `json:"identity"`
		Arguments json.RawMessage `json:"arguments"`
	}{Identity: IdentityHash{ProviderID: identity.ProviderID, ToolID: identity.ToolID, Version: identity.Version}, Arguments: canonical})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

type IdentityHash struct {
	ProviderID string `json:"provider_id"`
	ToolID     string `json:"tool_id"`
	Version    string `json:"version"`
}
