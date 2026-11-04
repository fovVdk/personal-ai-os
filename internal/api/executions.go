// English: These handlers expose owner-scoped durable execution status and cooperative cancellation.
// 中文：这些处理器提供按本地所有者隔离的持久执行状态查询和协作式取消。
package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"personal-ai-os/internal/execution"
	paierrors "personal-ai-os/pkg/errors"
)

func (s *Server) getExecution(w http.ResponseWriter, r *http.Request) {
	if s.DurableExecutions == nil {
		writeError(w, r, http.StatusServiceUnavailable, "execution_unavailable", "durable execution runtime is not configured")
		return
	}
	run, err := s.DurableExecutions.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "execution not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "get execution", err)
		return
	}
	if _, err := s.Goals.Get(r.Context(), defaultOwnerID, run.GoalID); errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "execution not found")
		return
	} else if err != nil {
		s.internalError(w, r, "verify execution owner", err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) cancelExecution(w http.ResponseWriter, r *http.Request) {
	if s.DurableExecutions == nil {
		writeError(w, r, http.StatusServiceUnavailable, "execution_unavailable", "durable execution runtime is not configured")
		return
	}
	id := chi.URLParam(r, "id")
	run, err := s.DurableExecutions.Get(r.Context(), id)
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "execution not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "load execution for cancellation", err)
		return
	}
	if _, err := s.Goals.Get(r.Context(), defaultOwnerID, run.GoalID); errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "execution not found")
		return
	} else if err != nil {
		s.internalError(w, r, "verify execution owner", err)
		return
	}
	if err := s.DurableExecutions.RequestCancel(r.Context(), id); errors.Is(err, execution.ErrUncertainOutcome) {
		writeError(w, r, http.StatusConflict, "execution_outcome_unknown", "execution outcome is uncertain and requires reconciliation")
		return
	} else if err != nil {
		writeError(w, r, http.StatusConflict, "execution_not_cancellable", err.Error())
		return
	}
	updated, err := s.DurableExecutions.Get(r.Context(), id)
	if err != nil {
		s.internalError(w, r, "read cancellation result", err)
		return
	}
	status := http.StatusOK
	if updated.Status == execution.StatusRunning {
		status = http.StatusAccepted
	}
	writeJSON(w, status, updated)
}
