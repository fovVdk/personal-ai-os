CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL UNIQUE,
    display_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO users (id, email, display_name)
VALUES ('00000000-0000-4000-8000-000000000001', 'local@personal-ai-os.invalid', 'Local Owner')
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS goals (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id uuid NOT NULL REFERENCES users(id),
    parent_goal_id uuid REFERENCES goals(id) ON DELETE RESTRICT,
    title text NOT NULL CHECK (length(trim(title)) > 0),
    description text NOT NULL DEFAULT '',
    priority smallint NOT NULL DEFAULT 3 CHECK (priority BETWEEN 1 AND 5),
    success_criteria jsonb NOT NULL DEFAULT '[]'::jsonb,
    constraints jsonb NOT NULL DEFAULT '[]'::jsonb,
    autonomy_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    review_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','active','monitoring','blocked','paused','completed','cancelled','archived')),
    progress smallint NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);
CREATE INDEX IF NOT EXISTS goals_owner_status_idx ON goals(owner_id, status, priority DESC, created_at DESC);
CREATE INDEX IF NOT EXISTS goals_parent_idx ON goals(parent_goal_id) WHERE parent_goal_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goal_id uuid NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    title text NOT NULL CHECK (length(trim(title)) > 0),
    description text NOT NULL DEFAULT '',
    priority smallint NOT NULL DEFAULT 3 CHECK (priority BETWEEN 1 AND 5),
    status text NOT NULL DEFAULT 'created' CHECK (status IN ('created','ready','running','waiting_approval','waiting_external','retrying','blocked','paused','completed','failed','cancelled')),
    success_criteria jsonb NOT NULL DEFAULT '[]'::jsonb,
    scheduled_at timestamptz,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tasks_goal_status_idx ON tasks(goal_id, status, priority DESC, created_at);

CREATE TABLE IF NOT EXISTS task_dependencies (
    task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    depends_on_task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, depends_on_task_id),
    CHECK (task_id <> depends_on_task_id)
);
