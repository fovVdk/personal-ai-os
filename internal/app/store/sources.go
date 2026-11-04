// English: This adapter persists immutable source snapshots and artifact references.
// 中文：本适配器持久化不可变来源快照及其报告引用关联。
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"personal-ai-os/internal/source"
	paierrors "personal-ai-os/pkg/errors"
)

type sourceRepository struct{ *Postgres }

func (p *Postgres) Sources() source.Repository { return sourceRepository{Postgres: p} }

var _ source.Repository = sourceRepository{Postgres: nil}

func (r sourceRepository) Create(ctx context.Context, item source.Snapshot) (source.Snapshot, error) {
	if err := item.Validate(); err != nil {
		return source.Snapshot{}, err
	}
	metadata, err := json.Marshal(item.Metadata)
	if err != nil {
		return source.Snapshot{}, fmt.Errorf("encode source metadata: %w", err)
	}
	if item.Metadata == nil {
		metadata = []byte("{}")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return source.Snapshot{}, fmt.Errorf("begin source snapshot transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var eligible bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goals g JOIN tasks t ON t.goal_id=g.id WHERE g.id=$1 AND g.owner_id=$2 AND t.id=$3)`, item.GoalID, item.OwnerID, item.TaskID).Scan(&eligible); err != nil {
		return source.Snapshot{}, fmt.Errorf("validate source Goal and Task: %w", err)
	}
	if !eligible {
		return source.Snapshot{}, paierrors.ErrNotFound
	}
	err = tx.QueryRow(ctx, `INSERT INTO source_snapshots(owner_id,goal_id,task_id,original_url,final_url,http_status,mime_type,retrieved_at,content_hash,raw_content,normalized_content,fetcher_version,metadata)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id::text,created_at`, item.OwnerID, item.GoalID, item.TaskID, item.OriginalURL, item.FinalURL, item.HTTPStatus, item.MIMEType, item.RetrievedAt.UTC(), item.ContentHash, item.RawContent, item.NormalizedContent, item.FetcherVersion, metadata).Scan(&item.ID, &item.CreatedAt)
	if err != nil {
		return source.Snapshot{}, fmt.Errorf("insert immutable source snapshot: %w", err)
	}
	if err := recordMutation(ctx, tx, item.OwnerID, "source.snapshot.created", "source_snapshots", item.ID, map[string]any{"id": item.ID, "goal_id": item.GoalID, "task_id": item.TaskID, "original_url": item.OriginalURL, "final_url": item.FinalURL, "http_status": item.HTTPStatus, "mime_type": item.MIMEType, "content_hash": item.ContentHash, "fetcher_version": item.FetcherVersion}); err != nil {
		return source.Snapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return source.Snapshot{}, fmt.Errorf("commit source snapshot: %w", err)
	}
	return item, nil
}

func (r sourceRepository) Get(ctx context.Context, ownerID, goalID, id string) (source.Snapshot, error) {
	var item source.Snapshot
	var metadata []byte
	err := r.pool.QueryRow(ctx, `SELECT id::text,owner_id::text,goal_id::text,task_id::text,original_url,final_url,http_status,mime_type,retrieved_at,content_hash,raw_content,normalized_content,fetcher_version,metadata,created_at
		FROM source_snapshots WHERE owner_id=$1 AND goal_id=$2 AND id=$3`, ownerID, goalID, id).Scan(
		&item.ID, &item.OwnerID, &item.GoalID, &item.TaskID, &item.OriginalURL, &item.FinalURL, &item.HTTPStatus, &item.MIMEType, &item.RetrievedAt, &item.ContentHash, &item.RawContent, &item.NormalizedContent, &item.FetcherVersion, &metadata, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return source.Snapshot{}, source.ErrNotFound
	}
	if err != nil {
		return source.Snapshot{}, fmt.Errorf("get source snapshot: %w", err)
	}
	if err := json.Unmarshal(metadata, &item.Metadata); err != nil {
		return source.Snapshot{}, fmt.Errorf("decode source metadata: %w", err)
	}
	return item, nil
}

func (r sourceRepository) List(ctx context.Context, ownerID, goalID string, limit int) ([]source.Snapshot, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `SELECT id::text,owner_id::text,goal_id::text,task_id::text,original_url,final_url,http_status,mime_type,retrieved_at,content_hash,fetcher_version,metadata,created_at
		FROM source_snapshots WHERE owner_id=$1 AND goal_id=$2 ORDER BY retrieved_at DESC,id DESC LIMIT $3`, ownerID, goalID, limit)
	if err != nil {
		return nil, fmt.Errorf("list source snapshots: %w", err)
	}
	defer rows.Close()
	items := make([]source.Snapshot, 0)
	for rows.Next() {
		var item source.Snapshot
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.GoalID, &item.TaskID, &item.OriginalURL, &item.FinalURL, &item.HTTPStatus, &item.MIMEType, &item.RetrievedAt, &item.ContentHash, &item.FetcherVersion, &metadata, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan source snapshot: %w", err)
		}
		if err := json.Unmarshal(metadata, &item.Metadata); err != nil {
			return nil, fmt.Errorf("decode source metadata: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source snapshots: %w", err)
	}
	return items, nil
}

