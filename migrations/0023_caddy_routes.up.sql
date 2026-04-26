CREATE TABLE caddy_routes (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id    UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    domain     TEXT NOT NULL,
    upstream   TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(node_id, domain)
);

CREATE INDEX idx_caddy_routes_node_id ON caddy_routes(node_id);
CREATE INDEX idx_caddy_routes_user_id ON caddy_routes(user_id);

ALTER TABLE caddy_routes ENABLE ROW LEVEL SECURITY;
CREATE POLICY caddy_routes_isolation_policy ON caddy_routes
    USING (user_id = current_setting('autohost.current_user_id')::uuid);
