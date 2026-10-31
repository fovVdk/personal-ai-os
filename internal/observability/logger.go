// Package observability provides structured logging and instrumentation boundaries.
// 中文：本包提供结构化日志及指标/链路追踪的扩展边界。
package observability

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger creates a JSON structured logger at the requested level.
// 中文：NewLogger 按指定级别创建 JSON 结构化日志记录器。
func NewLogger(level string) *slog.Logger {
	var parsed slog.Level
	switch strings.ToLower(level) {
	case "debug":
		parsed = slog.LevelDebug
	case "warn":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	default:
		parsed = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parsed}))
}
