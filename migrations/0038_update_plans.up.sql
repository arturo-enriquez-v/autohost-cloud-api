-- Migration 0038: Update plans catalog for Free (Developer), Pay-As-You-Go, and Pro / Business

ALTER TABLE plans ADD COLUMN IF NOT EXISTS max_custom_commands INT NOT NULL DEFAULT 5;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS retention_days INT NOT NULL DEFAULT 3;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS polling_interval_sec INT NOT NULL DEFAULT 60;

-- Insert or update the 3 official plans
INSERT INTO plans (id, name, description, price_cents, max_nodes, max_projects, max_custom_commands, retention_days, polling_interval_sec, is_active)
VALUES
  ('free',           'Free (Developer)', 'Ideal for developers and personal projects. Monitor up to 2 self-hosted nodes free forever.',   0,  2,  2,  5,  3, 60, true),
  ('pay_as_you_go',  'Pay-As-You-Go',   'Limitless scalability. 2 free monitored nodes included, pay only for extra connected nodes.',     0, -1, -1, -1, 14, 15, true),
  ('pro',            'Pro / Business',  'For growing startups and businesses requiring high-capacity node monitoring.',     2900, 10, -1, -1, 90,  5, true)
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

-- Deactivate old deprecated plans
UPDATE plans
SET is_active = false
WHERE id NOT IN ('free', 'pay_as_you_go', 'pro');

-- Re-assign any users on old plans to the new 'free' plan
UPDATE user_subscriptions
SET plan_id = 'free', updated_at = now()
WHERE plan_id NOT IN ('free', 'pay_as_you_go', 'pro');
