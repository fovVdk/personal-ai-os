// Package runtime provides provider-independent agent execution contracts.
// 中文：runtime 包定义与具体 Provider/框架无关的 Agent 执行契约。
// English: This file defines provider-independent agent requests, budgets, outcomes, and evidence.
// 中文：本文件定义与供应商无关的 Agent 请求、预算、结果状态和证据结构。
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"personal-ai-os/internal/goal"
)

var ErrBudgetExceeded = errors.New("agent execution budget exceeded")

type Request struct {
	OwnerID            string
	TaskID             string
	GoalID             string
	Title              string
	Description        string
	SuccessCriteria    []string
	GoalCriteria       []goal.SuccessCriterion
	TaskCriterionIDs   []string
	MaxSteps           int
	MaxModelCalls      int
	MaxTokens          int
	MaxToolCalls       int
	MaxCost            float64
	Deadline           time.Time
	ResumeCheckpoint   json.RawMessage
	CompletedSteps     []string
	CheckpointSequence int64
	StateReference     string
	Tools              ToolCaller `json:"-"`
}

type ToolCallRequest struct {
	ProviderID     string
	ToolID         string
	Version        string
	StepID         string
	IdempotencyKey string
	ApprovalID     string
	Arguments      json.RawMessage
}

type ToolCallResult struct {
	InvocationID string
	Status       string
	ApprovalID   string
	Result       json.RawMessage
	Replayed     bool
}

// ToolCaller exposes only the Go Core Gateway contract; Agent code never receives native Tool handlers.
// 中文：ToolCaller 只暴露 Go Core Gateway 契约；Agent 代码不会收到 Native Tool Handler。
type ToolCaller interface {
	ExecuteTool(context.Context, ToolCallRequest) (ToolCallResult, error)
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
	Verified     bool
}

// Verification is produced by a Core-injected verifier, never by an Agent/model response.
// 中文：Verification 只能由 Core 注入的验证器生成，不能由 Agent/模型响应决定。
type Verification struct {
	Verified bool
	Method   string
	Evidence []Evidence
}

// ResultVerifier independently checks an Agent's claims against durable artifacts or trusted external state.
// 中文：ResultVerifier 根据持久化产物或可信外部状态独立核验 Agent 声明。
type ResultVerifier interface {
	Verify(context.Context, Request, Result) (Verification, error)
}

type Outcome string

const (
	OutcomeSucceeded  Outcome = "succeeded"
	OutcomeFailed     Outcome = "failed"
	OutcomeUnknown    Outcome = "unknown"
	OutcomeUnverified Outcome = "unverified"
)

type Evidence struct {
	CriterionID      string
	CriterionVersion int
	Source           string
	Description      string
	Reference        string
	ContentHash      string
	VerifierType     string
	VerifierVersion  string
}

type Agent interface {
	ID() string
	Execute(context.Context, Request) (Result, error)
}

// IdempotencyDeclaration is an explicit capability; absence means external effects must not be replayed automatically.
// 中文：IdempotencyDeclaration 显式声明可重放能力；未实现此接口表示不得自动重放外部副作用。
type IdempotencyDeclaration interface {
	IdempotencyGuarantee() bool
}

type Checkpoint struct {
	StepID         string
	Progress       json.RawMessage
	CompletedSteps []string
	StateReference string
	SchemaVersion  int
}

type CheckpointWriter func(context.Context, Checkpoint) error
type StepIdempotencyClaim func(context.Context, string, string, string) (bool, error)

// ResumableAgent can restore from a checkpoint and persist progress as steps finish.
// 中文：ResumableAgent 可从检查点恢复，并在步骤完成时持久化进度。
type ResumableAgent interface {
	ExecuteResumable(context.Context, Request, *Checkpoint, CheckpointWriter, StepIdempotencyClaim) (Result, error)
}
