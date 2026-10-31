// English: These handlers register and query node metadata; registration never asserts liveness.
// 中文：这些处理器注册和查询节点元数据；注册本身不代表节点在线。
// 中文：这些处理器负责节点元数据注册与查询；注册操作不会伪造节点在线状态。
package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"personal-ai-os/internal/node"
	paierrors "personal-ai-os/pkg/errors"
)

type registerNodeRequest struct {
	Identity     string    `json:"identity"`
	Name         string    `json:"name"`
	Type         node.Type `json:"type"`
	Capabilities []string  `json:"capabilities"`
}

func (s *Server) registerNode(w http.ResponseWriter, r *http.Request) {
	if s.Nodes == nil {
		writeError(w, r, http.StatusServiceUnavailable, "node_unavailable", "node registry is not configured")
		return
	}
	var input registerNodeRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	item := node.Node{Identity: input.Identity, Name: input.Name, Type: input.Type, Capabilities: input.Capabilities, Status: node.Offline}
	if err := item.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_node", err.Error())
		return
	}
	created, err := s.Nodes.Register(r.Context(), item)
	if err != nil {
		s.internalError(w, r, "register node", err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	if s.Nodes == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	items, err := s.Nodes.List(r.Context(), 100)
	if err != nil {
		s.internalError(w, r, "list nodes", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	if s.Nodes == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "node not found")
		return
	}
	item, err := s.Nodes.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, paierrors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "get node", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
