// English: This adapter persists idempotent Tool invocations and consumes exact-context approvals transactionally.
// 中文：本适配器持久化幂等工具调用，并在事务中消费精确上下文审批。
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/tool/gateway"
	"personal-ai-os/internal/tool/registry"
)

func (p *Postgres) RequestApproval(ctx context.Context, request gateway.Request, definition registry.Definition, inputHash string) (gateway.InvocationResult, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return gateway.InvocationResult{}, fmt.Errorf("begin tool approval request: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockToolIdempotency(ctx, tx, request); err != nil {
		return gateway.InvocationResult{}, err
	}
	ownerID, err := validateToolLease(ctx, tx, request)
	if err != nil {
		return gateway.InvocationResult{}, err
	}
	var existingID, existingHash, existingStatus, linkedApprovalID, goalID, taskID, stepID, agentID, providerID, existingToolID, version string
	var approvalStatus string
	var consumedAt sql.NullTime
	var existingExpiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT i.id::text,i.input_hash,i.status,COALESCE(i.approval_id::text,''),i.goal_id::text,i.task_id::text,i.step_id,i.agent_id,
		i.provider_id,i.tool_id,i.tool_version,COALESCE(a.status,''),a.consumed_at,COALESCE(a.expires_at,'epoch'::timestamptz)
		FROM tool_invocations i LEFT JOIN approvals a ON a.id=i.approval_id WHERE i.execution_id=$1 AND i.idempotency_key=$2 FOR UPDATE OF i`, request.ExecutionID, request.IdempotencyKey).Scan(
		&existingID, &existingHash, &existingStatus, &linkedApprovalID, &goalID, &taskID, &stepID, &agentID, &providerID, &existingToolID, &version, &approvalStatus, &consumedAt, &existingExpiresAt)
	if err == nil {
		if existingHash != inputHash || goalID != request.GoalID || taskID != request.TaskID || stepID != request.StepID || agentID != request.AgentID || providerID != definition.Identity.ProviderID || existingToolID != definition.Identity.ToolID || version != definition.Identity.Version {
			return gateway.InvocationResult{}, errors.New("tool idempotency key reused with different input")
		}
		if existingStatus == "waiting_approval" {
			if linkedApprovalID == "" {
				return gateway.InvocationResult{}, errors.New("waiting Tool invocation has no approval link")
			}
			if err := tx.Commit(ctx); err != nil {
				return gateway.InvocationResult{}, err
			}
			if approvalStatus == "pending" {
				return gateway.InvocationResult{InvocationID: existingID, Status: existingStatus, ApprovalID: linkedApprovalID}, nil
			}
			if approvalStatus == "approved" && !consumedAt.Valid && existingExpiresAt.After(time.Now().UTC()) {
				return gateway.InvocationResult{InvocationID: existingID, Status: "approved", ApprovalID: linkedApprovalID}, nil
			}
			return gateway.InvocationResult{InvocationID: existingID, Status: string(approvalStatus), ApprovalID: linkedApprovalID}, errors.New("Tool approval is no longer executable")
		}
		return gateway.InvocationResult{InvocationID: existingID, Status: existingStatus}, errors.New("tool invocation already exists and is not awaiting approval")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return gateway.InvocationResult{}, fmt.Errorf("lookup pending tool invocation: %w", err)
	}
	contextHash := toolContextHash(request, definition.Identity)
	toolID := qualifiedToolID(definition.Identity)
	var approvalID, invocationID string
	expiresAt := time.Now().UTC().Add(15 * time.Minute)
	if err := tx.QueryRow(ctx, `INSERT INTO approvals(user_id,task_id,tool_id,action,parameters_hash,risk_level,status,expires_at,tool_version,execution_id,step_id,agent_id,invocation_context_hash)
		VALUES($1,$2,$3,$4,$5,$6,'pending',$7,$8,$9,$10,$11,$12) RETURNING id::text`, ownerID, request.TaskID, toolID, definition.Identity.ToolID, inputHash, definition.RiskLevel, expiresAt, definition.Identity.Version, request.ExecutionID, request.StepID, request.AgentID, contextHash).Scan(&approvalID); err != nil {
		return gateway.InvocationResult{}, fmt.Errorf("insert context-bound approval: %w", err)
	}
	args := json.RawMessage(request.Arguments)
	if _, err := tx.Exec(ctx, `INSERT INTO tool_invocations(goal_id,task_id,execution_id,step_id,agent_id,worker_id,fencing_token,provider_id,tool_id,tool_version,idempotency_key,input_hash,arguments,status,approval_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'waiting_approval',$14)`, request.GoalID, request.TaskID, request.ExecutionID, request.StepID, request.AgentID, request.Lease.WorkerID, request.Lease.Fencing, definition.Identity.ProviderID, definition.Identity.ToolID, definition.Identity.Version, request.IdempotencyKey, inputHash, args, approvalID); err != nil {
		return gateway.InvocationResult{}, fmt.Errorf("persist waiting tool invocation: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM tool_invocations WHERE execution_id=$1 AND idempotency_key=$2`, request.ExecutionID, request.IdempotencyKey).Scan(&invocationID); err != nil {
		return gateway.InvocationResult{}, fmt.Errorf("load waiting tool invocation: %w", err)
	}
	if err := recordMutation(ctx, tx, ownerID, "approval.requested", "approvals", approvalID, map[string]any{"tool_invocation_id": invocationID, "tool_id": toolID, "tool_version": definition.Identity.Version, "execution_id": request.ExecutionID, "step_id": request.StepID}); err != nil {
		return gateway.InvocationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return gateway.InvocationResult{}, fmt.Errorf("commit tool approval request: %w", err)
	}
	return gateway.InvocationResult{InvocationID: invocationID, Status: "waiting_approval", ApprovalID: approvalID}, nil
}

