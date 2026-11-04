// English: These handlers expose bounded Supervisor turns and runtime readiness metadata.
// 中文：这些处理器提供有界 Supervisor 执行轮次及运行状态信息。
// 中文：这些处理器公开受预算限制的 Supervisor 轮次执行及运行状态信息。
package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"personal-ai-os/internal/planner"
	"personal-ai-os/internal/supervisor"
	paierrors "personal-ai-os/pkg/errors"
)

func (s *Server) evaluateGoal(w http.ResponseWriter, r *http.Request) {
	if s.Supervisor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "supervisor_unavailable", "goal evaluator is not configured")
		return
	}
	evaluation, err := s.Supervisor.EvaluateGoal(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_not_found", "goal not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "evaluate goal", err)
		return
	}
	writeJSON(w, http.StatusCreated, evaluation)
}

func (s *Server) listGoalEvaluations(w http.ResponseWriter, r *http.Request) {
	if s.Supervisor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "supervisor_unavailable", "goal evaluator is not configured")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, r, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	items, err := s.Supervisor.ListEvaluations(r.Context(), chi.URLParam(r, "id"), limit)
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_not_found", "goal not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "list goal evaluations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listGoalDecisions(w http.ResponseWriter, r *http.Request) {
	if s.Supervisor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "supervisor_unavailable", "decision history is unavailable")
		return
	}
	items, err := s.Supervisor.ListDecisions(r.Context(), chi.URLParam(r, "id"), 50)
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_not_found", "goal not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "list Supervisor decisions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getCurrentPlan(w http.ResponseWriter, r *http.Request) {
	if s.Supervisor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "supervisor_unavailable", "plan repository is unavailable")
		return
	}
	item, err := s.Supervisor.CurrentPlan(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_not_found", "goal not found")
		return
	}
	if errors.Is(err, planner.ErrPlanConflict) {
		writeError(w, r, http.StatusNotFound, "plan_not_found", "Goal has no committed Plan Version")
		return
	}
	if err != nil {
		s.internalError(w, r, "get current Plan Version", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) listPlanProposals(w http.ResponseWriter, r *http.Request) {
	if s.Supervisor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "supervisor_unavailable", "plan repository is unavailable")
		return
	}
	items, err := s.Supervisor.ListPlanProposals(r.Context(), chi.URLParam(r, "id"), 50)
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_not_found", "goal not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "list Plan Proposals", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) proposeGoalPlan(w http.ResponseWriter, r *http.Request) {
	if s.Supervisor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "supervisor_unavailable", "dynamic Planner is unavailable")
		return
	}
	proposal, err := s.Supervisor.ProposePlan(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_not_found", "goal not found")
		return
	}
	if errors.Is(err, planner.ErrPlanConflict) {
		writeError(w, r, http.StatusConflict, "plan_version_conflict", "the current Plan Version changed; refresh and replan")
		return
	}
	if err != nil {
		s.internalError(w, r, "create Plan Proposal", err)
		return
	}
	writeJSON(w, http.StatusCreated, proposal)
}

