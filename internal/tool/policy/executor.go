// Package policy provides a mandatory authorization gate for tool side effects.
// 中文：policy 包为工具副作用提供强制授权门。
// English: This executor runs the core authorization and exact approval check before invoking a tool.
// 中文：本执行器在调用工具前强制执行核心权限判断和精确审批参数验证。
package policy

import (
	"context"
	"errors"
	"time"

	"personal-ai-os/internal/approval"
	"personal-ai-os/internal/policy"
)

var ErrApprovalRequired = errors.New("tool execution requires an approved request")

type Tool interface {
	Execute(context.Context, map[string]any) (any, error)
}

// Executor evaluates core policy and validates exact approval binding before invoking a tool.
// 中文：Executor 在调用工具前评估核心策略并验证精确审批绑定。
type Executor struct {
	Now func() time.Time
}

func (e Executor) Execute(ctx context.Context, toolID, action string, level policy.AutonomyLevel, allowed, approvalRequired bool, parameters map[string]any, approved *approval.Request, tool Tool) (any, error) {
	decision, err := policy.Evaluate(policy.Request{ToolID: toolID, Action: action, Level: level, ToolAllowed: allowed, ApprovalRequired: approvalRequired})
	if err != nil {
		return nil, err
	}
	if decision.Decision == policy.Deny {
		return nil, errors.New(decision.Reason)
	}
	if decision.Decision == policy.RequireApproval {
		if approved == nil {
			return nil, ErrApprovalRequired
		}
		now := time.Now().UTC()
		if e.Now != nil {
			now = e.Now().UTC()
		}
		if err := approved.ValidFor(toolID, action, parameters, now); err != nil {
			return nil, err
		}
	}
	if tool == nil {
		return nil, errors.New("tool implementation is required")
	}
	return tool.Execute(ctx, parameters)
}
