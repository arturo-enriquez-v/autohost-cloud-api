

DROP INDEX IF EXISTS idx_node_alerts_user_id;
ALTER TABLE node_alerts DROP CONSTRAINT IF EXISTS node_alerts_user_id_fkey;
ALTER TABLE node_alerts DROP COLUMN IF EXISTS user_id;

DROP INDEX IF EXISTS idx_node_metrics_user_id;
ALTER TABLE node_metrics DROP CONSTRAINT IF EXISTS node_metrics_user_id_fkey;
ALTER TABLE node_metrics DROP COLUMN IF EXISTS user_id;

DROP INDEX IF EXISTS idx_node_commands_user_id;
ALTER TABLE node_commands DROP CONSTRAINT IF EXISTS node_commands_user_id_fkey;
ALTER TABLE node_commands DROP COLUMN IF EXISTS user_id;

DROP INDEX IF EXISTS idx_jobs_user_id;
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_user_id_fkey;
ALTER TABLE jobs DROP COLUMN IF EXISTS user_id;

DROP INDEX IF EXISTS idx_schedules_tasks_user_id;
ALTER TABLE schedules_tasks DROP CONSTRAINT IF EXISTS schedules_tasks_user_id_fkey;
ALTER TABLE schedules_tasks DROP COLUMN IF EXISTS user_id;

DROP INDEX IF EXISTS idx_node_tokens_user_id;
ALTER TABLE node_tokens DROP CONSTRAINT IF EXISTS node_tokens_user_id_fkey;
ALTER TABLE node_tokens DROP COLUMN IF EXISTS user_id;