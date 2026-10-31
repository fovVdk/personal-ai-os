// English: These tests exercise API validation, authentication, and Goal/Task/Approval/Supervisor flows.
// 中文：本测试覆盖 API 输入校验、认证及 Goal/Task/Approval/Supervisor 流程。
// 中文：本测试覆盖 API 参数校验、认证以及 Goal/Task/Approval/Supervisor 流程。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentregistry "personal-ai-os/internal/agent/registry"
	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/approval"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/planner"
	"personal-ai-os/internal/supervisor"
	"personal-ai-os/internal/task"
	paierrors "personal-ai-os/pkg/errors"
)

type memoryGoals struct{ items map[string]goal.Goal }

func (m *memoryGoals) Create(_ context.Context, item goal.Goal) (goal.Goal, error) {
	item.ID = "goal-test"
	item.CreatedAt = time.Now().UTC()
	item.UpdatedAt = item.CreatedAt
	m.items[item.ID] = item
	return item, nil
}
func (m *memoryGoals) Get(_ context.Context, owner, id string) (goal.Goal, error) {
	item, ok := m.items[id]
	if !ok || item.OwnerID != owner {
		return goal.Goal{}, paierrors.ErrNotFound
	}
	return item, nil
}
func (m *memoryGoals) List(_ context.Context, owner string, _ int) ([]goal.Goal, error) {
	items := make([]goal.Goal, 0)
	for _, item := range m.items {
		if item.OwnerID == owner {
			items = append(items, item)
		}
	}
	return items, nil
}
func (m *memoryGoals) Update(_ context.Context, owner string, item goal.Goal) (goal.Goal, error) {
	if _, ok := m.items[item.ID]; !ok {
		return goal.Goal{}, paierrors.ErrNotFound
	}
	item.OwnerID = owner
	m.items[item.ID] = item
	return item, nil
}
func (m *memoryGoals) Transition(_ context.Context, owner, id string, next goal.Status, evidence []goal.Evidence) (goal.Goal, error) {
	item, ok := m.items[id]
	if !ok || item.OwnerID != owner {
		return goal.Goal{}, paierrors.ErrNotFound
	}
	if err := item.Transition(next, evidence, time.Now()); err != nil {
		return goal.Goal{}, err
	}
	m.items[id] = item
	return item, nil
}

type memoryTasks struct{ items map[string]task.Task }

func (m *memoryTasks) Create(_ context.Context, item task.Task) (task.Task, error) {
	item.ID = fmt.Sprintf("task-%d", len(m.items)+1)
	item.CreatedAt = time.Now().UTC()
	item.UpdatedAt = item.CreatedAt
	m.items[item.ID] = item
	return item, nil
}
func (m *memoryTasks) CreateMany(ctx context.Context, items []task.Task) ([]task.Task, error) {
	created := make([]task.Task, len(items))
	for i, item := range items {
		var err error
		created[i], err = m.Create(ctx, item)
		if err != nil {
			return nil, err
		}
	}
	ids := make(map[string]string, len(created))
	for _, item := range created {
		if item.PlanKey != "" {
			ids[item.PlanKey] = item.ID
		}
	}
	for i := range created {
		for j, dependency := range created[i].DependsOn {
			if id, ok := ids[dependency]; ok {
				created[i].DependsOn[j] = id
			}
		}
	}
	for _, item := range created {
		m.items[item.ID] = item
	}
	return created, nil
}
func (m *memoryTasks) Get(_ context.Context, id string) (task.Task, error) {
	item, ok := m.items[id]
	if !ok {
		return task.Task{}, paierrors.ErrNotFound
	}
	return item, nil
}
func (m *memoryTasks) ListByGoal(_ context.Context, goalID string, _ int) ([]task.Task, error) {
	items := make([]task.Task, 0)
	for _, item := range m.items {
		if item.GoalID == goalID {
			items = append(items, item)
		}
	}
	return items, nil
}
func (m *memoryTasks) Transition(_ context.Context, id string, next task.Status) (task.Task, error) {
	item, ok := m.items[id]
	if !ok {
		return task.Task{}, paierrors.ErrNotFound
	}
	if err := item.Transition(next, time.Now()); err != nil {
		return task.Task{}, err
	}
	if next == task.Running {
		item.AttemptCount++
	}
	m.items[id] = item
	return item, nil
}

type memoryApprovals struct{ items map[string]approval.Request }

func (m *memoryApprovals) Create(_ context.Context, item approval.Request) (approval.Request, error) {
	item.ID = fmt.Sprintf("approval-%d", len(m.items)+1)
	m.items[item.ID] = item
	return item, nil
}

