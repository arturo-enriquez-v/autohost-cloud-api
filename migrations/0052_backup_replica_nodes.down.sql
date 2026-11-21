ALTER TABLE backup_records
    DROP COLUMN IF EXISTS replica_path,
    DROP COLUMN IF EXISTS replica_node_id;

ALTER TABLE backup_configs
    DROP COLUMN IF EXISTS replica_node_id;
