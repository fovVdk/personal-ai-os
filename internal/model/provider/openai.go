// English: This adapter implements bounded OpenAI-compatible Chat Completions without executing tool calls.
// 中文：本适配器实现有界的 OpenAI 兼容 Chat Completions 请求，不会执行模型返回的工具调用。
// 中文：本适配器实现有界的 OpenAI 兼容 Chat Completions 调用，但不会执行返回的工具提议。
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

type OpenAICompatible struct {
	BaseURL      string
	APIKey       string
	HTTPClient   *http.Client
	ProviderName string
}
type chatRequest struct {
	Model       string            `json:"model"`
	Messages    []gateway.Message `json:"messages"`
	Tools       []map[string]any  `json:"tools,omitempty"`
	Temperature float64           `json:"temperature,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
}
type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p OpenAICompatible) Name() string {
	if p.ProviderName != "" {
		return p.ProviderName
	}
	return "openai-compatible"
}
func (p OpenAICompatible) Complete(ctx context.Context, request gateway.Request) (gateway.Response, error) {
	if p.APIKey == "" {
		return gateway.Response{}, errors.New("model API key is required")
	}
	if strings.ContainsAny(p.APIKey, "\r\n") {
		return gateway.Response{}, errors.New("model API key contains invalid header characters")
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		return gateway.Response{}, errors.New("model base URL is required")
	}
	if strings.ContainsAny(base, "\r\n") {
		return gateway.Response{}, errors.New("model base URL contains invalid characters")
	}
	if request.Model == "" {
		return gateway.Response{}, errors.New("model name is required")
	}
	if len(request.Messages) == 0 {
		return gateway.Response{}, errors.New("at least one model message is required")
	}
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tools := make([]map[string]any, 0, len(request.Tools))
	for _, definition := range request.Tools {
		var schema any
		if len(definition.Parameters) == 0 || json.Unmarshal(definition.Parameters, &schema) != nil {
			return gateway.Response{}, errors.New("tool parameters must contain valid JSON schema")
		}
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": definition.Name, "description": definition.Description, "parameters": schema}})
	}
	payload, err := json.Marshal(chatRequest{Model: request.Model, Messages: request.Messages, Tools: tools, Temperature: request.Temperature, MaxTokens: request.MaxTokens})
	if err != nil {
		return gateway.Response{}, fmt.Errorf("encode model request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return gateway.Response{}, fmt.Errorf("create model request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+p.APIKey)
	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return gateway.Response{}, fmt.Errorf("call model provider: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return gateway.Response{}, fmt.Errorf("read model response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return gateway.Response{}, fmt.Errorf("model provider returned HTTP %d", response.StatusCode)
	}
	var decoded chatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return gateway.Response{}, fmt.Errorf("decode model response: %w", err)
	}
	if decoded.Error != nil {
		return gateway.Response{}, errors.New("model provider returned an error payload")
	}
	if len(decoded.Choices) == 0 {
		return gateway.Response{}, errors.New("model provider returned no choices")
	}
	toolCalls := make([]gateway.ToolCall, 0, len(decoded.Choices[0].Message.ToolCalls))
	for _, call := range decoded.Choices[0].Message.ToolCalls {
		var arguments any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
			return gateway.Response{}, errors.New("model returned invalid tool arguments JSON")
		}
		canonicalArguments, err := json.Marshal(arguments)
		if err != nil {
			return gateway.Response{}, errors.New("model returned invalid tool arguments")
		}
		toolCalls = append(toolCalls, gateway.ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: canonicalArguments})
	}
	return gateway.Response{Content: decoded.Choices[0].Message.Content, ToolCalls: toolCalls, Provider: p.Name(), Model: decoded.Model, Usage: gateway.Usage{InputTokens: decoded.Usage.PromptTokens, OutputTokens: decoded.Usage.CompletionTokens, TotalTokens: decoded.Usage.TotalTokens}}, nil
}
