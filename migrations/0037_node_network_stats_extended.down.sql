ALTER TABLE node_network_stats DROP COLUMN IF EXISTS rps;
ALTER TABLE node_network_stats DROP COLUMN IF EXISTS requests;
ALTER TABLE node_network_stats DROP COLUMN IF EXISTS connections;
ALTER TABLE node_network_stats DROP COLUMN IF EXISTS peers;
