// English: This adapter persists approval requests and serializes decisions with row locks.
// 中文：本适配器持久化审批请求，并通过行锁串行化审批决策。
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"personal-ai-os/internal/approval"
	paierrors "personal-ai-os/pkg/errors"
)

func (p *Postgres) CreateApproval(ctx context.Context, item approval.Request) (approval.Request, error) {
	if item.Status == "" {
		item.Status = approval.Pending
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return approval.Request{}, fmt.Errorf("begin approval creation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	err = tx.QueryRow(ctx, `INSERT INTO approvals(user_id,task_id,tool_id,action,parameters_hash,risk_level,status,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text,created_at`, item.UserID, item.TaskID, item.ToolID, item.Action, item.ParametersHash, item.RiskLevel, item.Status, item.ExpiresAt).Scan(&item.ID, &item.CreatedAt)
	if err != nil {
		return approval.Request{}, fmt.Errorf("create approval: %w", err)
	}
	if err := recordMutation(ctx, tx, item.UserID, "approval.requested", "approvals", item.ID, item); err != nil {
		return approval.Request{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return approval.Request{}, fmt.Errorf("commit approval creation: %w", err)
	}
	return item, nil
}

func (p *Postgres) ListPending(ctx context.Context, userID string, limit int) ([]approval.Request, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, `SELECT id::text,user_id::text,task_id::text,tool_id,action,parameters_hash,risk_level,status,expires_at,approved_at,created_at
		FROM approvals WHERE user_id=$1 AND status='pending' AND expires_at>now() ORDER BY created_at LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	defer rows.Close()
	items := make([]approval.Request, 0)
	for rows.Next() {
		item, err := scanApproval(rows)
		if err != nil {
			return nil, fmt.Errorf("scan approval: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate approvals: %w", err)
	}
	return items, nil
}

func (p *Postgres) GetApproval(ctx context.Context, userID, id string) (approval.Request, error) {
	item, err := scanApproval(p.pool.QueryRow(ctx, `SELECT id::text,user_id::text,task_id::text,tool_id,action,parameters_hash,risk_level,status,expires_at,approved_at,created_at
		FROM approvals WHERE user_id=$1 AND id=$2`, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return approval.Request{}, paierrors.ErrNotFound
	}
	if err != nil {
		return approval.Request{}, fmt.Errorf("get approval: %w", err)
	}
	return item, nil
}

func (p *Postgres) DecideApproval(ctx context.Context, userID, id string, status approval.Status, now time.Time) (approval.Request, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return approval.Request{}, fmt.Errorf("begin approval decision: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	item, err := scanApproval(tx.QueryRow(ctx, `SELECT id::text,user_id::text,task_id::text,tool_id,action,parameters_hash,risk_level,status,expires_at,approved_at,created_at
		FROM approvals WHERE user_id=$1 AND id=$2 FOR UPDATE`, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return approval.Request{}, paierrors.ErrNotFound
	}
	if err != nil {
		return approval.Request{}, fmt.Errorf("load approval decision: %w", err)
	}
	var decisionErr error
	if status == approval.Approved || status == approval.Rejected {
		decisionErr = item.Decide(status, now)
		if decisionErr != nil && !errors.Is(decisionErr, approval.ErrExpired) {
			return approval.Request{}, decisionErr
		}
	}
	if status == approval.Expired || status == approval.Revoked {
		if item.Status != approval.Pending {
			return approval.Request{}, fmt.Errorf("%w: %s -> %s", approval.ErrInvalidTransition, item.Status, status)
		}
		item.Status = status
	}
	_, err = tx.Exec(ctx, `UPDATE approvals SET status=$3,approved_at=$4 WHERE user_id=$1 AND id=$2`, userID, id, item.Status, item.ApprovedAt)
	if err != nil {
		return approval.Request{}, fmt.Errorf("persist approval decision: %w", err)
	}
	if err := recordMutation(ctx, tx, userID, "approval."+string(item.Status), "approvals", item.ID, item); err != nil {
		return approval.Request{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return approval.Request{}, fmt.Errorf("commit approval decision: %w", err)
	}
	if decisionErr != nil {
		return item, decisionErr
	}
	return item, nil
}

type approvalScanner interface{ Scan(...any) error }

func scanApproval(row approvalScanner) (approval.Request, error) {
	var item approval.Request
	err := row.Scan(&item.ID, &item.UserID, &item.TaskID, &item.ToolID, &item.Action, &item.ParametersHash, &item.RiskLevel, &item.Status, &item.ExpiresAt, &item.ApprovedAt, &item.CreatedAt)
	return item, err
}

var _ interface {
	CreateApproval(context.Context, approval.Request) (approval.Request, error)
	ListPending(context.Context, string, int) ([]approval.Request, error)
	GetApproval(context.Context, string, string) (approval.Request, error)
	DecideApproval(context.Context, string, string, approval.Status, time.Time) (approval.Request, error)
} = (*Postgres)(nil)

type approvalRepository struct{ *Postgres }

func (r approvalRepository) Create(ctx context.Context, item approval.Request) (approval.Request, error) {
	return r.CreateApproval(ctx, item)
}
func (r approvalRepository) ListPending(ctx context.Context, userID string, limit int) ([]approval.Request, error) {
	return r.Postgres.ListPending(ctx, userID, limit)
}
func (r approvalRepository) Get(ctx context.Context, userID, id string) (approval.Request, error) {
	return r.GetApproval(ctx, userID, id)
}
func (r approvalRepository) Decide(ctx context.Context, userID, id string, status approval.Status, now time.Time) (approval.Request, error) {
	return r.DecideApproval(ctx, userID, id, status, now)
}
func (p *Postgres) Approvals() approval.Repository { return approvalRepository{Postgres: p} }

var _ approval.Repository = approvalRepository{Postgres: nil}
var _ approval.Repository = approvalRepository{Postgres: nil}
