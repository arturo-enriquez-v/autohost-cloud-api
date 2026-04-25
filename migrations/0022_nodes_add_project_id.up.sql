-- Add project_id column to nodes (direct FK, replaces the project_nodes join table approach)
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects(id) ON DELETE SET NULL;
