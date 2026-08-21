-- Migration 0043: Add plan feature limits (Backup Cloud Storage, Retention, AI Diagnostics per month)

ALTER TABLE plans ADD COLUMN IF NOT EXISTS max_backup_storage_bytes BIGINT NOT NULL DEFAULT 524288000;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS max_backup_retention_days INT NOT NULL DEFAULT 3;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS max_ai_diagnostics_per_month INT NOT NULL DEFAULT 5;

-- Update plans with exact feature limits:
-- Free: 2 nodes, 2 projects, 5 custom commands, 3 days retention, 60s polling, 500MB backup, 3 days backup retention, 5 AI diagnostics/mo
UPDATE plans
SET
  max_backup_storage_bytes = 524288000, -- 500 MB
  max_backup_retention_days = 3,
  max_ai_diagnostics_per_month = 5,
  updated_at = now()
WHERE id = 'free';

-- Starter: 2 nodes, unlimited projects (-1), unlimited commands (-1), 14 days metrics retention, 15s polling, 10GB backup, 14 days backup retention, 20 AI diagnostics/mo
UPDATE plans
SET
  max_backup_storage_bytes = 10737418240, -- 10 GB
  max_backup_retention_days = 14,
  max_ai_diagnostics_per_month = 20,
  updated_at = now()
WHERE id = 'starter';

-- Pro: 10 nodes, unlimited projects (-1), unlimited commands (-1), 90 days metrics retention, 5s polling, 100GB backup, 90 days backup retention, -1 (unlimited) AI diagnostics/mo
UPDATE plans
SET
  max_backup_storage_bytes = 107374182400, -- 100 GB
  max_backup_retention_days = 90,
  max_ai_diagnostics_per_month = -1, -- Unlimited
  updated_at = now()
WHERE id = 'pro';
