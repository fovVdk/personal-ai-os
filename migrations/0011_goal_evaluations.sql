CREATE TABLE IF NOT EXISTS goal_evaluations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goal_id uuid NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    goal_updated_at timestamptz NOT NULL,
    plan_version integer NOT NULL DEFAULT 0 CHECK (plan_version >= 0),
    evaluation_status text NOT NULL CHECK (evaluation_status IN ('needs_criteria','in_progress','blocked','ready_for_verification','terminal')),
    recommended_action text NOT NULL CHECK (recommended_action IN ('continue','replan','wait_approval','wait_condition','verify_completion','stop','escalate')),
    payload jsonb NOT NULL,
    evaluated_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS goal_evaluations_history_idx ON goal_evaluations(goal_id,evaluated_at DESC,id DESC);