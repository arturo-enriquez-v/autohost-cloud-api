-- Migration 0051: Users Management, Roles, Project Memberships & Granular Node Permissions

-- 1. Add global role, active status, and creator tracking to users
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS role VARCHAR(32) NOT NULL DEFAULT 'member',
    ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS created_by UUID REFERENCES users(id) ON DELETE SET NULL;

-- Backfill existing instance owners / node owners / project owners as 'admin'
UPDATE users
SET role = 'admin'
WHERE id IN (
    SELECT id FROM users ORDER BY created_at ASC LIMIT 1
)
OR id IN (
    SELECT DISTINCT owner_id FROM nodes WHERE owner_id IS NOT NULL
)
OR id IN (
    SELECT DISTINCT user_id FROM projects WHERE user_id IS NOT NULL
);

-- 2. Project Memberships (links non-admin users to projects)
CREATE TABLE IF NOT EXISTS project_members (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    default_role        VARCHAR(32) NOT NULL DEFAULT 'viewer', -- 'viewer', 'operator', 'editor', 'custom'
    all_nodes           BOOLEAN NOT NULL DEFAULT false,
    default_permissions JSONB NOT NULL DEFAULT '[]'::jsonb,
    added_by            UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(project_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_project_members_user ON project_members(user_id);
CREATE INDEX IF NOT EXISTS idx_project_members_project ON project_members(project_id);

-- 3. Granular Per-Node Permissions for Users
CREATE TABLE IF NOT EXISTS user_node_permissions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    project_id  UUID REFERENCES projects(id) ON DELETE CASCADE,
    node_id     UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    role_preset VARCHAR(32) NOT NULL DEFAULT 'viewer', -- 'viewer', 'operator', 'editor', 'custom'
    permissions JSONB NOT NULL DEFAULT '[]'::jsonb,
    granted_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, node_id)
);

CREATE INDEX IF NOT EXISTS idx_user_node_perms_user ON user_node_permissions(user_id);
CREATE INDEX IF NOT EXISTS idx_user_node_perms_node ON user_node_permissions(node_id);
CREATE INDEX IF NOT EXISTS idx_user_node_perms_project ON user_node_permissions(project_id);
