// English: This adapter runs a registered framework-neutral Agent for a claimed durable execution.
// 中文：本适配器使用已注册的框架无关 Agent 执行被持久化 Worker 认领的任务。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"personal-ai-os/internal/agent/registry"
	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/supervisor"
)

type durableAgentExecutor struct{ agents *registry.Registry }

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
	if result.Outcome != agentruntime.OutcomeSucceeded || !result.Verified || result.Steps < 1 || result.Steps > request.MaxSteps || len(result.Evidence) == 0 {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: supervisor.ErrUnverifiedResult}
	}
	if result.ModelCalls < 0 || result.ToolCalls < 0 || result.ModelTokens < result.InputTokens+result.OutputTokens || result.InputTokens < 0 || result.OutputTokens < 0 || result.ModelTokens > request.MaxTokens || result.ModelCalls > request.MaxModelCalls || result.ToolCalls > request.MaxToolCalls || result.Cost < 0 || math.IsNaN(result.Cost) || math.IsInf(result.Cost, 0) || request.MaxCost > 0 && result.Cost > request.MaxCost {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: errors.New("Agent result exceeded persisted execution budget")}
	}
	completedAt := time.Now().UTC()
	if !request.Deadline.IsZero() && completedAt.After(request.Deadline) {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailureUncertain, Err: context.DeadlineExceeded}
	}
	evidence := make([]execution.Evidence, 0, len(result.Evidence))
	supervisorEvidence := make([]supervisor.Evidence, 0, len(result.Evidence))
	for _, item := range result.Evidence {
		evidence = append(evidence, execution.Evidence{Source: item.Source, Description: item.Description, Reference: item.Reference, ObservedAt: completedAt})
		supervisorEvidence = append(supervisorEvidence, supervisor.Evidence{TaskID: run.TaskID, Source: item.Source, Description: item.Description, Reference: item.Reference, ObservedAt: completedAt})
	}
	output := supervisor.Execution{ID: run.ID, TaskID: run.TaskID, AgentID: profile.ID, Summary: result.Summary, Outcome: result.Outcome,
		InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, TotalTokens: result.ModelTokens, ModelCalls: result.ModelCalls,
		Evidence: supervisorEvidence, CompletedAt: completedAt, Verified: result.Verified}
	encoded, err := json.Marshal(output)
	if err != nil {
		return execution.ExecutionResult{}, fmt.Errorf("encode durable Agent result: %w", err)
	}
	return execution.ExecutionResult{Result: encoded, Evidence: evidence, VerifiedResult: result.Verified}, nil
}

func convertCheckpoint(checkpoint *execution.Checkpoint) *agentruntime.Checkpoint {
	if checkpoint == nil {
		return nil
	}
	return &agentruntime.Checkpoint{StepID: checkpoint.StepID, Progress: append(json.RawMessage(nil), checkpoint.Progress...),
		CompletedSteps: append([]string(nil), checkpoint.CompletedSteps...), StateReference: checkpoint.StateReference,
		SchemaVersion: checkpoint.SchemaVersion}
}
