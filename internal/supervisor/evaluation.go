// English: This file derives conservative Goal evaluations from persisted Core facts.
// 中文：本文件仅根据 Core 持久化事实生成保守的 Goal 评估。
package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/planner"
	"personal-ai-os/internal/task"
	paierrors "personal-ai-os/pkg/errors"
)

type EvaluationStatus string

const (
	EvaluationNeedsCriteria EvaluationStatus = "needs_criteria"
	EvaluationInProgress    EvaluationStatus = "in_progress"
	EvaluationBlocked       EvaluationStatus = "blocked"
	EvaluationReady         EvaluationStatus = "ready_for_verification"
	EvaluationTerminal      EvaluationStatus = "terminal"
)

type RecommendedAction string

const (
	ActionContinue         RecommendedAction = "continue"
	ActionReplan           RecommendedAction = "replan"
	ActionWaitApproval     RecommendedAction = "wait_approval"
	ActionWaitCondition    RecommendedAction = "wait_condition"
	ActionVerifyCompletion RecommendedAction = "verify_completion"
	ActionStop             RecommendedAction = "stop"
	ActionEscalate         RecommendedAction = "escalate"
)

type CriterionAssessment struct {
	CriterionID      string             `json:"criterion_id,omitempty"`
	CriterionVersion int                `json:"criterion_version,omitempty"`
	Criterion        string             `json:"criterion"`
	Type             goal.CriterionType `json:"type,omitempty"`
	Required         bool               `json:"required"`
	Status           string             `json:"status"`
	Evidence         []string           `json:"evidence_refs,omitempty"`
	EvidenceHashes   []string           `json:"evidence_hashes,omitempty"`
	VerifierType     string             `json:"verifier_type,omitempty"`
	VerifierVersion  string             `json:"verifier_version,omitempty"`
	Reason           string             `json:"reason"`
	VerificationID   string             `json:"verification_id,omitempty"`
}

type TaskIssue struct {
	TaskID string      `json:"task_id"`
	Title  string      `json:"title"`
	Status task.Status `json:"status"`
	Reason string      `json:"reason"`
}

type EvaluationEvidenceSummary struct {
	VerifiedExecutions int      `json:"verified_executions"`
	EvidenceItems      int      `json:"evidence_items"`
	TaskIDs            []string `json:"task_ids,omitempty"`
}

type EvaluationBudgetSummary struct {
	ModelCallsUsed   int `json:"model_calls_used"`
	ModelCallsLimit  int `json:"model_calls_limit"`
	ModelTokensUsed  int `json:"model_tokens_used"`
	ModelTokensLimit int `json:"model_tokens_limit"`
	ToolCallsUsed    int `json:"tool_calls_used"`
	ToolCallsLimit   int `json:"tool_calls_limit"`
}

// Evaluation is a persisted advisory snapshot; it never mutates Goal or Task state.
// 中文：Evaluation 是持久化的建议快照，不直接修改 Goal 或 Task 状态。
type Evaluation struct {
	ID                        string                    `json:"evaluation_id,omitempty"`
	DecisionID                string                    `json:"decision_id,omitempty"`
	GoalID                    string                    `json:"goal_id"`
	GoalUpdatedAt             time.Time                 `json:"goal_updated_at"`
	PlanVersion               int                       `json:"plan_version"`
	PlanVersionKnown          bool                      `json:"plan_version_known"`
	Status                    EvaluationStatus          `json:"evaluation_status"`
	CompletionPercentage      *int                      `json:"completion_percentage"`
	CompletedCriteria         []CriterionAssessment     `json:"completed_criteria"`
	UnmetCriteria             []CriterionAssessment     `json:"unmet_criteria"`
	TaskSummary               []TaskIssue               `json:"tasks"`
	BlockedTasks              []TaskIssue               `json:"blocked_tasks"`
	FailedTasks               []TaskIssue               `json:"failed_tasks"`
	PendingApprovals          []TaskIssue               `json:"pending_approvals"`
	PendingExternalConditions []TaskIssue               `json:"pending_external_conditions"`
	PendingDependencies       []TaskIssue               `json:"pending_dependencies"`
	EvidenceSummary           EvaluationEvidenceSummary `json:"evidence_summary"`
	BudgetSummary             EvaluationBudgetSummary   `json:"budget_summary"`
	RecommendedAction         RecommendedAction         `json:"recommended_action"`
	Reason                    string                    `json:"reason"`
	CreatedAt                 time.Time                 `json:"created_at"`
}

