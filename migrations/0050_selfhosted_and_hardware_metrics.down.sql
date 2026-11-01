-- Revert Migration 0050

ALTER TABLE node_metrics
    DROP COLUMN IF EXISTS cpu_temp_celsius;

ALTER TABLE caddy_routes
    DROP COLUMN IF EXISTS tls_mode;

ALTER TABLE nodes
    DROP COLUMN IF EXISTS is_local;

