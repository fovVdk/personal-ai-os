// English: This adapter runs a registered framework-neutral Agent for a claimed durable execution.
// 中文：本适配器使用已注册的框架无关 Agent 执行被持久化 Worker 认领的任务。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"personal-ai-os/internal/agent/registry"
	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/supervisor"
	toolgateway "personal-ai-os/internal/tool/gateway"
	toolregistry "personal-ai-os/internal/tool/registry"
)

type durableAgentExecutor struct {
	agents   *registry.Registry
	tools    *toolgateway.Gateway
	verifier agentruntime.ResultVerifier
}

func (e durableAgentExecutor) Execute(ctx context.Context, run execution.Run, checkpoint *execution.Checkpoint, progress execution.Progress) (execution.ExecutionResult, error) {
	if e.agents == nil {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: errors.New("agent registry is unavailable")}
	}
	var request agentruntime.Request
	if err := json.Unmarshal(run.Payload, &request); err != nil {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: fmt.Errorf("decode persisted Agent request: %w", err)}
	}
	request.TaskID = run.TaskID
	request.GoalID = run.GoalID
	request.ExecutionID = run.ID
	toolCaller := &durableToolCaller{gateway: e.tools, run: run, progress: progress, maxCalls: request.MaxToolCalls}
	request.Tools = toolCaller
	if checkpoint != nil {
		request.ResumeCheckpoint = append(json.RawMessage(nil), checkpoint.Progress...)
		request.CompletedSteps = append([]string(nil), checkpoint.CompletedSteps...)
		request.CheckpointSequence = checkpoint.Sequence
		request.StateReference = checkpoint.StateReference
	}
	if deadline, ok := ctx.Deadline(); ok {
		request.Deadline = deadline
	}
	profile, err := e.agents.Get(run.AgentID)
	if err != nil {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: fmt.Errorf("select persisted Agent %q: %w", run.AgentID, err)}
	}
	resumable, supportsResume := profile.Agent.(agentruntime.ResumableAgent)
	if run.Idempotent && !supportsResume {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: errors.New("idempotent execution requires a ResumableAgent with step-level idempotency support")}
	}
	var result agentruntime.Result
	if supportsResume {
		result, err = resumable.ExecuteResumable(ctx, request, convertCheckpoint(checkpoint), func(checkpointCtx context.Context, value agentruntime.Checkpoint) error {
			return progress.SaveCheckpoint(checkpointCtx, execution.Checkpoint{StepID: value.StepID, Progress: value.Progress, CompletedSteps: value.CompletedSteps, StateReference: value.StateReference, SchemaVersion: value.SchemaVersion})
		}, progress.ClaimStep)
	} else {
		result, err = profile.Agent.Execute(ctx, request)
	}
	if err != nil {
		return execution.ExecutionResult{}, err
	}
	// Enforce the budget using Core-observed calls, not the Agent-reported counter.
	// 中文：预算以 Core 实际观察到的调用次数为准，而不是 Agent 自报的计数。
	if observedCalls := int(toolCaller.calls.Load()); observedCalls > result.ToolCalls {
		result.ToolCalls = observedCalls
	}
	if result.Outcome != agentruntime.OutcomeSucceeded || result.Steps < 1 || result.Steps > request.MaxSteps {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: supervisor.ErrUnverifiedResult}
	}
	if result.ModelCalls < 0 || result.ToolCalls < 0 || result.ModelTokens < result.InputTokens+result.OutputTokens || result.InputTokens < 0 || result.OutputTokens < 0 || result.ModelTokens > request.MaxTokens || result.ModelCalls > request.MaxModelCalls || result.ToolCalls > request.MaxToolCalls || result.Cost < 0 || math.IsNaN(result.Cost) || math.IsInf(result.Cost, 0) || request.MaxCost > 0 && result.Cost > request.MaxCost {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: errors.New("Agent result exceeded persisted execution budget")}
	}
	if e.verifier == nil {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: supervisor.ErrUnverifiedResult}
	}
	verification, err := e.verifier.Verify(ctx, request, result)
	if err != nil || !verification.Verified || strings.TrimSpace(verification.Method) == "" || len(verification.Evidence) == 0 {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: supervisor.ErrUnverifiedResult}
	}
	completedAt := time.Now().UTC()
	if !request.Deadline.IsZero() && completedAt.After(request.Deadline) {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailureUncertain, Err: context.DeadlineExceeded}
	}
	evidence := make([]execution.Evidence, 0, len(verification.Evidence))
	supervisorEvidence := make([]supervisor.Evidence, 0, len(verification.Evidence))
	for _, item := range verification.Evidence {
		evidence = append(evidence, execution.Evidence{CriterionID: item.CriterionID, CriterionVersion: item.CriterionVersion, Source: item.Source, Description: item.Description, Reference: item.Reference, ContentHash: item.ContentHash, VerifierType: item.VerifierType, VerifierVersion: item.VerifierVersion, ObservedAt: completedAt})
		supervisorEvidence = append(supervisorEvidence, supervisor.Evidence{TaskID: run.TaskID, CriterionID: item.CriterionID, CriterionVersion: item.CriterionVersion, Source: item.Source, Description: item.Description, Reference: item.Reference, ContentHash: item.ContentHash, VerifierType: item.VerifierType, VerifierVersion: item.VerifierVersion, ObservedAt: completedAt})
	}
	output := supervisor.Execution{ID: run.ID, TaskID: run.TaskID, AgentID: profile.ID, Summary: result.Summary, Outcome: result.Outcome,
		InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, TotalTokens: result.ModelTokens, ModelCalls: result.ModelCalls,
		ToolCalls: result.ToolCalls, Evidence: supervisorEvidence, CompletedAt: completedAt, Verified: true, VerificationMethod: verification.Method}
	encoded, err := json.Marshal(output)
	if err != nil {
		return execution.ExecutionResult{}, fmt.Errorf("encode durable Agent result: %w", err)
	}
	return execution.ExecutionResult{Result: encoded, Evidence: evidence, VerifiedResult: true}, nil
}

