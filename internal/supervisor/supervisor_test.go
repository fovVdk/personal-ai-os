// English: These tests cover structured planning, unverified output rejection, and crash-window recovery.
// 中文：本测试覆盖结构化规划、未验证输出拒绝及崩溃提交窗口恢复。
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	agentregistry "personal-ai-os/internal/agent/registry"
	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/planner"
	"personal-ai-os/internal/task"
	paierrors "personal-ai-os/pkg/errors"
)

type goalsRepo struct{ item goal.Goal }

func (r *goalsRepo) Create(_ context.Context, item goal.Goal) (goal.Goal, error) { return item, nil }
func (r *goalsRepo) Get(_ context.Context, owner, id string) (goal.Goal, error) {
	if r.item.ID != id {
		return goal.Goal{}, paierrors.ErrNotFound
	}
	return r.item, nil
}
func (r *goalsRepo) List(context.Context, string, int) ([]goal.Goal, error) {
	return []goal.Goal{r.item}, nil
}
func (r *goalsRepo) Update(_ context.Context, _ string, item goal.Goal) (goal.Goal, error) {
	r.item = item
	return item, nil
}
func (r *goalsRepo) Transition(_ context.Context, _ string, id string, next goal.Status, evidence []goal.Evidence) (goal.Goal, error) {
	if err := r.item.Transition(next, evidence, time.Now()); err != nil {
		return goal.Goal{}, err
	}
	return r.item, nil
}

type tasksRepo struct {
	items []task.Task
	next  int
}

func (r *tasksRepo) Create(_ context.Context, item task.Task) (task.Task, error) {
	r.next++
	item.ID = fmt.Sprintf("task-%d", r.next)
	r.items = append(r.items, item)
	return item, nil
}
func (r *tasksRepo) CreateMany(ctx context.Context, items []task.Task) ([]task.Task, error) {
	result := make([]task.Task, len(items))
	for i, item := range items {
		created, err := r.Create(ctx, item)
		if err != nil {
			return nil, err
		}
		result[i] = created
	}
	for i := range result {
		for j, key := range result[i].DependsOn {
			for _, candidate := range result {
				if candidate.PlanKey == key {
					result[i].DependsOn[j] = candidate.ID
				}
			}
		}
	}
	r.items = result
	return result, nil
}
func (r *tasksRepo) Get(_ context.Context, id string) (task.Task, error) {
	for _, item := range r.items {
		if item.ID == id {
			return item, nil
		}
	}
	return task.Task{}, paierrors.ErrNotFound
}
func (r *tasksRepo) ListByGoal(_ context.Context, id string, _ int) ([]task.Task, error) {
	result := make([]task.Task, 0)
	for _, item := range r.items {
		if item.GoalID == id {
			result = append(result, item)
		}
	}
	return result, nil
}
func (r *tasksRepo) Transition(_ context.Context, id string, next task.Status) (task.Task, error) {
	for i, item := range r.items {
		if item.ID == id {
			if err := item.Transition(next, time.Now()); err != nil {
				return task.Task{}, err
			}
			if next == task.Running {
				item.AttemptCount++
			}
			r.items[i] = item
			return item, nil
		}
	}
	return task.Task{}, paierrors.ErrNotFound
}

type executionRepo struct{ items []Execution }

func (r *executionRepo) Save(_ context.Context, item Execution) (Execution, error) {
	if item.Outcome != agentruntime.OutcomeSucceeded {
		return Execution{}, errors.New("test execution requires verified success")
	}
	item.ID = fmt.Sprintf("run-%d", len(r.items)+1)
	r.items = append(r.items, item)
	return item, nil
}
func (r *executionRepo) ListByGoal(context.Context, string) ([]Execution, error) { return r.items, nil }

type recoverableTasks struct{ items []task.Task }

func (r recoverableTasks) Recoverable(context.Context, int) ([]task.Task, error) {
	return append([]task.Task(nil), r.items...), nil
}

