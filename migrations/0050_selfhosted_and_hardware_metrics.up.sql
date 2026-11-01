-- Migration 0050: Self-Hosted Mode (Local Node Auto-Enrollment, Caddy TLS Mode, Hardware Temperature Metrics)

-- 1. Allow local node to be provisioned prior to initial admin registration and mark local master node
ALTER TABLE nodes
    ALTER COLUMN owner_id DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS is_local BOOLEAN NOT NULL DEFAULT false;

-- 2. Add tls_mode to caddy_routes ('auto', 'internal', 'http_only') for homelab / LAN / Tailscale domains
ALTER TABLE caddy_routes
    ADD COLUMN IF NOT EXISTS tls_mode TEXT NOT NULL DEFAULT 'auto';

-- 3. Add hardware temperature metric (Celsius) to node_metrics
ALTER TABLE node_metrics
    ADD COLUMN IF NOT EXISTS cpu_temp_celsius DECIMAL(5,2);

