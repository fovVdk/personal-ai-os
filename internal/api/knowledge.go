// English: These handlers import bounded text documents and return cited keyword-search results.
// 中文：这些处理器导入长度受限的文本文档，并返回带引用的关键词搜索结果。
// 中文：这些处理器导入受大小限制的文本文档，并返回带引用的关键词检索结果。
package api

import (
	"errors"
	"net/http"
	"strings"

	"personal-ai-os/internal/knowledge"
	paierrors "personal-ai-os/pkg/errors"
)

type importDocumentRequest struct {
	Title    string         `json:"title"`
	Source   string         `json:"source"`
	Content  string         `json:"content"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func (s *Server) importDocument(w http.ResponseWriter, r *http.Request) {
	if s.Knowledge == nil {
		writeError(w, r, http.StatusServiceUnavailable, "knowledge_unavailable", "knowledge service is not configured")
		return
	}
	var input importDocumentRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.Source) == "" || strings.TrimSpace(input.Content) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_document", "title, source, and content are required")
		return
	}
	chunks, err := knowledge.SplitText(input.Content, 1000, 100)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_document", err.Error())
		return
	}
	document := knowledge.Document{OwnerID: defaultOwnerID, Title: input.Title, Source: input.Source, ContentHash: knowledge.HashContent(input.Content), Metadata: input.Metadata, Chunks: chunks}
	if err := document.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_document", err.Error())
		return
	}
	created, err := s.Knowledge.Import(r.Context(), document)
	if errors.Is(err, paierrors.ErrConflict) {
		writeError(w, r, http.StatusConflict, "duplicate_document", "document with identical content already exists")
		return
	}
	if err != nil {
		s.internalError(w, r, "import knowledge document", err)
		return
	}
	created.Chunks = nil
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request) {
	if s.Knowledge == nil {
		writeError(w, r, http.StatusServiceUnavailable, "knowledge_unavailable", "knowledge service is not configured")
		return
	}
	items, err := s.Knowledge.List(r.Context(), defaultOwnerID, 100)
	if err != nil {
		s.internalError(w, r, "list knowledge documents", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) searchKnowledge(w http.ResponseWriter, r *http.Request) {
	if s.Knowledge == nil {
		writeError(w, r, http.StatusServiceUnavailable, "knowledge_unavailable", "knowledge service is not configured")
		return
	}
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
	items, err := s.Knowledge.Search(r.Context(), defaultOwnerID, input.Query, 20)
	if err != nil {
		s.internalError(w, r, "search knowledge", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
