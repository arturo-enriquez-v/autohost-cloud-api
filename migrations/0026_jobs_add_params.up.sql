-- Add params column to jobs for persisting parameterized dispatch payloads
-- (e.g. marketplace.install compose+env, caddy route config).
-- This allows re-dispatching pending jobs when a node reconnects.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS params TEXT NOT NULL DEFAULT '';
