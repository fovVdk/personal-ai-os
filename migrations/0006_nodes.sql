CREATE TABLE IF NOT EXISTS nodes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    identity text NOT NULL UNIQUE,
    name text NOT NULL,
    node_type text NOT NULL CHECK (node_type IN ('core','worker','vision','robot','home')),
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb,
    status text NOT NULL DEFAULT 'offline' CHECK (status IN ('online','degraded','offline')),
    last_heartbeat timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS nodes_status_type_idx ON nodes(status,node_type,name);
