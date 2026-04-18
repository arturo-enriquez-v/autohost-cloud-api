CREATE TABLE IF NOT EXISTS alert_events (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID REFERENCES users(id) ON DELETE CASCADE,
    node_id    UUID REFERENCES nodes(id) ON DELETE CASCADE,
    alert_type TEXT        NOT NULL,  -- 'offline' | 'cpu' | 'ram' | 'disk'
    message    TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_alert_events_user_id    ON alert_events(user_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_node_id    ON alert_events(node_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_created_at ON alert_events(created_at DESC);

ALTER TABLE alert_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY alert_events_isolation_policy ON alert_events
    USING (user_id = current_setting('autohost.current_user_id')::uuid);
