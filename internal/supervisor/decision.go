// English: Supervisor decisions are deterministic records derived from Evaluation facts.
// 中文：Supervisor Decision 是根据 Evaluation 事实确定性生成的持久化记录。
package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"personal-ai-os/internal/planner"
	"personal-ai-os/internal/task"
	paierrors "personal-ai-os/pkg/errors"
)

var ErrDecisionStoreUnavailable = errors.New("supervisor decision store is unavailable")

type DecisionType string

const (
	DecisionContinueExecution DecisionType = "continue_execution"
	DecisionReplan            DecisionType = "replan"
	DecisionVerifyCompletion  DecisionType = "verify_completion"
	DecisionWaitApproval      DecisionType = "wait_approval"
	DecisionWaitCondition     DecisionType = "wait_condition"
	DecisionBudgetExhausted   DecisionType = "budget_exhausted"
	DecisionStop              DecisionType = "stop"
	DecisionEscalate          DecisionType = "escalate"
)

type Decision struct {
	ID                  string       `json:"decision_id,omitempty"`
	GoalID              string       `json:"goal_id"`
	GoalUpdatedAt       time.Time    `json:"goal_updated_at"`
	PlanVersion         int          `json:"plan_version"`
	PlanVersionKnown    bool         `json:"plan_version_known"`
	EvaluationID        string       `json:"evaluation_id,omitempty"`
	Type                DecisionType `json:"decision_type"`
	Status              string       `json:"decision_status"`
	Reason              string       `json:"reason"`
	ProgressFingerprint string       `json:"progress_fingerprint"`
	CreatedAt           time.Time    `json:"created_at"`
	CompletedAt         *time.Time   `json:"completed_at,omitempty"`
}

type DecisionStore interface {
	SaveDecision(context.Context, Decision) (Decision, error)
	ListDecisions(context.Context, string, int) ([]Decision, error)
}

func (s *Supervisor) CurrentPlan(ctx context.Context, goalID string) (planner.PlanVersion, error) {
	if s.Goals == nil || s.Plans == nil {
		return planner.PlanVersion{}, planner.ErrPlanConflict
	}
	if _, err := s.Goals.Get(ctx, goalOwnerID(), goalID); err != nil {
		if errors.Is(err, paierrors.ErrNotFound) {
			return planner.PlanVersion{}, paierrors.ErrNotFound
		}
		return planner.PlanVersion{}, err
	}
	return s.Plans.GetCurrentPlan(ctx, goalID)
}

func (s *Supervisor) ListPlanProposals(ctx context.Context, goalID string, limit int) ([]planner.PlanProposal, error) {
	if s.Goals == nil || s.Plans == nil {
		return nil, planner.ErrProposalNotFound
	}
	if _, err := s.Goals.Get(ctx, goalOwnerID(), goalID); err != nil {
		if errors.Is(err, paierrors.ErrNotFound) {
			return nil, paierrors.ErrNotFound
		}
		return nil, err
	}
	return s.Plans.ListProposals(ctx, goalID, limit)
}

