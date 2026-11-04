ALTER TABLE event_outbox
    ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS dispatch_owner text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS dispatch_lease_until timestamptz,
    ADD COLUMN IF NOT EXISTS dead_lettered_at timestamptz,
    ADD COLUMN IF NOT EXISTS last_error text NOT NULL DEFAULT '';

DROP INDEX IF EXISTS event_outbox_pending_idx;
CREATE INDEX IF NOT EXISTS event_outbox_dispatch_idx ON event_outbox(next_attempt_at,occurred_at,id)
    WHERE published_at IS NULL AND dead_lettered_at IS NULL;

CREATE TABLE IF NOT EXISTS goal_job_wake_events (
    event_id uuid PRIMARY KEY REFERENCES event_outbox(id) ON DELETE RESTRICT,
    goal_job_id uuid NOT NULL REFERENCES goal_jobs(id) ON DELETE RESTRICT,
    goal_id uuid NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS goal_job_wake_events_goal_idx ON goal_job_wake_events(goal_id,created_at DESC);