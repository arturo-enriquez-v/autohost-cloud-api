-- Migration 0048: Remove plans and user_subscriptions tables (open-source self-hosted edition)
DROP TABLE IF EXISTS user_subscriptions;
DROP TABLE IF EXISTS plans;

