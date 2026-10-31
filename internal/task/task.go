// Package task defines durable work items and their lifecycle rules.
// 中文：task 包定义持久化工作项及其生命周期规则。
// English: This aggregate validates Task lifecycle transitions and tracks durable attempts.
// 中文：本聚合验证 Task 状态转换，并记录持久化执行尝试次数。
package task

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Status string

const (
	Created         Status = "created"
	Ready           Status = "ready"
	Running         Status = "running"
	WaitingApproval Status = "waiting_approval"
	WaitingExternal Status = "waiting_external"
	Retrying        Status = "retrying"
	Blocked         Status = "blocked"
	Paused          Status = "paused"
	Completed       Status = "completed"
	Failed          Status = "failed"
	Cancelled       Status = "cancelled"
)

var ErrInvalidTransition = errors.New("invalid task status transition")

// Task has an independent identity and can be resumed across process restarts.
// 中文：Task 拥有独立身份，持久化后可在服务重启时继续处理。
type Task struct {
	ID              string     `json:"id"`
	GoalID          string     `json:"goal_id"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Priority        int        `json:"priority"`
	Status          Status     `json:"status"`
	AttemptCount    int        `json:"attempt_count"`
	PlanKey         string     `json:"plan_key,omitempty"`
	SuccessCriteria []string   `json:"success_criteria"`
	DependsOn       []string   `json:"depends_on,omitempty"`
	ScheduledAt     *time.Time `json:"scheduled_at,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (t Task) Validate() error {
	if strings.TrimSpace(t.Title) == "" {
		return errors.New("task title is required")
	}
	if t.GoalID == "" {
		return errors.New("task must belong to a goal")
	}
	if t.Priority < 1 || t.Priority > 5 {
		return errors.New("task priority must be between 1 and 5")
	}
	if !validStatus(t.Status) {
		return fmt.Errorf("unknown task status %q", t.Status)
	}
	if t.AttemptCount < 0 {
		return errors.New("task attempt_count cannot be negative")
	}
	return nil
}

func (t *Task) Transition(next Status, now time.Time) error {
	if !allowedTransition(t.Status, next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.Status, next)
	}
	t.Status = next
	t.UpdatedAt = now.UTC()
	if next == Running && t.StartedAt == nil {
		startedAt := now.UTC()
		t.StartedAt = &startedAt
	}
	if next == Completed {
		completedAt := now.UTC()
		t.CompletedAt = &completedAt
	}
	return nil
}

func validStatus(status Status) bool {
	switch status {
	case Created, Ready, Running, WaitingApproval, WaitingExternal, Retrying, Blocked, Paused, Completed, Failed, Cancelled:
		return true
	default:
		return false
	}
}

func allowedTransition(from, to Status) bool {
	switch from {
	case Created:
		return to == Ready || to == Cancelled
	case Ready:
		return to == Running || to == Blocked || to == Paused || to == Cancelled
	case Running:
		return to == WaitingApproval || to == WaitingExternal || to == Retrying || to == Blocked || to == Paused || to == Completed || to == Failed || to == Cancelled
	case WaitingApproval:
		return to == Ready || to == Running || to == Cancelled || to == Failed
	case WaitingExternal:
		return to == Ready || to == Running || to == Cancelled || to == Failed
	case Retrying:
		return to == Running || to == Failed || to == Cancelled
	case Blocked, Paused:
		return to == Ready || to == Cancelled
	case Failed:
		return to == Retrying || to == Cancelled
	default:
		return false
	}
}
