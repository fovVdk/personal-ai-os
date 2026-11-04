// English: These tests prove that the durable Core ignores Agent self-attestation.
// 中文：本测试确保 Durable Core 不信任 Agent 自报的验证结论。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"personal-ai-os/internal/agent/registry"
	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/supervisor"
)

type claimsSuccessAgent struct{}

func (claimsSuccessAgent) ID() string { return "claims-success" }
func (claimsSuccessAgent) Execute(context.Context, agentruntime.Request) (agentruntime.Result, error) {
	return agentruntime.Result{
		Outcome: agentruntime.OutcomeSucceeded, Verified: true, Steps: 1,
		ModelCalls: 1, InputTokens: 1, OutputTokens: 1, ModelTokens: 2,
		Summary:  "agent self-asserted success",
		Evidence: []agentruntime.Evidence{{Source: "agent", Description: "claimed evidence", Reference: "model://claim"}},
	}, nil
}

type fixedResultVerifier struct{ verification agentruntime.Verification }

func (v fixedResultVerifier) Verify(context.Context, agentruntime.Request, agentruntime.Result) (agentruntime.Verification, error) {
	return v.verification, nil
}

func durableExecutorFixture(t *testing.T) (*registry.Registry, execution.Run) {
	t.Helper()
	agents := registry.New()
	if err := agents.Register(registry.Profile{ID: "claims-success", Name: "Claiming Agent", Version: "test", Agent: claimsSuccessAgent{}}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(agentruntime.Request{TaskID: "task-1", GoalID: "goal-1", MaxSteps: 2, MaxModelCalls: 2, MaxTokens: 100, MaxToolCalls: 0, Deadline: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return agents, execution.Run{ID: "run-1", TaskID: "task-1", GoalID: "goal-1", AgentID: "claims-success", Payload: payload}
}

func TestDurableExecutorRejectsAgentSelfVerification(t *testing.T) {
	agents, run := durableExecutorFixture(t)
	executor := durableAgentExecutor{agents: agents}
	_, err := executor.Execute(context.Background(), run, nil, nil)
	if !errors.Is(err, supervisor.ErrUnverifiedResult) {
		t.Fatalf("without Core verifier, Agent self-attestation must fail; err=%v", err)
	}
}

func TestDurableExecutorPersistsOnlyVerifierEvidence(t *testing.T) {
	agents, run := durableExecutorFixture(t)
	trusted := agentruntime.Verification{Verified: true, Method: "test-artifact-check/v1", Evidence: []agentruntime.Evidence{{Source: "filesystem-checker", Description: "artifact hash matched expected value", Reference: "sha256:abc123"}}}
	executor := durableAgentExecutor{agents: agents, verifier: fixedResultVerifier{verification: trusted}}
	result, err := executor.Execute(context.Background(), run, nil, nil)
	if err != nil {
		t.Fatalf("verified execution: %v", err)
	}
	var saved supervisor.Execution
	if err := json.Unmarshal(result.Result, &saved); err != nil {
		t.Fatal(err)
	}
	if !result.VerifiedResult || !saved.Verified || saved.VerificationMethod != trusted.Method || len(saved.Evidence) != 1 || saved.Evidence[0].Source != "filesystem-checker" || saved.Evidence[0].Reference != "sha256:abc123" {
		t.Fatalf("Core did not persist verifier authority/evidence: result=%+v saved=%+v", result, saved)
	}
	if saved.Evidence[0].Source == "agent" || saved.Evidence[0].Reference == "model://claim" {
		t.Fatalf("agent-supplied evidence was persisted as verified: %+v", saved.Evidence)
	}
}
