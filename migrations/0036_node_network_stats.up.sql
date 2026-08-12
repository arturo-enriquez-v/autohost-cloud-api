-- 0036_node_network_stats.up.sql
-- Stores per-node network interface stats and listening port snapshots
-- sent by the agent every 30 seconds.

CREATE TABLE IF NOT EXISTS node_network_stats (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id     UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    collected_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Aggregated totals across all non-loopback interfaces
    rx_bytes    BIGINT NOT NULL DEFAULT 0,
    tx_bytes    BIGINT NOT NULL DEFAULT 0,
    rx_dropped  BIGINT NOT NULL DEFAULT 0,
    tx_dropped  BIGINT NOT NULL DEFAULT 0,
    rx_errors   BIGINT NOT NULL DEFAULT 0,
    tx_errors   BIGINT NOT NULL DEFAULT 0,

    -- JSON array of per-interface stats  [{interface, rx_bytes, ...}]
    interfaces  JSONB NOT NULL DEFAULT '[]',

    -- JSON array of listening ports  [{port, protocol, bind_addr}]
    ports       JSONB NOT NULL DEFAULT '[]',

    -- JSON array of active VPN peers  [{ip, hostname, online, relay}]
    peers       JSONB NOT NULL DEFAULT '[]',

    -- JSON array of active TCP sockets  [{remote_ip, remote_port, local_port}]
    connections JSONB NOT NULL DEFAULT '[]',

    -- JSON array of recent HTTP requests [{client_ip, method, path, status_code}]
    requests    JSONB NOT NULL DEFAULT '[]',

    -- Requests per second
    rps         NUMERIC NOT NULL DEFAULT 0
);

ALTER TABLE node_network_stats ADD COLUMN IF NOT EXISTS peers JSONB NOT NULL DEFAULT '[]';
ALTER TABLE node_network_stats ADD COLUMN IF NOT EXISTS connections JSONB NOT NULL DEFAULT '[]';
ALTER TABLE node_network_stats ADD COLUMN IF NOT EXISTS requests JSONB NOT NULL DEFAULT '[]';
ALTER TABLE node_network_stats ADD COLUMN IF NOT EXISTS rps NUMERIC NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_node_network_stats_node_collected
    ON node_network_stats (node_id, collected_at DESC);
