-- Remove duplicate rows (keep the latest per node+app) before adding the constraint.
DELETE FROM marketplace_apps
WHERE id NOT IN (
    SELECT DISTINCT ON (node_id, app_name) id
    FROM marketplace_apps
    ORDER BY node_id, app_name, created_at DESC
);

-- One app entry per (node, app_name) — retries upsert instead of accumulating rows.
ALTER TABLE marketplace_apps ADD CONSTRAINT marketplace_apps_node_app_uq UNIQUE (node_id, app_name);
