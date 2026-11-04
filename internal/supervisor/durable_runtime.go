// English: This adapter boundary submits bounded Task work to the durable runtime and waits on persisted execution state.
// 中文：本文件定义 Supervisor 提交有界 Task 执行并读取持久化运行状态的适配边界。
package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/task"
)

type DurableRuntime interface {
	Enqueue(context.Context, execution.EnqueueRequest) (execution.Run, bool, error)
	Get(context.Context, string) (execution.Run, error)
	VerifiedTaskIDs(context.Context, string) ([]string, error)
}

func (s *Supervisor) executeDurably(ctx context.Context, item task.Task, agentID string, request agentruntime.Request, retrySafe, idempotent bool) (Execution, execution.Run, error) {
	if s.Runtime == nil {
		return Execution{}, execution.Run{}, errors.New("durable execution runtime is not configured")
	}
	persistedRequest := request
	persistedRequest.Deadline = time.Time{}
	payload, err := json.Marshal(persistedRequest)
	if err != nil {
		return Execution{}, execution.Run{}, fmt.Errorf("encode durable agent request: %w", err)
	}
	hash := sha256.Sum256(payload)
	key := fmt.Sprintf("attempt-%d", item.AttemptCount)
	run, _, err := s.Runtime.Enqueue(ctx, execution.EnqueueRequest{
		TaskID: item.ID, AgentID: agentID, IdempotencyScope: item.ID, IdempotencyKey: key,
		InputHash: hex.EncodeToString(hash[:]), Payload: payload, MaxAttempts: max(1, s.Budgets.MaxRetries+1), RetrySafe: retrySafe, Idempotent: idempotent,
	})
	if err != nil {
		return Execution{}, execution.Run{}, fmt.Errorf("enqueue durable task execution: %w", err)
	}
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		switch run.Status {
		case execution.StatusSucceeded:
			var result Execution
			if err := json.Unmarshal(run.Result, &result); err != nil {
				return Execution{}, run, fmt.Errorf("decode durable execution result: %w", err)
			}
			result.ID, result.TaskID = run.ID, item.ID
			return result, run, nil
		case execution.StatusFailed:
			if run.FailureReason == ErrUnverifiedResult.Error() {
				return Execution{}, run, ErrUnverifiedResult
			}
			return Execution{}, run, fmt.Errorf("durable execution failed (%s): %s", run.FailureClass, run.FailureReason)
		case execution.StatusUnknown, execution.StatusWaitingExternal, execution.StatusWaitingApproval, execution.StatusCancelled:
			return Execution{}, run, nil
		}
		select {
		case <-ctx.Done():
			return Execution{}, run, ctx.Err()
		case <-poll.C:
			run, err = s.Runtime.Get(ctx, run.ID)
			if err != nil {
				return Execution{}, run, fmt.Errorf("poll durable execution: %w", err)
			}
		}
	}
}
