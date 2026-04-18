

ALTER TABLE node_alerts ADD COLUMN IF NOT EXISTS user_id UUID;
ALTER TABLE node_alerts ADD CONSTRAINT node_alerts_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_node_alerts_user_id ON node_alerts(user_id);

ALTER TABLE node_metrics ADD COLUMN IF NOT EXISTS user_id UUID;
ALTER TABLE node_metrics ADD CONSTRAINT node_metrics_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_node_metrics_user_id ON node_metrics(user_id);

ALTER TABLE node_commands ADD COLUMN IF NOT EXISTS user_id UUID;
ALTER TABLE node_commands ADD CONSTRAINT node_commands_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_node_commands_user_id ON node_commands(user_id);

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS user_id UUID;
ALTER TABLE jobs ADD CONSTRAINT jobs_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_jobs_user_id ON jobs(user_id);

ALTER TABLE scheduled_tasks ADD COLUMN IF NOT EXISTS user_id UUID;
ALTER TABLE scheduled_tasks ADD CONSTRAINT scheduled_tasks_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_scheduled_tasks_user_id ON scheduled_tasks(user_id);

ALTER TABLE node_tokens ADD COLUMN IF NOT EXISTS user_id UUID;
ALTER TABLE node_tokens ADD CONSTRAINT node_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_node_tokens_user_id ON node_tokens(user_id);