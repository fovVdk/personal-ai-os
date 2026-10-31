// English: ModelGenerator requests JSON plans and rejects schema, ownership, or budget inconsistencies.
// 中文：ModelGenerator 请求 JSON 计划，并拒绝 Schema、Goal 归属或预算不一致的输出。
package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"personal-ai-os/internal/model/gateway"
)

const systemPrompt = `Create a safe, practical plan for the user's goal. Return only JSON matching this schema: {"goal_id":"...","summary":"...","estimated_hours":1,"risks":["..."],"tasks":[{"key":"stable-key","title":"...","description":"...","success_criteria":["..."],"depends_on":["other-key"],"estimated_hours":1,"risks":["..."]}]}. Do not claim that actions have already been performed. Keep external side effects out of the plan; describe work for human review.`

type ModelGenerator struct {
	Gateway gateway.Gateway
	Model   string
	Timeout time.Duration
}

func (g ModelGenerator) GenerateContext(parent context.Context, input GoalInput) (Plan, error) {
	if g.Gateway == nil {
		return Plan{}, errors.New("planner model gateway is required")
	}
	if input.ID == "" || strings.TrimSpace(input.Title) == "" {
		return Plan{}, errors.New("goal id and title are required")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Plan{}, fmt.Errorf("encode planner input: %w", err)
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	response, err := g.Gateway.Complete(ctx, gateway.Request{Model: g.Model, Timeout: timeout, MaxTokens: 4000, Temperature: 0, Messages: []gateway.Message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: string(payload)}}})
	if err != nil {
		return Plan{}, fmt.Errorf("generate plan with model: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(response.Content))
	decoder.DisallowUnknownFields()
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("decode structured plan: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Plan{}, errors.New("planner response must contain one JSON value only")
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, fmt.Errorf("validate structured plan: %w", err)
	}
	if plan.GoalID != input.ID {
		return Plan{}, errors.New("model returned a plan for a different goal")
	}
	if plan.EstimatedHours > 10000 {
		return Plan{}, errors.New("model plan estimate exceeds safety limit")
	}
	var taskHours float64
	for _, task := range plan.Tasks {
		if task.EstimatedHours > 10000 {
			return Plan{}, errors.New("model task estimate exceeds safety limit")
		}
		taskHours += task.EstimatedHours
	}
	if plan.EstimatedHours+0.01 < taskHours {
		return Plan{}, errors.New("model plan total estimate is less than the sum of task estimates")
	}
	plan.ModelCalls = 1
	plan.ModelTokens = response.Usage.TotalTokens
	plan.ModelInputTokens = response.Usage.InputTokens
	plan.ModelOutputTokens = response.Usage.OutputTokens
	return plan, nil
}
