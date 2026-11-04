// English: This PostgreSQL adapter implements fenced execution claims, durable idempotency, checkpoints, and lease recovery.
// 中文：本 PostgreSQL 适配器实现带 fencing 的执行认领、持久幂等键、Checkpoint 和租约恢复。
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"personal-ai-os/internal/execution"
	paierrors "personal-ai-os/pkg/errors"
)

type DurableExecutionStore struct{ pool *pgxpool.Pool }

func NewDurableExecutionStore(pool *pgxpool.Pool) *DurableExecutionStore {
	return &DurableExecutionStore{pool: pool}
}

func (s *DurableExecutionStore) Enqueue(ctx context.Context, request execution.EnqueueRequest) (execution.Run, bool, error) {
	if strings.TrimSpace(request.TaskID) == "" || strings.TrimSpace(request.AgentID) == "" || strings.TrimSpace(request.IdempotencyScope) == "" || strings.TrimSpace(request.IdempotencyKey) == "" || len(request.IdempotencyScope) > 256 || len(request.IdempotencyKey) > 256 || len(request.AgentID) > 128 || len(request.Payload) == 0 || len(request.Payload) > 1024*1024 || len(request.InputHash) != sha256.Size*2 || !isHexHash(request.InputHash) || !json.Valid(request.Payload) || request.MaxAttempts < 1 || request.MaxAttempts > 100 {
		return execution.Run{}, false, errors.New("execution enqueue request is invalid")
	}
	payloadHash := sha256.Sum256(request.Payload)
	if hex.EncodeToString(payloadHash[:]) != strings.ToLower(request.InputHash) {
		return execution.Run{}, false, errors.New("execution input hash does not match canonical payload")
	}
	canonicalInput, err := json.Marshal(struct {
		TaskID      string `json:"task_id"`
		AgentID     string `json:"agent_id"`
		MaxAttempts int    `json:"max_attempts"`
		RetrySafe   bool   `json:"retry_safe"`
		Idempotent  bool   `json:"idempotent"`
		PayloadHash string `json:"payload_hash"`
	}{TaskID: request.TaskID, AgentID: request.AgentID, MaxAttempts: request.MaxAttempts, RetrySafe: request.RetrySafe, Idempotent: request.Idempotent, PayloadHash: strings.ToLower(request.InputHash)})
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("encode canonical execution identity: %w", err)
	}
	canonicalHash := sha256.Sum256(canonicalInput)
	request.InputHash = hex.EncodeToString(canonicalHash[:])
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("begin execution enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	lockKey := fmt.Sprintf("%d:%s:%s", len(request.IdempotencyScope), request.IdempotencyScope, request.IdempotencyKey)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", lockKey); err != nil {
		return execution.Run{}, false, fmt.Errorf("lock execution idempotency key: %w", err)
	}
	var existing execution.Run
	existing, err = scanDurableRun(tx.QueryRow(ctx, `SELECT e.id::text,e.task_id::text,t.goal_id::text,e.agent_id,e.worker_id,
		i.scope,i.idempotency_key,i.input_hash,e.payload,e.result,e.status,e.attempt,e.max_attempts,e.fencing_token,
		COALESCE(e.lease_until,'epoch'::timestamptz),COALESCE(e.heartbeat_at,'epoch'::timestamptz),e.available_at,
		e.retry_safe,e.idempotent,e.cancellation_requested,e.error_class,e.failure_reason,e.state_reason,e.started_at,e.updated_at
		FROM execution_idempotency i JOIN execution_runs e ON e.id=i.execution_run_id JOIN tasks t ON t.id=e.task_id
		WHERE i.scope=$1 AND i.idempotency_key=$2`, request.IdempotencyScope, request.IdempotencyKey))
	if err == nil {
		if existing.InputHash != strings.ToLower(request.InputHash) {
			return execution.Run{}, false, execution.ErrIdempotencyReuse
		}
		if err := tx.Commit(ctx); err != nil {
			return execution.Run{}, false, fmt.Errorf("commit idempotent execution lookup: %w", err)
		}
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return execution.Run{}, false, fmt.Errorf("lookup execution idempotency key: %w", err)
	}
	var goalID, taskStatus string
	err = tx.QueryRow(ctx, `SELECT t.goal_id::text,t.status FROM tasks t WHERE t.id=$1 FOR UPDATE`, request.TaskID).Scan(&goalID, &taskStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Run{}, false, paierrors.ErrNotFound
	}
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("lock execution task: %w", err)
	}
	if taskStatus != "ready" && taskStatus != "retrying" {
		return execution.Run{}, false, fmt.Errorf("task status %q cannot be enqueued", taskStatus)
	}
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM task_dependencies d JOIN tasks predecessor ON predecessor.id=d.depends_on_task_id
		WHERE d.task_id=$1 AND predecessor.status<>'completed')`, request.TaskID).Scan(&blocked); err != nil {
		return execution.Run{}, false, fmt.Errorf("check execution dependencies: %w", err)
	}
	if blocked {
		return execution.Run{}, false, errors.New("task dependencies are not completed")
	}
	var runID string
	err = tx.QueryRow(ctx, `INSERT INTO execution_runs(task_id,status,idempotency_key,input_hash,payload,agent_id,attempt,max_attempts,retry_safe,idempotent,available_at,state_reason,state_changed_at)
		VALUES($1,'queued',$2,$3,$4,$5,1,$6,$7,$8,now(),'execution enqueued',now()) RETURNING id::text,started_at,updated_at`, request.TaskID,
		fmt.Sprintf("durable:%d:%s:%s", len(request.IdempotencyScope), request.IdempotencyScope, request.IdempotencyKey), strings.ToLower(request.InputHash), request.Payload, request.AgentID, request.MaxAttempts, request.RetrySafe, request.Idempotent).Scan(&runID, &existing.CreatedAt, &existing.UpdatedAt)
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("insert queued execution: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO execution_idempotency(scope,idempotency_key,input_hash,execution_run_id) VALUES($1,$2,$3,$4)`, request.IdempotencyScope, request.IdempotencyKey, strings.ToLower(request.InputHash), runID); err != nil {
		return execution.Run{}, false, fmt.Errorf("persist execution idempotency key: %w", err)
	}
	if err := recordExecutionState(ctx, tx, runID, "", string(execution.StatusQueued), "", 0, "enqueued", ""); err != nil {
		return execution.Run{}, false, err
	}
	if err := recordMutation(ctx, tx, LocalOwnerID, "execution.queued", "execution_runs", runID, map[string]any{"task_id": request.TaskID, "agent_id": request.AgentID, "idempotency_scope": request.IdempotencyScope}); err != nil {
		return execution.Run{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return execution.Run{}, false, fmt.Errorf("commit execution enqueue: %w", err)
	}
	run := execution.Run{ID: runID, TaskID: request.TaskID, GoalID: goalID, AgentID: request.AgentID, IdempotencyScope: request.IdempotencyScope, IdempotencyKey: request.IdempotencyKey, InputHash: strings.ToLower(request.InputHash), Payload: append([]byte(nil), request.Payload...), Status: execution.StatusQueued, Attempt: 1, MaxAttempts: request.MaxAttempts, RetrySafe: request.RetrySafe, Idempotent: request.Idempotent, AvailableAt: existing.CreatedAt, CreatedAt: existing.CreatedAt, UpdatedAt: existing.UpdatedAt}
	return run, false, nil
}

