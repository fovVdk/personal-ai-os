// English: This adapter maintains a durable per-Goal model call and token usage ledger.
// 中文：本适配器按 Goal 持久化模型调用次数与 Token 用量账本。
// 中文：本适配器维护按 Goal 累计的模型调用次数与 Token 持久化账本。
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"personal-ai-os/internal/supervisor"
)

type ModelUsageStore struct{ pool *pgxpool.Pool }

func NewModelUsageStore(pool *pgxpool.Pool) *ModelUsageStore { return &ModelUsageStore{pool: pool} }

func (s *ModelUsageStore) ModelUsage(ctx context.Context, goalID string) (supervisor.UsageTotals, error) {
	var usage supervisor.UsageTotals
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(model_calls),0),COALESCE(sum(total_tokens),0),
		GREATEST(COALESCE(sum(tool_calls),0),(SELECT count(*) FROM tool_invocations WHERE goal_id=$1))
		FROM model_usage WHERE goal_id=$1`, goalID).Scan(&usage.ModelCalls, &usage.ModelTokens, &usage.ToolCalls)
	if err != nil {
		return supervisor.UsageTotals{}, fmt.Errorf("query model usage: %w", err)
	}
	return usage, nil
}

func (s *ModelUsageStore) RecordModelUsage(ctx context.Context, goalID string, calls, toolCalls, inputTokens, outputTokens int) error {
	if calls < 0 || toolCalls < 0 || inputTokens < 0 || outputTokens < 0 {
		return fmt.Errorf("model usage counters cannot be negative")
	}
	totalTokens := inputTokens + outputTokens
	if calls == 0 && toolCalls == 0 && totalTokens == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin model usage transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `INSERT INTO model_usage(goal_id,provider,model,operation,model_calls,tool_calls,input_tokens,output_tokens,total_tokens)
		VALUES($1,'planner','configured-model','plan-generation',$2,$3,$4,$5,$6)`, goalID, calls, toolCalls, inputTokens, outputTokens, totalTokens); err != nil {
		return fmt.Errorf("record model usage: %w", err)
	}
	if err := recordMutation(ctx, tx, LocalOwnerID, "model.usage_recorded", "goals", goalID, map[string]int{"model_calls": calls, "tool_calls": toolCalls, "input_tokens": inputTokens, "output_tokens": outputTokens, "total_tokens": totalTokens}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit model usage: %w", err)
	}
	return nil
}

var _ supervisor.UsageStore = (*ModelUsageStore)(nil)
