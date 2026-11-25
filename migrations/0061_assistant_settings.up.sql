-- Settings of the built-in assistant (one row). The provider API key is encrypted with
-- SECRETS_ENCRYPTION_KEY.
CREATE TABLE IF NOT EXISTS assistant_settings (
    id                 SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled            BOOLEAN NOT NULL DEFAULT FALSE,
    base_url           TEXT NOT NULL DEFAULT '',     -- OpenAI-compatible API, e.g. http://localhost:11434/v1
    api_key_ciphertext TEXT NOT NULL DEFAULT '',
    default_model      TEXT NOT NULL DEFAULT '',
    models             JSONB NOT NULL DEFAULT '[]',  -- extra models users may pick
    reasoning_effort   TEXT NOT NULL DEFAULT '',     -- '' (model default) | none | low | medium | high
    updated_by         UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
