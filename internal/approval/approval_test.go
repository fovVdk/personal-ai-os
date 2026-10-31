// English: These tests cover parameter binding, expiry, and one-time approval decisions.
// 中文：本测试覆盖参数绑定、审批过期及一次性审批决策。
package approval

import (
	"errors"
	"testing"
	"time"
)

func TestApprovalBindsParametersAndExpires(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	request, err := New("user", "task", "door", "unlock", map[string]any{"room": "garage"}, 3, now.Add(time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := request.ValidFor("door", "unlock", map[string]any{"room": "garage"}, now); err == nil {
		t.Fatal("pending request must not authorize execution")
	}
	if err := request.Decide(Approved, now); err != nil {
		t.Fatal(err)
	}
	if err := request.ValidFor("door", "unlock", map[string]any{"room": "garage"}, now); err != nil {
		t.Fatalf("matching approved action rejected: %v", err)
	}
	if err := request.ValidFor("door", "unlock", map[string]any{"room": "front"}, now); !errors.Is(err, ErrParametersChanged) {
		t.Fatalf("expected changed-parameter error, got %v", err)
	}
	if err := request.ValidFor("door", "unlock", map[string]any{"room": "garage"}, now.Add(time.Minute)); err == nil {
		t.Fatal("expired approval must not authorize execution")
	}
}

func TestExpiredApprovalCannotBeDecided(t *testing.T) {
	now := time.Now().UTC()
	request, err := New("user", "task", "tool", "action", map[string]int{"count": 1}, 1, now.Add(time.Second), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Decide(Approved, now.Add(time.Second)); err == nil || request.Status != Expired {
		t.Fatalf("expected expired request, status=%s err=%v", request.Status, err)
	}
}
