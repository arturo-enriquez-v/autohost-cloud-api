DROP INDEX IF EXISTS idx_node_alerts_telegram_bot_id;
ALTER TABLE node_alerts DROP COLUMN IF EXISTS telegram_bot_id;
DROP TABLE IF EXISTS telegram_bots;
