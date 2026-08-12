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
    ports       JSONB NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS idx_node_network_stats_node_collected
    ON node_network_stats (node_id, collected_at DESC);
