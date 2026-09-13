-- 0045_node_container_settings.up.sql
-- Stores per-node container display order and locked containers (actions blocked).

CREATE TABLE IF NOT EXISTS node_container_settings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id UUID NOT NULL UNIQUE REFERENCES nodes(id) ON DELETE CASCADE,
    sort_order JSONB NOT NULL DEFAULT '[]'::jsonb,
    locked_containers JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_node_container_settings_node_id ON node_container_settings(node_id);
