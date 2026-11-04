// English: These handlers expose bounded Supervisor turns and runtime readiness metadata.
// 中文：这些处理器提供有界 Supervisor 执行轮次及运行状态信息。
// 中文：这些处理器公开受预算限制的 Supervisor 轮次执行及运行状态信息。
package api

import (
	"context"
	"errors"
	"net/http"

	"personal-ai-os/internal/supervisor"
	paierrors "personal-ai-os/pkg/errors"
)

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
