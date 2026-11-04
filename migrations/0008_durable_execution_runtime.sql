ALTER TABLE execution_runs
    DROP CONSTRAINT execution_runs_status_check,
    ADD CONSTRAINT execution_runs_status_check CHECK (status IN (
        'pending','queued','claimed','running','waiting_approval','waiting_external',
        'retrying','retry_wait','succeeded','failed','cancelled','unknown'
    )),
    ADD COLUMN payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN agent_id text NOT NULL DEFAULT '',
    ADD COLUMN worker_id text,
    ADD COLUMN claimed_at timestamptz,
    ADD COLUMN lease_until timestamptz,
    ADD COLUMN heartbeat_at timestamptz,
    ADD COLUMN fencing_token bigint NOT NULL DEFAULT 0 CHECK (fencing_token >= 0),
    ADD COLUMN available_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN max_attempts integer NOT NULL DEFAULT 3 CHECK (max_attempts > 0),
    ADD COLUMN retry_safe boolean NOT NULL DEFAULT true,
    ADD COLUMN idempotent boolean NOT NULL DEFAULT false,
    ADD COLUMN cancellation_requested boolean NOT NULL DEFAULT false,
    ADD COLUMN error_class text NOT NULL DEFAULT '',
    ADD COLUMN state_reason text NOT NULL DEFAULT '',
    ADD COLUMN state_changed_at timestamptz NOT NULL DEFAULT now();

CREATE INDEX execution_runs_claim_idx ON execution_runs(status,available_at,started_at)
    WHERE status IN ('queued','retry_wait');
CREATE INDEX execution_runs_lease_idx ON execution_runs(lease_until)
    WHERE status IN ('claimed','running');

CREATE TABLE execution_idempotency (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    scope text NOT NULL CHECK (length(trim(scope)) > 0),
    idempotency_key text NOT NULL CHECK (length(trim(idempotency_key)) > 0),
    input_hash text NOT NULL CHECK (length(input_hash) = 64),
    execution_run_id uuid NOT NULL REFERENCES execution_runs(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(scope,idempotency_key),
    UNIQUE(execution_run_id)
);
CREATE INDEX execution_idempotency_run_idx ON execution_idempotency(execution_run_id);

CREATE TABLE execution_checkpoints (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    execution_run_id uuid NOT NULL REFERENCES execution_runs(id) ON DELETE CASCADE,
    sequence bigint NOT NULL CHECK (sequence > 0),
    step_id text NOT NULL CHECK (length(trim(step_id)) > 0),
    progress jsonb NOT NULL DEFAULT '{}'::jsonb,
    completed_steps jsonb NOT NULL DEFAULT '[]'::jsonb,
    state_reference text NOT NULL DEFAULT '',
    schema_version integer NOT NULL CHECK (schema_version > 0),
    fencing_token bigint NOT NULL CHECK (fencing_token > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(execution_run_id,sequence)
);
CREATE INDEX execution_checkpoints_latest_idx ON execution_checkpoints(execution_run_id,sequence DESC);

CREATE TABLE execution_state_history (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    execution_run_id uuid NOT NULL REFERENCES execution_runs(id) ON DELETE CASCADE,
    from_status text NOT NULL,
    to_status text NOT NULL,
    worker_id text NOT NULL DEFAULT '',
    fencing_token bigint NOT NULL DEFAULT 0,
    reason text NOT NULL DEFAULT '',
    error_class text NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX execution_state_history_run_idx ON execution_state_history(execution_run_id,occurred_at);

CREATE TABLE execution_step_idempotency (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    execution_run_id uuid NOT NULL REFERENCES execution_runs(id) ON DELETE CASCADE,
    step_id text NOT NULL CHECK (length(trim(step_id)) > 0),
    idempotency_key text NOT NULL CHECK (length(trim(idempotency_key)) > 0),
    input_hash text NOT NULL CHECK (length(input_hash) = 64),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(execution_run_id,step_id),
    UNIQUE(execution_run_id,idempotency_key)
);
