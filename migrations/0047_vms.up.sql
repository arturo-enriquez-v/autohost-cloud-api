CREATE TABLE IF NOT EXISTS vms (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    host_node_id UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    node_id UUID REFERENCES nodes(id) ON DELETE SET NULL,
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(128) NOT NULL,
    vcpus INT NOT NULL DEFAULT 2,
    ram_mb INT NOT NULL DEFAULT 2048,
    disk_gb INT NOT NULL DEFAULT 20,
    image VARCHAR(64) NOT NULL DEFAULT 'ubuntu-24.04',
    status VARCHAR(32) NOT NULL DEFAULT 'creating',
    ip_address VARCHAR(45) NOT NULL DEFAULT '',
    cloud_init_user VARCHAR(64) NOT NULL DEFAULT 'ubuntu',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_vms_host_name UNIQUE(host_node_id, name)
);

CREATE INDEX IF NOT EXISTS idx_vms_host_node_id ON vms(host_node_id);
CREATE INDEX IF NOT EXISTS idx_vms_owner_id ON vms(owner_id);
CREATE INDEX IF NOT EXISTS idx_vms_node_id ON vms(node_id);

ALTER TABLE vms ENABLE ROW LEVEL SECURITY;
CREATE POLICY vms_isolation ON vms
    USING (owner_id = current_setting('autohost.current_user_id', true)::uuid);
