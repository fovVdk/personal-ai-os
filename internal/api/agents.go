// English: These handlers expose agent profile metadata without leaking runtime implementations.
// 中文：这些处理器仅公开 Agent Profile 元数据，不泄漏 Runtime 实现。
// 中文：这些处理器仅公开 Agent 配置元数据，不暴露运行时实现细节。
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"personal-ai-os/internal/agent/registry"
)

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	profiles := s.Agents.List()
	items := make([]map[string]any, 0, len(profiles))
	for _, profile := range profiles {
		items = append(items, map[string]any{"id": profile.ID, "name": profile.Name, "version": profile.Version, "capabilities": profile.Capabilities})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "agent not found")
		return
	}
	profile, err := s.Agents.Get(chi.URLParam(r, "id"))
	if err == registry.ErrNotFound {
		writeError(w, r, http.StatusNotFound, "not_found", "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": profile.ID, "name": profile.Name, "version": profile.Version, "capabilities": profile.Capabilities})
}
