// English: This provider returns deterministic placeholder text and never claims task verification.
// 中文：本 Provider 返回确定性占位文本，且不会声称任务已通过验证。
// 中文：本 Provider 返回确定性占位文本，且不会声称任务已被验证。
package provider

import (
	"context"
	"errors"
	"strings"

	"personal-ai-os/internal/model/gateway"
)

type Mock struct{ Reply string }

func (Mock) Name() string { return "mock" }
func (m Mock) Complete(ctx context.Context, request gateway.Request) (gateway.Response, error) {
	if err := ctx.Err(); err != nil {
		return gateway.Response{}, err
	}
	if len(request.Messages) == 0 {
		return gateway.Response{}, errors.New("at least one model message is required")
	}
	content := m.Reply
	if content == "" {
		content = "Mock model response (unverified): " + strings.TrimSpace(request.Messages[len(request.Messages)-1].Content)
	}
	return gateway.Response{Content: content, Provider: m.Name(), Model: request.Model}, nil
}