type EvaluationStore interface {
	SaveEvaluation(context.Context, Evaluation) (Evaluation, error)
	ListEvaluations(context.Context, string, int) ([]Evaluation, error)
}

// EvaluateGoalSnapshot only makes claims supported by current persisted facts.
// Existing Goal success criteria are free-form text, so criterion satisfaction and
// percentage remain unknown until the independent verifier/structured-criteria stages.
// 中文：EvaluateGoalSnapshot 仅对当前持久化事实作出判断；现有成功标准为自由文本，独立验证器上线前不推断其满足度或百分比。
func EvaluateGoalSnapshot(item goal.Goal, tasks []task.Task, executions []Execution, usage UsageTotals, budgets Budgets, now time.Time) Evaluation {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	e := Evaluation{
		GoalID: item.ID, GoalUpdatedAt: item.UpdatedAt, Status: EvaluationInProgress,
		PlanVersionKnown: false, CreatedAt: now,
		CompletedCriteria: make([]CriterionAssessment, 0), UnmetCriteria: make([]CriterionAssessment, 0),
		BlockedTasks: make([]TaskIssue, 0), FailedTasks: make([]TaskIssue, 0),
		TaskSummary:      make([]TaskIssue, 0, len(tasks)),
		PendingApprovals: make([]TaskIssue, 0), PendingExternalConditions: make([]TaskIssue, 0),
		PendingDependencies: make([]TaskIssue, 0),
		EvidenceSummary:     EvaluationEvidenceSummary{TaskIDs: make([]string, 0)},
		BudgetSummary: EvaluationBudgetSummary{
			ModelCallsUsed: usage.ModelCalls, ModelCallsLimit: budgets.MaxModelCalls,
			ModelTokensUsed: usage.ModelTokens, ModelTokensLimit: budgets.MaxModelTokens,
			ToolCallsUsed: usage.ToolCalls, ToolCallsLimit: budgets.MaxToolCalls,
		},
	}

	verifiedTasks := make(map[string]struct{})
	for _, run := range executions {
		if run.Outcome != agentruntime.OutcomeSucceeded || !run.Verified || strings.TrimSpace(run.VerificationMethod) == "" || len(run.Evidence) == 0 {
			continue
		}
		valid := false
		for _, evidence := range run.Evidence {
			if strings.TrimSpace(evidence.Source) == "" || strings.TrimSpace(evidence.Description) == "" {
				continue
			}
			e.EvidenceSummary.EvidenceItems++
			valid = true
		}
		if valid {
			verifiedTasks[run.TaskID] = struct{}{}
		}
	}
	e.EvidenceSummary.VerifiedExecutions = len(verifiedTasks)

	statuses := make(map[string]task.Status, len(tasks))
	for _, item := range tasks {
		e.TaskSummary = append(e.TaskSummary, TaskIssue{TaskID: item.ID, Title: item.Title, Status: item.Status, Reason: "persisted Task state"})
		statuses[item.ID] = item.Status
	}
	allCompletedWithEvidence := len(tasks) > 0
	activelyRunnable := false
	for _, item := range tasks {
		_, hasEvidence := verifiedTasks[item.ID]
		if hasEvidence {
			e.EvidenceSummary.TaskIDs = append(e.EvidenceSummary.TaskIDs, item.ID)
		}
		switch item.Status {
		case task.WaitingApproval:
			e.PendingApprovals = append(e.PendingApprovals, TaskIssue{TaskID: item.ID, Title: item.Title, Status: item.Status, Reason: "task is waiting for human approval"})
		case task.WaitingExternal:
			e.PendingExternalConditions = append(e.PendingExternalConditions, TaskIssue{TaskID: item.ID, Title: item.Title, Status: item.Status, Reason: "task is waiting for an external condition"})
		case task.Blocked:
			e.BlockedTasks = append(e.BlockedTasks, TaskIssue{TaskID: item.ID, Title: item.Title, Status: item.Status, Reason: "task is explicitly blocked"})
		case task.Paused:
			e.BlockedTasks = append(e.BlockedTasks, TaskIssue{TaskID: item.ID, Title: item.Title, Status: item.Status, Reason: "task is paused and requires an explicit resume"})
		case task.Failed:
			e.FailedTasks = append(e.FailedTasks, TaskIssue{TaskID: item.ID, Title: item.Title, Status: item.Status, Reason: "most recent task attempt failed"})
		case task.Running, task.Ready, task.Created, task.Retrying:
			activelyRunnable = true
		}
		for _, dependencyID := range item.DependsOn {
			dependencyStatus, exists := statuses[dependencyID]
			if exists && dependencyStatus == task.Completed {
				continue
			}
			e.PendingDependencies = append(e.PendingDependencies, TaskIssue{
				TaskID: item.ID, Title: item.Title, Status: item.Status,
				Reason: fmt.Sprintf("dependency %s is not completed", dependencyID),
			})
		}
		if item.Status != task.Completed || !hasEvidence {
			allCompletedWithEvidence = false
		}
	}
	hasLegacyCriteria := false
	for _, criterion := range item.SuccessCriteria {
		criterion = strings.TrimSpace(criterion)
		if criterion == "" {
			continue
		}
		hasLegacyCriteria = true
		e.UnmetCriteria = append(e.UnmetCriteria, CriterionAssessment{
			CriterionID: fmt.Sprintf("legacy-%d", len(e.UnmetCriteria)+1), Criterion: criterion, Required: true,
			Status: "unknown", Reason: "free-form success criteria cannot be verified deterministically; migrate this criterion to a typed rule",
		})
	}
	allRequiredCriteriaSatisfied := !hasLegacyCriteria
	requiredCriterionCount := 0
	for _, criterion := range item.Criteria {
		assessment := verifyCriterion(item.ID, criterion, tasks, executions)
		if assessment.Status == "satisfied" {
			e.CompletedCriteria = append(e.CompletedCriteria, assessment)
		} else {
			e.UnmetCriteria = append(e.UnmetCriteria, assessment)
		}
		if criterion.Required {
			requiredCriterionCount++
			if assessment.Status != "satisfied" {
				allRequiredCriteriaSatisfied = false
			}
		}
	}
	if requiredCriterionCount == 0 {
		allRequiredCriteriaSatisfied = false
	}

	budgetExhausted := (budgets.MaxModelCalls > 0 && usage.ModelCalls >= budgets.MaxModelCalls) ||
		(budgets.MaxModelTokens > 0 && usage.ModelTokens >= budgets.MaxModelTokens) ||
		(budgets.MaxToolCalls > 0 && usage.ToolCalls >= budgets.MaxToolCalls)
	switch {
	case item.Status == goal.Completed || item.Status == goal.Cancelled || item.Status == goal.Archived:
		e.Status, e.RecommendedAction, e.Reason = EvaluationTerminal, ActionStop, "goal is already in a terminal lifecycle state"
	case item.Status == goal.Paused:
		e.Status, e.RecommendedAction, e.Reason = EvaluationBlocked, ActionStop, "goal is paused and requires an explicit resume"
	case item.Status == goal.Blocked:
		e.Status, e.RecommendedAction, e.Reason = EvaluationBlocked, ActionWaitCondition, "goal is explicitly blocked"
	case budgetExhausted:
		e.Status, e.RecommendedAction, e.Reason = EvaluationBlocked, ActionStop, "a configured model or tool budget is exhausted"
	case len(e.PendingApprovals) > 0:
		e.Status, e.RecommendedAction, e.Reason = EvaluationBlocked, ActionWaitApproval, "one or more tasks require human approval"
	case len(e.PendingExternalConditions) > 0:
		e.Status, e.RecommendedAction, e.Reason = EvaluationBlocked, ActionWaitCondition, "one or more tasks await an external condition"
	case len(item.Criteria) == 0 && !hasLegacyCriteria:
		e.Status, e.RecommendedAction, e.Reason = EvaluationNeedsCriteria, ActionReplan, "goal has no explicit success criteria; clarify criteria before claiming completion"
	case len(e.FailedTasks) > 0 || len(e.BlockedTasks) > 0:
		e.Status, e.RecommendedAction, e.Reason = EvaluationBlocked, ActionReplan, "failed or blocked tasks require plan review"
	case len(tasks) == 0:
		e.Status, e.RecommendedAction, e.Reason = EvaluationInProgress, ActionReplan, "goal has no persisted tasks"
	case activelyRunnable:
		e.Status, e.RecommendedAction, e.Reason = EvaluationInProgress, ActionContinue, "tasks remain ready or in progress"
	case allRequiredCriteriaSatisfied:
		e.Status, e.RecommendedAction, e.Reason = EvaluationReady, ActionVerifyCompletion, "all required typed success criteria match Core-persisted verified evidence"
	case allCompletedWithEvidence:
		e.Status, e.RecommendedAction, e.Reason = EvaluationBlocked, ActionEscalate, "all tasks completed but required success criteria are not satisfied by matching evidence"
	default:
		e.Status, e.RecommendedAction, e.Reason = EvaluationBlocked, ActionEscalate, "no runnable task or known wait condition can advance the goal"
	}
	return e
}

