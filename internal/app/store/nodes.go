// English: This adapter persists node identities, capabilities, and heartbeat health state.
// 中文：本适配器持久化节点身份、能力和心跳健康状态。
// 中文：本适配器持久化节点身份、能力清单及心跳健康状态。
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"personal-ai-os/internal/node"
	paierrors "personal-ai-os/pkg/errors"
)

func (p *Postgres) RegisterNode(ctx context.Context, item node.Node) (node.Node, error) {
	if err := item.Validate(); err != nil {
		return node.Node{}, err
	}
	capabilities, err := json.Marshal(item.Capabilities)
	if err != nil {
		return node.Node{}, fmt.Errorf("encode node capabilities: %w", err)
	}
	item.Status = node.Offline
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return node.Node{}, fmt.Errorf("begin node registration: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	err = tx.QueryRow(ctx, `INSERT INTO nodes(identity,name,node_type,capabilities,status) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(identity) DO UPDATE SET name=EXCLUDED.name,node_type=EXCLUDED.node_type,capabilities=EXCLUDED.capabilities,status='offline',updated_at=now()
		RETURNING id::text,created_at,updated_at`, item.Identity, item.Name, item.Type, capabilities, item.Status).Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return node.Node{}, fmt.Errorf("register node: %w", err)
	}
	if err := recordMutation(ctx, tx, "system", "node.registered", "nodes", item.ID, map[string]any{"identity": item.Identity, "name": item.Name, "type": item.Type, "capabilities": item.Capabilities}); err != nil {
		return node.Node{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return node.Node{}, fmt.Errorf("commit node registration: %w", err)
	}
	return item, nil
}

func (p *Postgres) ListNodes(ctx context.Context, limit int) ([]node.Node, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, `SELECT id::text,identity,name,node_type,capabilities,status,last_heartbeat,created_at,updated_at FROM nodes ORDER BY name LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()
	items := make([]node.Node, 0)
	for rows.Next() {
		item, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate nodes: %w", err)
	}
	return items, nil
}

func (p *Postgres) GetNode(ctx context.Context, id string) (node.Node, error) {
	item, err := scanNode(p.pool.QueryRow(ctx, `SELECT id::text,identity,name,node_type,capabilities,status,last_heartbeat,created_at,updated_at FROM nodes WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return node.Node{}, paierrors.ErrNotFound
	}
	if err != nil {
		return node.Node{}, fmt.Errorf("get node: %w", err)
	}
	return item, nil
}

func (p *Postgres) HeartbeatNode(ctx context.Context, id string, status node.Status, at time.Time) (node.Node, error) {
	if status != node.Online && status != node.Degraded {
		return node.Node{}, errors.New("heartbeat status must be online or degraded")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return node.Node{}, fmt.Errorf("begin node heartbeat: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var item node.Node
	var capabilities []byte
	err = tx.QueryRow(ctx, `UPDATE nodes SET status=$2,last_heartbeat=$3,updated_at=now() WHERE id=$1 RETURNING id::text,identity,name,node_type,capabilities,status,last_heartbeat,created_at,updated_at`, id, status, at.UTC()).Scan(&item.ID, &item.Identity, &item.Name, &item.Type, &capabilities, &item.Status, &item.LastHeartbeat, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return node.Node{}, paierrors.ErrNotFound
	}
	if err != nil {
		return node.Node{}, fmt.Errorf("heartbeat node: %w", err)
	}
	if err := json.Unmarshal(capabilities, &item.Capabilities); err != nil {
		return node.Node{}, fmt.Errorf("decode node capabilities: %w", err)
	}
	if err := recordMutation(ctx, tx, "node:"+item.Identity, "node.heartbeat", "nodes", item.ID, map[string]any{"status": item.Status, "last_heartbeat": item.LastHeartbeat}); err != nil {
		return node.Node{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return node.Node{}, fmt.Errorf("commit node heartbeat: %w", err)
	}
	return item, nil
}

type nodeScanner interface{ Scan(...any) error }

func scanNode(row nodeScanner) (node.Node, error) {
	var item node.Node
	var capabilities []byte
	err := row.Scan(&item.ID, &item.Identity, &item.Name, &item.Type, &capabilities, &item.Status, &item.LastHeartbeat, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return node.Node{}, err
	}
	if err := json.Unmarshal(capabilities, &item.Capabilities); err != nil {
		return node.Node{}, fmt.Errorf("decode node capabilities: %w", err)
	}
	return item, nil
}

func (p *Postgres) Nodes() node.Repository { return nodeRepository{Postgres: p} }

type nodeRepository struct{ *Postgres }

func (r nodeRepository) Register(ctx context.Context, item node.Node) (node.Node, error) {
	return r.RegisterNode(ctx, item)
}
func (r nodeRepository) List(ctx context.Context, limit int) ([]node.Node, error) {
	return r.ListNodes(ctx, limit)
}
func (r nodeRepository) Get(ctx context.Context, id string) (node.Node, error) {
	return r.GetNode(ctx, id)
}
func (r nodeRepository) Heartbeat(ctx context.Context, id string, status node.Status, at time.Time) (node.Node, error) {
	return r.HeartbeatNode(ctx, id, status, at)
}

var _ node.Repository = nodeRepository{Postgres: nil}
