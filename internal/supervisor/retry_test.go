// English: These tests cover retry recovery while refusing to trust unverified Mock output.
// 中文：本测试验证失败重试与恢复，并确保 Mock 未验证结果不会被当成成功。
package supervisor

import (
	"context"
	"errors"
	"testing"
	"time"

	agentregistry "personal-ai-os/internal/agent/registry"
	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/planner"
	"personal-ai-os/internal/task"
)

type failOnceAgent struct{ calls int }

func (a *failOnceAgent) ID() string { return "planning" }
func (a *failOnceAgent) Execute(ctx context.Context, request agentruntime.Request) (agentruntime.Result, error) {
	a.calls++
	if a.calls == 1 {
		return agentruntime.Result{}, errors.New("temporary failure")
	}
	return agentruntime.MockAgent{AgentID: "planning"}.Execute(ctx, request)
}

func TestTurnRetriesFailedTaskWithinPersistentAttemptBudget(t *testing.T) {
	goalRepo := &goalsRepo{item: goal.Goal{ID: "goal-retry", OwnerID: localOwner, Title: "Retry", Priority: 3, Status: goal.Active}}
	taskRepo := &tasksRepo{items: []task.Task{{ID: "task-retry", GoalID: "goal-retry", Title: "One task", Priority: 3, Status: task.Created}}}
	executions := &executionRepo{}
	flaky := &failOnceAgent{}
	agents := agentregistry.New()
	if err := agents.Register(agentregistry.Profile{ID: "planning", Name: "Planning", Version: "mock-v1", Agent: flaky}); err != nil {
		t.Fatal(err)
	}
	orchestrator := Supervisor{Goals: goalRepo, Tasks: taskRepo, Planner: planner.MockGenerator{}, Agents: agents, Executions: executions,
		Budgets: Budgets{MaxSteps: 3, MaxModelCalls: 2, MaxToolCalls: 0, MaxModelTokens: 64000, MaxRetries: 1, MaxRuntime: time.Second}}
	if _, err := orchestrator.Turn(context.Background(), TurnRequest{GoalID: "goal-retry"}); err == nil {
		t.Fatal("first agent failure should be surfaced")
	}
	if taskRepo.items[0].Status != task.Failed || taskRepo.items[0].AttemptCount != 1 {
		t.Fatalf("failed task state=%+v", taskRepo.items[0])
	}
	result, err := orchestrator.Turn(context.Background(), TurnRequest{GoalID: "goal-retry"})
	if err == nil {
		t.Fatal("unverified mock output must not complete the task")
	}
	if result.Goal.Status == goal.Completed || taskRepo.items[0].AttemptCount != 2 || len(result.Executions) != 0 {
		t.Fatalf("retry result goal=%s task=%+v executions=%d", result.Goal.Status, taskRepo.items[0], len(result.Executions))
	}
}