func (s *DurableExecutionStore) Claim(ctx context.Context, workerID string, leaseDuration time.Duration) (execution.Run, error) {
	if strings.TrimSpace(workerID) == "" || leaseDuration < time.Second {
		return execution.Run{}, errors.New("worker id and a lease of at least one second are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return execution.Run{}, fmt.Errorf("begin execution claim: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var run execution.Run
	run, err = scanDurableRun(tx.QueryRow(ctx, `SELECT e.id::text,e.task_id::text,t.goal_id::text,e.agent_id,e.worker_id,
		i.scope,i.idempotency_key,i.input_hash,e.payload,e.result,e.status,
		CASE WHEN e.status='retry_wait' THEN e.attempt+1 ELSE e.attempt END,e.max_attempts,e.fencing_token,
		COALESCE(e.lease_until,'epoch'::timestamptz),COALESCE(e.heartbeat_at,'epoch'::timestamptz),e.available_at,
		e.retry_safe,e.idempotent,e.cancellation_requested,e.error_class,e.failure_reason,e.state_reason,e.started_at,e.updated_at
		FROM execution_runs e JOIN tasks t ON t.id=e.task_id JOIN execution_idempotency i ON i.execution_run_id=e.id
		WHERE e.status IN ('queued','retry_wait') AND e.available_at<=now() AND e.attempt<=e.max_attempts
		AND t.status IN ('ready','retrying')
		AND NOT EXISTS (SELECT 1 FROM task_dependencies d JOIN tasks p ON p.id=d.depends_on_task_id WHERE d.task_id=t.id AND p.status<>'completed')
		ORDER BY e.available_at,e.started_at,e.id FOR UPDATE OF e SKIP LOCKED LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Run{}, execution.ErrNoExecution
	}
	if err != nil {
		return execution.Run{}, fmt.Errorf("select execution to claim: %w", err)
	}
	from := string(run.Status)
	err = tx.QueryRow(ctx, `UPDATE execution_runs SET status='claimed',worker_id=$2,claimed_at=now(),heartbeat_at=now(),lease_until=now()+$3::interval,
		fencing_token=fencing_token+1,attempt=CASE WHEN status='retry_wait' THEN attempt+1 ELSE attempt END,state_reason='claimed by worker',state_changed_at=now(),updated_at=now()
		WHERE id=$1 AND status IN ('queued','retry_wait') RETURNING fencing_token,lease_until,heartbeat_at,attempt,updated_at`, run.ID, workerID, leaseDuration.String()).Scan(&run.FencingToken, &run.LeaseUntil, &run.HeartbeatAt, &run.Attempt, &run.UpdatedAt)
	if err != nil {
		return execution.Run{}, fmt.Errorf("persist execution claim: %w", err)
	}
	taskTag, err := tx.Exec(ctx, `UPDATE tasks SET status='running',attempt_count=attempt_count+1,started_at=COALESCE(started_at,now()),updated_at=now() WHERE id=$1 AND status IN ('ready','retrying')`, run.TaskID)
	if err != nil {
		return execution.Run{}, fmt.Errorf("start claimed task: %w", err)
	}
	if taskTag.RowsAffected() != 1 {
		return execution.Run{}, execution.ErrLeaseLost
	}
	if err := recordExecutionState(ctx, tx, run.ID, from, string(execution.StatusClaimed), workerID, run.FencingToken, "worker claim", ""); err != nil {
		return execution.Run{}, err
	}
	if err := recordMutation(ctx, tx, workerID, "execution.claimed", "execution_runs", run.ID, map[string]any{"task_id": run.TaskID, "worker_id": workerID, "fencing_token": run.FencingToken, "lease_until": run.LeaseUntil}); err != nil {
		return execution.Run{}, err
	}
	if err := recordMutation(ctx, tx, workerID, "task.started", "tasks", run.TaskID, map[string]any{"execution_id": run.ID, "worker_id": workerID, "fencing_token": run.FencingToken}); err != nil {
		return execution.Run{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return execution.Run{}, fmt.Errorf("commit execution claim: %w", err)
	}
	run.Status, run.WorkerID = execution.StatusClaimed, workerID
	return run, nil
}

func (s *DurableExecutionStore) Start(ctx context.Context, lease execution.Lease) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin execution start: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var from string
	var taskID string
	err = tx.QueryRow(ctx, `UPDATE execution_runs SET status='running',state_reason='worker started execution',state_changed_at=now(),updated_at=now()
		WHERE id=$1 AND worker_id=$2 AND fencing_token=$3 AND status='claimed' AND lease_until>now() RETURNING task_id::text,'claimed'`, lease.ExecutionID, lease.WorkerID, lease.Fencing).Scan(&taskID, &from)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("start claimed execution: %w", err)
	}
	if err := recordExecutionState(ctx, tx, lease.ExecutionID, from, string(execution.StatusRunning), lease.WorkerID, lease.Fencing, "execution started", ""); err != nil {
		return err
	}
	if err := recordMutation(ctx, tx, lease.WorkerID, "execution.started", "execution_runs", lease.ExecutionID, map[string]any{"task_id": taskID, "fencing_token": lease.Fencing}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *DurableExecutionStore) Heartbeat(ctx context.Context, lease execution.Lease, duration time.Duration) (execution.Lease, error) {
	if duration < time.Second {
		return execution.Lease{}, errors.New("lease duration must be at least one second")
	}
	var updated execution.Lease
	updated.ExecutionID, updated.WorkerID, updated.Fencing = lease.ExecutionID, lease.WorkerID, lease.Fencing
	err := s.pool.QueryRow(ctx, `UPDATE execution_runs SET heartbeat_at=now(),lease_until=now()+$4::interval,updated_at=now()
		WHERE id=$1 AND worker_id=$2 AND fencing_token=$3 AND status='running' AND lease_until>now() AND NOT cancellation_requested
		RETURNING lease_until`, lease.ExecutionID, lease.WorkerID, lease.Fencing, duration.String()).Scan(&updated.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Lease{}, execution.ErrLeaseLost
	}
	if err != nil {
		return execution.Lease{}, fmt.Errorf("extend execution lease: %w", err)
	}
	return updated, nil
}

func (s *DurableExecutionStore) SaveCheckpoint(ctx context.Context, lease execution.Lease, checkpoint execution.Checkpoint) (execution.Checkpoint, error) {
	if checkpoint.StepID == "" || checkpoint.SchemaVersion < 1 || checkpoint.StateReference != "" && strings.ContainsAny(checkpoint.StateReference, "\r\n\x00") {
		return execution.Checkpoint{}, errors.New("checkpoint metadata is invalid")
	}
	if len(checkpoint.Progress) == 0 {
		checkpoint.Progress = []byte("{}")
	}
	progress := json.RawMessage(checkpoint.Progress)
	completed, err := json.Marshal(checkpoint.CompletedSteps)
	if err != nil {
		return execution.Checkpoint{}, fmt.Errorf("encode checkpoint completed steps: %w", err)
	}
	if len(progress) > 64*1024 || len(checkpoint.CompletedSteps) > 1024 || !json.Valid(progress) {
		return execution.Checkpoint{}, errors.New("checkpoint progress must be valid JSON, <=64 KiB and contain at most 1024 completed steps; store large data as external references")
	}
	var decodedProgress any
	if err := json.Unmarshal(progress, &decodedProgress); err != nil {
		return execution.Checkpoint{}, fmt.Errorf("decode checkpoint progress: %w", err)
	}
	if containsSensitiveCheckpointField(decodedProgress) {
		return execution.Checkpoint{}, errors.New("checkpoint progress contains a sensitive field; secrets must remain in a secret store")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return execution.Checkpoint{}, fmt.Errorf("begin checkpoint write: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var runStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM execution_runs WHERE id=$1 AND worker_id=$2 AND fencing_token=$3 AND status='running' AND lease_until>now() FOR UPDATE`, lease.ExecutionID, lease.WorkerID, lease.Fencing).Scan(&runStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Checkpoint{}, execution.ErrLeaseLost
	}
	if err != nil {
		return execution.Checkpoint{}, fmt.Errorf("verify checkpoint lease: %w", err)
	}
	var latest int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(max(sequence),0) FROM execution_checkpoints WHERE execution_run_id=$1`, lease.ExecutionID).Scan(&latest)
	if err != nil {
		return execution.Checkpoint{}, fmt.Errorf("read checkpoint sequence: %w", err)
	}
	if checkpoint.Sequence == 0 {
		checkpoint.Sequence = latest + 1
	}
	if checkpoint.Sequence != latest+1 {
		return execution.Checkpoint{}, fmt.Errorf("checkpoint sequence %d must follow %d", checkpoint.Sequence, latest)
	}
	checkpoint.ExecutionID, checkpoint.FencingToken = lease.ExecutionID, lease.Fencing
	err = tx.QueryRow(ctx, `INSERT INTO execution_checkpoints(execution_run_id,sequence,step_id,progress,completed_steps,state_reference,schema_version,fencing_token)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`, lease.ExecutionID, checkpoint.Sequence, checkpoint.StepID, progress, completed, checkpoint.StateReference, checkpoint.SchemaVersion, lease.Fencing).Scan(&checkpoint.CreatedAt)
	if err != nil {
		return execution.Checkpoint{}, fmt.Errorf("insert execution checkpoint: %w", err)
	}
	if err := recordMutation(ctx, tx, lease.WorkerID, "execution.checkpoint_saved", "execution_runs", lease.ExecutionID, map[string]any{"sequence": checkpoint.Sequence, "step_id": checkpoint.StepID, "fencing_token": lease.Fencing, "state_reference": checkpoint.StateReference}); err != nil {
		return execution.Checkpoint{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return execution.Checkpoint{}, fmt.Errorf("commit execution checkpoint: %w", err)
	}
	return checkpoint, nil
}

func containsSensitiveCheckpointField(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			name := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
			for _, sensitive := range []string{"password", "passwd", "secret", "api_key", "access_token", "refresh_token", "authorization", "credential", "private_key"} {
				if strings.Contains(name, sensitive) {
					return true
				}
			}
			if containsSensitiveCheckpointField(item) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if containsSensitiveCheckpointField(item) {
				return true
			}
		}
	}
	return false
}

func (s *DurableExecutionStore) LatestCheckpoint(ctx context.Context, executionID string) (execution.Checkpoint, error) {
	var checkpoint execution.Checkpoint
	var progress, completed []byte
	err := s.pool.QueryRow(ctx, `SELECT execution_run_id::text,sequence,step_id,progress,completed_steps,state_reference,schema_version,fencing_token,created_at
		FROM execution_checkpoints WHERE execution_run_id=$1 ORDER BY sequence DESC LIMIT 1`, executionID).Scan(&checkpoint.ExecutionID, &checkpoint.Sequence, &checkpoint.StepID, &progress, &completed, &checkpoint.StateReference, &checkpoint.SchemaVersion, &checkpoint.FencingToken, &checkpoint.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Checkpoint{}, execution.ErrNoExecution
	}
	if err != nil {
		return execution.Checkpoint{}, fmt.Errorf("load latest execution checkpoint: %w", err)
	}
	checkpoint.Progress = append([]byte(nil), progress...)
	if err := json.Unmarshal(completed, &checkpoint.CompletedSteps); err != nil {
		return execution.Checkpoint{}, fmt.Errorf("decode checkpoint steps: %w", err)
	}
	return checkpoint, nil
}

func (s *DurableExecutionStore) Complete(ctx context.Context, lease execution.Lease, result []byte, evidence []execution.Evidence, verified bool) error {
	if !verified {
		return errors.New("execution result is not independently verified")
	}
	if len(evidence) == 0 || len(result) == 0 || !json.Valid(result) {
		return errors.New("execution completion requires valid JSON result and evidence")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin execution completion: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var taskID, agentID string
	err = tx.QueryRow(ctx, `UPDATE execution_runs SET status='succeeded',result=$4,completed_at=now(),worker_id=NULL,lease_until=NULL,
		heartbeat_at=now(),failure_reason='',error_class='',state_reason='verified execution completed',state_changed_at=now(),updated_at=now()
		WHERE id=$1 AND worker_id=$2 AND fencing_token=$3 AND status='running' AND lease_until>now() AND NOT cancellation_requested RETURNING task_id::text,agent_id`, lease.ExecutionID, lease.WorkerID, lease.Fencing, result).Scan(&taskID, &agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("persist execution success: %w", err)
	}
	for _, item := range evidence {
		if strings.TrimSpace(item.Source) == "" || strings.TrimSpace(item.Description) == "" {
			return errors.New("execution evidence source and description are required")
		}
		observedAt := item.ObservedAt.UTC()
		if item.ObservedAt.IsZero() {
			observedAt = time.Now().UTC()
		}
		if _, err := tx.Exec(ctx, `INSERT INTO execution_evidence(execution_run_id,source,description,reference,observed_at) VALUES($1,$2,$3,$4,$5)`, lease.ExecutionID, item.Source, item.Description, item.Reference, observedAt); err != nil {
			return fmt.Errorf("persist execution evidence: %w", err)
		}
	}
	taskTag, err := tx.Exec(ctx, `UPDATE tasks SET status='completed',completed_at=now(),updated_at=now() WHERE id=$1 AND status='running'`, taskID)
	if err != nil {
		return fmt.Errorf("complete task from execution: %w", err)
	}
	if taskTag.RowsAffected() != 1 {
		return fmt.Errorf("complete task from execution: %w", execution.ErrLeaseLost)
	}
	var modelUsage struct {
		ModelCalls   int  `json:"model_calls"`
		ToolCalls    int  `json:"tool_calls"`
		InputTokens  int  `json:"input_tokens"`
		OutputTokens int  `json:"output_tokens"`
		TotalTokens  int  `json:"total_tokens"`
		Verified     bool `json:"verified"`
	}
	if err := json.Unmarshal(result, &modelUsage); err != nil {
		return fmt.Errorf("decode execution model usage: %w", err)
	}
	if !modelUsage.Verified || modelUsage.ModelCalls < 0 || modelUsage.ToolCalls < 0 || modelUsage.InputTokens < 0 || modelUsage.OutputTokens < 0 || modelUsage.TotalTokens < modelUsage.InputTokens+modelUsage.OutputTokens {
		return errors.New("execution result contains invalid model usage")
	}
	if modelUsage.ModelCalls > 0 || modelUsage.ToolCalls > 0 || modelUsage.TotalTokens > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO model_usage(goal_id,provider,model,operation,model_calls,tool_calls,input_tokens,output_tokens,total_tokens)
			SELECT goal_id,'agent',$2,'agent-execution',$3,$4,$5,$6,$7 FROM tasks WHERE id=$1`, taskID, agentID, modelUsage.ModelCalls, modelUsage.ToolCalls, modelUsage.InputTokens, modelUsage.OutputTokens, modelUsage.TotalTokens); err != nil {
			return fmt.Errorf("persist durable execution model usage: %w", err)
		}
	}
	if err := recordExecutionState(ctx, tx, lease.ExecutionID, string(execution.StatusRunning), string(execution.StatusSucceeded), lease.WorkerID, lease.Fencing, "verified result and evidence persisted", ""); err != nil {
		return err
	}
	if err := recordMutation(ctx, tx, lease.WorkerID, "execution.succeeded", "execution_runs", lease.ExecutionID, map[string]any{"task_id": taskID, "fencing_token": lease.Fencing, "evidence_count": len(evidence)}); err != nil {
		return err
	}
	if err := recordMutation(ctx, tx, lease.WorkerID, "task.completed", "tasks", taskID, map[string]any{"execution_id": lease.ExecutionID, "fencing_token": lease.Fencing}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit execution completion: %w", err)
	}
	return nil
}

func (s *DurableExecutionStore) Fail(ctx context.Context, lease execution.Lease, class execution.FailureClass, reason string, delay time.Duration) error {
	var next execution.RunStatus
	var taskStatus string
	switch class {
	case execution.FailureRetryable:
		next, taskStatus = execution.StatusRetryWait, "retrying"
	case execution.FailurePermanent:
		next, taskStatus = execution.StatusFailed, "failed"
	case execution.FailureUncertain:
		next, taskStatus = execution.StatusUnknown, "waiting_external"
	case execution.FailureCancelled:
		next, taskStatus = execution.StatusCancelled, "cancelled"
	case execution.FailureApprovalNeeded:
		next, taskStatus = execution.StatusWaitingApproval, "waiting_approval"
	default:
		return fmt.Errorf("unsupported failure class %q", class)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin execution failure: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var taskID string
	var from execution.RunStatus
	var attempts, maxAttempts int
	var retrySafe, idempotent bool
	var cancellationRequested bool
	err = tx.QueryRow(ctx, `SELECT task_id::text,status,attempt,max_attempts,retry_safe,idempotent,cancellation_requested FROM execution_runs WHERE id=$1 AND worker_id=$2 AND fencing_token=$3 AND status IN ('claimed','running') AND lease_until>now() FOR UPDATE`, lease.ExecutionID, lease.WorkerID, lease.Fencing).Scan(&taskID, &from, &attempts, &maxAttempts, &retrySafe, &idempotent, &cancellationRequested)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("verify failed execution lease: %w", err)
	}
	if cancellationRequested {
		class, next, taskStatus = execution.FailureUncertain, execution.StatusUnknown, "waiting_external"
		reason = "execution cancellation requested while an external outcome may be in flight"
	}
	if !cancellationRequested && class == execution.FailureRetryable && (!retrySafe || attempts >= maxAttempts) {
		next, taskStatus, class = execution.StatusFailed, "failed", execution.FailurePermanent
	}
	if !cancellationRequested && class == execution.FailureUncertain {
		if retrySafe && idempotent && attempts < maxAttempts {
			// Only an explicit idempotency guarantee allows replay after an uncertain outcome.
			next, taskStatus, class = execution.StatusRetryWait, "retrying", execution.FailureSafeResume
		} else {
			next, taskStatus = execution.StatusUnknown, "waiting_external"
		}
	}
	if !cancellationRequested && class == execution.FailureApprovalNeeded {
		var alreadyApproved bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tool_invocations i JOIN approvals a ON a.id=i.approval_id
			WHERE i.execution_id=$1 AND i.status='waiting_approval' AND a.status='approved' AND a.consumed_at IS NULL AND a.expires_at>now())`, lease.ExecutionID).Scan(&alreadyApproved); err != nil {
			return fmt.Errorf("check approval before waiting transition: %w", err)
		}
		if alreadyApproved {
			next, taskStatus, class = execution.StatusQueued, "retrying", execution.FailureRecovered
			reason = "Tool approval was granted while the Worker was unwinding; execution queued for continuation"
		}
	}
	if delay < 0 {
		delay = 0
	}
	_, err = tx.Exec(ctx, `UPDATE execution_runs SET status=$4,available_at=CASE WHEN $4='retry_wait' THEN now()+$5::interval ELSE available_at END,
		worker_id=NULL,lease_until=NULL,failure_reason=$6,error_class=$7,state_reason=$6,state_changed_at=now(),updated_at=now()
		WHERE id=$1 AND worker_id=$2 AND fencing_token=$3`, lease.ExecutionID, lease.WorkerID, lease.Fencing, next, delay.String(), reason, class)
	if err != nil {
		return fmt.Errorf("persist execution failure state: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE tasks SET status=$2,updated_at=now() WHERE id=$1 AND status IN ('running','waiting_approval','waiting_external')`, taskID, taskStatus); err != nil {
		return fmt.Errorf("persist failed task state: %w", err)
	}
	if err := recordExecutionState(ctx, tx, lease.ExecutionID, string(from), string(next), lease.WorkerID, lease.Fencing, reason, class); err != nil {
		return err
	}
	if err := recordMutation(ctx, tx, lease.WorkerID, "execution."+string(next), "execution_runs", lease.ExecutionID, map[string]any{"task_id": taskID, "fencing_token": lease.Fencing, "error_class": class, "reason": reason}); err != nil {
		return err
	}
	if err := recordMutation(ctx, tx, lease.WorkerID, "task."+taskStatus, "tasks", taskID, map[string]any{"execution_id": lease.ExecutionID, "error_class": class, "reason": reason}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit execution failure: %w", err)
	}
	return nil
}

