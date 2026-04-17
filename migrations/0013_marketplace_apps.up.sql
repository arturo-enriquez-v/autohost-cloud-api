CREATE TABLE IF NOT EXISTS marketplace_apps (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id     UUID        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    app_name    TEXT        NOT NULL,
    job_id      UUID        REFERENCES jobs(id) ON DELETE SET NULL,
    status      TEXT        NOT NULL DEFAULT 'installing'
                            CHECK (status IN ('installing', 'running', 'failed')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_marketplace_apps_node_id ON marketplace_apps(node_id);
