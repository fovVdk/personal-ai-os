// English: Versioned plan proposals are advisory until the Core commits them.
// 中文：版本化计划提案在 Core 提交前仅为建议。
package planner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"personal-ai-os/internal/goal"
)

var ErrPlanConflict = errors.New("plan proposal base version conflict")
var ErrProposalExpired = errors.New("plan proposal expired")
var ErrProposalNotFound = errors.New("plan proposal not found")
var ErrNoPlanProgress = errors.New("plan proposal makes no executable progress")

type ProposalStatus string

const (
	ProposalProposed ProposalStatus = "proposed"
	ProposalAccepted ProposalStatus = "accepted"
	ProposalRejected ProposalStatus = "rejected"
	ProposalExpired  ProposalStatus = "expired"
	ProposalConflict ProposalStatus = "conflict"
)

type PlanProposal struct {
	ID              string         `json:"proposal_id,omitempty"`
	GoalID          string         `json:"goal_id"`
	BasePlanVersion int            `json:"base_plan_version"`
	ProposedVersion int            `json:"proposed_plan_version"`
	Trigger         string         `json:"trigger"`
	Reason          string         `json:"reason"`
	EvaluationID    string         `json:"evaluation_id,omitempty"`
	RetainedTaskIDs []string       `json:"retained_task_ids,omitempty"`
	Plan            Plan           `json:"plan"`
	Status          ProposalStatus `json:"status"`
	Fingerprint     string         `json:"fingerprint,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	ExpiresAt       time.Time      `json:"expires_at"`
	ResolvedAt      *time.Time     `json:"resolved_at,omitempty"`
}

func (p PlanProposal) Validate(criteria []goal.SuccessCriterion, existingTaskCount int, maxTasks int) error {
	if p.GoalID == "" || p.Plan.GoalID != p.GoalID {
		return errors.New("proposal and plan must target the same Goal")
	}
	if p.BasePlanVersion < 0 || p.ProposedVersion != p.BasePlanVersion+1 {
		return errors.New("proposed plan version must be the next version")
	}
	if strings.TrimSpace(p.Trigger) == "" || strings.TrimSpace(p.Reason) == "" {
		return errors.New("plan proposal trigger and reason are required")
	}
	if !p.ExpiresAt.IsZero() && !p.CreatedAt.IsZero() && !p.ExpiresAt.After(p.CreatedAt) {
		return errors.New("plan proposal expiry must be after creation")
	}
	if err := p.Plan.Validate(); err != nil {
		return fmt.Errorf("validate proposed plan: %w", err)
	}
	if maxTasks < 1 || len(p.Plan.Tasks) > maxTasks || existingTaskCount > maxTasks {
		return errors.New("plan proposal exceeds task budget")
	}
	known := make(map[string]bool, len(criteria))
	required := make(map[string]bool, len(criteria))
	for _, criterion := range criteria {
		known[criterion.ID] = true
		if criterion.Required {
			required[criterion.ID] = false
		}
	}
	for _, spec := range p.Plan.Tasks {
		seen := make(map[string]bool, len(spec.CriterionIDs))
		for _, id := range spec.CriterionIDs {
			if !known[id] {
				return fmt.Errorf("task %q maps to unknown criterion %q", spec.Key, id)
			}
			if seen[id] {
				return fmt.Errorf("task %q repeats criterion mapping %q", spec.Key, id)
			}
			seen[id] = true
			required[id] = true
		}
	}
	for id, mapped := range required {
		if !mapped {
			return fmt.Errorf("required criterion %q is not mapped to any proposed task", id)
		}
	}
	return nil
}

func (p PlanProposal) ComputeFingerprint() (string, error) {
	value := struct {
		GoalID          string
		BasePlanVersion int
		Trigger         string
		Reason          string
		Plan            Plan
	}{p.GoalID, p.BasePlanVersion, p.Trigger, p.Reason, p.Plan}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode plan proposal fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

type PlanVersion struct {
	ID             string    `json:"plan_id"`
	GoalID         string    `json:"goal_id"`
	Version        int       `json:"version"`
	ParentVersion  int       `json:"parent_version"`
	Status         string    `json:"status"`
	PlanningReason string    `json:"planning_reason"`
	Trigger        string    `json:"trigger"`
	EvaluationID   string    `json:"evaluation_id,omitempty"`
	Plan           Plan      `json:"plan"`
	CreatedAt      time.Time `json:"created_at"`
}

type ProposalRepository interface {
	CreateProposal(context.Context, PlanProposal) (PlanProposal, error)
	AcceptProposal(context.Context, string, string, int) (PlanVersion, error)
	RejectProposal(context.Context, string, string, string) (PlanProposal, error)
	GetCurrentPlan(context.Context, string) (PlanVersion, error)
	ListProposals(context.Context, string, int) ([]PlanProposal, error)
}