func (s *DurableExecutionStore) RequestCancel(ctx context.Context, executionID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin execution cancellation request: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var taskID string
	var status execution.RunStatus
	err = tx.QueryRow(ctx, `SELECT task_id::text,status FROM execution_runs WHERE id=$1 FOR UPDATE`, executionID).Scan(&taskID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return paierrors.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load execution to cancel: %w", err)
	}
	if terminalExecutionStatus(status) {
		return fmt.Errorf("execution is terminal: %s", status)
	}
	if status == execution.StatusWaitingExternal {
		return execution.ErrUncertainOutcome
	}
	if status == execution.StatusQueued || status == execution.StatusRetryWait || status == execution.StatusPending || status == execution.StatusWaitingApproval {
		_, err = tx.Exec(ctx, `UPDATE execution_runs SET status='cancelled',worker_id=NULL,lease_until=NULL,cancellation_requested=true,state_reason='cancelled before execution',state_changed_at=now(),updated_at=now() WHERE id=$1`, executionID)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE tasks SET status='cancelled',updated_at=now() WHERE id=$1 AND status IN ('created','ready','retrying','waiting_approval','waiting_external')`, taskID)
		}
	} else if status == execution.StatusClaimed {
		_, err = tx.Exec(ctx, `UPDATE execution_runs SET status='cancelled',worker_id=NULL,lease_until=NULL,cancellation_requested=true,state_reason='claimed execution cancelled before start',state_changed_at=now(),updated_at=now() WHERE id=$1`, executionID)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE tasks SET status='cancelled',updated_at=now() WHERE id=$1 AND status='running'`, taskID)
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE execution_runs SET cancellation_requested=true,state_reason='cancellation requested; outcome may be uncertain',state_changed_at=now(),updated_at=now() WHERE id=$1`, executionID)
	}
	if err != nil {
		return fmt.Errorf("persist execution cancellation request: %w", err)
	}
	if status == execution.StatusQueued || status == execution.StatusRetryWait || status == execution.StatusPending || status == execution.StatusClaimed || status == execution.StatusWaitingApproval {
		if err := recordExecutionState(ctx, tx, executionID, string(status), string(execution.StatusCancelled), "", 0, "cancelled before safe execution", execution.FailureCancelled); err != nil {
			return err
		}
		if err := recordMutation(ctx, tx, LocalOwnerID, "task.cancelled", "tasks", taskID, map[string]any{"execution_id": executionID}); err != nil {
			return err
		}
	}
	if err := recordMutation(ctx, tx, "system", "execution.cancel_requested", "execution_runs", executionID, map[string]any{"task_id": taskID, "status": status}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit execution cancellation: %w", err)
	}
	return nil
}

func (s *DurableExecutionStore) CancelTask(ctx context.Context, taskID string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin durable Task cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var runID string
	var status execution.RunStatus
	err = tx.QueryRow(ctx, `SELECT id::text,status FROM execution_runs WHERE task_id=$1 AND status IN ('pending','queued','claimed','running','retry_wait','waiting_approval','waiting_external','unknown') ORDER BY started_at DESC LIMIT 1 FOR UPDATE`, taskID).Scan(&runID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit Task cancellation lookup: %w", err)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find active Task execution: %w", err)
	}
	if status == execution.StatusUnknown || status == execution.StatusWaitingExternal {
		return false, execution.ErrUncertainOutcome
	}
	if status == execution.StatusClaimed {
		if _, err := tx.Exec(ctx, `UPDATE execution_runs SET status='cancelled',worker_id=NULL,lease_until=NULL,cancellation_requested=true,state_reason='claimed execution cancelled before executor start',state_changed_at=now(),updated_at=now() WHERE id=$1`, runID); err != nil {
			return false, fmt.Errorf("cancel claimed execution: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks SET status='cancelled',updated_at=now() WHERE id=$1 AND status='running'`, taskID); err != nil {
			return false, fmt.Errorf("cancel claimed Task: %w", err)
		}
		if err := recordExecutionState(ctx, tx, runID, string(status), string(execution.StatusCancelled), "", 0, "claimed execution cancelled before executor start", execution.FailureCancelled); err != nil {
			return false, err
		}
		if err := recordMutation(ctx, tx, LocalOwnerID, "task.cancelled", "tasks", taskID, map[string]any{"execution_id": runID}); err != nil {
			return false, err
		}
	} else if status == execution.StatusRunning {
		if _, err := tx.Exec(ctx, `UPDATE execution_runs SET cancellation_requested=true,state_reason='Task cancellation requested; in-flight outcome may be uncertain',state_changed_at=now(),updated_at=now() WHERE id=$1`, runID); err != nil {
			return false, fmt.Errorf("request active execution cancellation: %w", err)
		}
		if err := recordMutation(ctx, tx, LocalOwnerID, "execution.cancel_requested", "execution_runs", runID, map[string]any{"task_id": taskID, "status": status}); err != nil {
			return false, err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE execution_runs SET status='cancelled',worker_id=NULL,lease_until=NULL,cancellation_requested=true,state_reason='cancelled before safe execution',state_changed_at=now(),updated_at=now() WHERE id=$1`, runID); err != nil {
			return false, fmt.Errorf("cancel unstarted execution: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks SET status='cancelled',updated_at=now() WHERE id=$1 AND status IN ('created','ready','retrying','running','waiting_approval','waiting_external')`, taskID); err != nil {
			return false, fmt.Errorf("cancel unstarted Task: %w", err)
		}
		if err := recordExecutionState(ctx, tx, runID, string(status), string(execution.StatusCancelled), "", 0, "cancelled before safe execution", execution.FailureCancelled); err != nil {
			return false, err
		}
		if err := recordMutation(ctx, tx, LocalOwnerID, "task.cancelled", "tasks", taskID, map[string]any{"execution_id": runID}); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit durable Task cancellation: %w", err)
	}
	return true, nil
}

