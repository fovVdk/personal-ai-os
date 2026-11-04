// English: These tests verify Tool registration and model-controlled identity cannot create authorization grants.
// 中文：本测试验证工具注册以及模型提供的身份信息都不能自行产生授权。
package gateway

import (
	"context"
	"testing"

	"personal-ai-os/internal/policy"
	"personal-ai-os/internal/tool/registry"
)

func TestPolicyAuthorizerDefaultsToDenyAndHighRiskNeedsApproval(t *testing.T) {
	definition := registry.Definition{Identity: registry.Identity{ProviderID: "mcp.example", ToolID: "admin", Version: "v1"}, RiskLevel: 3}
	authorizer := PolicyAuthorizer{Grants: StaticGrants{}}
	result, err := authorizer.Authorize(context.Background(), Request{AgentID: "agent"}, definition)
	if err != nil || result.Decision != policy.Deny {
		t.Fatalf("missing grant result=%+v err=%v", result, err)
	}
	authorizer.Grants = StaticGrants{grantKey(definition.Identity): {Allowed: true, Autonomy: policy.A0}}
	result, err = authorizer.Authorize(context.Background(), Request{}, definition)
	if err != nil || result.Decision != policy.RequireApproval {
		t.Fatalf("high-risk result=%+v err=%v", result, err)
	}
}
