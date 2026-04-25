CREATE TABLE IF NOT EXISTS service_health_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    monitor_id  UUID NOT NULL REFERENCES service_monitors(id) ON DELETE CASCADE,
    node_id     UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status      TEXT NOT NULL,   -- 'up' | 'down'
    latency_ms  INT  NOT NULL DEFAULT 0,
    message     TEXT,
    checked_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_service_health_events_monitor_id  ON service_health_events(monitor_id);
CREATE INDEX IF NOT EXISTS idx_service_health_events_node_id     ON service_health_events(node_id);
CREATE INDEX IF NOT EXISTS idx_service_health_events_checked_at  ON service_health_events(checked_at DESC);
CREATE INDEX IF NOT EXISTS idx_service_health_events_user_id     ON service_health_events(user_id);

ALTER TABLE service_health_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY service_health_events_isolation_policy ON service_health_events
    USING (user_id = current_setting('autohost.current_user_id')::uuid);
