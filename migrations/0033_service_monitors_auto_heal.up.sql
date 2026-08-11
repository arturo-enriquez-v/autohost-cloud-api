ALTER TABLE service_monitors ADD COLUMN IF NOT EXISTS auto_heal_config JSONB DEFAULT '{}'::jsonb;
ALTER TABLE service_monitors ADD COLUMN IF NOT EXISTS auto_heal_state JSONB DEFAULT '{}'::jsonb;
