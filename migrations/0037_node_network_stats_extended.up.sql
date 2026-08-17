-- 0037_node_network_stats_extended.up.sql
-- Adds peers, connections, requests, and rps columns to node_network_stats table

ALTER TABLE node_network_stats ADD COLUMN IF NOT EXISTS peers JSONB NOT NULL DEFAULT '[]';
ALTER TABLE node_network_stats ADD COLUMN IF NOT EXISTS connections JSONB NOT NULL DEFAULT '[]';
ALTER TABLE node_network_stats ADD COLUMN IF NOT EXISTS requests JSONB NOT NULL DEFAULT '[]';
ALTER TABLE node_network_stats ADD COLUMN IF NOT EXISTS rps NUMERIC NOT NULL DEFAULT 0;
