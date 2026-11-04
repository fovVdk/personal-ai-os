// Package policy evaluates tool actions independently of agent and model runtimes.
// 中文：policy 包独立于 Agent 和模型 Runtime 评估工具动作。
// English: This policy engine maps A0–A3 autonomy levels to allow, approval, or deny decisions.
// 中文：本策略引擎依据 A0–A3 自主等级返回允许、需审批或拒绝决策。
package policy

import (
	"errors"
	"fmt"
)

type AutonomyLevel int

const (
	A0 AutonomyLevel = iota
	A1
	A2
	A3
)

type Decision string

const (
	Allow           Decision = "allow"
	RequireApproval Decision = "require_approval"
	Deny            Decision = "deny"
)

var ErrInvalidLevel = errors.New("autonomy level must be between A0 and A3")

type Request struct {
	ToolID           string
	Action           string
	Level            AutonomyLevel
	RiskLevel        int
	ToolAllowed      bool
	ApprovalRequired bool
}

type Result struct {
	Decision Decision
	Reason   string
}

// Evaluate applies deterministic policy rules before any side-effectful tool is invoked.
// 中文：Evaluate 在调用任何有副作用的工具前执行确定性策略判断。
func Evaluate(request Request) (Result, error) {
	if request.Level < A0 || request.Level > A3 {
		return Result{}, fmt.Errorf("%w: %d", ErrInvalidLevel, request.Level)
	}
	if request.RiskLevel < 0 || request.RiskLevel > 3 {
		return Result{Decision: Deny, Reason: "tool risk level is invalid"}, nil
	}
	if request.ToolID == "" || request.Action == "" {
		return Result{Decision: Deny, Reason: "tool and action are required"}, nil
	}
	if !request.ToolAllowed {
		return Result{Decision: Deny, Reason: "tool is not explicitly allowed"}, nil
	}
	if request.Level == A3 || request.RiskLevel == 3 || request.ApprovalRequired {
		return Result{Decision: RequireApproval, Reason: "human approval is required"}, nil
	}
	return Result{Decision: Allow, Reason: "permitted by autonomy policy"}, nil
}
