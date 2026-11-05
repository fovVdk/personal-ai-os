// Package config loads and validates application configuration from environment variables.
// 中文：config 包从环境变量加载并校验应用配置。
// English: This file loads and validates the environment-backed application configuration.
// 中文：本文件从环境变量加载应用配置，并在启动前验证安全与资源预算约束。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"personal-ai-os/internal/tool/mcp"
)

// Config contains runtime settings. Secrets are intentionally never included in String output.
// 中文：Config 保存运行时设置；Secret 不得通过其字符串形式暴露。
type Config struct {
	Environment           string
	Host                  string
	Port                  string
	DatabaseURL           string
	LogLevel              string
	APIKey                string
	ModelProvider         string
	ModelBaseURL          string
	ModelAPIKey           string
	ModelName             string
	OllamaBaseURL         string
	DataDir               string
	FileStorageDir        string
	ReportExportDir       string
	HTTPFetchAllowedHosts []string
	// MCPServers are operator-provided definitions; empty disables MCP connections.
	// 中文：MCPServers 由运维配置；为空时不连接 MCP Server。
	MCPServers       []mcp.ServerConfig
	MaxAgentSteps    int
	MaxModelCalls    int
	MaxToolCalls     int
	MaxModelTokens   int
	MaxExecutionCost float64
	MaxExecutionTime time.Duration
	MaxRetries       int
	WorkerID         string
	WorkerCount      int
	WorkerLease      time.Duration
	WorkerHeartbeat  time.Duration
	WorkerPoll       time.Duration
	WorkerRecovery   time.Duration
	ShutdownGrace    time.Duration
	RequestTimeout   time.Duration
	ShutdownTimeout  time.Duration
}

