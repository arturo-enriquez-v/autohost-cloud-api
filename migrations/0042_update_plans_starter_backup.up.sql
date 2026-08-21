-- Migration 0039: Update plans for Free (Developer), Starter ($5), and Pro / Business ($29) with Backup Cloud & AI limits

-- Insert or update the 3 official plans
INSERT INTO plans (id, name, description, price_cents, max_nodes, max_projects, max_custom_commands, retention_days, polling_interval_sec, is_active)
VALUES
  ('free',    'Free (Developer)', 'Ideal for developers and personal homelabs. Monitor up to 2 self-hosted nodes free with 500 MB backup storage.',   0,  2,  2,  5,  3, 60, true),
  ('starter', 'Starter',          'For growing developers and small projects. 2 nodes included (+$2.50/extra node), 10 GB cloud backup, and 14-day retention.', 500,  2, -1, -1, 14, 15, true),
  ('pro',     'Pro',              'For startups and companies with mission-critical infrastructure. 10 nodes included (+$2.00/extra node), 100 GB backup, and unlimited AI diagnostics.', 2900, 10, -1, -1, 90,  5, true)
ON CONFLICT (id) DO UPDATE SET
  name = EXCLUDED.name,
  description = EXCLUDED.description,
  price_cents = EXCLUDED.price_cents,
  max_nodes = EXCLUDED.max_nodes,
  max_projects = EXCLUDED.max_projects,
  max_custom_commands = EXCLUDED.max_custom_commands,
  retention_days = EXCLUDED.retention_days,
  polling_interval_sec = EXCLUDED.polling_interval_sec,
  is_active = EXCLUDED.is_active,
  updated_at = now();

-- Also update 'pay_as_you_go' users to 'starter' if any
UPDATE user_subscriptions
SET plan_id = 'starter', updated_at = now()
WHERE plan_id = 'pay_as_you_go';

-- Deactivate old pay_as_you_go if exists
UPDATE plans
SET is_active = false
WHERE id = 'pay_as_you_go';
