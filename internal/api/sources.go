package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"personal-ai-os/internal/source"
	paierrors "personal-ai-os/pkg/errors"
)

func (s *Server) verifySourceGoal(w http.ResponseWriter, r *http.Request) bool {
	if s.Sources == nil {
		writeError(w, r, http.StatusServiceUnavailable, "source_unavailable", "source snapshot storage is not configured")
		return false
	}
	if _, err := s.Goals.Get(r.Context(), defaultOwnerID, chi.URLParam(r, "id")); errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "goal_not_found", "Goal not found")
		return false
	} else if err != nil {
		s.internalError(w, r, "verify source Goal", err)
		return false
	}
	return true
}

func (s *Server) listSourceSnapshots(w http.ResponseWriter, r *http.Request) {
	if !s.verifySourceGoal(w, r) {
		return
	}
	items, err := s.Sources.List(r.Context(), defaultOwnerID, chi.URLParam(r, "id"), 50)
	if err != nil {
		s.internalError(w, r, "list source snapshots", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getSourceSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.verifySourceGoal(w, r) {
		return
	}
	item, err := s.Sources.Get(r.Context(), defaultOwnerID, chi.URLParam(r, "id"), chi.URLParam(r, "sourceID"))
	if errors.Is(err, source.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "source_not_found", "Source snapshot not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "get source snapshot", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) verifySourceTask(w http.ResponseWriter, r *http.Request, goalID, taskID string) bool {
	if s.Tasks == nil {
		writeError(w, r, http.StatusServiceUnavailable, "task_unavailable", "Task storage is not configured")
		return false
	}
	item, err := s.Tasks.Get(r.Context(), taskID)
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "task_not_found", "Task not found")
		return false
	}
	if err != nil {
		s.internalError(w, r, "verify source Task", err)
		return false
	}
	if item.GoalID != goalID {
		writeError(w, r, http.StatusBadRequest, "invalid_task", "Task does not belong to this Goal")
		return false
	}
	return true
}

func (s *Server) createSourceReference(w http.ResponseWriter, r *http.Request) {
	if !s.verifySourceGoal(w, r) {
		return
	}
	var input struct {
		TaskID       string `json:"task_id"`
		SnapshotID   string `json:"source_id"`
		ArtifactPath string `json:"artifact_path"`
		CitationURL  string `json:"url"`
		Quote        string `json:"quote"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !s.verifySourceTask(w, r, chi.URLParam(r, "id"), input.TaskID) {
		return
	}
	quote := strings.Join(strings.Fields(input.Quote), " ")
	item, err := s.Sources.AddReference(r.Context(), source.Reference{
		OwnerID: defaultOwnerID, GoalID: chi.URLParam(r, "id"), TaskID: input.TaskID,
		SnapshotID: input.SnapshotID, ArtifactPath: input.ArtifactPath, CitationURL: input.CitationURL,
		Quote: quote, QuoteHash: source.Hash([]byte(quote)),
	})
	if errors.Is(err, source.ErrNotFound) || errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "source_not_found", "Source snapshot, Task, or Goal not found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_source_reference", err.Error())
		return
	}
	results, err := (source.Verifier{Repository: s.Sources}).Verify(r.Context(), defaultOwnerID, item.GoalID, item.ArtifactPath, []source.ReferenceInput{{SnapshotID: item.SnapshotID, CitationURL: item.CitationURL, Quote: item.Quote, QuoteHash: item.QuoteHash}})
	if err != nil {
		s.internalError(w, r, "verify source reference", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"reference": item, "verification": results[0]})
}

func (s *Server) listSourceReferences(w http.ResponseWriter, r *http.Request) {
	if !s.verifySourceGoal(w, r) {
		return
	}
	artifactPath := r.URL.Query().Get("artifact_path")
	if strings.TrimSpace(artifactPath) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "artifact_path query parameter is required")
		return
	}
	items, err := s.Sources.ListReferences(r.Context(), defaultOwnerID, chi.URLParam(r, "id"), artifactPath)
	if err != nil {
		s.internalError(w, r, "list source references", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
