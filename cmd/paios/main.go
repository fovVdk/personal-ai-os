// Command paios is the executable entry point for Personal AI OS.
// 中文：paios 命令是 Personal AI OS 的可执行程序入口。
// English: This command wires infrastructure and starts the Personal AI OS HTTP service.
// 中文：本命令负责组装基础设施并启动 Personal AI OS HTTP 服务。
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"personal-ai-os/internal/agent/registry"
	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/api"
	"personal-ai-os/internal/app/config"
	"personal-ai-os/internal/app/database"
	"personal-ai-os/internal/app/store"
	"personal-ai-os/internal/model/gateway"
	"personal-ai-os/internal/model/provider"
	"personal-ai-os/internal/observability"
	"personal-ai-os/internal/planner"
	"personal-ai-os/internal/supervisor"
	"personal-ai-os/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] != "migrate" {
		return fmt.Errorf("unknown command %q (available: run, migrate)", os.Args[1])
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := observability.NewLogger(cfg.LogLevel)
	if cfg.APIKey == "" {
		if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
			return fmt.Errorf("refusing to bind unauthenticated API to non-loopback host %q", cfg.Host)
		}
		logger.Warn("API authentication is disabled; the API is bound to loopback")
	}

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := database.Open(startupCtx, cfg.DatabaseURL)
	cancelStartup()
	if err != nil {
		return err
	}
	defer pool.Close()

	migrationCtx, cancelMigrations := context.WithTimeout(context.Background(), 30*time.Second)
	if err := migrations.Apply(migrationCtx, pool); err != nil {
		cancelMigrations()
		return err
	}
	cancelMigrations()
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		logger.Info("database migrations applied")
		return nil
	}

	repositories := store.NewPostgres(pool)
	var modelProvider gateway.Provider
	switch cfg.ModelProvider {
	case "openai-compatible":
		modelProvider = provider.OpenAICompatible{BaseURL: cfg.ModelBaseURL, APIKey: cfg.ModelAPIKey}
	case "ollama":
		modelProvider = provider.Ollama{BaseURL: cfg.OllamaBaseURL}
	default:
		modelProvider = provider.Mock{}
	}
	modelGateway := gateway.Default{Providers: []gateway.Provider{modelProvider}}
	var planGenerator planner.Generator = planner.MockGenerator{}
	if cfg.ModelProvider != "mock" {
		planGenerator = planner.ModelGenerator{Gateway: modelGateway, Model: cfg.ModelName, Timeout: cfg.RequestTimeout}
	}
	agents := registry.New()
	for _, profile := range []registry.Profile{
		{ID: "general", Name: "General Agent", Version: "mock-v1", Capabilities: []string{"summarize", "task-support"}, Agent: agentruntime.MockAgent{AgentID: "general"}},
		{ID: "research", Name: "Research Agent", Version: "mock-v1", Capabilities: []string{"research-plan", "source-review"}, Agent: agentruntime.MockAgent{AgentID: "research"}},
		{ID: "planning", Name: "Planning Agent", Version: "mock-v1", Capabilities: []string{"structured-planning", "dependency-analysis"}, Agent: agentruntime.MockAgent{AgentID: "planning"}},
	} {
		if err := agents.Register(profile); err != nil {
			return fmt.Errorf("register agent %s: %w", profile.ID, err)
		}
	}
	supervisorService := &supervisor.Supervisor{
		Goals: repositories, Tasks: repositories.Tasks(), Planner: planGenerator, Agents: agents,
		Executions: store.NewExecutionStore(pool), Usage: store.NewModelUsageStore(pool), Recovery: repositories,
		Budgets: supervisor.Budgets{MaxSteps: cfg.MaxAgentSteps, MaxModelCalls: cfg.MaxModelCalls, MaxToolCalls: cfg.MaxToolCalls, MaxModelTokens: cfg.MaxModelTokens, MaxCost: cfg.MaxExecutionCost, MaxRetries: cfg.MaxRetries, MaxRuntime: cfg.MaxExecutionTime},
	}
	handler := (&api.Server{
		Goals: repositories, Tasks: repositories.Tasks(), Memories: repositories.Memories(), Knowledge: repositories.Knowledge(), Nodes: repositories.Nodes(), Approvals: repositories.Approvals(), Planner: planGenerator, Models: modelGateway, Agents: agents, Supervisor: supervisorService, DB: pool,
		Logger: logger, APIKey: cfg.APIKey, MaxRetries: cfg.MaxRetries, RequestTimeout: cfg.RequestTimeout,
	}).Handler()
	httpServer := &http.Server{
		Addr: net.JoinHostPort(cfg.Host, cfg.Port), Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: cfg.MaxExecutionTime + 10*time.Second, IdleTimeout: 60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("HTTP server starting", "address", httpServer.Addr, "environment", cfg.Environment)
		if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		if closeErr := httpServer.Close(); closeErr != nil {
			return fmt.Errorf("shutdown HTTP server: %w (close: %v)", err, closeErr)
		}
		return err
	}
	select {
	case err := <-serverErr:
		if err != nil {
			return err
		}
	case <-shutdownCtx.Done():
		return shutdownCtx.Err()
	}
	logger.Info("shutdown complete")
	return nil
}