func (s *DurableExecutionStore) CancelClaimed(ctx context.Context, lease execution.Lease, reason string) error {
	return s.Fail(ctx, lease, execution.FailureCancelled, reason, 0)
}

func (s *DurableExecutionStore) RecoverExpired(ctx context.Context, limit int, retryDelay time.Duration) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	if retryDelay < 0 {
		retryDelay = 0
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin expired execution recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(ctx, `SELECT e.id::text,e.task_id::text,e.status,e.worker_id,e.fencing_token,e.attempt,e.max_attempts,e.retry_safe,e.idempotent,e.cancellation_requested,
		EXISTS(SELECT 1 FROM tool_invocations i WHERE i.execution_id=e.id AND i.status IN ('running','waiting_approval'))
		FROM execution_runs e WHERE e.status IN ('claimed','running') AND e.lease_until<=now()
		ORDER BY e.lease_until,e.id FOR UPDATE OF e SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return 0, fmt.Errorf("select expired executions: %w", err)
	}
	type expired struct {
		id, taskID                    string
		status                        execution.RunStatus
		worker                        *string
		fence                         int64
		attempt, max                  int
		retrySafe, idempotent, cancel bool
		hasActiveTool                 bool
	}
	var expiredRuns []expired
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.id, &item.taskID, &item.status, &item.worker, &item.fence, &item.attempt, &item.max, &item.retrySafe, &item.idempotent, &item.cancel, &item.hasActiveTool); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired execution: %w", err)
		}
		expiredRuns = append(expiredRuns, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate expired executions: %w", err)
	}
	rows.Close()
	for _, item := range expiredRuns {
		next := execution.StatusUnknown
		taskStatus := "waiting_external"
		reason := "lease expired; execution outcome is uncertain"
		class := execution.FailureUncertain
		if item.hasActiveTool {
			reason = "lease expired with an active Tool invocation; external outcome requires reconciliation"
		} else if item.cancel {
			reason = "lease expired after cancellation request; outcome is uncertain"
		} else if item.retrySafe && item.status == execution.StatusClaimed {
			next, taskStatus, reason, class = execution.StatusRetryWait, "retrying", "lease expired before executor start; no side effect occurred", execution.FailureSafeRetry
		} else if item.retrySafe && item.idempotent && item.status == execution.StatusRunning && item.attempt < item.max {
			// A running operation may have completed an external side effect before lease loss; retry only when its input declares a strong idempotency guarantee.
			next, taskStatus, reason, class = execution.StatusRetryWait, "retrying", "lease expired during an explicitly idempotent execution", execution.FailureSafeResume
		}
		if _, err := tx.Exec(ctx, `UPDATE execution_runs SET status=$2,worker_id=NULL,lease_until=NULL,heartbeat_at=now(),available_at=CASE WHEN $2='retry_wait' THEN now()+$3::interval ELSE available_at END,
			failure_reason=$4,error_class=$5,state_reason=$4,state_changed_at=now(),updated_at=now() WHERE id=$1`, item.id, next, retryDelay.String(), reason, class); err != nil {
			return 0, fmt.Errorf("recover expired execution %s: %w", item.id, err)
		}
		if item.hasActiveTool {
			approvalRows, err := tx.Query(ctx, `UPDATE approvals SET status='revoked' WHERE id IN (
				SELECT approval_id FROM tool_invocations WHERE execution_id=$1 AND status='waiting_approval' AND approval_id IS NOT NULL)
				AND status IN ('pending','approved') AND consumed_at IS NULL RETURNING id::text`, item.id)
			if err != nil {
				return 0, fmt.Errorf("revoke approvals for expired Tool invocations %s: %w", item.id, err)
			}
			var revokedApprovalIDs []string
			for approvalRows.Next() {
				var approvalID string
				if err := approvalRows.Scan(&approvalID); err != nil {
					approvalRows.Close()
					return 0, fmt.Errorf("scan revoked Tool approval: %w", err)
				}
				revokedApprovalIDs = append(revokedApprovalIDs, approvalID)
			}
			if err := approvalRows.Err(); err != nil {
				approvalRows.Close()
				return 0, fmt.Errorf("iterate revoked Tool approvals: %w", err)
			}
			approvalRows.Close()
			for _, approvalID := range revokedApprovalIDs {
				if err := recordMutation(ctx, tx, "recovery-scanner", "approval.revoked", "approvals", approvalID, map[string]any{"execution_id": item.id, "reason": reason}); err != nil {
					return 0, err
				}
			}
			invocationRows, err := tx.Query(ctx, `UPDATE tool_invocations SET status=CASE WHEN status='waiting_approval' THEN 'cancelled' ELSE 'unknown' END,
				error_class='lease_expired',failure_reason=$2,completed_at=now(),updated_at=now() WHERE execution_id=$1 AND status IN ('running','waiting_approval') RETURNING id::text,status`, item.id, reason)
			if err != nil {
				return 0, fmt.Errorf("mark expired Tool invocations unknown %s: %w", item.id, err)
			}
			type recoveredInvocation struct{ id, status string }
			var recoveredInvocations []recoveredInvocation
			for invocationRows.Next() {
				var invocationID, invocationStatus string
				if err := invocationRows.Scan(&invocationID, &invocationStatus); err != nil {
					invocationRows.Close()
					return 0, fmt.Errorf("scan recovered Tool invocation: %w", err)
				}
				recoveredInvocations = append(recoveredInvocations, recoveredInvocation{id: invocationID, status: invocationStatus})
			}
			if err := invocationRows.Err(); err != nil {
				invocationRows.Close()
				return 0, fmt.Errorf("iterate recovered Tool invocations: %w", err)
			}
			invocationRows.Close()
			for _, invocation := range recoveredInvocations {
				if err := recordMutation(ctx, tx, "recovery-scanner", "tool.invocation_"+invocation.status, "tool_invocations", invocation.id, map[string]any{"execution_id": item.id, "reason": reason}); err != nil {
					return 0, err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks SET status=$2,updated_at=now() WHERE id=$1 AND status IN ('running','retrying')`, item.taskID, taskStatus); err != nil {
			return 0, fmt.Errorf("recover expired task %s: %w", item.taskID, err)
		}
		workerID := ""
		if item.worker != nil {
			workerID = *item.worker
		}
		if err := recordExecutionState(ctx, tx, item.id, string(item.status), string(next), workerID, item.fence, reason, class); err != nil {
			return 0, err
		}
		if err := recordMutation(ctx, tx, "recovery-scanner", "execution.recovered", "execution_runs", item.id, map[string]any{"task_id": item.taskID, "previous_worker": workerID, "fencing_token": item.fence, "status": next, "reason": reason}); err != nil {
			return 0, err
		}
		if err := recordMutation(ctx, tx, "recovery-scanner", "task."+taskStatus, "tasks", item.taskID, map[string]any{"execution_id": item.id, "status": taskStatus, "reason": reason}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit expired execution recovery: %w", err)
	}
	return len(expiredRuns), nil
}

