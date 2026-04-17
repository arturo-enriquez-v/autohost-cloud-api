-- Revert: remove 'install_app' from the allowed command_type values.
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_command_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_command_type_check
    CHECK (command_type IN ('default', 'custom'));
