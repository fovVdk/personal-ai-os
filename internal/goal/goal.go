// Package goal defines the Goal aggregate and its lifecycle rules.
// 中文：goal 包定义 Goal 聚合及其生命周期规则。
// English: This aggregate enforces Goal lifecycle transitions and evidence-gated completion.
// 中文：本聚合实现 Goal 生命周期状态转换，并要求提供证据后方可完成目标。
package goal

import (
	"errors"
	"fmt"
	"path/filepath"
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
var ErrCriteriaUnverified = errors.New("required structured success criteria are not verified")

type CriterionType string

const (
	CriterionVerifiedEvidenceCount  CriterionType = "verified_evidence_count"
	CriterionVerifiedTaskCompletion CriterionType = "verified_task_completion"
	CriterionArtifactExists         CriterionType = "artifact_exists"
	CriterionMarkdownStructure      CriterionType = "markdown_structure"
)

type CriterionSource string

const (
	CriterionSourceUser   CriterionSource = "user"
	CriterionSourceSystem CriterionSource = "system"
	CriterionSourceModel  CriterionSource = "model"
	CriterionSourceLegacy CriterionSource = "legacy"
)

type CriterionValidationMethod string

const CriterionMethodVerifiedExecutionEvidence CriterionValidationMethod = "verified_execution_evidence"
const CriterionMethodArtifactInStorage CriterionValidationMethod = "artifact_in_file_storage"

// SuccessCriterion defines a versioned, machine-checkable outcome requirement.
// 中文：SuccessCriterion 定义带版本且可由机器核验的目标完成条件。
type SuccessCriterion struct {
	ID               string                    `json:"id"`
	Name             string                    `json:"name"`
	Description      string                    `json:"description"`
	Type             CriterionType             `json:"type"`
	Required         bool                      `json:"required"`
	ValidationMethod CriterionValidationMethod `json:"validation_method"`
	MinEvidence      int                       `json:"min_evidence,omitempty"`
	EvidenceSources  []string                  `json:"evidence_sources,omitempty"`
	RequireReference bool                      `json:"require_reference,omitempty"`
	TaskIDs          []string                  `json:"task_ids,omitempty"`
	Source           CriterionSource           `json:"source"`
	Confirmed        bool                      `json:"confirmed"`
	ArtifactPath     string                    `json:"artifact_path,omitempty"`
	RequiredSections []string                  `json:"required_sections,omitempty"`
	MaxArtifactBytes int64                     `json:"max_artifact_bytes,omitempty"`
	Version          int                       `json:"version"`
	CreatedAt        time.Time                 `json:"created_at,omitempty"`
}

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
	ID              string             `json:"id"`
	Title           string             `json:"title"`
	Description     string             `json:"description"`
	OwnerID         string             `json:"owner_id"`
	ParentGoalID    *string            `json:"parent_goal_id,omitempty"`
	Priority        int                `json:"priority"`
	SuccessCriteria []string           `json:"success_criteria"`
	Criteria        []SuccessCriterion `json:"criteria,omitempty"`
	Constraints     []string           `json:"constraints"`
	AutonomyPolicy  map[string]any     `json:"autonomy_policy"`
	ReviewPolicy    map[string]any     `json:"review_policy"`
	Status          Status             `json:"status"`
	Progress        int                `json:"progress"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	CompletedAt     *time.Time         `json:"completed_at,omitempty"`
	Evidence        []Evidence         `json:"evidence,omitempty"`
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
	if err := ValidateSuccessCriteria(g.Criteria); err != nil {
		return err
	}
	if g.Status == Completed && (g.CompletedAt == nil || !hasEvidence(g.Evidence)) {
		return ErrEvidenceRequired
	}
	return nil
}

func ValidateSuccessCriteria(criteria []SuccessCriterion) error {
	seen := make(map[string]struct{}, len(criteria))
	requiredCount := 0
	for i, criterion := range criteria {
		id := strings.TrimSpace(criterion.ID)
		if id == "" || len(id) > 128 {
			return fmt.Errorf("success criterion %d requires an id of at most 128 characters", i)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate success criterion id %q", id)
		}
		seen[id] = struct{}{}
		if criterion.Required {
			requiredCount++
		}
		if strings.TrimSpace(criterion.Name) == "" || len(criterion.Name) > 200 {
			return fmt.Errorf("success criterion %q requires a name of at most 200 characters", id)
		}
		if len(criterion.Description) > 4000 {
			return fmt.Errorf("success criterion %q description exceeds 4000 characters", id)
		}
		if criterion.Type != CriterionVerifiedEvidenceCount && criterion.Type != CriterionVerifiedTaskCompletion && criterion.Type != CriterionArtifactExists && criterion.Type != CriterionMarkdownStructure {
			return fmt.Errorf("success criterion %q has unsupported type %q", id, criterion.Type)
		}
		if criterion.Version < 1 {
			return fmt.Errorf("success criterion %q version must be positive", id)
		}
		switch criterion.Source {
		case CriterionSourceUser, CriterionSourceSystem, CriterionSourceModel, CriterionSourceLegacy:
		default:
			return fmt.Errorf("success criterion %q has invalid source %q", id, criterion.Source)
		}
		if (criterion.Source == CriterionSourceModel || criterion.Source == CriterionSourceLegacy) && criterion.Confirmed {
			return fmt.Errorf("success criterion %q from %q cannot be pre-confirmed", id, criterion.Source)
		}
		if criterion.Type == CriterionArtifactExists || criterion.Type == CriterionMarkdownStructure {
			if criterion.ValidationMethod != CriterionMethodArtifactInStorage {
				return fmt.Errorf("success criterion %q has unsupported artifact validation method %q", id, criterion.ValidationMethod)
			}
			if strings.TrimSpace(criterion.ArtifactPath) == "" || len(criterion.ArtifactPath) > 1024 || !filepath.IsLocal(criterion.ArtifactPath) || filepath.IsAbs(criterion.ArtifactPath) || strings.ContainsAny(criterion.ArtifactPath, "\\\x00\r\n") {
				return fmt.Errorf("success criterion %q requires a safe relative artifact_path", id)
			}
			if criterion.MaxArtifactBytes < 1 || criterion.MaxArtifactBytes > 64<<20 {
				return fmt.Errorf("success criterion %q max_artifact_bytes must be between 1 byte and 64 MiB", id)
			}
			if criterion.Type == CriterionArtifactExists && len(criterion.RequiredSections) > 0 {
				return fmt.Errorf("success criterion %q cannot set required_sections for artifact_exists", id)
			}
			if criterion.Type == CriterionMarkdownStructure && len(criterion.RequiredSections) == 0 {
				return fmt.Errorf("success criterion %q requires Markdown sections", id)
			}
			if len(criterion.RequiredSections) > 32 {
				return fmt.Errorf("success criterion %q has too many required sections", id)
			}
			if len(criterion.TaskIDs) > 0 || criterion.MinEvidence != 0 || len(criterion.EvidenceSources) > 0 {
				return fmt.Errorf("success criterion %q combines incompatible artifact and evidence-count parameters", id)
			}
			continue
		}
		if criterion.ValidationMethod != CriterionMethodVerifiedExecutionEvidence {
			return fmt.Errorf("success criterion %q has unsupported validation method %q", id, criterion.ValidationMethod)
		}
		if len(criterion.EvidenceSources) > 32 {
			return fmt.Errorf("success criterion %q has too many evidence sources", id)
		}
		for _, source := range criterion.EvidenceSources {
			if strings.TrimSpace(source) == "" || len(source) > 128 {
				return fmt.Errorf("success criterion %q contains an invalid evidence source", id)
			}
		}
		switch criterion.Type {
		case CriterionVerifiedEvidenceCount:
			if criterion.MinEvidence < 1 || criterion.MinEvidence > 10000 {
				return fmt.Errorf("success criterion %q min_evidence must be between 1 and 10000", id)
			}
			if len(criterion.TaskIDs) > 0 {
				return fmt.Errorf("success criterion %q must not set task_ids for evidence count", id)
			}
		case CriterionVerifiedTaskCompletion:
			if len(criterion.TaskIDs) == 0 || len(criterion.TaskIDs) > 200 {
				return fmt.Errorf("success criterion %q requires between 1 and 200 task_ids", id)
			}
			if criterion.MinEvidence != 0 {
				return fmt.Errorf("success criterion %q must not set min_evidence for task completion", id)
			}
			if len(criterion.EvidenceSources) > 0 || criterion.RequireReference {
				return fmt.Errorf("success criterion %q must not set evidence count filters for task completion", id)
			}
			taskIDs := make(map[string]struct{}, len(criterion.TaskIDs))
			for _, taskID := range criterion.TaskIDs {
				if strings.TrimSpace(taskID) == "" {
					return fmt.Errorf("success criterion %q contains an empty task_id", id)
				}
				if _, ok := taskIDs[taskID]; ok {
					return fmt.Errorf("success criterion %q repeats task_id %q", id, taskID)
				}
				taskIDs[taskID] = struct{}{}
			}
		}
	}
	if len(criteria) > 0 && requiredCount == 0 {
		return errors.New("at least one structured success criterion must be required")
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
