// Package approval binds human authorization to an immutable action and parameter set.
// 中文：approval 包将人工授权绑定到不可变的动作及其参数集合。
// English: This file defines approval lifecycle rules and hashes exact action parameters.
// 中文：本文件定义审批状态机，并为具体动作参数生成绑定哈希。
package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Status string

const (
	Pending  Status = "pending"
	Approved Status = "approved"
	Rejected Status = "rejected"
	Expired  Status = "expired"
	Revoked  Status = "revoked"
)

var ErrInvalidTransition = errors.New("invalid approval status transition")
var ErrParametersChanged = errors.New("approval parameters no longer match")
var ErrExpired = errors.New("approval request has expired")

// Request captures the exact action for which a human decision is requested.
// 中文：Request 描述需要人工决策的精确动作及其参数绑定信息。
type Request struct {
	ID                    string     `json:"id"`
	UserID                string     `json:"user_id"`
	TaskID                string     `json:"task_id"`
	ToolID                string     `json:"tool_id"`
	ToolVersion           string     `json:"tool_version,omitempty"`
	ExecutionID           string     `json:"execution_id,omitempty"`
	StepID                string     `json:"step_id,omitempty"`
	AgentID               string     `json:"agent_id,omitempty"`
	InvocationContextHash string     `json:"invocation_context_hash,omitempty"`
	Action                string     `json:"action"`
	ParametersHash        string     `json:"parameters_hash"`
	RiskLevel             int        `json:"risk_level"`
	Status                Status     `json:"status"`
	ExpiresAt             time.Time  `json:"expires_at"`
	ApprovedAt            *time.Time `json:"approved_at,omitempty"`
	ConsumedAt            *time.Time `json:"consumed_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
}

// HashParameters returns a deterministic SHA-256 hash of JSON-compatible arguments.
// 中文：HashParameters 为 JSON 兼容参数生成确定性的 SHA-256 哈希。
func HashParameters(parameters any) (string, error) {
	encoded, err := json.Marshal(parameters)
	if err != nil {
		return "", fmt.Errorf("encode approval parameters: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func New(userID, taskID, toolID, action string, parameters any, riskLevel int, expiresAt, now time.Time) (Request, error) {
	if userID == "" || taskID == "" || toolID == "" || action == "" {
		return Request{}, errors.New("approval identity, task, tool and action are required")
	}
	if riskLevel < 0 || riskLevel > 3 {
		return Request{}, errors.New("risk level must be between 0 and 3")
	}
	if !expiresAt.After(now) {
		return Request{}, errors.New("approval expiry must be in the future")
	}
	hash, err := HashParameters(parameters)
	if err != nil {
		return Request{}, err
	}
	return Request{UserID: userID, TaskID: taskID, ToolID: toolID, Action: action, ParametersHash: hash, RiskLevel: riskLevel, Status: Pending, ExpiresAt: expiresAt.UTC(), CreatedAt: now.UTC()}, nil
}

func (r *Request) Decide(status Status, now time.Time) error {
	if r.Status != Pending {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, r.Status, status)
	}
	if !now.Before(r.ExpiresAt) {
		r.Status = Expired
		return ErrExpired
	}
	if status != Approved && status != Rejected && status != Revoked {
		return fmt.Errorf("%w: unsupported decision %q", ErrInvalidTransition, status)
	}
	r.Status = status
	if status == Approved {
		approvedAt := now.UTC()
		r.ApprovedAt = &approvedAt
	}
	return nil
}

// ValidFor checks status, expiry, and exact action/parameter binding immediately before execution.
// 中文：ValidFor 在执行前检查审批状态、有效期及动作/参数是否完全匹配。
func (r Request) ValidFor(toolID, action string, parameters any, now time.Time) error {
	if r.ConsumedAt != nil {
		return errors.New("approval request has already been consumed")
	}
	if r.Status != Approved {
		return fmt.Errorf("approval is not approved: %s", r.Status)
	}
	if !now.Before(r.ExpiresAt) {
		return errors.New("approval request has expired")
	}
	if r.ToolID != toolID || r.Action != action {
		return ErrParametersChanged
	}
	hash, err := HashParameters(parameters)
	if err != nil {
		return err
	}
	if hash != r.ParametersHash {
		return ErrParametersChanged
	}
	return nil
}
