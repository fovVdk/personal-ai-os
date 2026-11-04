// English: This adapter sends bounded, non-streaming chat requests to an Ollama-compatible endpoint.
// 中文：本适配器向 Ollama 兼容端点发送受限的非流式对话请求。
// 中文：本适配器向 Ollama 兼容端点发送受超时和响应大小限制的非流式对话请求。
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"personal-ai-os/internal/model/gateway"
)

type Ollama struct {
	BaseURL    string
	HTTPClient *http.Client
}
type ollamaRequest struct {
	Model    string           `json:"model"`
	Messages []ollamaMessage  `json:"messages"`
	Tools    []map[string]any `json:"tools,omitempty"`
	Stream   bool             `json:"stream"`
	Options  map[string]any   `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolName   string           `json:"tool_name,omitempty"`
}

type ollamaToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}
type ollamaResponse struct {
	Model   string `json:"model"`
	Message struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		ToolCalls []struct {
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"message"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	Error           string `json:"error"`
}

func (Ollama) Name() string { return "ollama" }
func (p Ollama) Complete(ctx context.Context, request gateway.Request) (gateway.Response, error) {
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "http://localhost:11434"
	}
	if strings.ContainsAny(base, "\r\n") {
		return gateway.Response{}, errors.New("Ollama base URL contains invalid characters")
	}
	if request.Model == "" {
		return gateway.Response{}, errors.New("ollama model name is required")
	}

	// Allow user-configured remote Ollama to use HTTPS.
	// 中文：允许用户配置的远程 Ollama 使用 HTTPS。

	if request.Model == "" {
		return gateway.Response{}, errors.New("ollama model name is required")
	}
	if len(request.Messages) == 0 {
		return gateway.Response{}, errors.New("at least one Ollama message is required")
	}
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	options := map[string]any{}
	if request.MaxTokens > 0 {
		options["num_predict"] = request.MaxTokens
	}
	if request.Temperature != 0 {
		options["temperature"] = request.Temperature
	}
	tools := make([]map[string]any, 0, len(request.Tools))
	for _, definition := range request.Tools {
		var schema any
		if len(definition.Parameters) == 0 || json.Unmarshal(definition.Parameters, &schema) != nil {
			return gateway.Response{}, errors.New("tool parameters must contain valid JSON schema")
		}
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": definition.Name, "description": definition.Description, "parameters": schema}})
	}
	messages := make([]ollamaMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		converted := ollamaMessage{Role: message.Role, Content: message.Content, ToolCallID: message.ToolCallID, ToolName: message.ToolName}
		for _, call := range message.ToolCalls {
			converted.ToolCalls = append(converted.ToolCalls, ollamaToolCall{ID: call.ID, Type: "function", Function: struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}{Name: call.Name, Arguments: append(json.RawMessage(nil), call.Arguments...)}})
		}
		messages = append(messages, converted)
	}
	payload, err := json.Marshal(ollamaRequest{Model: request.Model, Messages: messages, Tools: tools, Stream: false, Options: options})
	if err != nil {
		return gateway.Response{}, fmt.Errorf("encode Ollama request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return gateway.Response{}, fmt.Errorf("create Ollama request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return gateway.Response{}, fmt.Errorf("call Ollama: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return gateway.Response{}, fmt.Errorf("read Ollama response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return gateway.Response{}, fmt.Errorf("Ollama returned HTTP %d", response.StatusCode)
	}
	var decoded ollamaResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return gateway.Response{}, fmt.Errorf("decode Ollama response: %w", err)
	}
	if decoded.Error != "" {
		return gateway.Response{}, errors.New("Ollama returned a provider error")
	}
	toolCalls := make([]gateway.ToolCall, 0, len(decoded.Message.ToolCalls))
	for _, call := range decoded.Message.ToolCalls {
		toolCalls = append(toolCalls, gateway.ToolCall{Name: call.Function.Name, Arguments: call.Function.Arguments})
	}
	return gateway.Response{Content: decoded.Message.Content, ToolCalls: toolCalls, Provider: p.Name(), Model: decoded.Model, Usage: gateway.Usage{InputTokens: decoded.PromptEvalCount, OutputTokens: decoded.EvalCount, TotalTokens: decoded.PromptEvalCount + decoded.EvalCount}}, nil
}
