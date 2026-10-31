// Package goal defines the Goal aggregate and its lifecycle rules.
// 中文：goal 包定义 Goal 聚合及其生命周期规则。
// English: This aggregate enforces Goal lifecycle transitions and evidence-gated completion.
// 中文：本聚合实现 Goal 生命周期状态转换，并要求提供证据后方可完成目标。
package goal

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Status string

const (
	Draft      Status = "draft"
	Active     Status = "active"
	Monitoring Status = "monitoring"
	Blocked    Status = "blocked"
	Paused     Status = "paused"
	Completed  Status = "completed"
	Cancelled  Status = "cancelled"
	Archived   Status = "archived"
)

var ErrInvalidTransition = errors.New("invalid goal status transition")
var ErrEvidenceRequired = errors.New("evidence is required to complete a goal")

// Evidence records verifiable support for a claimed result.
// 中文：Evidence 保存用于核验结果声明的依据。
type Evidence struct {
	Source      string    `json:"source"`
	Description string    `json:"description"`
	Reference   string    `json:"reference,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}

// Goal is an outcome-oriented objective independent of any chat or agent session.
// 中文：Goal 是面向结果的目标，其身份独立于任何聊天或 Agent 会话。
type Goal struct {
	ID              string         `json:"id"`
	Title           string         `json:"title"`
	Description     string         `json:"description"`
	OwnerID         string         `json:"owner_id"`
	ParentGoalID    *string        `json:"parent_goal_id,omitempty"`
	Priority        int            `json:"priority"`
	SuccessCriteria []string       `json:"success_criteria"`
	Constraints     []string       `json:"constraints"`
	AutonomyPolicy  map[string]any `json:"autonomy_policy"`
	ReviewPolicy    map[string]any `json:"review_policy"`
	Status          Status         `json:"status"`
	Progress        int            `json:"progress"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	CompletedAt     *time.Time     `json:"completed_at,omitempty"`
	Evidence        []Evidence     `json:"evidence,omitempty"`
}

// Validate checks invariants which must hold regardless of persistence adapter.
// 中文：Validate 校验不受具体持久化适配器影响的领域不变量。
func (g Goal) Validate() error {
	if strings.TrimSpace(g.Title) == "" {
		return errors.New("goal title is required")
	}
	if g.Priority < 1 || g.Priority > 5 {
		return errors.New("goal priority must be between 1 and 5")
	}
	if g.Progress < 0 || g.Progress > 100 {
		return errors.New("goal progress must be between 0 and 100")
	}
	if !validStatus(g.Status) {
		return fmt.Errorf("unknown goal status %q", g.Status)
	}
	if g.Status == Completed && (g.CompletedAt == nil || !hasEvidence(g.Evidence)) {
		return ErrEvidenceRequired
	}
	return nil
}

// Transition applies an allowed lifecycle transition and records completion evidence.
// 中文：Transition 执行允许的生命周期转换，并在完成时记录证据。
func (g *Goal) Transition(next Status, evidence []Evidence, now time.Time) error {
	if !allowedTransition(g.Status, next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, g.Status, next)
	}
	if next == Completed && !hasEvidence(evidence) {
		return ErrEvidenceRequired
	}
	g.Status = next
	g.UpdatedAt = now.UTC()
	if next == Completed {
		g.Progress = 100
		completedAt := now.UTC()
		g.CompletedAt = &completedAt
		validEvidence := make([]Evidence, 0, len(evidence))
		for _, item := range evidence {
			if strings.TrimSpace(item.Source) == "" || strings.TrimSpace(item.Description) == "" {
				continue
			}
			if item.ObservedAt.IsZero() {
				item.ObservedAt = now.UTC()
			} else {
				item.ObservedAt = item.ObservedAt.UTC()
			}
			validEvidence = append(validEvidence, item)
		}
		g.Evidence = append(g.Evidence, validEvidence...)
	}
	return nil
}

func hasEvidence(items []Evidence) bool {
	for _, item := range items {
		if strings.TrimSpace(item.Source) != "" && strings.TrimSpace(item.Description) != "" {
			return true
		}
	}
	return false
}

func validStatus(status Status) bool {
	switch status {
	case Draft, Active, Monitoring, Blocked, Paused, Completed, Cancelled, Archived:
		return true
	default:
		return false
	}
}

func allowedTransition(from, to Status) bool {
	switch from {
	case Draft:
		return to == Active || to == Cancelled
	case Active:
		return to == Monitoring || to == Blocked || to == Paused || to == Completed || to == Cancelled
	case Monitoring:
		return to == Active || to == Blocked || to == Paused || to == Completed || to == Cancelled
	case Blocked:
		return to == Active || to == Paused || to == Cancelled
	case Paused:
		return to == Active || to == Cancelled
	case Completed, Cancelled:
		return to == Archived
	default:
		return false
	}
}
