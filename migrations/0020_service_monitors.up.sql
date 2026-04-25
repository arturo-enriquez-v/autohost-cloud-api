CREATE TABLE IF NOT EXISTS service_monitors (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id             UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    service_name        TEXT NOT NULL,
    check_type          TEXT NOT NULL,        -- 'process' | 'tcp' | 'http'
    -- HTTP options
    http_url            TEXT,
    http_expected_status INT  DEFAULT 200,
    -- TCP options
    tcp_host            TEXT,
    tcp_port            INT,
    -- Schedule / sensitivity
    interval_seconds    INT  NOT NULL DEFAULT 30,
    failure_threshold   INT  NOT NULL DEFAULT 3,
    -- Notification
    notify_telegram     BOOLEAN NOT NULL DEFAULT FALSE,
    -- Runtime state (updated by the agent via gRPC)
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    status              TEXT NOT NULL DEFAULT 'unknown',  -- 'healthy' | 'degraded' | 'down' | 'unknown'
    consecutive_failures INT  NOT NULL DEFAULT 0,
    last_checked_at     TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_service_monitors_node_id    ON service_monitors(node_id);
CREATE INDEX IF NOT EXISTS idx_service_monitors_user_id    ON service_monitors(user_id);

ALTER TABLE service_monitors ENABLE ROW LEVEL SECURITY;
CREATE POLICY service_monitors_isolation_policy ON service_monitors
    USING (user_id = current_setting('autohost.current_user_id')::uuid);
