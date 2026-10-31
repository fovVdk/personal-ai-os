// English: These tests protect Task transition validity, attempt timestamps, and terminal states.
// 中文：本测试保护 Task 状态转换有效性、尝试时间戳和终态规则。
// 中文：本测试保护 Task 状态转换、执行时间戳及终态规则。
package task

import (
	"errors"
	"testing"
	"time"
)

func TestTaskLifecycleAndTimestamps(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.FixedZone("local", -7*3600))
	task := Task{GoalID: "goal-1", Status: Created}
	if err := task.Transition(Ready, now); err != nil {
		t.Fatalf("created -> ready: %v", err)
	}
	if err := task.Transition(Running, now); err != nil {
		t.Fatalf("ready -> running: %v", err)
	}
	if task.StartedAt == nil || task.StartedAt.Location() != time.UTC {
		t.Fatalf("running timestamp was not normalized to UTC: %v", task.StartedAt)
	}
	if err := task.Transition(Completed, now); err != nil {
		t.Fatalf("running -> completed: %v", err)
	}
	if task.CompletedAt == nil {
		t.Fatal("completed timestamp was not set")
	}
	if err := task.Transition(Running, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected completed task to be terminal, got %v", err)
	}
}
