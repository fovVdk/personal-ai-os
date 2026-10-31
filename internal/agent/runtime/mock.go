// English: Mock agents return deterministic, explicitly unverified results without external side effects.
// 中文：Mock Agent 生成确定性且明确标记为未验证的结果，不执行外部副作用。
package runtime

import (
	"context"
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
	return Result{Summary: summary, Evidence: []Evidence{{Source: "mock-agent", Description: "Mock output generated; the task result has not been independently verified."}}, Steps: 1, Outcome: OutcomeUnverified}, nil
}
