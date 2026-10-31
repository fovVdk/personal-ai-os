// English: These tests protect Goal transition rules, UTC timestamps, and completion evidence.
// 中文：本测试保护 Goal 状态机规则、UTC 时间戳及完成证据要求。
package goal

import (
	"errors"
	"testing"
	"time"
)

func TestCompletionRequiresEvidenceAndSetsProgress(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.FixedZone("local", 3600))
	g := Goal{Status: Active, Progress: 40}
	if err := g.Transition(Completed, nil, now); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatalf("expected evidence requirement, got %v", err)
	}
	if g.Status != Active {
		t.Fatalf("failed transition mutated goal status: %s", g.Status)
	}
	evidence := []Evidence{{Source: "user-confirmation", Description: "Acceptance criteria verified"}}
	if err := g.Transition(Completed, evidence, now); err != nil {
		t.Fatalf("complete with evidence: %v", err)
	}
	if g.Progress != 100 || g.CompletedAt == nil || g.CompletedAt.Location() != time.UTC {
		t.Fatalf("completion fields not updated: %+v", g)
	}
}

func TestGoalRejectsInvalidTransitions(t *testing.T) {
	g := Goal{Status: Archived}
	if err := g.Transition(Active, nil, time.Now()); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected invalid transition, got %v", err)
	}
}
