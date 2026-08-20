-- Migration 0039 Down: Revert plans

UPDATE user_subscriptions SET plan_id = 'free' WHERE plan_id = 'starter';
DELETE FROM plans WHERE id = 'starter';
UPDATE plans SET is_active = true WHERE id = 'pay_as_you_go';
