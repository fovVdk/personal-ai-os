// English: These handlers expose safe MCP Server status and operator-controlled lifecycle actions.
// 中文：这些处理器提供安全的 MCP Server 状态查询和运维生命周期操作。
package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (s *Server) listMCPServers(w http.ResponseWriter, r *http.Request) {
	if s.MCPServers == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.MCPServers.Status()})
}

func (s *Server) setMCPServerEnabled(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.MCPServers == nil {
			writeError(w, r, http.StatusServiceUnavailable, "mcp_unavailable", "MCP manager is not configured")
			return
		}
		if err := s.MCPServers.SetEnabled(chi.URLParam(r, "id"), enabled); err != nil {
			if strings.Contains(err.Error(), "not registered") {
				writeError(w, r, http.StatusNotFound, "mcp_server_not_found", "MCP server is not registered")
			} else {
				writeError(w, r, http.StatusConflict, "mcp_server_lifecycle_conflict", "MCP manager cannot change this server state")
			}
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"id": chi.URLParam(r, "id"), "enabled": enabled})
	}
}

func (s *Server) reconnectMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.MCPServers == nil {
		writeError(w, r, http.StatusServiceUnavailable, "mcp_unavailable", "MCP manager is not configured")
		return
	}
	if err := s.MCPServers.Reconnect(chi.URLParam(r, "id")); err != nil {
		writeError(w, r, http.StatusConflict, "mcp_reconnect_conflict", "MCP server is not registered or enabled")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": chi.URLParam(r, "id"), "status": "reconnecting"})
}

func (s *Server) removeMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.MCPServers == nil {
		writeError(w, r, http.StatusServiceUnavailable, "mcp_unavailable", "MCP manager is not configured")
		return
	}
	if err := s.MCPServers.RemoveServer(chi.URLParam(r, "id")); err != nil {
		writeError(w, r, http.StatusNotFound, "mcp_server_not_found", "MCP server is not registered")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": chi.URLParam(r, "id"), "status": "removed"})
}