func verifyCriterion(goalID string, criterion goal.SuccessCriterion, tasks []task.Task, executions []Execution) CriterionAssessment {
	assessment := CriterionAssessment{
		CriterionID: criterion.ID, CriterionVersion: criterion.Version, Criterion: criterion.Name, Type: criterion.Type, Required: criterion.Required,
		VerifierType: "core.execution-evidence", VerifierVersion: "v1",
		Status: "insufficient_evidence", Reason: "no matching independently verified execution evidence",
	}
	if !criterion.Confirmed {
		assessment.Status = "unknown"
		assessment.Reason = "criterion has not been confirmed by the goal owner"
		return assessment
	}
	expectedMethod := goal.CriterionMethodVerifiedExecutionEvidence
	if criterion.Type == goal.CriterionArtifactExists || criterion.Type == goal.CriterionMarkdownStructure {
		expectedMethod = goal.CriterionMethodArtifactInStorage
	}
	if criterion.ValidationMethod != expectedMethod {
		assessment.Status = "unsupported"
		assessment.Reason = "validation method is not supported by this Core version"
		return assessment
	}
	taskByID := make(map[string]task.Task, len(tasks))
	for _, item := range tasks {
		taskByID[item.ID] = item
	}
	verifiedRuns := make(map[string][]Execution)
	for _, run := range executions {
		taskItem, belongsToGoal := taskByID[run.TaskID]
		if belongsToGoal && taskItem.GoalID == goalID && run.Outcome == agentruntime.OutcomeSucceeded && run.Verified && strings.TrimSpace(run.VerificationMethod) != "" {
			verifiedRuns[run.TaskID] = append(verifiedRuns[run.TaskID], run)
		}
	}
	appendReferences := func(run Execution, evidence Evidence) {
		if strings.TrimSpace(evidence.EvidenceID) == "" {
			return
		}
		for _, existing := range assessment.Evidence {
			if existing == evidence.EvidenceID {
				return
			}
		}
		assessment.Evidence = append(assessment.Evidence, evidence.EvidenceID)
		hash := evidence.ContentHash
		if hash == "" {
			hashInput, _ := json.Marshal(struct {
				EvidenceID, ExecutionID, TaskID, Source, Description, Reference, VerificationMethod string
				ObservedAt                                                                          time.Time
			}{evidence.EvidenceID, run.ID, evidence.TaskID, evidence.Source, evidence.Description, evidence.Reference, run.VerificationMethod, evidence.ObservedAt.UTC()})
			digest := sha256.Sum256(hashInput)
			hash = hex.EncodeToString(digest[:])
		}
		assessment.EvidenceHashes = append(assessment.EvidenceHashes, hash)
	}
	switch criterion.Type {
	case goal.CriterionVerifiedEvidenceCount:
		uniqueEvidence := make(map[string]struct{})
		for _, run := range executions {
			taskItem, belongsToGoal := taskByID[run.TaskID]
			if !belongsToGoal || taskItem.GoalID != goalID || run.Outcome != agentruntime.OutcomeSucceeded || !run.Verified || strings.TrimSpace(run.VerificationMethod) == "" {
				continue
			}
			for _, evidence := range run.Evidence {
				if strings.TrimSpace(evidence.EvidenceID) == "" || strings.TrimSpace(evidence.Source) == "" || strings.TrimSpace(evidence.Description) == "" ||
					evidence.CriterionID != criterion.ID || evidence.CriterionVersion != criterion.Version ||
					(criterion.RequireReference && strings.TrimSpace(evidence.Reference) == "") ||
					!sourceAllowed(criterion.EvidenceSources, evidence.Source) {
					continue
				}
				if _, exists := uniqueEvidence[evidence.EvidenceID]; exists {
					continue
				}
				uniqueEvidence[evidence.EvidenceID] = struct{}{}
				appendReferences(run, evidence)
			}
		}
		matches := len(uniqueEvidence)
		if matches >= criterion.MinEvidence {
			assessment.Status = "satisfied"
			assessment.Reason = fmt.Sprintf("found %d matching verified evidence items; minimum is %d", matches, criterion.MinEvidence)
		} else {
			assessment.Reason = fmt.Sprintf("found %d matching verified evidence items; minimum is %d", matches, criterion.MinEvidence)
		}
	case goal.CriterionVerifiedTaskCompletion:
		missing := make([]string, 0)
		for _, taskID := range criterion.TaskIDs {
			item, exists := taskByID[taskID]
			if !exists {
				missing = append(missing, taskID+" (unknown task)")
				continue
			}
			if item.GoalID != goalID {
				missing = append(missing, taskID+" (belongs to another goal)")
				continue
			}
			if item.Status != task.Completed {
				missing = append(missing, taskID+" (not completed)")
				continue
			}
			mapped := false
			for _, mappedCriterion := range item.CriterionIDs {
				if mappedCriterion == criterion.ID {
					mapped = true
					break
				}
			}
			if !mapped {
				missing = append(missing, taskID+" (task is not mapped to this criterion)")
				continue
			}
			runs := verifiedRuns[taskID]
			if len(runs) == 0 {
				missing = append(missing, taskID+" (no verified execution)")
				continue
			}
			hasEvidence := false
			validTaskEvidence := false
			for _, run := range runs {
				for _, evidence := range run.Evidence {
					if strings.TrimSpace(evidence.EvidenceID) != "" && evidence.CriterionID == criterion.ID && evidence.CriterionVersion == criterion.Version && strings.TrimSpace(evidence.Source) != "" && strings.TrimSpace(evidence.Description) != "" {
						if criterion.RequireReference && strings.TrimSpace(evidence.Reference) == "" {
							continue
						}
						if !sourceAllowed(criterion.EvidenceSources, evidence.Source) {
							continue
						}
						hasEvidence = true
						validTaskEvidence = true
						appendReferences(run, evidence)
					}
				}
			}
			if !hasEvidence || !validTaskEvidence {
				missing = append(missing, taskID+" (no valid evidence)")
			}
		}
		if len(missing) == 0 {
			assessment.Status = "satisfied"
			assessment.Reason = "all referenced Tasks are completed and have verified execution evidence"
		} else {
			assessment.Reason = "unsatisfied task requirements: " + strings.Join(missing, ", ")
		}
	case goal.CriterionArtifactExists, goal.CriterionMarkdownStructure:
		matched := 0
		for taskID, runs := range verifiedRuns {
			if taskByID[taskID].GoalID != goalID {
				continue
			}
			for _, run := range runs {
				if run.VerificationMethod != "core.file-artifact/v1" {
					continue
				}
				artifactEvidenceMatched := false
				for _, evidence := range run.Evidence {
					if evidence.CriterionID != criterion.ID || evidence.CriterionVersion != criterion.Version || evidence.VerifierType != "core.file-artifact" || evidence.VerifierVersion != "v1" || len(evidence.ContentHash) != 64 {
						continue
					}
					prefix := "artifact://" + strings.TrimPrefix(criterion.ArtifactPath, "./") + "#sha256="
					if !strings.HasPrefix(evidence.Reference, prefix) || !strings.HasSuffix(evidence.Reference, evidence.ContentHash) {
						continue
					}
					artifactEvidenceMatched = true
					appendReferences(run, evidence)
				}
				if !artifactEvidenceMatched {
					continue
				}
				if criterion.RequireReference {
					referenceCount := 0
					for _, evidence := range run.Evidence {
						if evidence.CriterionID != criterion.ID || evidence.CriterionVersion != criterion.Version || evidence.VerifierType != "core.source-reference" || evidence.VerifierVersion != "v1" || len(evidence.ContentHash) != 64 || !strings.HasPrefix(evidence.Reference, "source://") || !strings.HasSuffix(evidence.Reference, "#sha256="+evidence.ContentHash) {
							continue
						}
						referenceCount++
						appendReferences(run, evidence)
					}
					if referenceCount == 0 {
						continue
					}
				}
				matched++
			}
		}
		if matched > 0 {
			assessment.Status = "satisfied"
			assessment.Reason = fmt.Sprintf("Core artifact verifier validated the required file and content hash for criterion version %d", criterion.Version)
		} else {
			assessment.Reason = "no current Core artifact verification matches the required path and criterion version"
		}
	default:
		assessment.Status = "unsupported"
		assessment.Reason = "criterion type is not supported by this Core version"
	}
	return assessment
}

