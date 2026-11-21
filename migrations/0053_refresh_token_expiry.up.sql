-- Migration 0053: Expire refresh tokens and index their lookup hash.
-- Existing tokens get 30 days from the time this migration runs (matching the frontend refresh cookie).

ALTER TABLE refresh_tokens
    ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '30 days');

CREATE INDEX IF NOT EXISTS idx_refresh_token_hash ON refresh_tokens(token_hash);
