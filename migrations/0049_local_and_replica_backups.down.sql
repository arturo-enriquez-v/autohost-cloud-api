-- Migration 0049 (down): Revert Local and Replicated Backups

ALTER TABLE backup_configs DROP COLUMN IF EXISTS storage_destination;
ALTER TABLE backup_records DROP COLUMN IF EXISTS storage_destination;
ALTER TABLE backup_records DROP COLUMN IF EXISTS local_path;
ALTER TABLE backup_records DROP COLUMN IF EXISTS parent_path;
ALTER TABLE backup_records ALTER COLUMN s3_key SET NOT NULL;

