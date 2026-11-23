-- Custom tokens cannot be represented without the permissions column: remove them.
DELETE FROM api_tokens WHERE scope = 'custom';

ALTER TABLE api_tokens DROP CONSTRAINT IF EXISTS api_tokens_scope_check;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_scope_check CHECK (scope IN ('read', 'write'));

ALTER TABLE api_tokens DROP COLUMN IF EXISTS permissions;
