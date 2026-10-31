CREATE TABLE IF NOT EXISTS goal_evidence (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goal_id uuid NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    source text NOT NULL,
    description text NOT NULL,
    reference text,
    observed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS goal_evidence_goal_idx ON goal_evidence(goal_id,observed_at DESC);
