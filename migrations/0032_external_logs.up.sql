CREATE TABLE IF NOT EXISTS external_log_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    node_id      UUID REFERENCES nodes(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_ext_log_tokens_user_id ON external_log_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_ext_log_tokens_hash ON external_log_tokens(token_hash);

ALTER TABLE external_log_tokens ENABLE ROW LEVEL SECURITY;
CREATE POLICY external_log_tokens_isolation ON external_log_tokens
    USING (user_id = current_setting('autohost.current_user_id', true)::uuid);

CREATE TABLE IF NOT EXISTS external_logs (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    node_id    UUID REFERENCES nodes(id) ON DELETE SET NULL,
    token_id   UUID REFERENCES external_log_tokens(id) ON DELETE SET NULL,
    title      TEXT NOT NULL,
    level      TEXT NOT NULL DEFAULT 'info',
    message    TEXT,
    tags       TEXT[],
    metadata   JSONB DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_external_logs_user_id ON external_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_external_logs_node_id ON external_logs(node_id);
CREATE INDEX IF NOT EXISTS idx_external_logs_created_at ON external_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_external_logs_level ON external_logs(level);

ALTER TABLE external_logs ENABLE ROW LEVEL SECURITY;
CREATE POLICY external_logs_isolation ON external_logs
    USING (user_id = current_setting('autohost.current_user_id', true)::uuid);
