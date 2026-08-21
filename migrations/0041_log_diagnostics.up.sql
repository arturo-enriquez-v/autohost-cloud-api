CREATE TABLE IF NOT EXISTS log_diagnostics (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id),
    node_id         VARCHAR(255) NOT NULL,
    request_id      UUID NOT NULL UNIQUE,
    status          VARCHAR(20) NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','preview','approved','rejected')),
    diagnostic_json JSONB,
    sources         JSONB,
    system_health   VARCHAR(20),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_at     TIMESTAMPTZ
);

CREATE INDEX idx_log_diagnostics_user_id ON log_diagnostics(user_id);
CREATE INDEX idx_log_diagnostics_node_id ON log_diagnostics(node_id);
CREATE INDEX idx_log_diagnostics_request_id ON log_diagnostics(request_id);
CREATE INDEX idx_log_diagnostics_status ON log_diagnostics(status);
