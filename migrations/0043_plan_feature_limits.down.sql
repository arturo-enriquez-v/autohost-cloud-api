-- Migration 0043 Down: Remove plan feature limits

ALTER TABLE plans DROP COLUMN IF EXISTS max_backup_storage_bytes;
ALTER TABLE plans DROP COLUMN IF EXISTS max_backup_retention_days;
ALTER TABLE plans DROP COLUMN IF EXISTS max_ai_diagnostics_per_month;
