-- Allow OAuth users who have no password
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

-- Track OAuth provider linkage
ALTER TABLE users ADD COLUMN oauth_provider    TEXT;
ALTER TABLE users ADD COLUMN oauth_provider_id TEXT;

-- One account per provider+provider_id
CREATE UNIQUE INDEX idx_users_oauth
  ON users(oauth_provider, oauth_provider_id)
  WHERE oauth_provider IS NOT NULL;
