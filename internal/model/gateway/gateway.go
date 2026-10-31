// Package gateway defines a provider-neutral model request interface.
// 中文：gateway 包定义与模型供应商无关的请求接口。
// English: This file defines vendor-neutral model messages, token usage, tool schemas, and provider contracts.
// 中文：本文件定义与供应商无关的模型消息、Token 用量、工具 Schema 及 Provider 接口。
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrProviderUnavailable = errors.New("model provider unavailable")

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Request struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	Temperature float64          `json:"temperature,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Timeout     time.Duration    `json:"-"`
}
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}
type ToolCall struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type Response struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     Usage      `json:"usage"`
	Provider  string     `json:"provider"`
	Model     string     `json:"model"`
}
type Provider interface {
	Name() string
	Complete(context.Context, Request) (Response, error)
}
type Gateway interface {
	Complete(context.Context, Request) (Response, error)
}
