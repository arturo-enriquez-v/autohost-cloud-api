DROP INDEX IF EXISTS idx_users_oauth;
ALTER TABLE users DROP COLUMN IF EXISTS oauth_provider_id;
ALTER TABLE users DROP COLUMN IF EXISTS oauth_provider;
UPDATE users SET password_hash = '' WHERE password_hash IS NULL;
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
