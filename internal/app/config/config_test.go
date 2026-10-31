// English: These tests cover safe defaults and invalid environment configuration.
// 中文：本测试覆盖安全默认值及无效环境变量配置的拒绝行为。
package config

import (
	"strings"
	"testing"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"APP_ENV", "APP_HOST", "APP_PORT", "DATABASE_URL", "LOG_LEVEL", "PAIOS_API_KEY", "MODEL_PROVIDER", "MODEL_BASE_URL", "MODEL_API_KEY", "MODEL_NAME", "OLLAMA_BASE_URL", "DATA_DIR", "FILE_STORAGE_DIR", "MAX_AGENT_STEPS", "MAX_MODEL_CALLS", "MAX_TOOL_CALLS", "MAX_MODEL_TOKENS", "MAX_EXECUTION_SECONDS", "MAX_EXECUTION_COST", "MAX_RETRIES"} {
		t.Setenv(key, "")
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
