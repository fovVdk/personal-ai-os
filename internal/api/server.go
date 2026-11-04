// Package api exposes the versioned HTTP interface for Personal AI OS.
// 中文：api 包对外提供 Personal AI OS 的版本化 HTTP 接口。
// English: This file composes the versioned HTTP routes, middleware, and domain handlers.
// 中文：本文件组合版本化 HTTP 路由、中间件及领域处理器。
package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"personal-ai-os/internal/agent/registry"
	"personal-ai-os/internal/approval"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/knowledge"
	"personal-ai-os/internal/memory"
	"personal-ai-os/internal/model/gateway"
	"personal-ai-os/internal/node"
	"personal-ai-os/internal/planner"
	"personal-ai-os/internal/supervisor"
	"personal-ai-os/internal/task"
	paierrors "personal-ai-os/pkg/errors"
)

const defaultOwnerID = "00000000-0000-4000-8000-000000000001"

type readiness interface{ Ping(context.Context) error }
type durableExecutionAPI interface {
	Get(context.Context, string) (execution.Run, error)
	RequestCancel(context.Context, string) error
	CancelTask(context.Context, string) (bool, error)
}

// Server composes API routes from domain repositories and infrastructure checks.
// 中文：Server 使用领域仓储和基础设施检查组装 HTTP API 路由。
type Server struct {
	Goals             goal.Repository
	Tasks             task.Repository
	Memories          memory.Repository
	Nodes             node.Repository
	Knowledge         knowledge.Repository
	Planner           planner.Generator
	Models            gateway.Gateway
	Approvals         approval.Repository
	Agents            *registry.Registry
	Supervisor        *supervisor.Supervisor
	WorkerStatus      func() any
	DurableExecutions durableExecutionAPI
	DB                readiness
	Logger            *slog.Logger
	APIKey            string
	MaxRetries        int
	RequestTimeout    time.Duration
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(s.requestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(s.timeout()))
	r.Use(s.requestLogger)
	r.Get("/health", s.health)
	r.Get("/ready", s.ready)
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.authenticate)
		r.Post("/goals", s.createGoal)
		r.Get("/goals", s.listGoals)
		r.Get("/goals/{id}", s.getGoal)
		r.Patch("/goals/{id}", s.updateGoal)
		r.Post("/goals/{id}/activate", s.transitionGoal(goal.Active))
		r.Post("/goals/{id}/pause", s.transitionGoal(goal.Paused))
		r.Post("/goals/{id}/resume", s.transitionGoal(goal.Active))
		r.Post("/goals/{id}/complete", s.transitionGoal(goal.Completed))
		r.Post("/tasks", s.createTask)
		r.Get("/tasks", s.listTasks)
		r.Get("/tasks/{id}", s.getTask)
		r.Post("/tasks/{id}/pause", s.transitionTask(task.Paused))
		r.Post("/tasks/{id}/resume", s.transitionTask(task.Ready))
		r.Post("/tasks/{id}/retry", s.transitionTask(task.Retrying))
		r.Post("/tasks/{id}/cancel", s.transitionTask(task.Cancelled))
		r.Post("/planner/generate", s.generatePlan)
		r.Post("/supervisor/turn", s.supervisorTurn)
		r.Get("/supervisor/status", s.supervisorStatus)
		r.Get("/executions/{id}", s.getExecution)
		r.Post("/executions/{id}/cancel", s.cancelExecution)
		r.Get("/agents", s.listAgents)
		r.Get("/agents/{id}", s.getAgent)
		r.Post("/approvals", s.createApproval)
		r.Get("/approvals", s.listApprovals)
		r.Get("/approvals/{id}", s.getApproval)
		r.Post("/approvals/{id}/approve", s.decideApproval(approval.Approved))
		r.Post("/approvals/{id}/reject", s.decideApproval(approval.Rejected))
		r.Post("/memories", s.createMemory)
		r.Get("/memories", s.listMemories)
		r.Get("/memories/{id}", s.getMemory)
		r.Patch("/memories/{id}", s.updateMemory)
		r.Delete("/memories/{id}", s.deleteMemory)
		r.Post("/memories/search", s.searchMemories)
		r.Post("/knowledge/documents", s.importDocument)
		r.Get("/knowledge/documents", s.listDocuments)
		r.Post("/knowledge/search", s.searchKnowledge)
		r.Get("/nodes", s.listNodes)
		r.Post("/nodes/register", s.registerNode)
		r.Get("/nodes/{id}", s.getNode)
	})
	return r
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if id == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n\x00") {
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				id = fmt.Sprintf("req-%d", time.Now().UnixNano())
			} else {
				id = hex.EncodeToString(random[:])
			}
		}
		ctx := context.WithValue(r.Context(), middleware.RequestIDKey, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		if s.Logger != nil {
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			s.Logger.InfoContext(r.Context(), "http request", "request_id", middleware.GetReqID(r.Context()), "method", r.Method, "path", r.URL.Path, "status", status, "duration_ms", time.Since(started).Milliseconds())
		}
	})
}

