-- Add project_id column to nodes (direct FK, replaces the project_nodes join table approach)
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects(id) ON DELETE SET NULL;

-- Populate from existing project_nodes data (keep the first project assignment per node)
UPDATE nodes n
SET project_id = pn.project_id
FROM (
    SELECT DISTINCT ON (node_id) node_id, project_id
    FROM project_nodes
    ORDER BY node_id, added_at ASC
) pn
WHERE n.id = pn.node_id;
