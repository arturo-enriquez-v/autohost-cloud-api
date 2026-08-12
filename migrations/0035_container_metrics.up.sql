CREATE TABLE container_metrics (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    container_id VARCHAR(128) NOT NULL,
    container_name VARCHAR(255) NOT NULL,
    cpu_usage_percent DECIMAL(5,2) NOT NULL DEFAULT 0.00,
    memory_usage_bytes BIGINT NOT NULL DEFAULT 0,
    memory_usage_percent DECIMAL(5,2) NOT NULL DEFAULT 0.00,
    status VARCHAR(255) NOT NULL DEFAULT '',
    state VARCHAR(64) NOT NULL DEFAULT '',
    collected_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_container_metrics_node_container ON container_metrics(node_id, container_id, collected_at DESC);
CREATE INDEX idx_container_metrics_node_name ON container_metrics(node_id, container_name, collected_at DESC);
CREATE INDEX idx_container_metrics_collected_at ON container_metrics(collected_at DESC);
