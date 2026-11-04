package gateway

import (
	"context"
	"errors"

	"personal-ai-os/internal/policy"
	"personal-ai-os/internal/tool/registry"
)

// Grant is supplied by a trusted policy source, never by an Agent or model ToolCall.
// 中文：Grant 只能由可信策略源提供，绝不接受 Agent 或模型 ToolCall 自行声明。
type Grant struct {
	Allowed          bool
	Autonomy         policy.AutonomyLevel
	ApprovalRequired bool
}

type GrantResolver interface {
	ResolveGrant(context.Context, Request, registry.Definition) (Grant, error)
}

type PolicyAuthorizer struct {
	Grants GrantResolver
}

// Authorize maps a trusted grant and registered risk profile into the central Policy Engine.
// 中文：Authorize 将可信授权与已注册风险级别映射到中央 Policy Engine。
func (a PolicyAuthorizer) Authorize(ctx context.Context, request Request, definition registry.Definition) (Authorization, error) {
	if a.Grants == nil {
		return Authorization{Decision: policy.Deny, Reason: "trusted Tool grant resolver is not configured"}, nil
	}
	grant, err := a.Grants.ResolveGrant(ctx, request, definition)
	if err != nil {
		return Authorization{}, err
	}
	result, err := policy.Evaluate(policy.Request{
		ToolID: definition.Identity.ProviderID + "/" + definition.Identity.ToolID,
		Action: definition.Identity.ToolID, Level: grant.Autonomy, RiskLevel: definition.RiskLevel,
		ToolAllowed: grant.Allowed, ApprovalRequired: grant.ApprovalRequired,
	})
	if err != nil {
		return Authorization{}, err
	}
	return Authorization{Decision: result.Decision, Reason: result.Reason}, nil
}

var ErrNoGrant = errors.New("no Tool grant is configured")

type StaticGrants map[string]Grant

func (g StaticGrants) ResolveGrant(_ context.Context, _ Request, definition registry.Definition) (Grant, error) {
	grant, ok := g[grantKey(definition.Identity)]
	if !ok {
		return Grant{}, nil
	}
	return grant, nil
}

func grantKey(identity registry.Identity) string {
	return identity.ProviderID + "/" + identity.ToolID + "@" + identity.Version
}
