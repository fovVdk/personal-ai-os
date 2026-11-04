// English: These handlers expose Tool metadata only; registration never grants execution permission.
// 中文：这些处理器仅公开 Tool 元数据；工具注册本身不授予执行权限。
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"personal-ai-os/internal/tool/registry"
)

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	if s.Tools == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.Tools.List()})
}

func (s *Server) getTool(w http.ResponseWriter, r *http.Request) {
	if s.Tools == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "tool not found")
		return
	}
	identity := registry.Identity{ProviderID: chi.URLParam(r, "provider"), ToolID: chi.URLParam(r, "tool"), Version: chi.URLParam(r, "version")}
	entry, err := s.Tools.Get(identity)
	if err == registry.ErrNotFound {
		writeError(w, r, http.StatusNotFound, "not_found", "tool not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "get tool metadata", err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}
