DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS node_commands;

-- Restore the old custom_commands table
CREATE TABLE IF NOT EXISTS custom_commands (
    name        TEXT PRIMARY KEY,
    description TEXT,
    script_path TEXT NOT NULL,
    node_id     TEXT NOT NULL
);
