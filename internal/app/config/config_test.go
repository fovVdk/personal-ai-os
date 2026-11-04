// English: These tests cover safe defaults and invalid environment configuration.
// 中文：本测试覆盖安全默认值及无效环境变量配置的拒绝行为。
package config

import (
	"fmt"
	"strings"
	"testing"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"APP_ENV", "APP_HOST", "APP_PORT", "DATABASE_URL", "LOG_LEVEL", "PAIOS_API_KEY", "MODEL_PROVIDER", "MODEL_BASE_URL", "MODEL_API_KEY", "MODEL_NAME", "OLLAMA_BASE_URL", "DATA_DIR", "FILE_STORAGE_DIR", "HTTP_FETCH_ALLOWED_HOSTS", "MCP_SERVERS_JSON", "MAX_AGENT_STEPS", "MAX_MODEL_CALLS", "MAX_TOOL_CALLS", "MAX_MODEL_TOKENS", "MAX_EXECUTION_SECONDS", "MAX_EXECUTION_COST", "MAX_RETRIES", "PAIOS_WORKER_ID", "PAIOS_WORKER_COUNT", "PAIOS_WORKER_LEASE_SECONDS", "PAIOS_WORKER_HEARTBEAT_SECONDS", "PAIOS_WORKER_POLL_MILLISECONDS", "PAIOS_WORKER_RECOVERY_SECONDS", "SHUTDOWN_GRACE_SECONDS"} {
		t.Setenv(key, "")
	}
}

func TestLoadMCPServerConfiguration(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MCP_SERVERS_JSON", `[{"id":"docs","name":"Docs MCP","transport":"streamable_http","endpoint":"https://mcp.example.com/rpc","allowed_hosts":["mcp.example.com"],"allowed_tools":["search"],"enabled":false}]`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load MCP server config: %v", err)
	}
	if len(cfg.MCPServers) != 1 || cfg.MCPServers[0].ID != "docs" {
		t.Fatalf("MCP servers=%+v", cfg.MCPServers)
	}
	clearConfigEnv(t)
	t.Setenv("MCP_SERVERS_JSON", `[{"id":"docs","name":"Docs MCP","transport":"streamable_http","endpoint":"http://example.com/rpc","allowed_hosts":["example.com"]}]`)
	if _, err := Load(); err == nil {
		t.Fatal("non-loopback MCP HTTP endpoint must be rejected")
	}
	clearConfigEnv(t)
	t.Setenv("MCP_SERVERS_JSON", `[{"id":"docs","name":"Docs MCP","transport":"streamable_http","endpoint":"https://mcp.example.com/rpc","allowed_hosts":["mcp.example.com"],"unexpected":true}]`)
	if _, err := Load(); err == nil {
		t.Fatal("unknown MCP config fields must be rejected")
	}
	clearConfigEnv(t)
	servers := make([]string, 33)
	for index := range servers {
		servers[index] = fmt.Sprintf(`{"id":"s%d","name":"Server %d","transport":"streamable_http","endpoint":"https://mcp.example.com/rpc","allowed_hosts":["mcp.example.com"]}`, index, index)
	}
	t.Setenv("MCP_SERVERS_JSON", "["+strings.Join(servers, ",")+"]")
	if _, err := Load(); err == nil {
		t.Fatal("more than 32 MCP servers must be rejected")
	}
}

func TestLoadHTTPFetchHostAllowlist(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HTTP_FETCH_ALLOWED_HOSTS", "docs.example.com, api.example.com")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load host allowlist: %v", err)
	}
	if len(cfg.HTTPFetchAllowedHosts) != 2 || cfg.HTTPFetchAllowedHosts[0] != "docs.example.com" {
		t.Fatalf("hosts=%v", cfg.HTTPFetchAllowedHosts)
	}
	clearConfigEnv(t)
	t.Setenv("HTTP_FETCH_ALLOWED_HOSTS", "*.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("wildcard host allowlist must be rejected")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearConfigEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != "8080" || cfg.ModelProvider != "mock" || cfg.MaxAgentSteps < 1 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestProductionRequiresAPIKey(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_ENV", "production")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PAIOS_API_KEY") {
		t.Fatalf("expected production API key error, got %v", err)
	}
}

func TestProductionRejectsLoopbackHost(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAIOS_API_KEY", strings.Repeat("k", 32))
	if _, err := Load(); err == nil {
		t.Fatal("production should require an explicit non-loopback host")
	}
}

func TestLoadRejectsShortAPIKey(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("PAIOS_API_KEY", "too-short")
	if _, err := Load(); err == nil {
		t.Fatal("expected short API key to fail validation")
	}
}

func TestLoadRejectsInvalidExecutionBudgets(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MAX_TOOL_CALLS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid budget error")
	}
}

func TestLoadRejectsEmptyHost(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_HOST", " ")
	if _, err := Load(); err == nil {
		t.Fatal("expected empty host to fail validation")
	}
}

func TestLoadRejectsOutOfRangePort(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_PORT", "70000")
	if _, err := Load(); err == nil {
		t.Fatal("expected out-of-range port to fail validation")
	}
}

func TestLoadRejectsExcessiveExecutionTimeout(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MAX_EXECUTION_SECONDS", "86401")
	if _, err := Load(); err == nil {
		t.Fatal("expected excessive execution timeout to be rejected")
	}
}

func TestLoadAcceptsIPv6LoopbackHost(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_HOST", "::1")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load IPv6 host: %v", err)
	}
	if cfg.Host != "::1" {
		t.Fatalf("host=%q", cfg.Host)
	}
}

func TestLoadRejectsHostContainingPort(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_HOST", "0.0.0.0:8080")
	if _, err := Load(); err == nil {
		t.Fatal("expected host with embedded port to be rejected")
	}
}

func TestLoadRejectsOllamaModelProviderWithoutEndpoint(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MODEL_PROVIDER", "ollama")
	t.Setenv("OLLAMA_BASE_URL", " ")
	if _, err := Load(); err == nil {
		t.Fatal("expected blank Ollama endpoint to be rejected")
	}
}

func TestLoadRejectsRealProviderWithoutExplicitModelName(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MODEL_PROVIDER", "ollama")
	if _, err := Load(); err == nil {
		t.Fatal("real model provider must require an explicit model name")
	}
}

func TestLoadRejectsOpenAIProviderWithoutCredentials(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MODEL_PROVIDER", "openai-compatible")
	t.Setenv("MODEL_BASE_URL", "https://example.invalid/v1")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing model API key to be rejected")
	}
}

func TestLoadAcceptsConfiguredOpenAICompatibleProvider(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MODEL_PROVIDER", "openai-compatible")
	t.Setenv("MODEL_BASE_URL", "https://models.example/v1")
	t.Setenv("MODEL_API_KEY", "provider-secret")
	t.Setenv("MODEL_NAME", "safe-model")
	if _, err := Load(); err != nil {
		t.Fatalf("load configured model: %v", err)
	}
}

func TestLoadAcceptsAuthenticatedProductionBind(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("APP_HOST", "192.0.2.10")
	t.Setenv("PAIOS_API_KEY", strings.Repeat("k", 32))
	if _, err := Load(); err != nil {
		t.Fatalf("load authenticated production bind: %v", err)
	}
}
