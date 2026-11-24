-- Monitor queries read a monitor's events newest first (latest result, history, uptime); the composite index
-- serves them all and replaces the single-column one.
CREATE INDEX IF NOT EXISTS idx_service_health_events_monitor_checked
    ON service_health_events (monitor_id, checked_at DESC);
DROP INDEX IF EXISTS idx_service_health_events_monitor_id;
