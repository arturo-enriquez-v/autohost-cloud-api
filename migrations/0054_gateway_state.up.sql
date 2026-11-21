-- Migration 0054: Persist public folder shares, Tailscale Funnel routes and Cloudflare Tunnel state in
-- Postgres (previously JSON files under the storage dir). Share passwords are stored as bcrypt hashes.
-- Rows are removed automatically when their node or owner is deleted.

CREATE TABLE IF NOT EXISTS file_shares (
    id             TEXT PRIMARY KEY,
    owner_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    node_id        UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    node_name      TEXT NOT NULL DEFAULT '',
    folder_path    TEXT NOT NULL,
    title          TEXT NOT NULL DEFAULT '',
    permission     TEXT NOT NULL CHECK (permission IN ('read', 'write', 'editor')),
    password_hash  TEXT NOT NULL,
    funnel_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    funnel_url     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_file_shares_node ON file_shares(node_id);

CREATE TABLE IF NOT EXISTS funnel_routes (
    id               TEXT PRIMARY KEY,
    funnel_port      INTEGER NOT NULL CHECK (funnel_port IN (443, 8443, 10000)),
    path_prefix      TEXT NOT NULL,
    public_url       TEXT NOT NULL DEFAULT '',
    target_node_id   UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    target_node_name TEXT NOT NULL DEFAULT '',
    target_ip        TEXT NOT NULL,
    container_port   INTEGER NOT NULL CHECK (container_port BETWEEN 1 AND 65535),
    upstream         TEXT NOT NULL,
    container_id     TEXT NOT NULL DEFAULT '',
    container_name   TEXT NOT NULL DEFAULT '',
    container_image  TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (funnel_port, path_prefix)
);

CREATE TABLE IF NOT EXISTS cloudflare_tunnel_routes (
    domain           TEXT PRIMARY KEY,
    subdomain        TEXT NOT NULL DEFAULT '',
    base_domain      TEXT NOT NULL DEFAULT '',
    target_node_id   UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    target_node_name TEXT NOT NULL DEFAULT '',
    target_ip        TEXT NOT NULL,
    port             INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    upstream         TEXT NOT NULL,
    container_id     TEXT NOT NULL DEFAULT '',
    container_name   TEXT NOT NULL DEFAULT '',
    container_image  TEXT NOT NULL DEFAULT '',
    dns_configured   BOOLEAN NOT NULL DEFAULT FALSE,
    dns_message      TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Singleton platform-wide settings (funnel DNS name, cloudflare tunnel id/name/base domain/mode...).
CREATE TABLE IF NOT EXISTS platform_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
