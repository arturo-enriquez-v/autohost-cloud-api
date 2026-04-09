CREATE TABLE IF NOT EXISTS node_alerts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id     UUID NOT NULL UNIQUE REFERENCES nodes(id) ON DELETE CASCADE,
    telegram_bot_token  TEXT NOT NULL DEFAULT '',
    telegram_chat_id    TEXT NOT NULL DEFAULT '',
    cpu_threshold       REAL,
    ram_threshold       REAL,
    disk_threshold      REAL,
    alert_on_offline    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
