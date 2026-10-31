// English: These tests verify A3 side effects cannot run before a matching approval.
// 中文：本测试验证 A3 副作用在匹配审批通过前绝不会执行。
package policy

import (
	"context"
	"errors"
	"testing"
	"time"

	"personal-ai-os/internal/approval"
	corepolicy "personal-ai-os/internal/policy"
)

type countingTool struct{ calls int }

func (t *countingTool) Execute(context.Context, map[string]any) (any, error) {
	t.calls++
	return "executed", nil
}

func TestApprovalGateRunsBeforeTool(t *testing.T) {
	now := time.Now().UTC()
	tool := &countingTool{}
	executor := Executor{Now: func() time.Time { return now }}
	params := map[string]any{"target": "front-door"}
	if _, err := executor.Execute(context.Background(), "door", "unlock", corepolicy.A3, true, false, params, nil, tool); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("expected approval requirement, got %v", err)
	}
	if tool.calls != 0 {
		t.Fatal("tool executed before approval")
	}
	request, err := approval.New("user", "task", "door", "unlock", params, 3, now.Add(time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), "door", "unlock", corepolicy.A3, true, false, params, &request, tool); err == nil {
		t.Fatal("pending approval must be rejected")
	}
	if tool.calls != 0 {
		t.Fatal("tool executed while approval pending")
	}
	if err := request.Decide(approval.Approved, now); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), "door", "unlock", corepolicy.A3, true, false, params, &request, tool); err != nil {
		t.Fatalf("approved invocation failed: %v", err)
	}
	if tool.calls != 1 {
		t.Fatalf("tool calls=%d, want 1", tool.calls)
	}
}
