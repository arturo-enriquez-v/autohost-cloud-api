-- Add 'install_app' to the allowed command_type values in the jobs table.
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_command_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_command_type_check
    CHECK (command_type IN ('default', 'custom', 'install_app'));
