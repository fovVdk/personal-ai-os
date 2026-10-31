CREATE TABLE IF NOT EXISTS approvals (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    tool_id text NOT NULL,
    action text NOT NULL,
    parameters_hash text NOT NULL CHECK (length(parameters_hash) = 64),
    risk_level smallint NOT NULL CHECK (risk_level BETWEEN 0 AND 3),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','expired','revoked')),
    expires_at timestamptz NOT NULL,
    approved_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS approvals_user_status_expiry_idx ON approvals(user_id,status,expires_at);

CREATE TABLE IF NOT EXISTS execution_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN ('running','waiting_approval','waiting_external','retrying','succeeded','failed','unknown')),
    idempotency_key text NOT NULL UNIQUE,
    input_hash text NOT NULL CHECK (length(input_hash) = 64),
    result jsonb NOT NULL DEFAULT '{}'::jsonb,
    failure_reason text NOT NULL DEFAULT '',
    attempt integer NOT NULL DEFAULT 1 CHECK (attempt > 0),
    started_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);
CREATE INDEX IF NOT EXISTS execution_runs_recovery_idx ON execution_runs(status,updated_at) WHERE status IN ('running','retrying','unknown');

CREATE TABLE IF NOT EXISTS execution_evidence (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    execution_run_id uuid NOT NULL REFERENCES execution_runs(id) ON DELETE CASCADE,
    source text NOT NULL,
    description text NOT NULL,
    reference text,
    observed_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS event_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    attempts integer NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS event_outbox_pending_idx ON event_outbox(occurred_at) WHERE published_at IS NULL;

CREATE TABLE IF NOT EXISTS audit_records (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    actor text NOT NULL,
    action text NOT NULL,
    resource text NOT NULL,
    resource_id text NOT NULL,
    result text NOT NULL,
    correlation_id text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS audit_records_resource_idx ON audit_records(resource,resource_id,created_at DESC);

CREATE TABLE IF NOT EXISTS memories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id uuid NOT NULL REFERENCES users(id),
    category text NOT NULL CHECK (category IN ('fact','preference','experience','relationship','event','instruction')),
    content text NOT NULL CHECK (length(trim(content)) > 0),
    source text NOT NULL,
    confidence double precision NOT NULL DEFAULT 0.5 CHECK (confidence BETWEEN 0 AND 1),
    observed_at timestamptz,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);
CREATE INDEX IF NOT EXISTS memories_owner_category_idx ON memories(owner_id,category,updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS memories_search_idx ON memories USING gin(to_tsvector('simple',content));