func (s *Supervisor) ProposePlan(ctx context.Context, goalID string) (planner.PlanProposal, error) {
	if s.Goals == nil || s.Tasks == nil || s.Planner == nil || s.Plans == nil || s.Evaluations == nil {
		return planner.PlanProposal{}, errors.New("dynamic Planner dependencies are incomplete")
	}
	item, err := s.Goals.Get(ctx, goalOwnerID(), goalID)
	if err != nil {
		if errors.Is(err, paierrors.ErrNotFound) {
			return planner.PlanProposal{}, paierrors.ErrNotFound
		}
		return planner.PlanProposal{}, err
	}
	evaluation, err := s.EvaluateGoal(ctx, goalID)
	if err != nil {
		return planner.PlanProposal{}, err
	}
	if evaluation.RecommendedAction != ActionReplan && evaluation.RecommendedAction != ActionEscalate {
		return planner.PlanProposal{}, fmt.Errorf("Goal Evaluation recommends %s, not replan", evaluation.RecommendedAction)
	}
	usage := UsageTotals{}
	if s.Usage != nil {
		usage, err = s.Usage.ModelUsage(ctx, goalID)
		if err != nil {
			return planner.PlanProposal{}, fmt.Errorf("load Goal planning budget: %w", err)
		}
	}
	if (s.Budgets.MaxModelCalls > 0 && usage.ModelCalls >= s.Budgets.MaxModelCalls) || (s.Budgets.MaxModelTokens > 0 && usage.ModelTokens >= s.Budgets.MaxModelTokens) {
		return planner.PlanProposal{}, errors.New("Goal planning model budget is exhausted")
	}
	tasks, err := s.Tasks.ListByGoal(ctx, goalID, 200)
	if err != nil {
		return planner.PlanProposal{}, fmt.Errorf("load tasks for replanning: %w", err)
	}
	baseVersion := 0
	var currentPlan *planner.PlanVersion
	if current, err := s.Plans.GetCurrentPlan(ctx, goalID); err == nil {
		baseVersion = current.Version
		currentPlan = &current
	} else if !errors.Is(err, planner.ErrPlanConflict) {
		return planner.PlanProposal{}, fmt.Errorf("load current Plan Version: %w", err)
	}
	planSpecByKey := make(map[string]planner.TaskSpec)
	if currentPlan != nil {
		for _, spec := range currentPlan.Plan.Tasks {
			planSpecByKey[spec.Key] = spec
		}
	}
	existingTasks := make([]planner.ExistingTask, 0, len(tasks))
	for _, currentTask := range tasks {
		if currentTask.PlanKey == "" {
			if currentTask.Status != task.Completed && currentTask.Status != task.Cancelled {
				return planner.PlanProposal{}, fmt.Errorf("cannot safely replan active legacy Task %s without PlanKey", currentTask.ID)
			}
			continue
		}
		spec, known := planSpecByKey[currentTask.PlanKey]
		if !known {
			if currentTask.Status != task.Completed && currentTask.Status != task.Cancelled {
				return planner.PlanProposal{}, fmt.Errorf("cannot safely replan active Task %s absent from current Plan Version", currentTask.ID)
			}
			continue
		}
		existingTasks = append(existingTasks, planner.ExistingTask{Key: currentTask.PlanKey, Status: string(currentTask.Status), Title: currentTask.Title, Description: currentTask.Description, SuccessCriteria: currentTask.SuccessCriteria, CriterionIDs: currentTask.CriterionIDs, DependsOn: append([]string(nil), spec.DependsOn...)})
	}
	failed, blocked, unmet := make([]string, 0), make([]string, 0), make([]string, 0)
	for _, entry := range evaluation.FailedTasks {
		failed = append(failed, entry.Title+": "+entry.Reason)
	}
	for _, entry := range evaluation.BlockedTasks {
		blocked = append(blocked, entry.Title+": "+entry.Reason)
	}
	for _, entry := range evaluation.UnmetCriteria {
		unmet = append(unmet, entry.Criterion+": "+entry.Reason)
	}
	plan, err := s.Planner.GenerateContext(ctx, planner.GoalInput{ID: item.ID, Title: item.Title, Description: item.Description, SuccessCriteria: item.SuccessCriteria, Criteria: item.Criteria, Constraints: item.Constraints, UnmetCriteria: unmet, FailedTasks: failed, BlockedTasks: blocked, ExistingTasks: existingTasks})
	if err != nil {
		return planner.PlanProposal{}, fmt.Errorf("generate replan proposal: %w", err)
	}
	if plan.GoalID != item.ID {
		return planner.PlanProposal{}, errors.New("Planner returned a proposal for a different Goal")
	}
	proposedKeys := make(map[string]bool, len(plan.Tasks))
	for _, spec := range plan.Tasks {
		proposedKeys[spec.Key] = true
	}
	for _, currentTask := range existingTasks {
		if !proposedKeys[currentTask.Key] {
			spec := planSpecByKey[currentTask.Key]
			plan.Tasks = append(plan.Tasks, spec)
			proposedKeys[currentTask.Key] = true
		}
	}
	if err := plan.Validate(); err != nil {
		return planner.PlanProposal{}, fmt.Errorf("validate merged replan DAG: %w", err)
	}
	if s.Usage != nil {
		if plan.ModelCalls < 0 || plan.ModelTokens < 0 || (s.Budgets.MaxModelCalls > 0 && usage.ModelCalls+plan.ModelCalls > s.Budgets.MaxModelCalls) || (s.Budgets.MaxModelTokens > 0 && usage.ModelTokens+plan.ModelTokens > s.Budgets.MaxModelTokens) {
			return planner.PlanProposal{}, errors.New("replan proposal exceeds Goal model budget")
		}
		if plan.ModelCalls > 0 || plan.ModelTokens > 0 {
			if err := s.Usage.RecordModelUsage(ctx, goalID, plan.ModelCalls, 0, plan.ModelInputTokens, plan.ModelOutputTokens); err != nil {
				return planner.PlanProposal{}, fmt.Errorf("persist replan model usage: %w", err)
			}
		}
	}
	retained := make([]string, 0)
	for _, current := range tasks {
		if current.Status == task.Completed || current.Status == task.Running || current.Status == task.WaitingApproval || current.Status == task.WaitingExternal {
			retained = append(retained, current.ID)
		}
	}
	proposal := planner.PlanProposal{GoalID: goalID, BasePlanVersion: baseVersion, ProposedVersion: baseVersion + 1, Trigger: string(evaluation.RecommendedAction), Reason: evaluation.Reason, EvaluationID: evaluation.ID, RetainedTaskIDs: retained, Plan: plan, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(30 * time.Minute)}
	if err := proposal.Validate(item.Criteria, len(tasks), 200); err != nil {
		return planner.PlanProposal{}, fmt.Errorf("validate replan proposal: %w", err)
	}
	proposal.Fingerprint, err = proposal.ComputeFingerprint()
	if err != nil {
		return planner.PlanProposal{}, err
	}
	return s.Plans.CreateProposal(ctx, proposal)
}

