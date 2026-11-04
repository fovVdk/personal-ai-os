ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS plan_key text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS criterion_ids jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(criterion_ids)='array');
CREATE UNIQUE INDEX IF NOT EXISTS tasks_goal_plan_key_idx ON tasks(goal_id,plan_key) WHERE plan_key<>'';

ALTER TABLE execution_evidence
    ADD COLUMN IF NOT EXISTS criterion_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS criterion_version integer NOT NULL DEFAULT 0 CHECK (criterion_version >= 0),
    ADD COLUMN IF NOT EXISTS content_hash text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS verifier_type text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS verifier_version text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS criterion_verifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    evaluation_id uuid NOT NULL REFERENCES goal_evaluations(id) ON DELETE RESTRICT,
    goal_id uuid NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    goal_updated_at timestamptz NOT NULL,
    criterion_id text NOT NULL,
    criterion_version integer NOT NULL CHECK (criterion_version > 0),
    verifier_type text NOT NULL,
    verifier_version text NOT NULL,
    status text NOT NULL CHECK (status IN ('satisfied','insufficient_evidence','unknown','unsupported','rejected','inconclusive','pending')),
    reason text NOT NULL,
    evidence_hashes jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence_hashes)='array'),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS criterion_verifications_current_idx ON criterion_verifications(goal_id,criterion_id,criterion_version,created_at DESC);

CREATE TABLE IF NOT EXISTS criterion_verification_evidence (
    verification_id uuid NOT NULL REFERENCES criterion_verifications(id) ON DELETE RESTRICT,
    evidence_id uuid NOT NULL REFERENCES execution_evidence(id) ON DELETE RESTRICT,
    PRIMARY KEY (verification_id,evidence_id)
);

CREATE TABLE IF NOT EXISTS plan_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goal_id uuid NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    version integer NOT NULL CHECK (version > 0),
    parent_version integer NOT NULL DEFAULT 0 CHECK (parent_version >= 0),
    status text NOT NULL CHECK (status IN ('active','superseded')),
    planning_reason text NOT NULL,
    trigger_type text NOT NULL,
    evaluation_id uuid REFERENCES goal_evaluations(id) ON DELETE RESTRICT,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (goal_id,version)
);
CREATE UNIQUE INDEX IF NOT EXISTS plan_versions_one_active_idx ON plan_versions(goal_id) WHERE status='active';

CREATE TABLE IF NOT EXISTS plan_proposals (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goal_id uuid NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    base_version integer NOT NULL CHECK (base_version >= 0),
    proposed_version integer NOT NULL CHECK (proposed_version > 0),
    status text NOT NULL CHECK (status IN ('proposed','accepted','rejected','expired','conflict')),
    trigger_type text NOT NULL,
    reason text NOT NULL,
    evaluation_id uuid REFERENCES goal_evaluations(id) ON DELETE RESTRICT,
    fingerprint text NOT NULL,
    payload jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    UNIQUE (goal_id,fingerprint)
);
CREATE INDEX IF NOT EXISTS plan_proposals_history_idx ON plan_proposals(goal_id,created_at DESC);

CREATE TABLE IF NOT EXISTS supervisor_decisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goal_id uuid NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    goal_updated_at timestamptz NOT NULL,
    plan_version integer NOT NULL DEFAULT 0 CHECK (plan_version >= 0),
    evaluation_id uuid REFERENCES goal_evaluations(id) ON DELETE RESTRICT,
    decision_type text NOT NULL CHECK (decision_type IN ('continue_execution','replan','verify_completion','wait_approval','wait_condition','budget_exhausted','stop','escalate')),
    decision_status text NOT NULL CHECK (decision_status IN ('recorded','completed','waiting','failed')),
    reason text NOT NULL,
    progress_fingerprint text NOT NULL,
    related_proposal_id uuid REFERENCES plan_proposals(id) ON DELETE RESTRICT,
    related_execution_ids jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(related_execution_ids)='array'),
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE(goal_id,progress_fingerprint,decision_type)
);
CREATE INDEX IF NOT EXISTS supervisor_decisions_history_idx ON supervisor_decisions(goal_id,created_at DESC);