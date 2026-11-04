// English: This adapter provides durable, fenced idempotency claims for individual execution steps.
// 中文：本适配器为每个执行步骤提供持久化且受 fencing 保护的幂等认领。
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"personal-ai-os/internal/execution"
)

func (s *DurableExecutionStore) ClaimStep(ctx context.Context, lease execution.Lease, stepID, key, inputHash string) (bool, error) {
	if strings.TrimSpace(stepID) == "" || strings.TrimSpace(key) == "" || len(inputHash) != 64 || !isHexHash(inputHash) {
		return false, errors.New("step idempotency scope, key, and SHA-256 input hash are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin execution step idempotency claim: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM execution_runs WHERE id=$1 AND worker_id=$2 AND fencing_token=$3 AND status='running' AND lease_until>now() FOR UPDATE`, lease.ExecutionID, lease.WorkerID, lease.Fencing).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, execution.ErrLeaseLost
	}
	if err != nil {
		return false, fmt.Errorf("verify step idempotency lease: %w", err)
	}
	var existingHash string
	var existingKey string
	err = tx.QueryRow(ctx, `INSERT INTO execution_step_idempotency(execution_run_id,step_id,idempotency_key,input_hash)
		VALUES($1,$2,$3,$4) ON CONFLICT(execution_run_id,step_id) DO NOTHING RETURNING input_hash`, lease.ExecutionID, stepID, key, strings.ToLower(inputHash)).Scan(&existingHash)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT idempotency_key,input_hash FROM execution_step_idempotency WHERE execution_run_id=$1 AND step_id=$2 FOR UPDATE`, lease.ExecutionID, stepID).Scan(&existingKey, &existingHash)
		if err != nil {
			return false, fmt.Errorf("read existing step idempotency claim: %w", err)
		}
		if existingKey != key || existingHash != strings.ToLower(inputHash) {
			return false, execution.ErrIdempotencyReuse
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit duplicate step idempotency lookup: %w", err)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("insert execution step idempotency claim: %w", err)
	}
	if err := recordMutation(ctx, tx, lease.WorkerID, "execution.step_claimed", "execution_runs", lease.ExecutionID, map[string]any{"step_id": stepID, "idempotency_key": key, "fencing_token": lease.Fencing}); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit execution step idempotency claim: %w", err)
	}
	return true, nil
}
