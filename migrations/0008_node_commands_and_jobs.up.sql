-- Drop the old custom_commands table (was created in 0007)
DROP TABLE IF EXISTS custom_commands;

-- Node commands: commands available on a node (default built-ins + custom .sh scripts)
CREATE TABLE IF NOT EXISTS node_commands (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id     UUID        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    description TEXT,
    type        TEXT        NOT NULL DEFAULT 'default' CHECK (type IN ('default', 'custom')),
    script_path TEXT,                          -- only relevant for type='custom'
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(node_id, name)
);

-- Jobs: execution requests dispatched to nodes
CREATE TABLE IF NOT EXISTS jobs (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id      UUID        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    command_name TEXT        NOT NULL,
    command_type TEXT        NOT NULL DEFAULT 'default' CHECK (command_type IN ('default', 'custom')),
    status       TEXT        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    output       TEXT,
    error        TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ
);
