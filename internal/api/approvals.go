// English: These handlers bind human decisions to persisted, parameter-hashed approval requests.
// 中文：这些处理器将人工决策绑定到持久化且按参数哈希约束的审批请求。
package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"personal-ai-os/internal/approval"
	paierrors "personal-ai-os/pkg/errors"
)

type createApprovalRequest struct {
	TaskID           string         `json:"task_id"`
	ToolID           string         `json:"tool_id"`
	Action           string         `json:"action"`
	Parameters       map[string]any `json:"parameters"`
	RiskLevel        int            `json:"risk_level"`
	ExpiresInSeconds int            `json:"expires_in_seconds"`
}

func (s *Server) createApproval(w http.ResponseWriter, r *http.Request) {
	if s.Approvals == nil {
		writeError(w, r, http.StatusServiceUnavailable, "approval_unavailable", "approval service is not configured")
		return
	}
	var input createApprovalRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ExpiresInSeconds == 0 {
		input.ExpiresInSeconds = 900
	}
	if input.ExpiresInSeconds < 1 || input.ExpiresInSeconds > 86400 {
		writeError(w, r, http.StatusBadRequest, "invalid_expiry", "expires_in_seconds must be between 1 and 86400")
		return
	}
	if input.Parameters == nil {
		input.Parameters = map[string]any{}
	}
	if strings.TrimSpace(input.TaskID) == "" || strings.TrimSpace(input.ToolID) == "" || strings.TrimSpace(input.Action) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_approval", "task_id, tool_id, and action are required")
		return
	}
	taskItem, err := s.Tasks.Get(r.Context(), input.TaskID)
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "task_not_found", "task not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "verify approval task", err)
		return
	}
	if _, err := s.Goals.Get(r.Context(), defaultOwnerID, taskItem.GoalID); errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "task_not_found", "task not found")
		return
	} else if err != nil {
		s.internalError(w, r, "verify approval task owner", err)
		return
	}
	now := time.Now().UTC()
	item, err := approval.New(defaultOwnerID, input.TaskID, input.ToolID, input.Action, input.Parameters, input.RiskLevel, now.Add(time.Duration(input.ExpiresInSeconds)*time.Second), now)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_approval", err.Error())
		return
	}
	created, err := s.Approvals.Create(r.Context(), item)
	if err != nil {
		s.internalError(w, r, "create approval", err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	if s.Approvals == nil {
		writeError(w, r, http.StatusServiceUnavailable, "approval_unavailable", "approval service is not configured")
		return
	}
	items, err := s.Approvals.ListPending(r.Context(), defaultOwnerID, 100)
	if err != nil {
		s.internalError(w, r, "list approvals", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getApproval(w http.ResponseWriter, r *http.Request) {
	if s.Approvals == nil {
		writeError(w, r, http.StatusServiceUnavailable, "approval_unavailable", "approval service is not configured")
		return
	}
	item, err := s.Approvals.Get(r.Context(), defaultOwnerID, chi.URLParam(r, "id"))
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "approval not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "get approval", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) decideApproval(status approval.Status) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Approvals == nil {
			writeError(w, r, http.StatusServiceUnavailable, "approval_unavailable", "approval service is not configured")
			return
		}
		item, err := s.Approvals.Decide(r.Context(), defaultOwnerID, chi.URLParam(r, "id"), status, time.Now().UTC())
		if errors.Is(err, paierrors.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "approval not found")
			return
		}
		if errors.Is(err, approval.ErrExpired) {
			writeJSON(w, http.StatusConflict, item)
			return
		}
		if errors.Is(err, approval.ErrInvalidTransition) {
			writeError(w, r, http.StatusConflict, "invalid_transition", err.Error())
			return
		}
		if err != nil {
			s.internalError(w, r, "decide approval", err)
			return
		}
		if status == approval.Approved && item.ExecutionID != "" && s.DurableExecutions != nil {
			if err := s.DurableExecutions.ResumeApproval(r.Context(), defaultOwnerID, item.ID); err != nil {
				writeError(w, r, http.StatusConflict, "approval_resume_conflict", "approval was recorded but its execution is no longer resumable")
				return
			}
		}
		writeJSON(w, http.StatusOK, item)
	}
}
