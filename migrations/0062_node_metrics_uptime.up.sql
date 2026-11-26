-- System uptime the agent reports with every metric sample (seconds since boot). NULL for samples stored
-- before this column existed.
ALTER TABLE node_metrics ADD COLUMN IF NOT EXISTS uptime_seconds BIGINT;
