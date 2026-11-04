ALTER TABLE goals
    ADD COLUMN IF NOT EXISTS structured_success_criteria jsonb NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE goals
    ADD CONSTRAINT goals_structured_criteria_array_check
    CHECK (jsonb_typeof(structured_success_criteria) = 'array');

CREATE INDEX IF NOT EXISTS goals_structured_criteria_gin_idx
    ON goals USING gin (structured_success_criteria jsonb_path_ops);