// Load reads environment variables and applies safe local-development defaults.
// 中文：Load 从环境变量读取配置，并应用适用于本地开发的安全默认值。
func Load() (Config, error) {
	mcpServers, err := mcpServersEnv()
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Environment:           env("APP_ENV", "development"),
		Host:                  env("APP_HOST", "127.0.0.1"),
		Port:                  env("APP_PORT", "8080"),
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		LogLevel:              strings.ToLower(env("LOG_LEVEL", "info")),
		APIKey:                os.Getenv("PAIOS_API_KEY"),
		ModelProvider:         env("MODEL_PROVIDER", "mock"),
		ModelBaseURL:          os.Getenv("MODEL_BASE_URL"),
		ModelAPIKey:           os.Getenv("MODEL_API_KEY"),
		ModelName:             env("MODEL_NAME", "mock"),
		OllamaBaseURL:         env("OLLAMA_BASE_URL", "http://localhost:11434"),
		DataDir:               env("DATA_DIR", "./data"),
		FileStorageDir:        env("FILE_STORAGE_DIR", "./data/files"),
		ReportExportDir:       os.Getenv("REPORT_EXPORT_DIR"),
		HTTPFetchAllowedHosts: csvEnv("HTTP_FETCH_ALLOWED_HOSTS"),
		MCPServers:            mcpServers,
		MaxAgentSteps:         intEnv("MAX_AGENT_STEPS", 12),
		MaxModelCalls:         intEnv("MAX_MODEL_CALLS", 8),
		MaxToolCalls:          intEnv("MAX_TOOL_CALLS", 20),
		MaxModelTokens:        intEnv("MAX_MODEL_TOKENS", 64000),
		MaxRetries:            intEnv("MAX_RETRIES", 3),
		WorkerID:              env("PAIOS_WORKER_ID", "paios"),
		WorkerCount:           intEnv("PAIOS_WORKER_COUNT", 2),
		WorkerLease:           durationEnv("PAIOS_WORKER_LEASE_SECONDS", 30),
		WorkerHeartbeat:       durationEnv("PAIOS_WORKER_HEARTBEAT_SECONDS", 10),
		WorkerPoll:            workerPollDuration(),
		WorkerRecovery:        durationEnv("PAIOS_WORKER_RECOVERY_SECONDS", 5),
		ShutdownGrace:         durationEnv("SHUTDOWN_GRACE_SECONDS", 15),
		MaxExecutionTime:      durationEnv("MAX_EXECUTION_SECONDS", 300),
		MaxExecutionCost:      floatEnv("MAX_EXECUTION_COST", 0),
		RequestTimeout:        30 * time.Second,
		ShutdownTimeout:       15 * time.Second,
	}
	cfg.RequestTimeout = cfg.MaxExecutionTime + 5*time.Second
	if cfg.Port == "" {
		return Config{}, errors.New("APP_PORT must not be empty")
	}
	if _, err := strconv.Atoi(cfg.Port); err != nil {
		return Config{}, fmt.Errorf("APP_PORT must be a number: %w", err)
	}
	if strings.TrimSpace(cfg.Host) == "" || strings.TrimSpace(cfg.Host) != cfg.Host {
		return Config{}, errors.New("APP_HOST must not be empty")
	}
	if cfg.ReportExportDir != "" {
		if strings.TrimSpace(cfg.ReportExportDir) != cfg.ReportExportDir || strings.ContainsAny(cfg.ReportExportDir, "\x00\r\n") {
			return Config{}, errors.New("REPORT_EXPORT_DIR must be a clean local path")
		}
		if strings.TrimSpace(cfg.FileStorageDir) == "" {
			return Config{}, errors.New("FILE_STORAGE_DIR is required when REPORT_EXPORT_DIR is configured")
		}
		storageRoot, storageErr := filepath.Abs(cfg.FileStorageDir)
		exportRoot, exportErr := filepath.Abs(cfg.ReportExportDir)
		if storageErr != nil || exportErr != nil {
			return Config{}, errors.New("REPORT_EXPORT_DIR must be distinct from FILE_STORAGE_DIR and both roots must resolve")
		}
		storageRelative, relErr := filepath.Rel(storageRoot, exportRoot)
		if relErr != nil {
			return Config{}, errors.New("REPORT_EXPORT_DIR and FILE_STORAGE_DIR roots could not be compared safely")
		}
		exportRelative, relErr := filepath.Rel(exportRoot, storageRoot)
		if relErr != nil {
			return Config{}, errors.New("REPORT_EXPORT_DIR and FILE_STORAGE_DIR roots could not be compared safely")
		}
		if storageRelative == "." || exportRelative == "." || (!filepath.IsAbs(storageRelative) && storageRelative != ".." && !strings.HasPrefix(storageRelative, ".."+string(filepath.Separator))) || (!filepath.IsAbs(exportRelative) && exportRelative != ".." && !strings.HasPrefix(exportRelative, ".."+string(filepath.Separator))) {
			return Config{}, errors.New("REPORT_EXPORT_DIR and FILE_STORAGE_DIR must not contain one another")
		}
		storageResolved, storageResolveErr := resolveConfiguredRoot(cfg.FileStorageDir)
		exportResolved, exportResolveErr := resolveConfiguredRoot(cfg.ReportExportDir)
		if storageResolveErr != nil || exportResolveErr != nil {
			return Config{}, errors.New("REPORT_EXPORT_DIR and FILE_STORAGE_DIR roots could not be resolved safely")
		}
		storageResolvedRelative, relErr := filepath.Rel(storageResolved, exportResolved)
		if relErr != nil {
			return Config{}, errors.New("REPORT_EXPORT_DIR and FILE_STORAGE_DIR roots could not be compared safely")
		}
		exportResolvedRelative, relErr := filepath.Rel(exportResolved, storageResolved)
		if relErr != nil {
			return Config{}, errors.New("REPORT_EXPORT_DIR and FILE_STORAGE_DIR roots could not be compared safely")
		}
		if storageResolvedRelative == "." || exportResolvedRelative == "." || (!filepath.IsAbs(storageResolvedRelative) && storageResolvedRelative != ".." && !strings.HasPrefix(storageResolvedRelative, ".."+string(filepath.Separator))) || (!filepath.IsAbs(exportResolvedRelative) && exportResolvedRelative != ".." && !strings.HasPrefix(exportResolvedRelative, ".."+string(filepath.Separator))) {
			return Config{}, errors.New("REPORT_EXPORT_DIR and FILE_STORAGE_DIR resolve to overlapping directories")
		}
	}
	if liveURL := strings.TrimSpace(os.Getenv("PAIOS_LIVE_SOURCE_URL")); liveURL != "" {
		parsed, err := url.ParseRequestURI(liveURL)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
			return Config{}, errors.New("PAIOS_LIVE_SOURCE_URL must be an absolute credential-free HTTPS URL")
		}
		allowed := map[string]bool{}
		for _, host := range csvEnv("PAIOS_LIVE_SOURCE_ALLOWED_HOSTS") {
			if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "/:@*\\\r\n\x00") || strings.HasSuffix(host, ".") {
				return Config{}, fmt.Errorf("invalid PAIOS_LIVE_SOURCE_ALLOWED_HOSTS entry %q", host)
			}
			allowed[strings.ToLower(host)] = true
		}
		if !allowed[strings.ToLower(parsed.Hostname())] {
			return Config{}, errors.New("PAIOS_LIVE_SOURCE_URL host must appear exactly in PAIOS_LIVE_SOURCE_ALLOWED_HOSTS")
		}
	} else if strings.TrimSpace(os.Getenv("PAIOS_LIVE_SOURCE_ALLOWED_HOSTS")) != "" {
		return Config{}, errors.New("PAIOS_LIVE_SOURCE_ALLOWED_HOSTS requires PAIOS_LIVE_SOURCE_URL")
	}
	if strings.Contains(cfg.Host, ":") && net.ParseIP(cfg.Host) == nil {
		return Config{}, errors.New("APP_HOST must be a hostname or IP address without a port")
	}
	if strings.HasPrefix(cfg.Host, "[") || strings.HasSuffix(cfg.Host, "]") {
		return Config{}, errors.New("APP_HOST IPv6 address must not include brackets")
	}
	if port, _ := strconv.Atoi(cfg.Port); port < 1 || port > 65535 {
		return Config{}, errors.New("APP_PORT must be between 1 and 65535")
	}
	if cfg.Environment == "production" && cfg.APIKey == "" {
		return Config{}, errors.New("PAIOS_API_KEY is required in production")
	}
	if cfg.Environment != "development" && cfg.Environment != "test" && cfg.Environment != "production" {
		return Config{}, fmt.Errorf("unsupported APP_ENV %q", cfg.Environment)
	}
	if cfg.APIKey != "" && utf8.RuneCountInString(cfg.APIKey) < 32 {
		return Config{}, errors.New("PAIOS_API_KEY must contain at least 32 characters")
	}
	parsedHost := net.ParseIP(cfg.Host)
	if cfg.Environment == "production" && ((parsedHost != nil && parsedHost.IsLoopback()) || strings.EqualFold(cfg.Host, "localhost")) {
		return Config{}, errors.New("APP_HOST cannot be loopback in production; bind explicitly to a trusted interface")
	}
	if cfg.Environment == "production" && parsedHost == nil && (!strings.Contains(cfg.Host, ".") || strings.HasSuffix(cfg.Host, ".")) {
		return Config{}, errors.New("production APP_HOST must be an explicit IP address or fully qualified hostname")
	}
	if cfg.MaxAgentSteps < 1 || cfg.MaxModelCalls < 1 || cfg.MaxToolCalls < 1 || cfg.MaxModelTokens < 1 || cfg.MaxModelTokens > 128000 || cfg.MaxRetries < 0 || cfg.MaxExecutionTime <= 0 || cfg.MaxExecutionTime > 24*time.Hour || cfg.MaxExecutionCost < 0 || math.IsNaN(cfg.MaxExecutionCost) || math.IsInf(cfg.MaxExecutionCost, 0) {
		return Config{}, errors.New("execution budgets must be valid; MAX_RETRIES and MAX_EXECUTION_COST may be zero")
	}
	if strings.TrimSpace(cfg.WorkerID) == "" || cfg.WorkerCount < 1 || cfg.WorkerCount > 128 || cfg.WorkerLease < time.Second || cfg.WorkerHeartbeat <= 0 || cfg.WorkerHeartbeat*2 >= cfg.WorkerLease || cfg.WorkerPoll <= 0 || cfg.WorkerRecovery <= 0 || cfg.ShutdownGrace <= 0 {
		return Config{}, errors.New("worker id, concurrency, lease, heartbeat, poll, recovery, and shutdown limits must be valid")
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("unsupported LOG_LEVEL %q", cfg.LogLevel)
	}
	if cfg.ModelProvider != "mock" && cfg.ModelProvider != "openai-compatible" && cfg.ModelProvider != "ollama" {
		return Config{}, fmt.Errorf("unsupported MODEL_PROVIDER %q", cfg.ModelProvider)
	}
	if cfg.ModelProvider == "openai-compatible" && (strings.TrimSpace(cfg.ModelBaseURL) == "" || cfg.ModelAPIKey == "") {
		return Config{}, errors.New("MODEL_BASE_URL and MODEL_API_KEY are required for openai-compatible provider")
	}
	if cfg.ModelBaseURL != "" {
		parsed, err := url.ParseRequestURI(cfg.ModelBaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Config{}, errors.New("MODEL_BASE_URL must be an absolute HTTP or HTTPS URL")
		}
		if strings.ContainsAny(cfg.ModelBaseURL, "\r\n") {
			return Config{}, errors.New("MODEL_BASE_URL must not contain line breaks")
		}
	}
	if cfg.ModelProvider == "openai-compatible" && (strings.Contains(cfg.ModelAPIKey, "\n") || strings.Contains(cfg.ModelAPIKey, "\r")) {
		return Config{}, errors.New("MODEL_API_KEY must not contain line breaks")
	}
	if cfg.ModelProvider == "ollama" && strings.TrimSpace(cfg.OllamaBaseURL) == "" {
		return Config{}, errors.New("OLLAMA_BASE_URL is required for ollama provider")
	}
	if cfg.ModelProvider == "ollama" {
		parsed, err := url.ParseRequestURI(cfg.OllamaBaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Config{}, errors.New("OLLAMA_BASE_URL must be an absolute HTTP or HTTPS URL")
		}
		if strings.ContainsAny(cfg.OllamaBaseURL, "\r\n") {
			return Config{}, errors.New("OLLAMA_BASE_URL must not contain line breaks")
		}
	}
	if cfg.ModelProvider != "mock" && strings.TrimSpace(cfg.ModelName) == "" {
		return Config{}, errors.New("MODEL_NAME is required for configured model provider")
	}
	if cfg.ModelProvider != "mock" && os.Getenv("MODEL_NAME") == "" {
		return Config{}, errors.New("MODEL_NAME must be set explicitly for ollama/openai-compatible provider")
	}
	seenFetchHosts := map[string]bool{}
	for _, host := range cfg.HTTPFetchAllowedHosts {
		if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "/:@*\\\r\n\x00") || strings.HasSuffix(host, ".") {
			return Config{}, fmt.Errorf("invalid HTTP_FETCH_ALLOWED_HOSTS entry %q", host)
		}
		if seenFetchHosts[strings.ToLower(host)] {
			return Config{}, fmt.Errorf("duplicate HTTP_FETCH_ALLOWED_HOSTS entry %q", host)
		}
		seenFetchHosts[strings.ToLower(host)] = true
	}
	seenMCPIDs := map[string]bool{}
	for _, server := range cfg.MCPServers {
		if err := mcp.ValidateServerConfig(server); err != nil {
			return Config{}, fmt.Errorf("invalid MCP_SERVERS_JSON configuration: %w", err)
		}
		if seenMCPIDs[server.ID] {
			return Config{}, fmt.Errorf("duplicate MCP server id %q", server.ID)
		}
		seenMCPIDs[server.ID] = true
	}
	return cfg, nil
}

func resolveConfiguredRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Abs(resolved)
	}
	// Canonicalize through the nearest existing parent so aliases are detected
	// even when the final directory has not been created yet.
	// 中文：从最近存在的父目录开始规范化，确保最终目录尚未创建时也能识别路径别名。
	missing := make([]string, 0)
	parent := abs
	for {
		if _, statErr := os.Lstat(parent); statErr == nil {
			break
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		base := filepath.Base(parent)
		if base == "." || base == string(filepath.Separator) {
			return "", err
		}
		missing = append(missing, base)
		parent = filepath.Dir(parent)
	}
	canonicalParent, parentErr := filepath.EvalSymlinks(parent)
	if parentErr != nil {
		return "", parentErr
	}
	for i := len(missing) - 1; i >= 0; i-- {
		canonicalParent = filepath.Join(canonicalParent, missing[i])
	}
	return filepath.Clean(canonicalParent), nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func csvEnv(key string) []string {
	value := os.Getenv(key)
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		items = append(items, strings.TrimSpace(part))
	}
	return items
}

func mcpServersEnv() ([]mcp.ServerConfig, error) {
	value := os.Getenv("MCP_SERVERS_JSON")
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	if len(value) > 64<<10 {
		return nil, errors.New("MCP_SERVERS_JSON exceeds 64 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	var servers []mcp.ServerConfig
	if err := decoder.Decode(&servers); err != nil {
		return nil, errors.New("MCP_SERVERS_JSON must be a valid server array")
	}
	if len(servers) > 32 {
		return nil, errors.New("MCP_SERVERS_JSON may configure at most 32 servers")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("MCP_SERVERS_JSON contains trailing data")
	}
	return servers, nil
}

func intEnv(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
}

func durationEnv(key string, fallbackSeconds int) time.Duration {
	seconds := intEnv(key, fallbackSeconds)
	return time.Duration(seconds) * time.Second
}

func floatEnv(key string, fallback float64) float64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return -1
	}
	return parsed
}

func workerPollDuration() time.Duration {
	milliseconds := intEnv("PAIOS_WORKER_POLL_MILLISECONDS", 500)
	return time.Duration(milliseconds) * time.Millisecond
}
