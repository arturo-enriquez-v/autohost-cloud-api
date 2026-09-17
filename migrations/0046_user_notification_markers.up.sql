CREATE TABLE IF NOT EXISTS user_notification_markers (
    user_id             UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    last_seen_alerts_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_logs_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_notification_markers_user_id ON user_notification_markers(user_id);

ALTER TABLE user_notification_markers ENABLE ROW LEVEL SECURITY;
CREATE POLICY user_notification_markers_isolation ON user_notification_markers
    USING (user_id = current_setting('autohost.current_user_id', true)::uuid);
