-- Plans catalog
CREATE TABLE plans (
  id                   TEXT PRIMARY KEY,
  name                 TEXT NOT NULL,
  description          TEXT,
  price_cents          INT  NOT NULL DEFAULT 0,
  max_nodes            INT  NOT NULL DEFAULT 3,    -- -1 = unlimited
  max_projects         INT  NOT NULL DEFAULT 1,
  max_scheduled_tasks  INT  NOT NULL DEFAULT 5,
  max_days_requests_metrics INT NOT NULL DEFAULT 1000, -- Max number of requests for which metrics are stored (for the last 30 days)
  daily_report         BOOLEAN NOT NULL DEFAULT false, -- Whether to delay report generation until the end of the billing period
  is_active            BOOLEAN NOT NULL DEFAULT true,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO plans (id, name, description, price_cents, max_nodes, max_projects, max_scheduled_tasks, max_days_requests_metrics, daily_report) VALUES
  ('starter',  'Starter',  'El estándar para iniciar sin fricción.',                                              100,    5,   3,   5, 1000, false),
  ('indie',    'Indie',    'Para el desarrollador independiente que busca rentabilidad.',                          2900,  20,  10,  50, 1000, false),
  ('business', 'Business', 'Para empresas que migran cargas de trabajo hacia infraestructura propia.',             3900, -1,  -1,  -1, 1000, false),
  ('enterprise','Enterprise','Para organizaciones que necesitan soporte dedicado y personalización.', 4900, -1,  -1,  -1, 1000, true);

-- One subscription per user (current active plan)
CREATE TABLE user_subscriptions (
  id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id                 UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  plan_id                 TEXT NOT NULL REFERENCES plans(id),
  status                  TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'cancelled', 'past_due')),
  period_start            TIMESTAMPTZ NOT NULL DEFAULT now(),
  period_end              TIMESTAMPTZ,
  stripe_subscription_id  TEXT,
  stripe_customer_id      TEXT,
  created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id)
);

-- Backfill: assign starter plan to all existing users
INSERT INTO user_subscriptions (user_id, plan_id)
SELECT id, 'starter' FROM users;
