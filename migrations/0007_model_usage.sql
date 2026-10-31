CREATE TABLE IF NOT EXISTS model_usage (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goal_id uuid NOT NULL REFERENCES goals(id) ON DELETE CASCADE,
    provider text NOT NULL,
    model text NOT NULL,
    operation text NOT NULL,
    model_calls integer NOT NULL CHECK (model_calls >= 0),
    input_tokens integer NOT NULL CHECK (input_tokens >= 0),
    output_tokens integer NOT NULL CHECK (output_tokens >= 0),
    total_tokens integer NOT NULL CHECK (total_tokens >= input_tokens + output_tokens),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS model_usage_goal_idx ON model_usage(goal_id,created_at);