func (s *Server) acceptGoalPlanProposal(w http.ResponseWriter, r *http.Request) {
	if s.Supervisor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "supervisor_unavailable", "plan repository is unavailable")
		return
	}
	item, err := s.Supervisor.AcceptPlanProposal(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "proposalID"))
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_or_proposal_not_found", "Goal or Plan Proposal not found")
		return
	}
	if errors.Is(err, planner.ErrPlanConflict) {
		writeError(w, r, http.StatusConflict, "plan_version_conflict", "proposal base version is stale")
		return
	}
	if errors.Is(err, planner.ErrProposalExpired) {
		writeError(w, r, http.StatusConflict, "proposal_expired", "Plan Proposal has expired")
		return
	}
	if errors.Is(err, planner.ErrNoPlanProgress) {
		writeError(w, r, http.StatusConflict, "no_plan_progress", "Plan Proposal does not add executable work; Goal is preserved without a duplicate plan")
		return
	}
	if err != nil {
		s.internalError(w, r, "accept Plan Proposal", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) rejectGoalPlanProposal(w http.ResponseWriter, r *http.Request) {
	if s.Supervisor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "supervisor_unavailable", "plan repository is unavailable")
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.Supervisor.RejectPlanProposal(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "proposalID"), input.Reason)
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_or_proposal_not_found", "Goal or Plan Proposal not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "reject Plan Proposal", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) supervisorTurn(w http.ResponseWriter, r *http.Request) {
	if s.Supervisor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "supervisor_unavailable", "supervisor is not configured")
		return
	}
	var input supervisor.TurnRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := s.Supervisor.Turn(r.Context(), input)
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_not_found", "goal not found")
		return
	}
	if errors.Is(err, supervisor.ErrNoReadyTasks) {
		writeError(w, r, http.StatusConflict, "no_ready_tasks", "goal has no runnable tasks")
		return
	}
	if errors.Is(err, supervisor.ErrUnverifiedResult) {
		writeError(w, r, http.StatusConflict, "unverified_result", "agent output was not verified; task remains incomplete")
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		writeError(w, r, http.StatusGatewayTimeout, "execution_timeout", "supervisor execution budget expired")
		return
	}
	if err != nil {
		s.internalError(w, r, "supervisor turn", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) supervisorStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{"status": "ready", "execution_enabled": s.Supervisor != nil, "planner_enabled": s.Planner != nil, "registered_agents": 0}
	if s.WorkerStatus != nil {
		status["workers"] = s.WorkerStatus()
	}
	if s.Agents != nil {
		status["registered_agents"] = len(s.Agents.List())
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) createOrWakeGoalJob(w http.ResponseWriter, r *http.Request) {
	if s.GoalJobs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "goal_job_unavailable", "background Goal Runtime is not configured")
		return
	}
	goalID := chi.URLParam(r, "id")
	if _, err := s.Goals.Get(r.Context(), defaultOwnerID, goalID); errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_not_found", "Goal not found")
		return
	} else if err != nil {
		s.internalError(w, r, "load Goal for Job", err)
		return
	}
	job, err := s.GoalJobs.CreateOrWake(r.Context(), goalID, 100)
	if err != nil {
		if errors.Is(err, paierrors.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "goal_not_found", "Goal not found")
			return
		}
		s.internalError(w, r, "create or wake Goal Job", err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) listGoalJobs(w http.ResponseWriter, r *http.Request) {
	if s.GoalJobs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "goal_job_unavailable", "background Goal Runtime is not configured")
		return
	}
	goalID := chi.URLParam(r, "id")
	if _, err := s.Goals.Get(r.Context(), defaultOwnerID, goalID); errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_not_found", "Goal not found")
		return
	} else if err != nil {
		s.internalError(w, r, "verify Goal Job history owner", err)
		return
	}
	items, err := s.GoalJobs.ListByGoal(r.Context(), defaultOwnerID, goalID, 50)
	if err != nil {
		s.internalError(w, r, "list Goal Jobs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getGoalJob(w http.ResponseWriter, r *http.Request) {
	if s.GoalJobs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "goal_job_unavailable", "background Goal Runtime is not configured")
		return
	}
	item, err := s.GoalJobs.Get(r.Context(), defaultOwnerID, chi.URLParam(r, "jobID"))
	if errors.Is(err, supervisor.ErrGoalJobNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_job_not_found", "Goal Job not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "get Goal Job", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) controlGoalJob(w http.ResponseWriter, r *http.Request, action string) {
	if s.GoalJobs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "goal_job_unavailable", "background Goal Runtime is not configured")
		return
	}
	var item supervisor.GoalJob
	var err error
	switch action {
	case "pause":
		item, err = s.GoalJobs.Pause(r.Context(), defaultOwnerID, chi.URLParam(r, "jobID"))
	case "resume":
		item, err = s.GoalJobs.Resume(r.Context(), defaultOwnerID, chi.URLParam(r, "jobID"))
	case "cancel":
		item, err = s.GoalJobs.Cancel(r.Context(), defaultOwnerID, chi.URLParam(r, "jobID"))
	default:
		err = supervisor.ErrGoalJobNotRunnable
	}
	if errors.Is(err, supervisor.ErrGoalJobNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_job_not_found", "Goal Job not found")
		return
	}
	if errors.Is(err, supervisor.ErrGoalJobNotRunnable) {
		writeError(w, r, http.StatusConflict, "goal_job_state_conflict", "Goal Job cannot perform this lifecycle transition")
		return
	}
	if err != nil {
		s.internalError(w, r, "control Goal Job", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) pauseGoalJob(w http.ResponseWriter, r *http.Request) {
	s.controlGoalJob(w, r, "pause")
}
func (s *Server) resumeGoalJob(w http.ResponseWriter, r *http.Request) {
	s.controlGoalJob(w, r, "resume")
}
func (s *Server) cancelGoalJob(w http.ResponseWriter, r *http.Request) {
	s.controlGoalJob(w, r, "cancel")
}
