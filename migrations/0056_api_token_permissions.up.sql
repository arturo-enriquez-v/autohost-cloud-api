-- Migration 0056: Per-token node permissions. A "custom" API token carries an explicit list of node
-- permissions (subset of AllNodePermissions); its effective permissions on a node are the intersection
-- with its owner's. "read" and "write" tokens keep an empty list.

ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS permissions TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE api_tokens DROP CONSTRAINT IF EXISTS api_tokens_scope_check;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_scope_check CHECK (scope IN ('read', 'write', 'custom'));
