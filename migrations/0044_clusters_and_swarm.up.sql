-- Add 'swarm' to the allowed command_type values in the jobs table
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_command_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_command_type_check
    CHECK (command_type IN ('default', 'custom', 'install_app', 'swarm'));

-- Clusters table
CREATE TABLE IF NOT EXISTS clusters (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'active',
    overlay_network VARCHAR(100) NOT NULL DEFAULT 'autohost-net',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_clusters_user_id ON clusters(user_id);

-- Cluster nodes table
CREATE TABLE IF NOT EXISTS cluster_nodes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id UUID NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    node_id UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    role VARCHAR(20) NOT NULL DEFAULT 'worker', -- 'manager' or 'worker'
    swarm_node_id VARCHAR(128) NOT NULL DEFAULT '',
    status VARCHAR(50) NOT NULL DEFAULT 'active',
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_cluster_nodes_cluster_node UNIQUE(cluster_id, node_id)
);

CREATE INDEX IF NOT EXISTS idx_cluster_nodes_cluster_id ON cluster_nodes(cluster_id);
CREATE INDEX IF NOT EXISTS idx_cluster_nodes_node_id ON cluster_nodes(node_id);

-- Cluster services table (stateless services)
CREATE TABLE IF NOT EXISTS cluster_services (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id UUID NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    image VARCHAR(255) NOT NULL,
    ports VARCHAR(255) NOT NULL DEFAULT '',
    env_vars JSONB NOT NULL DEFAULT '{}'::jsonb,
    replicas INT NOT NULL DEFAULT 1,
    swarm_service_id VARCHAR(128) NOT NULL DEFAULT '',
    status VARCHAR(50) NOT NULL DEFAULT 'running',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_cluster_services_cluster_name UNIQUE(cluster_id, name)
);

CREATE INDEX IF NOT EXISTS idx_cluster_services_cluster_id ON cluster_services(cluster_id);

-- Cluster service autoscaling policies
CREATE TABLE IF NOT EXISTS cluster_service_autoscaling (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_service_id UUID NOT NULL REFERENCES cluster_services(id) ON DELETE CASCADE,
    enabled BOOLEAN NOT NULL DEFAULT true,
    metric_type VARCHAR(20) NOT NULL DEFAULT 'cpu', -- 'cpu' or 'ram'
    scale_up_threshold DECIMAL(5,2) NOT NULL DEFAULT 75.00,
    scale_down_threshold DECIMAL(5,2) NOT NULL DEFAULT 30.00,
    scale_up_duration_sec INT NOT NULL DEFAULT 300,
    scale_down_duration_sec INT NOT NULL DEFAULT 600,
    min_replicas INT NOT NULL DEFAULT 1,
    max_replicas INT NOT NULL DEFAULT 4,
    cooldown_seconds INT NOT NULL DEFAULT 180,
    last_scaled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_cluster_service_autoscaling_service UNIQUE(cluster_service_id)
);

CREATE INDEX IF NOT EXISTS idx_cluster_service_autoscaling_service_id ON cluster_service_autoscaling(cluster_service_id);
