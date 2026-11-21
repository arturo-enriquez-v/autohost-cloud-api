-- Migration 0052: Add replica_node_id and replica_path for node-to-node backup replication

ALTER TABLE backup_configs
    ADD COLUMN IF NOT EXISTS replica_node_id UUID REFERENCES nodes(id) ON DELETE SET NULL;

ALTER TABLE backup_records
    ADD COLUMN IF NOT EXISTS replica_node_id UUID REFERENCES nodes(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS replica_path TEXT;
