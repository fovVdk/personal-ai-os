-- Phase 11: persist Tool invocations and bind one-time approvals to an exact runtime context.
-- Phase 11：持久化 Tool Invocation，并将一次性审批绑定到精确运行上下文。
ALTER TABLE approvals
    ADD COLUMN tool_version text NOT NULL DEFAULT '',
    ADD COLUMN execution_id uuid REFERENCES execution_runs(id) ON DELETE RESTRICT,
    ADD COLUMN step_id text NOT NULL DEFAULT '',
    ADD COLUMN agent_id text NOT NULL DEFAULT '',
    ADD COLUMN invocation_context_hash text NOT NULL DEFAULT '',
    ADD COLUMN consumed_at timestamptz;

CREATE INDEX approvals_execution_context_idx ON approvals(execution_id,step_id,status)
    WHERE execution_id IS NOT NULL;

CREATE TABLE tool_invocations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goal_id uuid NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    execution_id uuid NOT NULL REFERENCES execution_runs(id) ON DELETE RESTRICT,
    step_id text NOT NULL CHECK (length(trim(step_id)) > 0),
    agent_id text NOT NULL CHECK (length(trim(agent_id)) > 0),
    worker_id text NOT NULL CHECK (length(trim(worker_id)) > 0),
    fencing_token bigint NOT NULL CHECK (fencing_token > 0),
    provider_id text NOT NULL CHECK (length(trim(provider_id)) > 0),
    tool_id text NOT NULL CHECK (length(trim(tool_id)) > 0),
    tool_version text NOT NULL CHECK (length(trim(tool_version)) > 0),
    idempotency_key text NOT NULL CHECK (length(trim(idempotency_key)) > 0),
    input_hash text NOT NULL CHECK (length(input_hash) = 64),
    arguments jsonb NOT NULL,
    status text NOT NULL CHECK (status IN ('waiting_approval','running','succeeded','failed','unknown','cancelled')),
    approval_id uuid REFERENCES approvals(id) ON DELETE RESTRICT,
    result jsonb,
    error_class text NOT NULL DEFAULT '',
    failure_reason text NOT NULL DEFAULT '',
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(execution_id,idempotency_key)
);
CREATE INDEX tool_invocations_recovery_idx ON tool_invocations(status,updated_at)
    WHERE status IN ('waiting_approval','running','unknown');
CREATE INDEX tool_invocations_task_idx ON tool_invocations(task_id,created_at DESC);
