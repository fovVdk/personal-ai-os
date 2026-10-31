CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS knowledge_documents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id uuid NOT NULL REFERENCES users(id),
    title text NOT NULL CHECK (length(trim(title)) > 0),
    source text NOT NULL,
    content_hash text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(owner_id,content_hash)
);
CREATE INDEX IF NOT EXISTS knowledge_documents_owner_idx ON knowledge_documents(owner_id,created_at DESC);

CREATE TABLE IF NOT EXISTS knowledge_chunks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id uuid NOT NULL REFERENCES knowledge_documents(id) ON DELETE CASCADE,
    chunk_index integer NOT NULL CHECK (chunk_index >= 0),
    content text NOT NULL CHECK (length(trim(content)) > 0),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    embedding vector,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(document_id,chunk_index)
);
CREATE INDEX IF NOT EXISTS knowledge_chunks_document_idx ON knowledge_chunks(document_id,chunk_index);
CREATE INDEX IF NOT EXISTS knowledge_chunks_search_idx ON knowledge_chunks USING gin(to_tsvector('simple',content));
