CREATE TABLE IF NOT EXISTS telegram_bots (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    bot_token   TEXT NOT NULL,
    chat_id     TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_telegram_bots_user_id ON telegram_bots(user_id);

ALTER TABLE node_alerts ADD COLUMN IF NOT EXISTS telegram_bot_id UUID REFERENCES telegram_bots(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_node_alerts_telegram_bot_id ON node_alerts(telegram_bot_id);
