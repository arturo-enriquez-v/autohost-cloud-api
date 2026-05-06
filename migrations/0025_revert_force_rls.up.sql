-- Revert FORCE ROW LEVEL SECURITY applied in 0024.
-- The app DB user is the table owner; with FORCE it is subject to RLS policies
-- that require `autohost.current_user_id` to be set — but the Go code never sets
-- that parameter, so every query fails with:
--   pq: unrecognized configuration parameter "autohost.current_user_id"
-- Removing FORCE restores the previous behaviour: the owner bypasses RLS
-- (policies still exist for non-owner roles / future enforcement).
ALTER TABLE nodes              NO FORCE ROW LEVEL SECURITY;
ALTER TABLE node_metrics       NO FORCE ROW LEVEL SECURITY;
ALTER TABLE node_commands      NO FORCE ROW LEVEL SECURITY;
ALTER TABLE jobs               NO FORCE ROW LEVEL SECURITY;
ALTER TABLE scheduled_tasks    NO FORCE ROW LEVEL SECURITY;
ALTER TABLE node_tokens        NO FORCE ROW LEVEL SECURITY;
ALTER TABLE node_alerts        NO FORCE ROW LEVEL SECURITY;
ALTER TABLE enroll_tokens      NO FORCE ROW LEVEL SECURITY;
ALTER TABLE refresh_tokens     NO FORCE ROW LEVEL SECURITY;
