CREATE TABLE IF NOT EXISTS source_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    goal_id uuid NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    original_url text NOT NULL CHECK (length(original_url) BETWEEN 1 AND 2048),
    final_url text NOT NULL CHECK (length(final_url) BETWEEN 1 AND 2048),
    http_status integer NOT NULL CHECK (http_status BETWEEN 100 AND 599),
    mime_type text NOT NULL CHECK (length(mime_type) BETWEEN 1 AND 128),
    retrieved_at timestamptz NOT NULL,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    raw_content bytea NOT NULL,
    normalized_content text NOT NULL,
    fetcher_version text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (octet_length(raw_content) <= 2097152),
    CHECK (octet_length(normalized_content) <= 2097152)
);
CREATE INDEX IF NOT EXISTS source_snapshots_goal_history_idx ON source_snapshots(goal_id,retrieved_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS source_snapshots_content_hash_idx ON source_snapshots(content_hash);

CREATE TABLE IF NOT EXISTS source_references (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    goal_id uuid NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    snapshot_id uuid NOT NULL REFERENCES source_snapshots(id) ON DELETE RESTRICT,
    artifact_path text NOT NULL CHECK (length(artifact_path) BETWEEN 1 AND 1024),
    citation_url text NOT NULL CHECK (length(citation_url) BETWEEN 1 AND 2048),
    quote text NOT NULL CHECK (length(quote) BETWEEN 1 AND 8000),
    quote_hash text NOT NULL CHECK (quote_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS source_references_artifact_idx ON source_references(goal_id,artifact_path,created_at DESC);
CREATE INDEX IF NOT EXISTS source_references_snapshot_idx ON source_references(snapshot_id);

CREATE OR REPLACE FUNCTION reject_source_snapshot_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	RAISE EXCEPTION 'source snapshots and source references are immutable';
END;
$$;
DROP TRIGGER IF EXISTS source_snapshots_immutable ON source_snapshots;
CREATE TRIGGER source_snapshots_immutable BEFORE UPDATE ON source_snapshots
    FOR EACH ROW EXECUTE FUNCTION reject_source_snapshot_update();
DROP TRIGGER IF EXISTS source_references_immutable ON source_references;
CREATE TRIGGER source_references_immutable BEFORE UPDATE ON source_references
    FOR EACH ROW EXECUTE FUNCTION reject_source_snapshot_update();
