

ALTER TABLE nodes ENABLE ROW LEVEL SECURITY;
CREATE POLICY nodes_isolation_policy ON nodes
    USING (owner_id = current_setting('autohost.current_user_id')::uuid);

ALTER TABLE node_metrics ENABLE ROW LEVEL SECURITY;
CREATE POLICY node_metrics_isolation_policy ON node_metrics
    USING (user_id = current_setting('autohost.current_user_id')::uuid);

ALTER TABLE node_commands ENABLE ROW LEVEL SECURITY;
CREATE POLICY node_commands_isolation_policy ON node_commands
    USING (user_id = current_setting('autohost.current_user_id')::uuid);

ALTER TABLE jobs ENABLE ROW LEVEL SECURITY;
CREATE POLICY jobs_isolation_policy ON jobs
    USING (user_id = current_setting('autohost.current_user_id')::uuid);

ALTER TABLE scheduled_tasks ENABLE ROW LEVEL SECURITY;
CREATE POLICY scheduled_tasks_isolation_policy ON scheduled_tasks
    USING (user_id = current_setting('autohost.current_user_id')::uuid);

ALTER TABLE node_tokens ENABLE ROW LEVEL SECURITY;
CREATE POLICY node_tokens_isolation_policy ON node_tokens
    USING (user_id = current_setting('autohost.current_user_id')::uuid);

ALTER TABLE node_alerts ENABLE ROW LEVEL SECURITY;
CREATE POLICY node_alerts_isolation_policy ON node_alerts
    USING (user_id = current_setting('autohost.current_user_id')::uuid);
