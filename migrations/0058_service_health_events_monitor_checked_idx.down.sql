CREATE INDEX IF NOT EXISTS idx_service_health_events_monitor_id ON service_health_events (monitor_id);
DROP INDEX IF EXISTS idx_service_health_events_monitor_checked;
