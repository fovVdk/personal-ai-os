// English: This handler validates structured plans and atomically persists the generated task set.
// 中文：此处理器验证结构化计划，并原子化持久化生成的任务集合。
// 中文：本处理器验证结构化计划，并以事务方式持久化生成的任务集合。
package api

import (
	"errors"
	"net/http"

	"personal-ai-os/internal/planner"
	"personal-ai-os/internal/task"
	paierrors "personal-ai-os/pkg/errors"
)

type generatePlanRequest struct {
	GoalID string `json:"goal_id"`
}

type generatePlanResponse struct {
	Plan  planner.Plan `json:"plan"`
	Tasks []task.Task  `json:"tasks"`
}

func (s *Server) generatePlan(w http.ResponseWriter, r *http.Request) {
	if s.Planner == nil {
		writeError(w, r, http.StatusServiceUnavailable, "planner_unavailable", "planner is not configured")
		return
	}
	var input generatePlanRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	goalItem, err := s.Goals.Get(r.Context(), defaultOwnerID, input.GoalID)
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_not_found", "goal not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "get goal for planning", err)
		return
	}
	plan, err := s.Planner.GenerateContext(r.Context(), planner.GoalInput{
		ID: goalItem.ID, Title: goalItem.Title, Description: goalItem.Description,
		SuccessCriteria: goalItem.SuccessCriteria, Constraints: goalItem.Constraints,
	})
	if err != nil {
		s.internalError(w, r, "generate plan", err)
		return
	}
	if err := plan.Validate(); err != nil || plan.GoalID != goalItem.ID {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_plan", "planner returned an invalid structured plan")
		return
	}
	items := make([]task.Task, len(plan.Tasks))
	for i, spec := range plan.Tasks {
		items[i] = task.Task{
			GoalID: goalItem.ID, PlanKey: spec.Key, Title: spec.Title, Description: spec.Description,
			Priority: 3, Status: task.Created, SuccessCriteria: spec.SuccessCriteria, DependsOn: spec.DependsOn,
		}
		if err := items[i].Validate(); err != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_plan", "planner returned an invalid task")
			return
		}
	}
	created, err := s.Tasks.CreateMany(r.Context(), items)
	if err != nil {
		s.internalError(w, r, "persist plan tasks", err)
		return
	}
	writeJSON(w, http.StatusCreated, generatePlanResponse{Plan: plan, Tasks: created})
}