type memoryExecutions struct{ items []supervisor.Execution }

func (m *memoryExecutions) Save(_ context.Context, item supervisor.Execution) (supervisor.Execution, error) {
	item.ID = fmt.Sprintf("run-%d", len(m.items)+1)
	m.items = append(m.items, item)
	return item, nil
}
func (m *memoryExecutions) ListByGoal(_ context.Context, _ string) ([]supervisor.Execution, error) {
	return append([]supervisor.Execution(nil), m.items...), nil
}
func (m *memoryApprovals) ListPending(_ context.Context, userID string, _ int) ([]approval.Request, error) {
	items := make([]approval.Request, 0)
	for _, item := range m.items {
		if item.UserID == userID && item.Status == approval.Pending {
			items = append(items, item)
		}
	}
	return items, nil
}
func (m *memoryApprovals) Get(_ context.Context, userID, id string) (approval.Request, error) {
	item, ok := m.items[id]
	if !ok || item.UserID != userID {
		return approval.Request{}, paierrors.ErrNotFound
	}
	return item, nil
}
func (m *memoryApprovals) Decide(_ context.Context, userID, id string, status approval.Status, now time.Time) (approval.Request, error) {
	item, err := m.Get(context.Background(), userID, id)
	if err != nil {
		return item, err
	}
	if err := item.Decide(status, now); err != nil {
		return item, err
	}
	m.items[id] = item
	return item, nil
}

func TestHealthAndAPIKeyAuthentication(t *testing.T) {
	server := &Server{APIKey: "secret", Goals: &memoryGoals{items: map[string]goal.Goal{}}, Tasks: &memoryTasks{items: map[string]task.Task{}}}
	handler := server.Handler()
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status=%d", health.Code)
	}
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/goals", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
}

func TestGoalAndTaskAPIFlow(t *testing.T) {
	goals := &memoryGoals{items: map[string]goal.Goal{}}
	tasks := &memoryTasks{items: map[string]task.Task{}}
	handler := (&Server{Goals: goals, Tasks: tasks}).Handler()
	goalResponse := httptest.NewRecorder()
	handler.ServeHTTP(goalResponse, httptest.NewRequest(http.MethodPost, "/api/v1/goals", strings.NewReader(`{"title":"Build a home plan"}`)))
	if goalResponse.Code != http.StatusCreated {
		t.Fatalf("create goal status=%d body=%s", goalResponse.Code, goalResponse.Body.String())
	}
	var createdGoal goal.Goal
	if err := json.Unmarshal(goalResponse.Body.Bytes(), &createdGoal); err != nil {
		t.Fatal(err)
	}
	taskBody := `{"goal_id":"` + createdGoal.ID + `","title":"Research requirements"}`
	taskResponse := httptest.NewRecorder()
	handler.ServeHTTP(taskResponse, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(taskBody)))
	if taskResponse.Code != http.StatusCreated {
		t.Fatalf("create task status=%d body=%s", taskResponse.Code, taskResponse.Body.String())
	}
}