func (p *Postgres) Verify(ctx context.Context, request gateway.Request) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tool lease verification: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := validateToolLease(ctx, tx, request); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tool lease verification: %w", err)
	}
	return nil
}

// ResumeApproval requeues a waiting durable run only when its exact context-bound approval remains valid.
// 中文：仅当精确上下文绑定的审批仍有效时，ResumeApproval 才会重新排队等待中的持久化执行。
func (p *Postgres) ResumeApproval(ctx context.Context, userID, approvalID string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin approved Tool continuation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var executionID, status string
	var consumedAt sql.NullTime
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT COALESCE(execution_id::text,''),status,consumed_at,expires_at FROM approvals WHERE id=$1 AND user_id=$2 FOR UPDATE`, approvalID, userID).Scan(&executionID, &status, &consumedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("approval not found")
	}
	if err != nil {
		return fmt.Errorf("load approval continuation: %w", err)
	}
	if executionID == "" {
		return nil
	}
	if status != "approved" || consumedAt.Valid || !expiresAt.After(time.Now().UTC()) {
		return errors.New("approved Tool request is expired, consumed, or no longer approved")
	}
	var taskID, runStatus, invocationStatus string
	err = tx.QueryRow(ctx, `SELECT e.task_id::text,e.status,i.status FROM execution_runs e JOIN tool_invocations i ON i.execution_id=e.id
		WHERE e.id=$1 AND i.approval_id=$2 FOR UPDATE OF e,i`, executionID, approvalID).Scan(&taskID, &runStatus, &invocationStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("approval is not linked to a persisted Tool invocation")
	}
	if err != nil {
		return fmt.Errorf("load approved Tool execution: %w", err)
	}
	if invocationStatus != "waiting_approval" {
		return errors.New("Tool invocation is not waiting for approval")
	}
	if runStatus == "running" {
		// The Worker is still unwinding the approval-required Agent call; Fail() will queue it if approval is already granted.
		// 中文：Worker 仍在结束需要审批的 Agent 调用；如果审批已通过，Fail() 会负责重新排队。
		return tx.Commit(ctx)
	}
	if runStatus == "queued" {
		return tx.Commit(ctx)
	}
	if runStatus != "waiting_approval" {
		return fmt.Errorf("execution status %q cannot resume an approved Tool call", runStatus)
	}
	if _, err := tx.Exec(ctx, `UPDATE execution_runs SET status='queued',worker_id=NULL,lease_until=NULL,heartbeat_at=NULL,available_at=now(),state_reason='approved Tool invocation queued for continuation',state_changed_at=now(),updated_at=now() WHERE id=$1 AND status='waiting_approval'`, executionID); err != nil {
		return fmt.Errorf("queue approved execution: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE tasks SET status='retrying',updated_at=now() WHERE id=$1 AND status='waiting_approval'`, taskID); err != nil {
		return fmt.Errorf("queue approved task: %w", err)
	}
	if err := recordExecutionState(ctx, tx, executionID, "waiting_approval", "queued", "approval-manager", 0, "approved Tool invocation", execution.FailureRecovered); err != nil {
		return err
	}
	if err := recordMutation(ctx, tx, "approval-manager", "execution.approved_resume", "execution_runs", executionID, map[string]any{"approval_id": approvalID, "task_id": taskID}); err != nil {
		return err
	}
	if err := recordMutation(ctx, tx, "approval-manager", "task.retrying", "tasks", taskID, map[string]any{"execution_id": executionID, "approval_id": approvalID}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit approved Tool continuation: %w", err)
	}
	return nil
}