func (s *Server) timeout() time.Duration {
	if s.RequestTimeout <= 0 {
		return 30 * time.Second
	}
	return s.RequestTimeout
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.APIKey == "" {
			next.ServeHTTP(w, r)
			return
		}
		provided := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if provided == "" {
			provided = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if len(provided) != len(s.APIKey) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.APIKey)) != 1 {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "valid API key required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		writeError(w, r, http.StatusServiceUnavailable, "not_ready", "database is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.DB.Ping(ctx); err != nil {
		if s.Logger != nil {
			s.Logger.WarnContext(r.Context(), "readiness check failed", "error", err)
		}
		writeError(w, r, http.StatusServiceUnavailable, "not_ready", "database is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

type createGoalRequest struct {
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	ParentGoalID    *string  `json:"parent_goal_id"`
	Priority        int      `json:"priority"`
	SuccessCriteria []string `json:"success_criteria"`
	Constraints     []string `json:"constraints"`
}

func (s *Server) createGoal(w http.ResponseWriter, r *http.Request) {
	var input createGoalRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Priority == 0 {
		input.Priority = 3
	}
	item := goal.Goal{Title: input.Title, Description: input.Description, OwnerID: defaultOwnerID, ParentGoalID: input.ParentGoalID, Priority: input.Priority,
		SuccessCriteria: input.SuccessCriteria, Constraints: input.Constraints, AutonomyPolicy: map[string]any{}, ReviewPolicy: map[string]any{}, Status: goal.Draft}
	if err := item.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_goal", err.Error())
		return
	}
	created, err := s.Goals.Create(r.Context(), item)
	if err != nil {
		s.internalError(w, r, "create goal", err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	items, err := s.Goals.List(r.Context(), defaultOwnerID, 100)
	if err != nil {
		s.internalError(w, r, "list goals", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getGoal(w http.ResponseWriter, r *http.Request) {
	item, err := s.Goals.Get(r.Context(), defaultOwnerID, chi.URLParam(r, "id"))
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "goal not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "get goal", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

type goalPatch struct {
	Title           *string   `json:"title"`
	Description     *string   `json:"description"`
	Priority        *int      `json:"priority"`
	SuccessCriteria *[]string `json:"success_criteria"`
	Constraints     *[]string `json:"constraints"`
}

func (s *Server) updateGoal(w http.ResponseWriter, r *http.Request) {
	item, err := s.Goals.Get(r.Context(), defaultOwnerID, chi.URLParam(r, "id"))
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "goal not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "get goal for update", err)
		return
	}
	var patch goalPatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	if patch.Title != nil {
		item.Title = *patch.Title
	}
	if patch.Description != nil {
		item.Description = *patch.Description
	}
	if patch.Priority != nil {
		item.Priority = *patch.Priority
	}
	if patch.SuccessCriteria != nil {
		item.SuccessCriteria = *patch.SuccessCriteria
	}
	if patch.Constraints != nil {
		item.Constraints = *patch.Constraints
	}
	if err := item.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_goal", err.Error())
		return
	}
	updated, err := s.Goals.Update(r.Context(), defaultOwnerID, item)
	if err != nil {
		s.internalError(w, r, "update goal", err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) transitionGoal(next goal.Status) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var evidence []goal.Evidence
		if next == goal.Completed {
			var input struct {
				Evidence []goal.Evidence `json:"evidence"`
			}
			if !decodeJSON(w, r, &input) {
				return
			}
			evidence = input.Evidence
		}
		item, err := s.Goals.Transition(r.Context(), defaultOwnerID, chi.URLParam(r, "id"), next, evidence)
		if errors.Is(err, paierrors.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "goal not found")
			return
		}
		if errors.Is(err, goal.ErrEvidenceRequired) {
			writeError(w, r, http.StatusUnprocessableEntity, "evidence_required", err.Error())
			return
		}
		if errors.Is(err, goal.ErrInvalidTransition) {
			writeError(w, r, http.StatusConflict, "invalid_transition", err.Error())
			return
		}
		if err != nil {
			s.internalError(w, r, "transition goal", err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

type createTaskRequest struct {
	GoalID          string   `json:"goal_id"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	Priority        int      `json:"priority"`
	SuccessCriteria []string `json:"success_criteria"`
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var input createTaskRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Priority == 0 {
		input.Priority = 3
	}
	if _, err := s.Goals.Get(r.Context(), defaultOwnerID, input.GoalID); errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_not_found", "goal not found")
		return
	} else if err != nil {
		s.internalError(w, r, "verify task goal", err)
		return
	}
	item := task.Task{GoalID: input.GoalID, Title: input.Title, Description: input.Description, Priority: input.Priority, Status: task.Created, SuccessCriteria: input.SuccessCriteria}
	if err := item.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_task", err.Error())
		return
	}
	created, err := s.Tasks.Create(r.Context(), item)
	if err != nil {
		s.internalError(w, r, "create task", err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	goalID := r.URL.Query().Get("goal_id")
	if goalID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "goal_id query parameter is required")
		return
	}
	if _, err := s.Goals.Get(r.Context(), defaultOwnerID, goalID); errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_not_found", "goal not found")
		return
	} else if err != nil {
		s.internalError(w, r, "verify task goal", err)
		return
	}
	items, err := s.Tasks.ListByGoal(r.Context(), goalID, 100)
	if err != nil {
		s.internalError(w, r, "list tasks", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	item, err := s.Tasks.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "task not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "get task", err)
		return
	}
	if _, err := s.Goals.Get(r.Context(), defaultOwnerID, item.GoalID); errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "task not found")
		return
	} else if err != nil {
		s.internalError(w, r, "verify task goal", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) transitionTask(next task.Status) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		current, err := s.Tasks.Get(r.Context(), chi.URLParam(r, "id"))
		if errors.Is(err, paierrors.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "task not found")
			return
		}
		if err != nil {
			s.internalError(w, r, "get task for transition", err)
			return
		}
		if next == task.Retrying && current.AttemptCount > s.MaxRetries {
			writeError(w, r, http.StatusConflict, "retry_budget_exhausted", "task retry budget is exhausted")
			return
		}
		if _, err := s.Goals.Get(r.Context(), defaultOwnerID, current.GoalID); errors.Is(err, paierrors.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "task not found")
			return
		} else if err != nil {
			s.internalError(w, r, "verify task goal", err)
			return
		}
		if next == task.Cancelled && s.DurableExecutions != nil {
			cancelled, err := s.DurableExecutions.CancelTask(r.Context(), current.ID)
			if errors.Is(err, execution.ErrUncertainOutcome) {
				writeError(w, r, http.StatusConflict, "execution_outcome_unknown", "task has an uncertain external execution outcome and requires reconciliation")
				return
			}
			if err != nil {
				s.internalError(w, r, "request execution cancellation", err)
				return
			}
			if cancelled {
				updated, err := s.Tasks.Get(r.Context(), current.ID)
				if err != nil {
					s.internalError(w, r, "read task cancellation state", err)
					return
				}
				if updated.Status == task.Running {
					writeJSON(w, http.StatusAccepted, map[string]any{"task": updated, "cancellation_requested": true, "outcome_may_be_uncertain": true})
					return
				}
				writeJSON(w, http.StatusOK, updated)
				return
			}
		}
		updated, err := s.Tasks.Transition(r.Context(), current.ID, next)
		if errors.Is(err, paierrors.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "task not found")
			return
		}
		if errors.Is(err, task.ErrInvalidTransition) {
			writeError(w, r, http.StatusConflict, "invalid_transition", err.Error())
			return
		}
		if err != nil {
			s.internalError(w, r, "transition task", err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

type memoryInput struct {
	Category   memory.Category `json:"category"`
	Content    string          `json:"content"`
	Source     string          `json:"source"`
	Confidence *float64        `json:"confidence,omitempty"`
	ObservedAt *time.Time      `json:"observed_at,omitempty"`
	Metadata   map[string]any  `json:"metadata,omitempty"`
}

func (s *Server) createMemory(w http.ResponseWriter, r *http.Request) {
	var input memoryInput
	if !decodeJSON(w, r, &input) {
		return
	}
	confidence := 0.5
	if input.Confidence != nil {
		confidence = *input.Confidence
	}
	item := memory.Memory{OwnerID: defaultOwnerID, Category: input.Category, Content: input.Content, Source: input.Source, Confidence: confidence, ObservedAt: input.ObservedAt, Metadata: input.Metadata}
	if err := item.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_memory", err.Error())
		return
	}
	created, err := s.Memories.Create(r.Context(), item)
	if err != nil {
		s.internalError(w, r, "create memory", err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listMemories(w http.ResponseWriter, r *http.Request) {
	items, err := s.Memories.List(r.Context(), defaultOwnerID, 100)
	if err != nil {
		s.internalError(w, r, "list memories", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getMemory(w http.ResponseWriter, r *http.Request) {
	item, err := s.Memories.Get(r.Context(), defaultOwnerID, chi.URLParam(r, "id"))
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "memory not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "get memory", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) updateMemory(w http.ResponseWriter, r *http.Request) {
	item, err := s.Memories.Get(r.Context(), defaultOwnerID, chi.URLParam(r, "id"))
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "memory not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "get memory for update", err)
		return
	}
	var input memoryInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Category == "" && input.Content == "" && input.Source == "" && input.Confidence == nil && input.ObservedAt == nil && input.Metadata == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_memory", "at least one memory field is required")
		return
	}
	if input.Category != "" {
		item.Category = input.Category
	}
	if input.Content != "" {
		item.Content = input.Content
	}
	if input.Source != "" {
		item.Source = input.Source
	}
	if input.Confidence != nil {
		item.Confidence = *input.Confidence
	}
	if input.ObservedAt != nil {
		item.ObservedAt = input.ObservedAt
	}
	if input.Metadata != nil {
		item.Metadata = input.Metadata
	}
	if err := item.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_memory", err.Error())
		return
	}
	updated, err := s.Memories.Update(r.Context(), defaultOwnerID, item)
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "memory not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "update memory", err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	if err := s.Memories.Delete(r.Context(), defaultOwnerID, chi.URLParam(r, "id")); errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "memory not found")
		return
	} else if err != nil {
		s.internalError(w, r, "delete memory", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) searchMemories(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Query string `json:"query"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Query) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_query", "query is required")
		return
	}
	items, err := s.Memories.Search(r.Context(), defaultOwnerID, input.Query, 100)
	if err != nil {
		s.internalError(w, r, "search memories", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	if s.Logger != nil {
		s.Logger.ErrorContext(r.Context(), operation, "request_id", middleware.GetReqID(r.Context()), "error_type", fmt.Sprintf("%T", err))
	}
	writeError(w, r, http.StatusInternalServerError, "internal_error", "request could not be completed")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 1 MiB")
			return false
		}
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must contain a single JSON value")
		return false
	}
	return true
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}
type errorEnvelope struct {
	Error apiError `json:"error"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: apiError{Code: code, Message: message, RequestID: middleware.GetReqID(r.Context())}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}