func (r sourceRepository) AddReference(ctx context.Context, item source.Reference) (source.Reference, error) {
	if item.OwnerID == "" || item.GoalID == "" || item.TaskID == "" || item.SnapshotID == "" || item.ArtifactPath == "" || !filepath.IsLocal(item.ArtifactPath) || filepath.IsAbs(item.ArtifactPath) || strings.ContainsAny(item.ArtifactPath, "\\\x00\r\n") || strings.TrimSpace(item.Quote) == "" || len(item.Quote) > 8000 {
		return source.Reference{}, errors.New("source reference fields are invalid")
	}
	if item.QuoteHash != source.Hash([]byte(strings.Join(strings.Fields(item.Quote), " "))) {
		return source.Reference{}, errors.New("source reference quote hash does not match normalized quote")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return source.Reference{}, fmt.Errorf("begin source reference transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var originalURL, finalURL, normalizedContent string
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT s.original_url,s.final_url,s.normalized_content,EXISTS(SELECT 1 FROM tasks t WHERE t.id=$3 AND t.goal_id=s.goal_id)
		FROM source_snapshots s WHERE s.id=$1 AND s.owner_id=$2 AND s.goal_id=$4 FOR SHARE`, item.SnapshotID, item.OwnerID, item.TaskID, item.GoalID).Scan(&originalURL, &finalURL, &normalizedContent, &eligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return source.Reference{}, source.ErrNotFound
	}
	if err != nil {
		return source.Reference{}, fmt.Errorf("verify source snapshot reference: %w", err)
	}
	if !eligible {
		return source.Reference{}, paierrors.ErrNotFound
	}
	// Citation URLs must be exactly bound to this immutable snapshot.
	if item.CitationURL != originalURL && item.CitationURL != finalURL {
		return source.Reference{}, errors.New("citation URL does not match source snapshot")
	}
	normalizedQuote := strings.Join(strings.Fields(item.Quote), " ")
	if !strings.Contains(normalizedContent, normalizedQuote) {
		return source.Reference{}, errors.New("quoted text is not present in the immutable source snapshot")
	}
	err = tx.QueryRow(ctx, `INSERT INTO source_references(owner_id,goal_id,task_id,snapshot_id,artifact_path,citation_url,quote,quote_hash)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text,created_at`, item.OwnerID, item.GoalID, item.TaskID, item.SnapshotID, item.ArtifactPath, item.CitationURL, item.Quote, item.QuoteHash).Scan(&item.ID, &item.CreatedAt)
	if err != nil {
		return source.Reference{}, fmt.Errorf("insert immutable source reference: %w", err)
	}
	if err := recordMutation(ctx, tx, item.OwnerID, "source.reference.created", "source_references", item.ID, map[string]any{"id": item.ID, "goal_id": item.GoalID, "task_id": item.TaskID, "snapshot_id": item.SnapshotID, "artifact_path": item.ArtifactPath, "citation_url": item.CitationURL, "quote_hash": item.QuoteHash}); err != nil {
		return source.Reference{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return source.Reference{}, fmt.Errorf("commit source reference: %w", err)
	}
	return item, nil
}

func (r sourceRepository) ListReferences(ctx context.Context, ownerID, goalID, artifactPath string) ([]source.Reference, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text,owner_id::text,goal_id::text,task_id::text,snapshot_id::text,artifact_path,citation_url,quote,quote_hash,created_at
		FROM source_references WHERE owner_id=$1 AND goal_id=$2 AND artifact_path=$3 ORDER BY created_at,id`, ownerID, goalID, artifactPath)
	if err != nil {
		return nil, fmt.Errorf("list source references: %w", err)
	}
	defer rows.Close()
	items := make([]source.Reference, 0)
	for rows.Next() {
		var item source.Reference
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.GoalID, &item.TaskID, &item.SnapshotID, &item.ArtifactPath, &item.CitationURL, &item.Quote, &item.QuoteHash, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan source reference: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source references: %w", err)
	}
	return items, nil
}
