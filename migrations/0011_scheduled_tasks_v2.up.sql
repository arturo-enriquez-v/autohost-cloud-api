-- Replace interval_minutes with flexible schedule_type + interval_seconds + run_at_time
-- Add last_job_id to track the most recent execution log

ALTER TABLE scheduled_tasks
  ADD COLUMN schedule_type    TEXT NOT NULL DEFAULT 'interval'
      CHECK (schedule_type IN ('interval', 'daily')),
  ADD COLUMN interval_seconds INT,
  ADD COLUMN run_at_time      TEXT,
  ADD COLUMN last_job_id      UUID REFERENCES jobs(id) ON DELETE SET NULL;

-- Migrate existing rows: convert interval_minutes → interval_seconds
UPDATE scheduled_tasks
SET interval_seconds = interval_minutes * 60
WHERE schedule_type = 'interval';

ALTER TABLE scheduled_tasks DROP COLUMN interval_minutes;
