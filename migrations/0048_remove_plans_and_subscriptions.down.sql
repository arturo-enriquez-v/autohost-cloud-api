-- Migration 0048 Down: Recreate plans and user_subscriptions tables
CREATE TABLE IF NOT EXISTS plans (
  id                       TEXT PRIMARY KEY,
  name                     TEXT NOT NULL,
  description              TEXT,
  price_cents              INT NOT NULL DEFAULT 0,
  max_nodes                INT NOT NULL DEFAULT -1,
  max_projects             INT NOT NULL DEFAULT -1,
  max_custom_commands      INT NOT NULL DEFAULT -1,
  retention_days           INT NOT NULL DEFAULT 30,
  polling_interval_sec     INT NOT NULL DEFAULT 15,
  max_backup_storage_bytes BIGINT NOT NULL DEFAULT -1,
  max_backup_retention_days INT NOT NULL DEFAULT -1,
  max_ai_diagnostics_per_month INT NOT NULL DEFAULT -1,
  is_active                BOOLEAN NOT NULL DEFAULT true,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_subscriptions (
  id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id                 UUID NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
  plan_id                 TEXT NOT NULL REFERENCES plans(id),
  status                  TEXT NOT NULL DEFAULT 'active',
  period_start            TIMESTAMPTZ NOT NULL DEFAULT now(),
  period_end              TIMESTAMPTZ,
  stripe_subscription_id  TEXT,
  stripe_customer_id      TEXT,
  created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