func (p *Postgres) Begin(ctx context.Context, request gateway.Request, definition registry.Definition, inputHash string) (gateway.Reservation, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return gateway.Reservation{}, fmt.Errorf("begin tool invocation reservation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockToolIdempotency(ctx, tx, request); err != nil {
		return gateway.Reservation{}, err
	}
	if _, err := validateToolLease(ctx, tx, request); err != nil {
		return gateway.Reservation{}, err
	}
	var id, storedHash, status, storedApprovalID string
	var result []byte
	err = tx.QueryRow(ctx, `SELECT id::text,input_hash,status,result,COALESCE(approval_id::text,'') FROM tool_invocations WHERE execution_id=$1 AND idempotency_key=$2 FOR UPDATE`, request.ExecutionID, request.IdempotencyKey).Scan(&id, &storedHash, &status, &result, &storedApprovalID)
	if err == nil {
		if storedHash != inputHash {
			return gateway.Reservation{}, errors.New("tool idempotency key reused with different input")
		}
		if status == "waiting_approval" {
			if request.ApprovalID == "" {
				return gateway.Reservation{}, errors.New("tool invocation is still waiting for approval")
			}
			if request.ApprovalID != storedApprovalID {
				return gateway.Reservation{}, errors.New("approval ID is not the approval bound to this Tool invocation")
			}
			if err := consumeToolApproval(ctx, tx, request, definition, inputHash); err != nil {
				return gateway.Reservation{}, err
			}
			_, err = tx.Exec(ctx, `UPDATE tool_invocations SET status='running',worker_id=$2,fencing_token=$3,started_at=now(),updated_at=now() WHERE id=$1`, id, request.Lease.WorkerID, request.Lease.Fencing)
			if err != nil {
				return gateway.Reservation{}, fmt.Errorf("start approved tool invocation: %w", err)
			}
			if err := recordMutation(ctx, tx, request.AgentID, "tool.invocation_started", "tool_invocations", id, map[string]any{"execution_id": request.ExecutionID, "step_id": request.StepID, "approval_id": request.ApprovalID}); err != nil {
				return gateway.Reservation{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return gateway.Reservation{}, fmt.Errorf("commit approved invocation start: %w", err)
			}
			return gateway.Reservation{InvocationID: id, ExecutionID: request.ExecutionID, WorkerID: request.Lease.WorkerID, Fencing: request.Lease.Fencing, Status: "running"}, nil
		}
		if err := tx.Commit(ctx); err != nil {
			return gateway.Reservation{}, fmt.Errorf("commit invocation replay lookup: %w", err)
		}
		return gateway.Reservation{InvocationID: id, ExecutionID: request.ExecutionID, WorkerID: request.Lease.WorkerID, Fencing: request.Lease.Fencing, Status: status, Result: result, Replay: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return gateway.Reservation{}, fmt.Errorf("lookup tool invocation: %w", err)
	}
	if request.ApprovalID != "" {
		return gateway.Reservation{}, errors.New("approval does not match an existing pending invocation")
	}
	args := json.RawMessage(request.Arguments)
	err = tx.QueryRow(ctx, `INSERT INTO tool_invocations(goal_id,task_id,execution_id,step_id,agent_id,worker_id,fencing_token,provider_id,tool_id,tool_version,idempotency_key,input_hash,arguments,status,started_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'running',now()) RETURNING id::text`, request.GoalID, request.TaskID, request.ExecutionID, request.StepID, request.AgentID, request.Lease.WorkerID, request.Lease.Fencing, definition.Identity.ProviderID, definition.Identity.ToolID, definition.Identity.Version, request.IdempotencyKey, inputHash, args).Scan(&id)
	if err != nil {
		return gateway.Reservation{}, fmt.Errorf("reserve tool invocation: %w", err)
	}
	if err := recordMutation(ctx, tx, request.AgentID, "tool.invocation_started", "tool_invocations", id, map[string]any{"execution_id": request.ExecutionID, "step_id": request.StepID, "provider_id": definition.Identity.ProviderID, "tool_id": definition.Identity.ToolID, "tool_version": definition.Identity.Version, "input_hash": inputHash}); err != nil {
		return gateway.Reservation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return gateway.Reservation{}, fmt.Errorf("commit tool invocation reservation: %w", err)
	}
	return gateway.Reservation{InvocationID: id, ExecutionID: request.ExecutionID, WorkerID: request.Lease.WorkerID, Fencing: request.Lease.Fencing, Status: "running"}, nil
}

func (p *Postgres) Complete(ctx context.Context, reservation gateway.Reservation, result json.RawMessage) error {
	if !json.Valid(result) {
		return errors.New("tool result must be valid JSON")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tool result commit: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var taskID string
	err = tx.QueryRow(ctx, `UPDATE tool_invocations i SET status='succeeded',result=$5,completed_at=now(),updated_at=now(),error_class='',failure_reason=''
		FROM execution_runs e WHERE i.id=$1 AND i.execution_id=e.id AND i.execution_id=$2 AND i.worker_id=$3 AND i.fencing_token=$4
		AND i.status='running' AND e.worker_id=$3 AND e.fencing_token=$4 AND e.status='running' AND e.lease_until>now() AND NOT e.cancellation_requested
		RETURNING i.task_id::text`, reservation.InvocationID, reservation.ExecutionID, reservation.WorkerID, reservation.Fencing, result).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("persist successful tool invocation: %w", err)
	}
	if err := recordMutation(ctx, tx, reservation.WorkerID, "tool.invocation_succeeded", "tool_invocations", reservation.InvocationID, map[string]any{"task_id": taskID, "execution_id": reservation.ExecutionID}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit successful tool invocation: %w", err)
	}
	return nil
}

func (p *Postgres) Fail(ctx context.Context, reservation gateway.Reservation, class, reason string) error {
	return p.finishToolInvocation(ctx, reservation, "failed", class, reason)
}

func (p *Postgres) MarkUnknown(ctx context.Context, reservation gateway.Reservation, reason string) error {
	return p.finishToolInvocation(ctx, reservation, "unknown", "uncertain", reason)
}

func (p *Postgres) finishToolInvocation(ctx context.Context, reservation gateway.Reservation, status, class, reason string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tool invocation %s transition: %w", status, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tag, err := tx.Exec(ctx, `UPDATE tool_invocations SET status=$5,error_class=$6,failure_reason=$7,completed_at=now(),updated_at=now()
		WHERE id=$1 AND execution_id=$2 AND worker_id=$3 AND fencing_token=$4 AND status='running'`, reservation.InvocationID, reservation.ExecutionID, reservation.WorkerID, reservation.Fencing, status, class, reason)
	if err != nil {
		return fmt.Errorf("persist tool invocation %s: %w", status, err)
	}
	if tag.RowsAffected() != 1 {
		return execution.ErrLeaseLost
	}
	if err := recordMutation(ctx, tx, reservation.WorkerID, "tool.invocation_"+status, "tool_invocations", reservation.InvocationID, map[string]any{"execution_id": reservation.ExecutionID, "error_class": class, "reason": reason}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tool invocation %s: %w", status, err)
	}
	return nil
}

func validateToolLease(ctx context.Context, tx pgx.Tx, request gateway.Request) (string, error) {
	var ownerID string
	err := tx.QueryRow(ctx, `SELECT g.owner_id::text FROM execution_runs e JOIN tasks t ON t.id=e.task_id JOIN goals g ON g.id=t.goal_id
		WHERE e.id=$1 AND e.task_id=$2 AND t.goal_id=$3 AND g.owner_id=$4 AND e.worker_id=$5 AND e.fencing_token=$6
		AND e.status='running' AND e.lease_until>now() AND NOT e.cancellation_requested FOR UPDATE OF e`, request.ExecutionID, request.TaskID, request.GoalID, LocalOwnerID, request.Lease.WorkerID, request.Lease.Fencing).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", execution.ErrLeaseLost
	}
	if err != nil {
		return "", fmt.Errorf("validate tool execution context and lease: %w", err)
	}
	return ownerID, nil
}

func consumeToolApproval(ctx context.Context, tx pgx.Tx, request gateway.Request, definition registry.Definition, inputHash string) error {
	contextHash := toolContextHash(request, definition.Identity)
	var approvalID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM approvals WHERE id=$1 AND user_id=$2 AND task_id=$3 AND tool_id=$4 AND action=$5
		AND parameters_hash=$6 AND tool_version=$7 AND execution_id=$8 AND step_id=$9 AND agent_id=$10 AND invocation_context_hash=$11
		AND status='approved' AND consumed_at IS NULL AND expires_at>now() FOR UPDATE`, request.ApprovalID, LocalOwnerID, request.TaskID, qualifiedToolID(definition.Identity), definition.Identity.ToolID, inputHash, definition.Identity.Version, request.ExecutionID, request.StepID, request.AgentID, contextHash).Scan(&approvalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("approval is missing, expired, consumed, or bound to different invocation parameters/context")
	}
	if err != nil {
		return fmt.Errorf("validate exact tool approval: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE approvals SET consumed_at=now() WHERE id=$1 AND status='approved' AND consumed_at IS NULL`, approvalID)
	if err != nil {
		return fmt.Errorf("consume tool approval: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("approval was concurrently consumed")
	}
	if err := recordMutation(ctx, tx, LocalOwnerID, "approval.consumed", "approvals", approvalID, map[string]any{"execution_id": request.ExecutionID, "step_id": request.StepID, "tool_version": definition.Identity.Version}); err != nil {
		return err
	}
	return nil
}

func toolContextHash(request gateway.Request, identity registry.Identity) string {
	payload, _ := json.Marshal(struct{ GoalID, TaskID, ExecutionID, StepID, AgentID, ProviderID, ToolID, Version, IdempotencyKey string }{
		request.GoalID, request.TaskID, request.ExecutionID, request.StepID, request.AgentID, identity.ProviderID, identity.ToolID, identity.Version, request.IdempotencyKey,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func lockToolIdempotency(ctx context.Context, tx pgx.Tx, request gateway.Request) error {
	lockKey := fmt.Sprintf("%s:%d:%s:tool", request.ExecutionID, len(request.IdempotencyKey), request.IdempotencyKey)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", lockKey); err != nil {
		return fmt.Errorf("lock tool invocation idempotency key: %w", err)
	}
	return nil
}

func qualifiedToolID(identity registry.Identity) string {
	return identity.ProviderID + "/" + identity.ToolID
}

var _ gateway.InvocationStore = (*Postgres)(nil)
