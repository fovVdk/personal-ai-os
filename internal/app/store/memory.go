// English: This adapter persists provenance-aware memories and owner-scoped full-text search.
// 中文：本适配器持久化带来源的记忆，并提供按所有者隔离的全文搜索。
// 中文：本适配器持久化带来源的个人记忆，并按所有者执行全文检索。
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"personal-ai-os/internal/memory"
	paierrors "personal-ai-os/pkg/errors"
)

var _ memory.Repository = memoryRepository{Postgres: nil}

func (p *Postgres) CreateMemory(ctx context.Context, item memory.Memory) (memory.Memory, error) {
	metadata, err := json.Marshal(item.Metadata)
	if err != nil {
		return memory.Memory{}, fmt.Errorf("encode memory metadata: %w", err)
	}
	if item.Metadata == nil {
		metadata = []byte("{}")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return memory.Memory{}, fmt.Errorf("begin memory transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var observed any
	if item.ObservedAt != nil {
		observed = *item.ObservedAt
	}
	err = tx.QueryRow(ctx, `INSERT INTO memories(owner_id,category,content,source,confidence,observed_at,metadata)
		VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text,created_at,updated_at`, item.OwnerID, item.Category, item.Content, item.Source, item.Confidence, observed, metadata).Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return memory.Memory{}, fmt.Errorf("insert memory: %w", err)
	}
	if err := recordMutation(ctx, tx, item.OwnerID, "memory.created", "memories", item.ID, map[string]any{"id": item.ID, "category": item.Category, "source": item.Source, "confidence": item.Confidence}); err != nil {
		return memory.Memory{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return memory.Memory{}, fmt.Errorf("commit memory creation: %w", err)
	}
	return item, nil
}

func (p *Postgres) GetMemory(ctx context.Context, ownerID, id string) (memory.Memory, error) {
	item, err := scanMemory(p.pool.QueryRow(ctx, `SELECT id::text,owner_id::text,category,content,source,confidence,observed_at,metadata,created_at,updated_at
		FROM memories WHERE owner_id=$1 AND id=$2 AND deleted_at IS NULL`, ownerID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.Memory{}, paierrors.ErrNotFound
	}
	if err != nil {
		return memory.Memory{}, fmt.Errorf("get memory: %w", err)
	}
	return item, nil
}

func (p *Postgres) ListMemories(ctx context.Context, ownerID string, limit int) ([]memory.Memory, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, `SELECT id::text,owner_id::text,category,content,source,confidence,observed_at,metadata,created_at,updated_at
		FROM memories WHERE owner_id=$1 AND deleted_at IS NULL ORDER BY updated_at DESC LIMIT $2`, ownerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()
	items := make([]memory.Memory, 0)
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memories: %w", err)
	}
	return items, nil
}

func (p *Postgres) UpdateMemory(ctx context.Context, ownerID string, item memory.Memory) (memory.Memory, error) {
	metadata, err := json.Marshal(item.Metadata)
	if err != nil {
		return memory.Memory{}, fmt.Errorf("encode memory metadata: %w", err)
	}
	if item.Metadata == nil {
		metadata = []byte("{}")
	}
	var observed any
	if item.ObservedAt != nil {
		observed = *item.ObservedAt
	}
	item.OwnerID = ownerID
	var raw []byte
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return memory.Memory{}, fmt.Errorf("begin memory update: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	err = tx.QueryRow(ctx, `UPDATE memories SET category=$3,content=$4,source=$5,confidence=$6,observed_at=$7,metadata=$8,updated_at=now()
		WHERE owner_id=$1 AND id=$2 AND deleted_at IS NULL RETURNING id::text,owner_id::text,category,content,source,confidence,observed_at,metadata,created_at,updated_at`,
		ownerID, item.ID, item.Category, item.Content, item.Source, item.Confidence, observed, metadata).Scan(&item.ID, &item.OwnerID, &item.Category, &item.Content, &item.Source, &item.Confidence, &item.ObservedAt, &raw, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.Memory{}, paierrors.ErrNotFound
	}
	if err != nil {
		return memory.Memory{}, fmt.Errorf("update memory: %w", err)
	}
	if err := json.Unmarshal(raw, &item.Metadata); err != nil {
		return memory.Memory{}, fmt.Errorf("decode memory metadata: %w", err)
	}
	if err := recordMutation(ctx, tx, ownerID, "memory.updated", "memories", item.ID, map[string]any{"id": item.ID, "category": item.Category, "source": item.Source, "confidence": item.Confidence}); err != nil {
		return memory.Memory{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return memory.Memory{}, fmt.Errorf("commit memory update: %w", err)
	}
	return item, nil
}

func (p *Postgres) DeleteMemory(ctx context.Context, ownerID, id string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin memory deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tag, err := tx.Exec(ctx, `UPDATE memories SET deleted_at=now(),updated_at=now() WHERE owner_id=$1 AND id=$2 AND deleted_at IS NULL`, ownerID, id)
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return paierrors.ErrNotFound
	}
	if err := recordMutation(ctx, tx, ownerID, "memory.deleted", "memories", id, map[string]string{"id": id}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit memory deletion: %w", err)
	}
	return nil
}

func (p *Postgres) SearchMemories(ctx context.Context, ownerID, query string, limit int) ([]memory.Memory, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("search query is required")
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, `SELECT id::text,owner_id::text,category,content,source,confidence,observed_at,metadata,created_at,updated_at
		FROM memories WHERE owner_id=$1 AND deleted_at IS NULL AND (to_tsvector('simple',content) @@ plainto_tsquery('simple',$2) OR content ILIKE '%' || $2 || '%')
		ORDER BY ts_rank(to_tsvector('simple',content),plainto_tsquery('simple',$2)) DESC,updated_at DESC LIMIT $3`, ownerID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search memories: %w", err)
	}
	defer rows.Close()
	items := make([]memory.Memory, 0)
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory search: %w", err)
	}
	return items, nil
}

type memoryScanner interface{ Scan(...any) error }

func scanMemory(row memoryScanner) (memory.Memory, error) {
	var item memory.Memory
	var metadata []byte
	err := row.Scan(&item.ID, &item.OwnerID, &item.Category, &item.Content, &item.Source, &item.Confidence, &item.ObservedAt, &metadata, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return memory.Memory{}, err
	}
	if err := json.Unmarshal(metadata, &item.Metadata); err != nil {
		return memory.Memory{}, fmt.Errorf("decode memory metadata: %w", err)
	}
	return item, nil
}

type memoryRepository struct{ *Postgres }

func (r memoryRepository) Create(ctx context.Context, item memory.Memory) (memory.Memory, error) {
	return r.CreateMemory(ctx, item)
}
func (r memoryRepository) Get(ctx context.Context, ownerID, id string) (memory.Memory, error) {
	return r.GetMemory(ctx, ownerID, id)
}
func (r memoryRepository) List(ctx context.Context, ownerID string, limit int) ([]memory.Memory, error) {
	return r.ListMemories(ctx, ownerID, limit)
}
func (r memoryRepository) Update(ctx context.Context, ownerID string, item memory.Memory) (memory.Memory, error) {
	return r.UpdateMemory(ctx, ownerID, item)
}
func (r memoryRepository) Delete(ctx context.Context, ownerID, id string) error {
	return r.DeleteMemory(ctx, ownerID, id)
}
func (r memoryRepository) Search(ctx context.Context, ownerID, query string, limit int) ([]memory.Memory, error) {
	return r.SearchMemories(ctx, ownerID, query, limit)
}

func (p *Postgres) Memories() memory.Repository { return memoryRepository{Postgres: p} }