func sourceAllowed(allowed []string, source string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(source)) {
			return true
		}
	}
	return false
}

func (s *Supervisor) EvaluateGoal(ctx context.Context, goalID string) (Evaluation, error) {
	if s.Goals == nil || s.Tasks == nil || s.Executions == nil || s.Evaluations == nil || s.Decisions == nil {
		return Evaluation{}, fmt.Errorf("goal evaluator dependencies are incomplete")
	}
	if strings.TrimSpace(goalID) == "" {
		return Evaluation{}, fmt.Errorf("goal_id is required")
	}
	item, err := s.Goals.Get(ctx, goalOwnerID(), goalID)
	if err != nil {
		if errors.Is(err, paierrors.ErrNotFound) {
			return Evaluation{}, paierrors.ErrNotFound
		}
		return Evaluation{}, fmt.Errorf("load goal for evaluation: %w", err)
	}
	tasks, err := s.Tasks.ListByGoal(ctx, item.ID, 500)
	if err != nil {
		return Evaluation{}, fmt.Errorf("load goal tasks for evaluation: %w", err)
	}
	executions, err := s.Executions.ListByGoal(ctx, item.ID)
	if err != nil {
		return Evaluation{}, fmt.Errorf("load goal executions for evaluation: %w", err)
	}
	usage := UsageTotals{}
	if s.Usage != nil {
		usage, err = s.Usage.ModelUsage(ctx, item.ID)
		if err != nil {
			return Evaluation{}, fmt.Errorf("load goal usage for evaluation: %w", err)
		}
	}
	evaluation := EvaluateGoalSnapshot(item, tasks, executions, usage, s.Budgets, time.Now().UTC())
	if s.Plans != nil {
		planVersion, planErr := s.Plans.GetCurrentPlan(ctx, item.ID)
		if planErr == nil {
			evaluation.PlanVersion = planVersion.Version
			evaluation.PlanVersionKnown = true
		} else if !errors.Is(planErr, planner.ErrPlanConflict) {
			return Evaluation{}, fmt.Errorf("load current Plan Version for evaluation: %w", planErr)
		}
	}
	evaluation, err = s.Evaluations.SaveEvaluation(ctx, evaluation)
	if err != nil {
		return Evaluation{}, fmt.Errorf("persist goal evaluation: %w", err)
	}
	decision, err := NewDecision(evaluation)
	if err != nil {
		return Evaluation{}, err
	}
	decision, err = s.Decisions.SaveDecision(ctx, decision)
	if err != nil {
		return Evaluation{}, fmt.Errorf("persist Supervisor decision: %w", err)
	}
	evaluation.DecisionID = decision.ID
	return evaluation, nil
}

