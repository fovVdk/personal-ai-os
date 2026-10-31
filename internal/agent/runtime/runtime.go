// Package runtime provides provider-independent agent execution contracts.
// 中文：runtime 包定义与具体 Provider/框架无关的 Agent 执行契约。
// English: This file defines provider-independent agent requests, budgets, outcomes, and evidence.
// 中文：本文件定义与供应商无关的 Agent 请求、预算、结果状态和证据结构。
package runtime

import (
	"context"
	"errors"
	"time"
)

var ErrBudgetExceeded = errors.New("agent execution budget exceeded")

type Request struct {
	TaskID          string
	GoalID          string
	Title           string
	Description     string
	SuccessCriteria []string
	MaxSteps        int
	MaxModelCalls   int
	MaxTokens       int
	MaxToolCalls    int
	MaxCost         float64
	Deadline        time.Time
}

type Result struct {
	Summary      string
	Evidence     []Evidence
	Steps        int
	Cost         float64
	ModelCalls   int
	ModelTokens  int
	InputTokens  int
	OutputTokens int
	ToolCalls    int
	Outcome      Outcome
}

type Outcome string

const (
	OutcomeSucceeded  Outcome = "succeeded"
	OutcomeFailed     Outcome = "failed"
	OutcomeUnknown    Outcome = "unknown"
	OutcomeUnverified Outcome = "unverified"
)

type Evidence struct {
	Source      string
	Description string
	Reference   string
}

type Agent interface {
	ID() string
	Execute(context.Context, Request) (Result, error)
}
