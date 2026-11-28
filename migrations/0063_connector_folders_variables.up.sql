-- Folder of a connector, to group them in the panel: free text, "/" separates levels ("Cloudflare/DNS").
ALTER TABLE connectors ADD COLUMN IF NOT EXISTS folder TEXT NOT NULL DEFAULT '';

-- Variables shared by the connectors of a project, referenced as {{vars.NAME}}. Secret ones are stored
-- encrypted with SECRETS_ENCRYPTION_KEY (value_ciphertext) and are only sent to their allowed origins;
-- plain ones keep their value in value.
CREATE TABLE IF NOT EXISTS connector_variables (
    id               UUID PRIMARY KEY,
    project_id       UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    secret           BOOLEAN NOT NULL DEFAULT FALSE,
    value            TEXT NOT NULL DEFAULT '',
    value_ciphertext TEXT NOT NULL DEFAULT '',
    allowed_origins  TEXT[] NOT NULL DEFAULT '{}',          -- secret ones: "https://api.example.com"
    created_by       UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, name)
);