func (s *DurableExecutionStore) Get(ctx context.Context, id string) (execution.Run, error) {
	run, err := scanDurableRun(s.pool.QueryRow(ctx, `SELECT e.id::text,e.task_id::text,t.goal_id::text,e.agent_id,e.worker_id,
		i.scope,i.idempotency_key,i.input_hash,e.payload,e.result,e.status,e.attempt,e.max_attempts,e.fencing_token,
		COALESCE(e.lease_until,'epoch'::timestamptz),COALESCE(e.heartbeat_at,'epoch'::timestamptz),e.available_at,
		e.retry_safe,e.idempotent,e.cancellation_requested,e.error_class,e.failure_reason,e.state_reason,e.started_at,e.updated_at
		FROM execution_runs e JOIN tasks t ON t.id=e.task_id LEFT JOIN execution_idempotency i ON i.execution_run_id=e.id WHERE e.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Run{}, paierrors.ErrNotFound
	}
	if err != nil {
		return execution.Run{}, fmt.Errorf("get durable execution: %w", err)
	}
	return run, nil
}

func (s *DurableExecutionStore) ResumeApproval(ctx context.Context, userID, approvalID string) error {
	return (&Postgres{pool: s.pool}).ResumeApproval(ctx, userID, approvalID)
}

func (s *DurableExecutionStore) VerifiedTaskIDs(ctx context.Context, goalID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT e.task_id::text FROM execution_runs e JOIN tasks t ON t.id=e.task_id
		JOIN execution_evidence evidence ON evidence.execution_run_id=e.id
		WHERE t.goal_id=$1 AND e.status='succeeded' ORDER BY e.task_id::text`, goalID)
	if err != nil {
		return nil, fmt.Errorf("list verified durable task executions: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var taskID string
		if err := rows.Scan(&taskID); err != nil {
			return nil, fmt.Errorf("scan verified durable task id: %w", err)
		}
		ids = append(ids, taskID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verified durable task ids: %w", err)
	}
	return ids, nil
}

func scanDurableRun(row interface{ Scan(...any) error }) (execution.Run, error) {
	var run execution.Run
	var worker, scope, key *string
	var status, errorClass string
	var reason, stateReason string
	var payload, result []byte
	err := row.Scan(&run.ID, &run.TaskID, &run.GoalID, &run.AgentID, &worker, &scope, &key, &run.InputHash, &payload, &result, &status,
		&run.Attempt, &run.MaxAttempts, &run.FencingToken, &run.LeaseUntil, &run.HeartbeatAt, &run.AvailableAt,
		&run.RetrySafe, &run.Idempotent, &run.CancellationRequest, &errorClass, &reason, &stateReason, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		return execution.Run{}, err
	}
	run.WorkerID = stringValue(worker)
	run.IdempotencyScope = stringValue(scope)
	run.IdempotencyKey = stringValue(key)
	run.Payload = append([]byte(nil), payload...)
	run.Result = append([]byte(nil), result...)
	run.Status = execution.RunStatus(status)
	run.FailureClass = execution.FailureClass(errorClass)
	run.ErrorClass = errorClass
	run.FailureReason = reason
	run.StateReason = stateReason
	return run, nil
}

func recordExecutionState(ctx context.Context, tx pgx.Tx, runID, from, to string, worker string, fencing int64, reason string, class execution.FailureClass) error {
	_, err := tx.Exec(ctx, `INSERT INTO execution_state_history(execution_run_id,from_status,to_status,worker_id,fencing_token,reason,error_class)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, runID, from, to, worker, fencing, reason, class)
	if err != nil {
		return fmt.Errorf("record execution state transition: %w", err)
	}
	return nil
}

func isHexHash(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func terminalExecutionStatus(status execution.RunStatus) bool {
	switch status {
	case execution.StatusSucceeded, execution.StatusFailed, execution.StatusCancelled, execution.StatusUnknown:
		return true
	default:
		return false
	}
}

var _ execution.Repository = (*DurableExecutionStore)(nil)
