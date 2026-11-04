// English: This adapter combines the explicit native allowlist with operator-approved MCP tool names.
// 中文：本适配器组合原生工具显式白名单和运维配置的 MCP 工具授权。
package main

import (
	"context"

	"personal-ai-os/internal/policy"
	toolgateway "personal-ai-os/internal/tool/gateway"
	toolmcp "personal-ai-os/internal/tool/mcp"
	"personal-ai-os/internal/tool/registry"
)

type configuredToolGrants struct {
	native toolgateway.StaticGrants
	mcp    *toolmcp.Manager
}

// ResolveGrant only returns explicitly configured grants; discovered server metadata cannot add a grant.
// 中文：ResolveGrant 只返回显式配置的授权；Server 发现的元数据不能自行增权。
func (g configuredToolGrants) ResolveGrant(ctx context.Context, request toolgateway.Request, definition registry.Definition) (toolgateway.Grant, error) {
	if grant, ok := g.native[definition.Identity.ProviderID+"/"+definition.Identity.ToolID+"@"+definition.Identity.Version]; ok {
		return grant, nil
	}
	if g.mcp != nil && g.mcp.IsToolAllowed(definition.Identity) {
		return toolgateway.Grant{Allowed: true, Autonomy: policy.A0}, nil
	}
	return toolgateway.Grant{}, nil
}
