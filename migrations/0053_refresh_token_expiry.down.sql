DROP INDEX IF EXISTS idx_refresh_token_hash;

ALTER TABLE refresh_tokens
    DROP COLUMN IF EXISTS expires_at;