func TestTurnPlansAndRejectsUnverifiedMockResult(t *testing.T) {
	goalRepo := &goalsRepo{item: goal.Goal{ID: "goal-1", OwnerID: "00000000-0000-4000-8000-000000000001", Title: "Home automation", Priority: 3, Status: goal.Draft}}
	taskRepo := &tasksRepo{}
	executions := &executionRepo{}
	agents := agentregistry.New()
	if err := agents.Register(agentregistry.Profile{ID: "planning", Name: "Planning", Version: "mock-v1", Capabilities: []string{"plan"}, Agent: agentruntime.MockAgent{AgentID: "planning"}}); err != nil {
		t.Fatal(err)
	}
	if err := agents.Register(agentregistry.Profile{ID: "research", Name: "Research", Version: "mock-v1", Capabilities: []string{"research"}, Agent: agentruntime.MockAgent{AgentID: "research"}}); err != nil {
		t.Fatal(err)
	}
	orchestrator := Supervisor{Goals: goalRepo, Tasks: taskRepo, Planner: planner.MockGenerator{}, Agents: agents, Executions: executions, Budgets: Budgets{MaxSteps: 10, MaxModelCalls: 5, MaxToolCalls: 5, MaxModelTokens: 64000, MaxRetries: 1, MaxRuntime: time.Second}}
	result, err := orchestrator.Turn(context.Background(), TurnRequest{GoalID: "goal-1"})
	if err == nil {
		t.Fatal("mock output is unverified and must not complete tasks")
	}
	if goalRepo.item.Status != goal.Active || goalRepo.item.Progress == 100 {
		t.Fatalf("unverified mock output completed goal: %+v", goalRepo.item)
	}
	if len(taskRepo.items) != 5 || len(executions.items) != 0 {
		t.Fatalf("tasks=%d stored runs=%d", len(taskRepo.items), len(executions.items))
	}
	for _, item := range result.Tasks {
		if item.Status != task.Failed {
			t.Fatalf("task %s status=%s", item.ID, item.Status)
		}
	}
}

func TestTurnRecoversEvidencePersistedBeforeTaskStatusCommit(t *testing.T) {
	goalRepo := &goalsRepo{item: goal.Goal{ID: "goal-recovery", OwnerID: localOwner, Title: "Recovery", Priority: 3, Status: goal.Active}}
	startedAt := time.Now().UTC()
	storedTask := task.Task{ID: "task-recovery", GoalID: "goal-recovery", Title: "Already executed", Priority: 3, Status: task.Running, AttemptCount: 1, StartedAt: &startedAt}
	taskRepo := &tasksRepo{items: []task.Task{storedTask}}
	executions := &executionRepo{items: []Execution{{ID: "run-1", TaskID: storedTask.ID, AgentID: "planning", Summary: "done", Outcome: agentruntime.OutcomeSucceeded, Verified: true, VerificationMethod: "test-verifier/v1", Evidence: []Evidence{{TaskID: storedTask.ID, Source: "independent-verifier", Description: "verified"}}, CompletedAt: time.Now().UTC()}}}
	agents := agentregistry.New()
	if err := agents.Register(agentregistry.Profile{ID: "planning", Name: "Planning", Version: "mock-v1", Agent: agentruntime.MockAgent{AgentID: "planning"}}); err != nil {
		t.Fatal(err)
	}
	orchestrator := Supervisor{Goals: goalRepo, Tasks: taskRepo, Planner: planner.MockGenerator{}, Agents: agents, Executions: executions, Recovery: recoverableTasks{items: []task.Task{storedTask}}, Budgets: Budgets{MaxSteps: 2, MaxModelCalls: 1, MaxModelTokens: 64000, MaxRetries: 1, MaxRuntime: time.Second}}
	result, err := orchestrator.Turn(context.Background(), TurnRequest{GoalID: goalRepo.item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Goal.Status == goal.Completed || result.Status != "awaiting_verification" || taskRepo.items[0].Status != task.Completed || len(executions.items) != 1 {
		t.Fatalf("recovery goal=%s task=%s runs=%d", result.Goal.Status, taskRepo.items[0].Status, len(executions.items))
	}
}