type durableToolCaller struct {
	gateway  *toolgateway.Gateway
	run      execution.Run
	progress execution.Progress
	maxCalls int
	calls    atomic.Int32
}

func (c *durableToolCaller) ExecuteTool(ctx context.Context, call agentruntime.ToolCallRequest) (agentruntime.ToolCallResult, error) {
	if c.gateway == nil {
		return agentruntime.ToolCallResult{}, errors.New("Tool Gateway is not configured")
	}
	if c.maxCalls < 1 || int(c.calls.Add(1)) > c.maxCalls {
		return agentruntime.ToolCallResult{}, agentruntime.ErrBudgetExceeded
	}
	if call.StepID == "" || len(call.StepID) > 128 || call.IdempotencyKey == "" || len(call.IdempotencyKey) > 256 {
		return agentruntime.ToolCallResult{}, errors.New("Tool Call requires a bounded Step ID and idempotency key")
	}
	if c.progress == nil {
		return agentruntime.ToolCallResult{}, errors.New("durable execution step store is unavailable")
	}
	identity := toolregistry.Identity{ProviderID: call.ProviderID, ToolID: call.ToolID, Version: call.Version}
	lease := execution.Lease{ExecutionID: c.run.ID, WorkerID: c.run.WorkerID, Fencing: c.run.FencingToken, LeaseUntil: c.run.LeaseUntil}
	if _, err := c.progress.ClaimStep(ctx, call.StepID, call.IdempotencyKey, toolgateway.RequestHash(identity, call.Arguments)); err != nil {
		return agentruntime.ToolCallResult{}, fmt.Errorf("claim durable Tool step: %w", err)
	}
	result, err := c.gateway.Execute(ctx, toolgateway.Request{
		GoalID: c.run.GoalID, TaskID: c.run.TaskID, ExecutionID: c.run.ID, StepID: call.StepID, AgentID: c.run.AgentID,
		Identity:       identity,
		IdempotencyKey: call.IdempotencyKey, ApprovalID: call.ApprovalID,
		Lease:     lease,
		Arguments: call.Arguments,
	})
	if err == nil && result.Status == "waiting_approval" {
		err = execution.Failure{Class: execution.FailureApprovalNeeded, Err: toolgateway.ErrApprovalRequired}
	}
	return agentruntime.ToolCallResult{InvocationID: result.InvocationID, Status: result.Status, ApprovalID: result.ApprovalID, Result: result.Result, Replayed: result.Replayed}, err
}

func convertCheckpoint(checkpoint *execution.Checkpoint) *agentruntime.Checkpoint {
	if checkpoint == nil {
		return nil
	}
	return &agentruntime.Checkpoint{StepID: checkpoint.StepID, Progress: append(json.RawMessage(nil), checkpoint.Progress...),
		CompletedSteps: append([]string(nil), checkpoint.CompletedSteps...), StateReference: checkpoint.StateReference,
		SchemaVersion: checkpoint.SchemaVersion}
}
