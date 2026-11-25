-- Connectors: HTTP requests to external APIs defined from the web panel and shared within a project.
-- secret_ciphertext holds the credential encrypted with SECRETS_ENCRYPTION_KEY (never in plain text).
CREATE TABLE IF NOT EXISTS connectors (
    id                UUID PRIMARY KEY,
    project_id        UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    kind              TEXT NOT NULL,                          -- 'query' | 'action'
    method            TEXT NOT NULL,
    url               TEXT NOT NULL,                          -- may contain {{param}} in path and query
    headers           JSONB NOT NULL DEFAULT '[]',
    body_type         TEXT NOT NULL DEFAULT 'none',           -- 'none' | 'json' | 'form' | 'text'
    body              TEXT NOT NULL DEFAULT '',
    params            JSONB NOT NULL DEFAULT '[]',
    auth              JSONB NOT NULL DEFAULT '{"type":"none"}',
    secret_ciphertext TEXT NOT NULL DEFAULT '',
    response_mapping  JSONB NOT NULL DEFAULT '{}',
    cache_ttl_seconds INT NOT NULL DEFAULT 0,
    timeout_seconds   INT NOT NULL DEFAULT 15,
    created_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, name)
);

-- Run history of action connectors (who ran what, and the result). Pruned to the latest runs per connector.
CREATE TABLE IF NOT EXISTS connector_runs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connector_id UUID NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
    user_id      UUID REFERENCES users(id) ON DELETE SET NULL,
    params       JSONB NOT NULL DEFAULT '{}',               -- sensitive params masked
    status_code  INT NOT NULL DEFAULT 0,                    -- 0 when the external API could not be reached
    ok           BOOLEAN NOT NULL DEFAULT FALSE,
    error        TEXT NOT NULL DEFAULT '',
    duration_ms  INT NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_connector_runs_connector_created
    ON connector_runs (connector_id, created_at DESC);
