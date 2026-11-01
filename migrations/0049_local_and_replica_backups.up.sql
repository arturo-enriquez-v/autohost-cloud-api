-- Migration 0049: Local and Replicated Backups (No Cloud Provider Required)

-- Add storage_destination to backup_configs ('local', 'local_and_parent', 's3')
ALTER TABLE backup_configs
    ADD COLUMN IF NOT EXISTS storage_destination TEXT NOT NULL DEFAULT 'local_and_parent';

-- Add storage_destination, local_path and parent_path to backup_records
ALTER TABLE backup_records
    ADD COLUMN IF NOT EXISTS storage_destination TEXT NOT NULL DEFAULT 'local_and_parent',
    ADD COLUMN IF NOT EXISTS local_path TEXT,
    ADD COLUMN IF NOT EXISTS parent_path TEXT;

-- Make s3_key optional since local/replicated backups do not use S3
ALTER TABLE backup_records
    ALTER COLUMN s3_key DROP NOT NULL;