func (s *Supervisor) ListEvaluations(ctx context.Context, goalID string, limit int) ([]Evaluation, error) {
	if s.Goals == nil || s.Evaluations == nil {
		return nil, fmt.Errorf("goal evaluation repository is unavailable")
	}
	if _, err := s.Goals.Get(ctx, goalOwnerID(), goalID); err != nil {
		if errors.Is(err, paierrors.ErrNotFound) {
			return nil, paierrors.ErrNotFound
		}
		return nil, fmt.Errorf("verify goal for evaluation history: %w", err)
	}
	items, err := s.Evaluations.ListEvaluations(ctx, goalID, limit)
	if err != nil {
		return nil, fmt.Errorf("list goal evaluations: %w", err)
	}
	return items, nil
}

func (s *Supervisor) ListDecisions(ctx context.Context, goalID string, limit int) ([]Decision, error) {
	if s.Goals == nil || s.Decisions == nil {
		return nil, ErrDecisionStoreUnavailable
	}
	if _, err := s.Goals.Get(ctx, goalOwnerID(), goalID); err != nil {
		if errors.Is(err, paierrors.ErrNotFound) {
			return nil, paierrors.ErrNotFound
		}
		return nil, fmt.Errorf("verify Goal for decision history: %w", err)
	}
	items, err := s.Decisions.ListDecisions(ctx, goalID, limit)
	if err != nil {
		return nil, fmt.Errorf("list Supervisor decisions: %w", err)
	}
	return items, nil
}
