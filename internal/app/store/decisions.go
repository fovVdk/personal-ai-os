// English: This adapter idempotently persists Supervisor decisions and exposes their history.
// 中文：本适配器幂等保存 Supervisor Decision，并提供历史查询。
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"personal-ai-os/internal/supervisor"
)

type DecisionStore struct{ pool *pgxpool.Pool }

func NewDecisionStore(pool *pgxpool.Pool) *DecisionStore { return &DecisionStore{pool: pool} }

func (s *DecisionStore) SaveDecision(ctx context.Context, item supervisor.Decision) (supervisor.Decision, error) {
	if item.GoalID == "" || item.ProgressFingerprint == "" || item.Type == "" || item.Reason == "" {
		return supervisor.Decision{}, fmt.Errorf("Supervisor decision is incomplete")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return supervisor.Decision{}, fmt.Errorf("begin Supervisor decision transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	item, created, err := saveDecisionTx(ctx, tx, item)
	if err != nil {
		return supervisor.Decision{}, fmt.Errorf("persist Supervisor decision: %w", err)
	}
	if created {
		if err := recordMutation(ctx, tx, LocalOwnerID, "supervisor.decision", "supervisor_decisions", item.ID, item); err != nil {
			return supervisor.Decision{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return supervisor.Decision{}, fmt.Errorf("commit Supervisor decision: %w", err)
	}
	return item, nil
}

func saveDecisionTx(ctx context.Context, tx pgx.Tx, item supervisor.Decision) (supervisor.Decision, bool, error) {
	err := tx.QueryRow(ctx, `INSERT INTO supervisor_decisions(goal_id,goal_updated_at,plan_version,evaluation_id,decision_type,decision_status,reason,progress_fingerprint,completed_at)
		VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8,$9)
		ON CONFLICT(goal_id,progress_fingerprint,decision_type) DO NOTHING
		RETURNING id::text,created_at,completed_at`, item.GoalID, item.GoalUpdatedAt.UTC(), item.PlanVersion, item.EvaluationID, item.Type, item.Status, item.Reason, item.ProgressFingerprint, item.CompletedAt).Scan(&item.ID, &item.CreatedAt, &item.CompletedAt)
	if err == nil {
		return item, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return supervisor.Decision{}, false, err
	}
	err = tx.QueryRow(ctx, `SELECT id::text,created_at,completed_at FROM supervisor_decisions WHERE goal_id=$1 AND progress_fingerprint=$2 AND decision_type=$3`, item.GoalID, item.ProgressFingerprint, item.Type).Scan(&item.ID, &item.CreatedAt, &item.CompletedAt)
	if err != nil {
		return supervisor.Decision{}, false, err
	}
	return item, false, nil
}

func (s *DecisionStore) ListDecisions(ctx context.Context, goalID string, limit int) ([]supervisor.Decision, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text,goal_id::text,goal_updated_at,plan_version,COALESCE(evaluation_id::text,''),decision_type,decision_status,reason,progress_fingerprint,created_at,completed_at
		FROM supervisor_decisions WHERE goal_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2`, goalID, limit)
	if err != nil {
		return nil, fmt.Errorf("query Supervisor decisions: %w", err)
	}
	defer rows.Close()
	items := make([]supervisor.Decision, 0)
	for rows.Next() {
		var item supervisor.Decision
		if err := rows.Scan(&item.ID, &item.GoalID, &item.GoalUpdatedAt, &item.PlanVersion, &item.EvaluationID, &item.Type, &item.Status, &item.Reason, &item.ProgressFingerprint, &item.CreatedAt, &item.CompletedAt); err != nil {
			return nil, fmt.Errorf("scan Supervisor decision: %w", err)
		}
		item.PlanVersionKnown = item.PlanVersion > 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Supervisor decisions: %w", err)
	}
	return items, nil
}

var _ supervisor.DecisionStore = (*DecisionStore)(nil)
