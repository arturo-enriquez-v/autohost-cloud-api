-- What auto-heal did for a monitor (restart, recreate, gave up) and when the service recovered after it.
-- Detected by the API from the auto-heal state the agent reports with each check.
CREATE TABLE IF NOT EXISTS service_autoheal_events (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    monitor_id       UUID NOT NULL REFERENCES service_monitors(id) ON DELETE CASCADE,
    node_id          UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    action           TEXT NOT NULL,              -- 'restart' | 'recreate' | 'gave_up' | 'recovered'
    attempt          INT  NOT NULL DEFAULT 0,
    max_attempts     INT  NOT NULL DEFAULT 0,
    error            TEXT NOT NULL DEFAULT '',   -- why the restart/recreate command failed
    check_message    TEXT NOT NULL DEFAULT '',   -- the check result that came with it
    downtime_seconds INT,                        -- recovered: from the first failure to recovery
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_service_autoheal_events_monitor_created
    ON service_autoheal_events (monitor_id, created_at DESC);
