// Package supervisor coordinates durable task execution without delegating control to models.
// 中文：supervisor 包协调持久化任务执行，但不将系统控制权交给模型。
// English: This coordinator applies per-Goal budgets, schedules dependencies, persists evidence, and reconciles interrupted tasks.
// 中文：本协调器按 Goal 执行预算控制、依赖调度、证据持久化及中断任务恢复。
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	agentregistry "personal-ai-os/internal/agent/registry"
	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/planner"
	"personal-ai-os/internal/task"
	paierrors "personal-ai-os/pkg/errors"
)

var ErrNoReadyTasks = errors.New("goal has no ready tasks")
var ErrUnverifiedResult = errors.New("agent result is unverified and cannot complete the task")

type Evidence struct {
	TaskID      string    `json:"task_id"`
	Source      string    `json:"source"`
	Description string    `json:"description"`
	Reference   string    `json:"reference,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}

type Execution struct {
	ID           string               `json:"id,omitempty"`
	TaskID       string               `json:"task_id"`
	AgentID      string               `json:"agent_id"`
	Summary      string               `json:"summary"`
	Outcome      agentruntime.Outcome `json:"outcome"`
	InputTokens  int                  `json:"input_tokens"`
	OutputTokens int                  `json:"output_tokens"`
	TotalTokens  int                  `json:"total_tokens"`
	ModelCalls   int                  `json:"model_calls"`
	Evidence     []Evidence           `json:"evidence"`
	CompletedAt  time.Time            `json:"completed_at"`
}

type ExecutionStore interface {
	Save(context.Context, Execution) (Execution, error)
	ListByGoal(context.Context, string) ([]Execution, error)
}

type UsageStore interface {
	ModelUsage(context.Context, string) (UsageTotals, error)
	RecordModelUsage(context.Context, string, int, int, int) error
}

type UsageTotals struct {
	ModelCalls  int
	ModelTokens int
}

type StatusStore interface {
	Recoverable(context.Context, int) ([]task.Task, error)
}

type Budgets struct {
	MaxSteps       int
	MaxModelCalls  int
	MaxToolCalls   int
	MaxModelTokens int
	MaxCost        float64
	MaxRetries     int
	MaxRuntime     time.Duration
}

type Supervisor struct {
	Goals            goal.Repository
	Tasks            task.Repository
	Planner          planner.Generator
	Agents           *agentregistry.Registry
	Executions       ExecutionStore
	Usage            UsageStore
	Recovery         StatusStore
	Budgets          Budgets
	MaxExecutionCost float64
	Now              func() time.Time
}

type TurnRequest struct {
	GoalID string `json:"goal_id"`
}
type TurnResult struct {
	Goal       goal.Goal    `json:"goal"`
	Plan       planner.Plan `json:"plan"`
	Executions []Execution  `json:"executions"`
	Tasks      []task.Task  `json:"tasks"`
	Status     string       `json:"status"`
}

func (s *Supervisor) Turn(ctx context.Context, request TurnRequest) (TurnResult, error) {
	if s.Goals == nil || s.Tasks == nil || s.Planner == nil || s.Agents == nil || s.Executions == nil {
		return TurnResult{}, errors.New("supervisor dependencies are incomplete")
	}
	if request.GoalID == "" {
		return TurnResult{}, errors.New("goal_id is required")
	}
	budgets := s.Budgets
	if budgets.MaxSteps < 1 || budgets.MaxRuntime <= 0 || budgets.MaxModelCalls < 0 || budgets.MaxToolCalls < 0 || budgets.MaxRetries < 0 || budgets.MaxCost < 0 || math.IsNaN(budgets.MaxCost) || math.IsInf(budgets.MaxCost, 0) {
		return TurnResult{}, errors.New("supervisor budgets are invalid")
	}
	runCtx, cancel := context.WithTimeout(ctx, budgets.MaxRuntime)
	defer cancel()
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	goalItem, err := s.Goals.Get(runCtx, goalOwnerID(), request.GoalID)
	if errors.Is(err, paierrors.ErrNotFound) {
		return TurnResult{}, paierrors.ErrNotFound
	}
	if err != nil {
		return TurnResult{}, fmt.Errorf("load goal: %w", err)
	}
	savedExecutions, err := s.Executions.ListByGoal(runCtx, goalItem.ID)
	if err != nil {
		return TurnResult{}, fmt.Errorf("load completed execution evidence: %w", err)
	}
	succeeded := make(map[string]struct{}, len(savedExecutions))
	usedUsage := UsageTotals{}
	if s.Usage != nil {
		usedUsage, err = s.Usage.ModelUsage(runCtx, goalItem.ID)
		if err != nil {
			return TurnResult{}, fmt.Errorf("load goal model token usage: %w", err)
		}
	} else {
		for _, execution := range savedExecutions {
			usedUsage.ModelTokens += execution.TotalTokens
			usedUsage.ModelCalls += execution.ModelCalls
		}
	}
	for _, execution := range savedExecutions {
		if execution.Outcome == agentruntime.OutcomeSucceeded && len(execution.Evidence) > 0 {
			succeeded[execution.TaskID] = struct{}{}
		}
	}
	if s.Recovery != nil {
		recoverable, err := s.Recovery.Recoverable(runCtx, 100)
		if err != nil {
			return TurnResult{}, fmt.Errorf("load recoverable tasks: %w", err)
		}
		for _, stale := range recoverable {
			if stale.GoalID != goalItem.ID || (stale.Status != task.Running && stale.Status != task.Retrying) {
				continue
			}
			next := task.Failed
			if _, ok := succeeded[stale.ID]; ok && stale.Status == task.Running {
				next = task.Completed
			}
			updated, err := s.Tasks.Transition(runCtx, stale.ID, next)
			if err != nil {
				return TurnResult{}, fmt.Errorf("recover interrupted task: %w", err)
			}
			for i := range recoverable {
				if recoverable[i].ID == updated.ID {
					recoverable[i] = updated
					break
				}
			}
		}
	}
	if goalItem.Status == goal.Draft {
		goalItem, err = s.Goals.Transition(runCtx, goalOwnerID(), goalItem.ID, goal.Active, nil)
		if err != nil {
			return TurnResult{}, fmt.Errorf("activate goal: %w", err)
		}
	}
	if goalItem.Status == goal.Paused {
		return TurnResult{}, errors.New("paused goal must be resumed before supervision")
	}
	if goalItem.Status == goal.Completed || goalItem.Status == goal.Cancelled || goalItem.Status == goal.Archived {
		return TurnResult{}, fmt.Errorf("goal status %q cannot be supervised", goalItem.Status)
	}
	if goalItem.Status == goal.Monitoring {
		goalItem, err = s.Goals.Transition(runCtx, goalOwnerID(), goalItem.ID, goal.Active, nil)
		if err != nil {
			return TurnResult{}, fmt.Errorf("resume monitoring goal: %w", err)
		}
	}
	tasks, err := s.Tasks.ListByGoal(runCtx, goalItem.ID, 200)
	if err != nil {
		return TurnResult{}, fmt.Errorf("load goal tasks: %w", err)
	}
	plan := planner.Plan{GoalID: goalItem.ID}
	plannerModelCalls, plannerModelTokens := 0, 0
	if len(tasks) == 0 {
		if usedUsage.ModelCalls >= budgets.MaxModelCalls || usedUsage.ModelTokens >= budgets.MaxModelTokens {
			return TurnResult{}, errors.New("supervisor model budget exhausted before planning")
		}
		plan, err = s.Planner.GenerateContext(runCtx, planner.GoalInput{ID: goalItem.ID, Title: goalItem.Title, Description: goalItem.Description, SuccessCriteria: goalItem.SuccessCriteria, Constraints: goalItem.Constraints})
		if err != nil {
			return TurnResult{}, fmt.Errorf("generate plan: %w", err)
		}
		if err := plan.Validate(); err != nil || plan.GoalID != goalItem.ID {
			return TurnResult{}, errors.New("planner returned an invalid plan")
		}
		plannerModelCalls, plannerModelTokens = plan.ModelCalls, plan.ModelTokens
		if plannerModelCalls < 0 || plannerModelTokens < 0 || usedUsage.ModelCalls+plannerModelCalls > budgets.MaxModelCalls || usedUsage.ModelTokens+plannerModelTokens > budgets.MaxModelTokens {
			return TurnResult{}, errors.New("planner exceeded model call/token budget")
		}
		if s.Usage != nil && (plannerModelCalls > 0 || plannerModelTokens > 0) {
			if err := s.Usage.RecordModelUsage(runCtx, goalItem.ID, plannerModelCalls, plan.ModelInputTokens, plan.ModelOutputTokens); err != nil {
				return TurnResult{}, fmt.Errorf("persist planner token usage: %w", err)
			}
		}
		tasks = make([]task.Task, len(plan.Tasks))
		for i, spec := range plan.Tasks {
			tasks[i] = task.Task{GoalID: goalItem.ID, PlanKey: spec.Key, Title: spec.Title, Description: spec.Description, Priority: 3, Status: task.Created, SuccessCriteria: spec.SuccessCriteria, DependsOn: spec.DependsOn}
		}
		tasks, err = s.Tasks.CreateMany(runCtx, tasks)
		if err != nil {
			return TurnResult{}, fmt.Errorf("persist plan: %w", err)
		}
	}
	if plan.GoalID == "" {
		plan.Summary = "Existing goal tasks will be executed according to their persisted state."
	}
	completed := make(map[string]struct{}, len(tasks))
	for _, item := range tasks {
		if item.Status == task.Completed {
			if _, verified := succeeded[item.ID]; !verified {
				continue
			}
			completed[item.ID] = struct{}{}
		}
	}
	results := make([]Execution, 0)
	var observedCost float64
	observedModelCalls, observedToolCalls, observedTokens := plannerModelCalls, 0, plannerModelTokens
	for step := 0; step < budgets.MaxSteps; step++ {
		if err := runCtx.Err(); err != nil {
			return TurnResult{}, err
		}
		var selected *task.Task
		for i := range tasks {
			candidate := &tasks[i]
			if candidate.Status != task.Created && candidate.Status != task.Ready && candidate.Status != task.Failed && candidate.Status != task.Retrying {
				continue
			}
			if (candidate.Status == task.Failed || candidate.Status == task.Retrying) && candidate.AttemptCount > budgets.MaxRetries {
				continue
			}
			if _, hasEvidence := succeeded[candidate.ID]; hasEvidence {
				continue
			}
			ready := true
			for _, dependency := range candidate.DependsOn {
				if _, ok := completed[dependency]; !ok {
					ready = false
					break
				}
			}
			if len(candidate.DependsOn) == 0 {
				for _, predecessor := range tasks {
					if predecessor.ID != candidate.ID && predecessor.Priority > candidate.Priority && predecessor.Status != task.Completed && predecessor.Status != task.Cancelled {
						ready = false
						break
					}
				}
			}
			if ready && (selected == nil || candidate.Priority > selected.Priority) {
				selected = candidate
			}
		}
		if selected == nil {
			break
		}
		if selected.Status == task.Created {
			*selected, err = s.Tasks.Transition(runCtx, selected.ID, task.Ready)
			if err != nil {
				return TurnResult{}, fmt.Errorf("ready task: %w", err)
			}
		}
		if selected.Status == task.Failed {
			*selected, err = s.Tasks.Transition(runCtx, selected.ID, task.Retrying)
			if err != nil {
				return TurnResult{}, fmt.Errorf("schedule task retry: %w", err)
			}
		}
		*selected, err = s.Tasks.Transition(runCtx, selected.ID, task.Running)
		if err != nil {
			return TurnResult{}, fmt.Errorf("start task: %w", err)
		}
		agentID := "planning"
		if strings.Contains(strings.ToLower(selected.Title), "research") || strings.Contains(strings.ToLower(selected.Title), "inventory") {
			agentID = "research"
		}
		profile, err := s.Agents.Get(agentID)
		if err != nil {
			return TurnResult{}, fmt.Errorf("select %s agent: %w", agentID, err)
		}
		deadline := now.Add(budgets.MaxRuntime)
		if d, ok := runCtx.Deadline(); ok {
			deadline = d
		}
		remainingCost := budgets.MaxCost
		if budgets.MaxCost > 0 {
			remainingCost -= observedCost
			if remainingCost <= 0 {
				return TurnResult{}, errors.New("supervisor cost budget exceeded")
			}
		}
		remainingTokens := budgets.MaxModelTokens - usedUsage.ModelTokens - observedTokens
		if remainingTokens <= 0 {
			return TurnResult{}, errors.New("supervisor model token budget exceeded")
		}
		remainingCalls := budgets.MaxModelCalls - usedUsage.ModelCalls - observedModelCalls
		if remainingCalls < 0 {
			return TurnResult{}, errors.New("supervisor model call budget exceeded")
		}
		agentResult, runErr := profile.Agent.Execute(runCtx, agentruntime.Request{TaskID: selected.ID, GoalID: goalItem.ID, Title: selected.Title, Description: selected.Description, SuccessCriteria: selected.SuccessCriteria, MaxSteps: budgets.MaxSteps - step, MaxModelCalls: remainingCalls, MaxToolCalls: budgets.MaxToolCalls - observedToolCalls, MaxTokens: remainingTokens, MaxCost: remainingCost, Deadline: deadline})
		if runErr != nil {
			failedCtx, cancelFailure := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, transitionErr := s.Tasks.Transition(failedCtx, selected.ID, task.Failed)
			cancelFailure()
			if transitionErr != nil {
				return TurnResult{}, fmt.Errorf("task execution failed (%v) and status update failed: %w", runErr, transitionErr)
			}
			return TurnResult{}, fmt.Errorf("execute task %s: %w", selected.ID, runErr)
		}
		if agentResult.Outcome != agentruntime.OutcomeSucceeded || agentResult.Steps < 1 || agentResult.Steps > budgets.MaxSteps-step || len(agentResult.Evidence) == 0 {
			failedCtx, cancelFailure := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, transitionErr := s.Tasks.Transition(failedCtx, selected.ID, task.Failed)
			cancelFailure()
			if transitionErr != nil {
				return TurnResult{}, fmt.Errorf("invalid agent result and status update failed: %w", transitionErr)
			}
			return TurnResult{}, ErrUnverifiedResult
		}
		if agentResult.ModelCalls < 0 || agentResult.ToolCalls < 0 || observedModelCalls+agentResult.ModelCalls > remainingCalls || observedToolCalls+agentResult.ToolCalls > budgets.MaxToolCalls {
			failedCtx, cancelFailure := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, transitionErr := s.Tasks.Transition(failedCtx, selected.ID, task.Failed)
			cancelFailure()
			if transitionErr != nil {
				return TurnResult{}, fmt.Errorf("call budget exceeded and status update failed: %w", transitionErr)
			}
			return TurnResult{}, errors.New("agent result exceeded model/tool call budget")
		}
		observedModelCalls += agentResult.ModelCalls
		observedToolCalls += agentResult.ToolCalls
		if agentResult.InputTokens < 0 || agentResult.OutputTokens < 0 || agentResult.ModelTokens < agentResult.InputTokens+agentResult.OutputTokens || usedUsage.ModelTokens+observedTokens+agentResult.ModelTokens > budgets.MaxModelTokens {
			failedCtx, cancelFailure := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, transitionErr := s.Tasks.Transition(failedCtx, selected.ID, task.Failed)
			cancelFailure()
			if transitionErr != nil {
				return TurnResult{}, fmt.Errorf("model token budget/usage invalid and status update failed: %w", transitionErr)
			}
			return TurnResult{}, errors.New("agent result has invalid or over-budget model token usage")
		}
		observedTokens += agentResult.ModelTokens
		if agentResult.Cost < 0 || math.IsNaN(agentResult.Cost) || math.IsInf(agentResult.Cost, 0) || (budgets.MaxCost > 0 && observedCost+agentResult.Cost > budgets.MaxCost) {
			failedCtx, cancelFailure := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, transitionErr := s.Tasks.Transition(failedCtx, selected.ID, task.Failed)
			cancelFailure()
			if transitionErr != nil {
				return TurnResult{}, fmt.Errorf("cost budget exceeded and status update failed: %w", transitionErr)
			}
			return TurnResult{}, errors.New("agent result exceeded cost budget")
		}
		observedCost += agentResult.Cost
		completedAt := time.Now().UTC()
		if s.Now != nil {
			completedAt = s.Now().UTC()
		}
		evidence := make([]Evidence, 0, len(agentResult.Evidence))
		for _, item := range agentResult.Evidence {
			evidence = append(evidence, Evidence{TaskID: selected.ID, Source: item.Source, Description: item.Description, Reference: item.Reference, ObservedAt: completedAt})
		}
		execution, err := s.Executions.Save(runCtx, Execution{TaskID: selected.ID, AgentID: profile.ID, Summary: agentResult.Summary, Outcome: agentResult.Outcome, InputTokens: agentResult.InputTokens, OutputTokens: agentResult.OutputTokens, TotalTokens: agentResult.ModelTokens, ModelCalls: agentResult.ModelCalls, Evidence: evidence, CompletedAt: completedAt})
		if err != nil {
			return TurnResult{}, fmt.Errorf("persist execution evidence: %w", err)
		}
		*selected, err = s.Tasks.Transition(runCtx, selected.ID, task.Completed)
		if err != nil {
			return TurnResult{}, fmt.Errorf("complete task: %w", err)
		}
		completed[selected.ID] = struct{}{}
		results = append(results, execution)
	}
	if len(tasks) > 0 && len(completed) == len(tasks) {
		goalEvidence := []goal.Evidence{{Source: "task-executions", Description: fmt.Sprintf("%d goal tasks completed with persisted agent evidence", len(tasks)), Reference: goalItem.ID, ObservedAt: now}}
		goalItem, err = s.Goals.Transition(runCtx, goalOwnerID(), goalItem.ID, goal.Completed, goalEvidence)
		if err != nil {
			return TurnResult{}, fmt.Errorf("complete goal: %w", err)
		}
	} else if len(completed) > 0 && goalItem.Status == goal.Active {
		goalItem, err = s.Goals.Transition(runCtx, goalOwnerID(), goalItem.ID, goal.Monitoring, nil)
		if err != nil {
			return TurnResult{}, fmt.Errorf("set goal monitoring: %w", err)
		}
	}
	if len(results) == 0 && goalItem.Status != goal.Completed {
		for _, item := range tasks {
			if item.Status == task.Blocked || item.Status == task.WaitingApproval || item.Status == task.WaitingExternal || item.Status == task.Running || item.Status == task.Paused {
				return TurnResult{Goal: goalItem, Plan: plan, Executions: results, Tasks: tasks, Status: string(goalItem.Status)}, nil
			}
		}
		return TurnResult{}, ErrNoReadyTasks
	}
	status := string(goalItem.Status)
	return TurnResult{Goal: goalItem, Plan: plan, Executions: results, Tasks: tasks, Status: status}, nil
}

const localOwner = "00000000-0000-4000-8000-000000000001"

func goalOwnerID() string { return localOwner }
