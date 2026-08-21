-- Rollback migration 0040

ALTER TABLE backup_configs DROP COLUMN IF EXISTS backup_method;
ALTER TABLE backup_configs DROP COLUMN IF EXISTS pg_user;
ALTER TABLE backup_records DROP COLUMN IF EXISTS backup_method;
