ALTER TABLE scheduled_tasks DROP COLUMN IF EXISTS schedule_type;
ALTER TABLE scheduled_tasks DROP COLUMN IF EXISTS interval_seconds;
ALTER TABLE scheduled_tasks DROP COLUMN IF EXISTS run_at_time;
ALTER TABLE scheduled_tasks DROP COLUMN IF EXISTS last_job_id;
ALTER TABLE scheduled_tasks ADD COLUMN interval_minutes INT NOT NULL DEFAULT 60 CHECK (interval_minutes > 0);
