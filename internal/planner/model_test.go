// English: These tests validate model-generated plan schemas, estimates, and context cancellation.
// 中文：本测试验证模型生成计划的 Schema、估算值及 Context 取消行为。
// 中文：本测试验证模型计划 Schema、估算一致性及调用方取消传播。
package planner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/model/gateway"
)

type fixedGateway struct {
	response string
	err      error
	seen     gateway.Request
}

func (g *fixedGateway) Complete(ctx context.Context, request gateway.Request) (gateway.Response, error) {
	g.seen = request
	if err := ctx.Err(); err != nil {
		return gateway.Response{}, err
	}
	if g.err != nil {
		return gateway.Response{}, g.err
	}
	return gateway.Response{Content: g.response, Provider: "mock"}, nil
}

func TestModelGeneratorValidatesStructuredOutput(t *testing.T) {
	valid := Plan{GoalID: "goal-1", Summary: "safe staged plan", EstimatedHours: 1, Tasks: []TaskSpec{{Key: "review", Title: "Review requirements", SuccessCriteria: []string{"criteria recorded"}, EstimatedHours: 1}}}
	payload, _ := json.Marshal(valid)
	gatewayMock := &fixedGateway{response: string(payload)}
	generator := ModelGenerator{Gateway: gatewayMock, Model: "mock", Timeout: time.Second}
	criterion := goal.SuccessCriterion{ID: "sources", Name: "Cited sources", Type: goal.CriterionVerifiedEvidenceCount, Required: true, ValidationMethod: goal.CriterionMethodVerifiedExecutionEvidence, MinEvidence: 2, Source: goal.CriterionSourceUser, Confirmed: true, Version: 1}
	plan, err := generator.GenerateContext(context.Background(), GoalInput{ID: "goal-1", Title: "Goal", Criteria: []goal.SuccessCriterion{criterion}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.GoalID != "goal-1" || gatewayMock.seen.MaxTokens != 4000 {
		t.Fatalf("plan=%+v request=%+v", plan, gatewayMock.seen)
	}
	if len(gatewayMock.seen.Messages) != 2 || !strings.Contains(gatewayMock.seen.Messages[1].Content, `"criteria":[{"id":"sources"`) {
		t.Fatalf("Planner request omitted structured Goal criteria: %+v", gatewayMock.seen.Messages)
	}
	gatewayMock.response = `{"goal_id":"other","summary":"bad","tasks":[{"key":"x","title":"x"}],"unexpected":true}`
	if _, err := generator.GenerateContext(context.Background(), GoalInput{ID: "goal-1", Title: "Goal"}); err == nil {
		t.Fatal("unknown model schema fields must be rejected")
	}
}

func TestModelGeneratorRejectsTaskEstimateMismatch(t *testing.T) {
	valid := Plan{GoalID: "goal-1", Summary: "plan", EstimatedHours: 1, Tasks: []TaskSpec{{Key: "a", Title: "A", EstimatedHours: 2}}}
	payload, _ := json.Marshal(valid)
	generator := ModelGenerator{Gateway: &fixedGateway{response: string(payload)}, Model: "mock"}
	if _, err := generator.GenerateContext(context.Background(), GoalInput{ID: "goal-1", Title: "goal"}); err == nil {
		t.Fatal("inconsistent total estimate must be rejected")
	}
}

func TestModelGeneratorUsesCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	model := &fixedGateway{response: `{"goal_id":"goal-1","summary":"plan","estimated_hours":1,"tasks":[{"key":"a","title":"A","estimated_hours":1}]}`}
	_, err := (ModelGenerator{Gateway: model, Model: "mock"}).GenerateContext(ctx, GoalInput{ID: "goal-1", Title: "Goal"})
	if err == nil {
		t.Fatal("cancelled caller context must cancel planning")
	}
}
