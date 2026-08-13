-- Revert migration 0038
UPDATE plans SET is_active = true WHERE id IN ('starter', 'indie', 'business', 'enterprise');
UPDATE user_subscriptions SET plan_id = 'starter' WHERE plan_id IN ('free', 'pay_as_you_go', 'pro');
DELETE FROM plans WHERE id IN ('free', 'pay_as_you_go', 'pro');