func TestRejectsUnknownJSONFields(t *testing.T) {
	handler := (&Server{Goals: &memoryGoals{items: map[string]goal.Goal{}}, Tasks: &memoryTasks{items: map[string]task.Task{}}}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/goals", strings.NewReader(`{"title":"x","unexpected":true}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRejectsOversizedRequestBody(t *testing.T) {
	handler := (&Server{Goals: &memoryGoals{items: map[string]goal.Goal{}}, Tasks: &memoryTasks{items: map[string]task.Task{}}}).Handler()
	body := `{"title":"` + strings.Repeat("x", (1<<20)+1) + `"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/goals", strings.NewReader(body)))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 413", response.Code)
	}
}

func TestGoalCompletionRequiresEvidence(t *testing.T) {
	goals := &memoryGoals{items: map[string]goal.Goal{}}
	handler := (&Server{Goals: goals, Tasks: &memoryTasks{items: map[string]task.Task{}}}).Handler()
	create := httptest.NewRecorder()
	handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/v1/goals", strings.NewReader(`{"title":"A goal"}`)))
	var item goal.Goal
	if err := json.Unmarshal(create.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	activate := httptest.NewRecorder()
	handler.ServeHTTP(activate, httptest.NewRequest(http.MethodPost, "/api/v1/goals/"+item.ID+"/activate", nil))
	if activate.Code != http.StatusOK {
		t.Fatalf("activate status=%d body=%s", activate.Code, activate.Body.String())
	}
	complete := httptest.NewRecorder()
	handler.ServeHTTP(complete, httptest.NewRequest(http.MethodPost, "/api/v1/goals/"+item.ID+"/complete", strings.NewReader(`{"evidence":[]}`)))
	if complete.Code != http.StatusUnprocessableEntity {
		t.Fatalf("complete without evidence status=%d", complete.Code)
	}
	complete = httptest.NewRecorder()
	handler.ServeHTTP(complete, httptest.NewRequest(http.MethodPost, "/api/v1/goals/"+item.ID+"/complete", strings.NewReader(`{"evidence":[{"source":"test","description":"verified"}]}`)))
	if complete.Code != http.StatusOK {
		t.Fatalf("complete with evidence status=%d body=%s", complete.Code, complete.Body.String())
	}
}

func TestPlannerCreatesStructuredTasks(t *testing.T) {
	goals := &memoryGoals{items: map[string]goal.Goal{}}
	tasks := &memoryTasks{items: map[string]task.Task{}}
	handler := (&Server{Goals: goals, Tasks: tasks, Planner: planner.MockGenerator{}}).Handler()
	create := httptest.NewRecorder()
	handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/v1/goals", strings.NewReader(`{"title":"Build a home automation system"}`)))
	var goalItem goal.Goal
	if err := json.Unmarshal(create.Body.Bytes(), &goalItem); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/planner/generate", strings.NewReader(`{"goal_id":"`+goalItem.ID+`"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("planner status=%d body=%s", response.Code, response.Body.String())
	}
	if len(tasks.items) < 2 {
		t.Fatalf("expected generated tasks, got %d", len(tasks.items))
	}
}

func TestApprovalAPIHashesExactParametersAndDecides(t *testing.T) {
	goals := &memoryGoals{items: map[string]goal.Goal{"goal-1": {ID: "goal-1", OwnerID: defaultOwnerID, Title: "Goal", Priority: 3, Status: goal.Active}}}
	tasks := &memoryTasks{items: map[string]task.Task{"task-1": {ID: "task-1", GoalID: "goal-1", Title: "Review", Priority: 3, Status: task.Created}}}
	approvals := &memoryApprovals{items: map[string]approval.Request{}}
	handler := (&Server{Goals: goals, Tasks: tasks, Approvals: approvals}).Handler()
	response := httptest.NewRecorder()
	body := `{"task_id":"task-1","tool_id":"door","action":"unlock","parameters":{"room":"front"},"risk_level":3}`
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/approvals", strings.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create approval status=%d body=%s", response.Code, response.Body.String())
	}
	var created approval.Request
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ParametersHash == "" || created.Status != approval.Pending {
		t.Fatalf("unexpected approval: %+v", created)
	}
	wantHash, err := approval.HashParameters(map[string]any{"room": "front"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ParametersHash != wantHash {
		t.Fatal("approval hash does not match exact request parameters")
	}
	decide := httptest.NewRecorder()
	handler.ServeHTTP(decide, httptest.NewRequest(http.MethodPost, "/api/v1/approvals/"+created.ID+"/approve", nil))
	if decide.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", decide.Code, decide.Body.String())
	}
	updated, err := approvals.Get(context.Background(), defaultOwnerID, created.ID)
	if err != nil || updated.Status != approval.Approved {
		t.Fatalf("approval status=%s err=%v", updated.Status, err)
	}
}

func TestSupervisorTurnExecutesMockPlanAndReturnsEvidence(t *testing.T) {
	goals := &memoryGoals{items: map[string]goal.Goal{"goal-1": {ID: "goal-1", OwnerID: defaultOwnerID, Title: "Home automation", Priority: 3, Status: goal.Draft}}}
	tasks := &memoryTasks{items: map[string]task.Task{}}
	executions := &memoryExecutions{}
	agents := agentregistry.New()
	for _, id := range []string{"planning", "research"} {
		if err := agents.Register(agentregistry.Profile{ID: id, Name: id, Version: "mock-v1", Capabilities: []string{"planning"}, Agent: agentruntime.MockAgent{AgentID: id}}); err != nil {
			t.Fatal(err)
		}
	}
	orchestrator := &supervisor.Supervisor{Goals: goals, Tasks: tasks, Planner: planner.MockGenerator{}, Agents: agents, Executions: executions,
		Budgets: supervisor.Budgets{MaxSteps: 10, MaxModelCalls: 8, MaxToolCalls: 20, MaxModelTokens: 64000, MaxRetries: 2, MaxRuntime: time.Second}}
	handler := (&Server{Goals: goals, Tasks: tasks, Planner: planner.MockGenerator{}, Supervisor: orchestrator}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/supervisor/turn", strings.NewReader(`{"goal_id":"goal-1"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("turn status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "unverified_result" {
		t.Fatalf("unexpected supervisor error: %+v", envelope.Error)
	}
}
