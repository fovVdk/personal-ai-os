CREATE TABLE IF NOT EXISTS goal_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goal_id uuid NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN ('pending','running','waiting_approval','waiting_external','retry_wait','completed','failed','cancelled','paused')),
    current_stage text NOT NULL DEFAULT 'evaluate',
    lease_owner text,
    lease_expires_at timestamptz,
    heartbeat_at timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0 CHECK (fencing_token >= 0),
    next_run_at timestamptz,
    last_run_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    retry_count integer NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    run_count integer NOT NULL DEFAULT 0 CHECK (run_count >= 0),
    max_runs integer NOT NULL DEFAULT 100 CHECK (max_runs > 0),
    wake_requested boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS goal_jobs_one_live_goal_idx ON goal_jobs(goal_id)
    WHERE status IN ('pending','running','waiting_approval','waiting_external','retry_wait','paused');
CREATE INDEX IF NOT EXISTS goal_jobs_claim_idx ON goal_jobs(status,next_run_at,created_at)
    WHERE status IN ('pending','retry_wait','running');
CREATE INDEX IF NOT EXISTS goal_jobs_goal_history_idx ON goal_jobs(goal_id,created_at DESC,id DESC);