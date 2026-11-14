-- Rollback Migration 0051: Users Management, Roles, Project Memberships & Granular Node Permissions

DROP TABLE IF EXISTS user_node_permissions;
DROP TABLE IF EXISTS project_members;

ALTER TABLE users
    DROP COLUMN IF EXISTS created_by,
    DROP COLUMN IF EXISTS is_active,
    DROP COLUMN IF EXISTS role;