func (s *Supervisor) AcceptPlanProposal(ctx context.Context, goalID, proposalID string) (planner.PlanVersion, error) {
	if s.Goals == nil || s.Plans == nil {
		return planner.PlanVersion{}, planner.ErrProposalNotFound
	}
	if _, err := s.Goals.Get(ctx, goalOwnerID(), goalID); err != nil {
		if errors.Is(err, paierrors.ErrNotFound) {
			return planner.PlanVersion{}, paierrors.ErrNotFound
		}
		return planner.PlanVersion{}, err
	}
	return s.Plans.AcceptProposal(ctx, goalID, proposalID, 200)
}

func (s *Supervisor) RejectPlanProposal(ctx context.Context, goalID, proposalID, reason string) (planner.PlanProposal, error) {
	if s.Goals == nil || s.Plans == nil {
		return planner.PlanProposal{}, planner.ErrProposalNotFound
	}
	if _, err := s.Goals.Get(ctx, goalOwnerID(), goalID); err != nil {
		if errors.Is(err, paierrors.ErrNotFound) {
			return planner.PlanProposal{}, paierrors.ErrNotFound
		}
		return planner.PlanProposal{}, err
	}
	return s.Plans.RejectProposal(ctx, goalID, proposalID, reason)
}

func decisionForEvaluation(e Evaluation) (DecisionType, string) {
	switch e.RecommendedAction {
	case ActionContinue:
		return DecisionContinueExecution, "recorded"
	case ActionReplan:
		return DecisionReplan, "recorded"
	case ActionWaitApproval:
		return DecisionWaitApproval, "waiting"
	case ActionWaitCondition:
		return DecisionWaitCondition, "waiting"
	case ActionVerifyCompletion:
		return DecisionVerifyCompletion, "recorded"
	case ActionStop:
		if e.Status == EvaluationBlocked && (e.BudgetSummary.ModelCallsLimit > 0 && e.BudgetSummary.ModelCallsUsed >= e.BudgetSummary.ModelCallsLimit || e.BudgetSummary.ModelTokensLimit > 0 && e.BudgetSummary.ModelTokensUsed >= e.BudgetSummary.ModelTokensLimit || e.BudgetSummary.ToolCallsLimit > 0 && e.BudgetSummary.ToolCallsUsed >= e.BudgetSummary.ToolCallsLimit) {
			return DecisionBudgetExhausted, "completed"
		}
		return DecisionStop, "completed"
	default:
		return DecisionEscalate, "recorded"
	}
}

func NewDecision(e Evaluation) (Decision, error) {
	decisionType, status := decisionForEvaluation(e)
	type criterionFingerprint struct {
		CriterionID      string
		CriterionVersion int
		Status           string
		Evidence         []string
		EvidenceHashes   []string
		VerifierType     string
		VerifierVersion  string
		Reason           string
	}
	completed := make([]criterionFingerprint, 0, len(e.CompletedCriteria))
	for _, item := range e.CompletedCriteria {
		completed = append(completed, criterionFingerprint{item.CriterionID, item.CriterionVersion, item.Status, item.Evidence, item.EvidenceHashes, item.VerifierType, item.VerifierVersion, item.Reason})
	}
	unmet := make([]criterionFingerprint, 0, len(e.UnmetCriteria))
	for _, item := range e.UnmetCriteria {
		unmet = append(unmet, criterionFingerprint{item.CriterionID, item.CriterionVersion, item.Status, item.Evidence, item.EvidenceHashes, item.VerifierType, item.VerifierVersion, item.Reason})
	}
	fingerprintInput := struct {
		GoalID        string
		GoalUpdatedAt time.Time
		Status        EvaluationStatus
		Completed     []criterionFingerprint
		Unmet         []criterionFingerprint
		Blocked       []TaskIssue
		Tasks         []TaskIssue
		Failed        []TaskIssue
		Approvals     []TaskIssue
		External      []TaskIssue
		Dependencies  []TaskIssue
		Budget        EvaluationBudgetSummary
		Action        RecommendedAction
	}{e.GoalID, e.GoalUpdatedAt.UTC(), e.Status, completed, unmet, e.BlockedTasks, e.TaskSummary, e.FailedTasks, e.PendingApprovals, e.PendingExternalConditions, e.PendingDependencies, e.BudgetSummary, e.RecommendedAction}
	encoded, err := json.Marshal(fingerprintInput)
	if err != nil {
		return Decision{}, fmt.Errorf("encode decision fingerprint: %w", err)
	}
	hash := sha256.Sum256(encoded)
	return Decision{GoalID: e.GoalID, GoalUpdatedAt: e.GoalUpdatedAt.UTC(), PlanVersion: e.PlanVersion, PlanVersionKnown: e.PlanVersionKnown, EvaluationID: e.ID, Type: decisionType, Status: status, Reason: e.Reason, ProgressFingerprint: hex.EncodeToString(hash[:]), CreatedAt: e.CreatedAt.UTC()}, nil
}
