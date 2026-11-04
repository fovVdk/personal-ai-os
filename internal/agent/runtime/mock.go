// English: Mock agents return deterministic, explicitly unverified results without external side effects.
// 中文：Mock Agent 生成确定性且明确标记为未验证的结果，不执行外部副作用。
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// MockAgent gives deterministic, explicit results for local integration and acceptance tests.
// 中文：MockAgent 为本地集成与验收测试提供确定性、明确标记的结果。
type MockAgent struct {
	AgentID string
	Summary string
}

// IdempotencyGuarantee reports that MockAgent performs no external side effects and is safe to replay.
// 中文：IdempotencyGuarantee 声明 MockAgent 不执行外部副作用，可安全重复运行。
func (MockAgent) IdempotencyGuarantee() bool { return true }

func (a MockAgent) ExecuteResumable(ctx context.Context, request Request, checkpoint *Checkpoint, save CheckpointWriter, claim StepIdempotencyClaim) (Result, error) {
	input, err := json.Marshal(struct {
		TaskID string
		Title  string
	}{TaskID: request.TaskID, Title: request.Title})
	if err != nil {
		return Result{}, fmt.Errorf("encode mock step identity: %w", err)
	}
	sum := sha256.Sum256(input)
	claimed, err := claim(ctx, "mock-plan", "mock-plan:"+request.TaskID, hex.EncodeToString(sum[:]))
	if err != nil {
		return Result{}, err
	}
	_ = claimed // Duplicate claims are safe because this Agent performs no external side effects.
	if save != nil {
		progress, _ := json.Marshal(map[string]string{"phase": "generated", "task_id": request.TaskID})
		if err := save(ctx, Checkpoint{StepID: "mock-plan", Progress: progress, CompletedSteps: []string{"mock-plan"}, SchemaVersion: 1}); err != nil {
			return Result{}, err
		}
	}
	return a.Execute(ctx, request)
}

func (a MockAgent) ID() string {
	if a.AgentID == "" {
		return "planning"
	}
	return a.AgentID
}
func (a MockAgent) Execute(ctx context.Context, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if request.MaxSteps < 1 || request.MaxModelCalls < 0 || request.MaxToolCalls < 0 || request.MaxCost < 0 || math.IsNaN(request.MaxCost) || math.IsInf(request.MaxCost, 0) {
		return Result{}, ErrBudgetExceeded
	}
	if request.Deadline.IsZero() || !time.Now().Before(request.Deadline) {
		return Result{}, ErrBudgetExceeded
	}
	if request.Title == "" {
		return Result{}, fmt.Errorf("agent request title is required")
	}
	summary := a.Summary
	if summary == "" {
		summary = fmt.Sprintf("Prepared a reviewable next step for %s.", request.Title)
	}
	return Result{Summary: summary, Evidence: []Evidence{{Source: "mock-agent", Description: "Mock output generated; the task result has not been independently verified."}}, Steps: 1, Outcome: OutcomeUnverified, Verified: false}, nil
}
