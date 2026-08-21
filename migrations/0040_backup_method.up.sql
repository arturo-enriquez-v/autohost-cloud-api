-- Migration 0040: Add backup_method to backup_configs and backup_records

-- Add backup_method to backup_configs so users can choose the default method per container
ALTER TABLE backup_configs
    ADD COLUMN IF NOT EXISTS backup_method TEXT NOT NULL DEFAULT 'volume';

-- Add backup_method to backup_records so each record knows which method was used
ALTER TABLE backup_records
    ADD COLUMN IF NOT EXISTS backup_method TEXT NOT NULL DEFAULT 'volume';

-- Add pg_user to backup_configs for PostgreSQL-based backups
ALTER TABLE backup_configs
    ADD COLUMN IF NOT EXISTS pg_user TEXT NOT NULL DEFAULT 'postgres';
