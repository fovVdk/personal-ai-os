// English: This adapter stores execution outcomes and evidence in durable PostgreSQL transactions.
// 中文：本适配器在 PostgreSQL 事务中持久化执行结果和证据。
// 中文：本适配器在 PostgreSQL 事务中持久化执行结果与证据。
package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/supervisor"
)

type ExecutionStore struct{ pool *pgxpool.Pool }

func NewExecutionStore(pool *pgxpool.Pool) *ExecutionStore { return &ExecutionStore{pool: pool} }

func (s *ExecutionStore) Save(ctx context.Context, item supervisor.Execution) (supervisor.Execution, error) {
	payload, err := json.Marshal(item)
	if err != nil {
		return supervisor.Execution{}, fmt.Errorf("encode execution: %w", err)
	}
	if len(item.Evidence) == 0 {
		return supervisor.Execution{}, fmt.Errorf("execution result must include evidence")
	}
	if item.Outcome != agentruntime.OutcomeSucceeded {
		return supervisor.Execution{}, fmt.Errorf("only verified successful executions can be stored as succeeded")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return supervisor.Execution{}, fmt.Errorf("begin execution persistence: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var runID string
	err = tx.QueryRow(ctx, `INSERT INTO execution_runs(task_id,status,idempotency_key,input_hash,result,attempt,completed_at)
		VALUES($1,'succeeded',$2,$3,$4,1,$5) RETURNING id::text`, item.TaskID, "mock-execution:"+item.TaskID, fmt.Sprintf("%x", sha256.Sum256([]byte(item.TaskID+"\x00"+item.AgentID))), payload, item.CompletedAt).Scan(&runID)
	if err != nil {
		return supervisor.Execution{}, fmt.Errorf("insert execution run: %w", err)
	}
	for _, evidence := range item.Evidence {
		if _, err := tx.Exec(ctx, `INSERT INTO execution_evidence(execution_run_id,source,description,reference,observed_at) VALUES($1,$2,$3,$4,$5)`, runID, evidence.Source, evidence.Description, evidence.Reference, evidence.ObservedAt); err != nil {
			return supervisor.Execution{}, fmt.Errorf("insert execution evidence: %w", err)
		}
	}
	if item.TotalTokens > 0 || item.ModelCalls > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO model_usage(goal_id,provider,model,operation,model_calls,input_tokens,output_tokens,total_tokens)
			SELECT goal_id,$2,$3,'agent-execution',$4,$5,$6,$7 FROM tasks WHERE id=$1`, item.TaskID, item.AgentID, "configured-model", item.ModelCalls, item.InputTokens, item.OutputTokens, item.TotalTokens); err != nil {
			return supervisor.Execution{}, fmt.Errorf("record execution model usage: %w", err)
		}
	}
	item.ID = runID
	if err := recordMutation(ctx, tx, LocalOwnerID, "execution.succeeded", "execution_runs", runID, map[string]any{"task_id": item.TaskID, "agent_id": item.AgentID, "evidence_count": len(item.Evidence), "usage": map[string]int{"input_tokens": item.InputTokens, "output_tokens": item.OutputTokens, "total_tokens": item.TotalTokens}}); err != nil {
		return supervisor.Execution{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return supervisor.Execution{}, fmt.Errorf("commit execution persistence: %w", err)
	}
	return item, nil
}

func (s *ExecutionStore) ListByGoal(ctx context.Context, goalID string) ([]supervisor.Execution, error) {
	rows, err := s.pool.Query(ctx, `SELECT e.id::text,e.task_id::text,e.result FROM execution_runs e JOIN tasks t ON t.id=e.task_id WHERE t.goal_id=$1 AND e.status='succeeded' ORDER BY e.completed_at`, goalID)
	if err != nil {
		return nil, fmt.Errorf("list goal executions: %w", err)
	}
	defer rows.Close()
	items := make([]supervisor.Execution, 0)
	for rows.Next() {
		var id string
		var taskID string
		var raw []byte
		if err := rows.Scan(&id, &taskID, &raw); err != nil {
			return nil, fmt.Errorf("scan execution: %w", err)
		}
		var item supervisor.Execution
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("decode execution: %w", err)
		}
		item.ID = id
		item.TaskID = taskID
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate executions: %w", err)
	}
	return items, nil
}

var _ supervisor.ExecutionStore = (*ExecutionStore)(nil)
