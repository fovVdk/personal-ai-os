// English: This adapter stores content-addressed documents and returns full-text citations.
// 中文：本适配器存储内容寻址文档，并通过全文检索返回引用。
// 中文：本适配器保存内容哈希文档，并提供基于全文检索的可追溯引用。
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"personal-ai-os/internal/knowledge"
	paierrors "personal-ai-os/pkg/errors"
)

func (p *Postgres) ImportDocument(ctx context.Context, document knowledge.Document) (knowledge.Document, error) {
	if err := document.Validate(); err != nil {
		return knowledge.Document{}, err
	}
	metadata, err := json.Marshal(document.Metadata)
	if err != nil {
		return knowledge.Document{}, fmt.Errorf("encode document metadata: %w", err)
	}
	if document.Metadata == nil {
		metadata = []byte("{}")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return knowledge.Document{}, fmt.Errorf("begin document import: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	err = tx.QueryRow(ctx, `INSERT INTO knowledge_documents(owner_id,title,source,content_hash,metadata) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(owner_id,content_hash) DO NOTHING RETURNING id::text`, document.OwnerID, document.Title, document.Source, document.ContentHash, metadata).Scan(&document.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return knowledge.Document{}, paierrors.ErrConflict
	}
	if err != nil {
		return knowledge.Document{}, fmt.Errorf("insert knowledge document: %w", err)
	}
	for _, chunk := range document.Chunks {
		chunkMetadata, err := json.Marshal(chunk.Metadata)
		if err != nil {
			return knowledge.Document{}, fmt.Errorf("encode chunk metadata: %w", err)
		}
		if chunk.Metadata == nil {
			chunkMetadata = []byte("{}")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO knowledge_chunks(document_id,chunk_index,content,metadata) VALUES($1,$2,$3,$4)`, document.ID, chunk.Index, chunk.Content, chunkMetadata); err != nil {
			return knowledge.Document{}, fmt.Errorf("insert knowledge chunk: %w", err)
		}
	}
	if err := recordMutation(ctx, tx, document.OwnerID, "knowledge.document.created", "knowledge_documents", document.ID, map[string]any{"id": document.ID, "title": document.Title, "source": document.Source, "content_hash": document.ContentHash, "chunk_count": len(document.Chunks)}); err != nil {
		return knowledge.Document{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return knowledge.Document{}, fmt.Errorf("commit document import: %w", err)
	}
	return document, nil
}

func (p *Postgres) SearchKnowledge(ctx context.Context, ownerID, query string, limit int) ([]knowledge.Citation, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("search query is required")
	}
	if limit < 1 || limit > 200 {
		limit = 20
	}
	rows, err := p.pool.Query(ctx, `SELECT d.id::text,c.chunk_index,d.title,d.source,c.content FROM knowledge_documents d
		JOIN knowledge_chunks c ON c.document_id=d.id WHERE d.owner_id=$1 AND (to_tsvector('simple',c.content) @@ plainto_tsquery('simple',$2) OR c.content ILIKE '%' || $2 || '%')
		ORDER BY ts_rank(to_tsvector('simple',c.content),plainto_tsquery('simple',$2)) DESC LIMIT $3`, ownerID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search knowledge: %w", err)
	}
	defer rows.Close()
	citations := make([]knowledge.Citation, 0)
	for rows.Next() {
		var citation knowledge.Citation
		if err := rows.Scan(&citation.DocumentID, &citation.ChunkIndex, &citation.Title, &citation.Source, &citation.Excerpt); err != nil {
			return nil, fmt.Errorf("scan knowledge citation: %w", err)
		}
		citations = append(citations, citation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge results: %w", err)
	}
	return citations, nil
}

func (p *Postgres) ListDocuments(ctx context.Context, ownerID string, limit int) ([]knowledge.Document, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, `SELECT id::text,owner_id::text,title,source,content_hash,metadata FROM knowledge_documents WHERE owner_id=$1 ORDER BY created_at DESC LIMIT $2`, ownerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list knowledge documents: %w", err)
	}
	defer rows.Close()
	items := make([]knowledge.Document, 0)
	for rows.Next() {
		var item knowledge.Document
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.Title, &item.Source, &item.ContentHash, &metadata); err != nil {
			return nil, fmt.Errorf("scan knowledge document: %w", err)
		}
		if err := json.Unmarshal(metadata, &item.Metadata); err != nil {
			return nil, fmt.Errorf("decode document metadata: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge documents: %w", err)
	}
	return items, nil
}

type knowledgeRepository struct{ *Postgres }

func (r knowledgeRepository) Import(ctx context.Context, document knowledge.Document) (knowledge.Document, error) {
	return r.ImportDocument(ctx, document)
}
func (r knowledgeRepository) List(ctx context.Context, ownerID string, limit int) ([]knowledge.Document, error) {
	return r.ListDocuments(ctx, ownerID, limit)
}
func (r knowledgeRepository) Search(ctx context.Context, ownerID, query string, limit int) ([]knowledge.Citation, error) {
	return r.SearchKnowledge(ctx, ownerID, query, limit)
}
func (p *Postgres) Knowledge() knowledge.Repository { return knowledgeRepository{Postgres: p} }

var _ knowledge.Repository = knowledgeRepository{Postgres: nil}
