ALTER TABLE user_report_settings ADD COLUMN IF NOT EXISTS excluded_node_ids JSONB NOT NULL DEFAULT '[]'::jsonb;
