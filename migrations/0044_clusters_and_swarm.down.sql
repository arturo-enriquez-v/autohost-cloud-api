DROP TABLE IF EXISTS cluster_service_autoscaling;
DROP TABLE IF EXISTS cluster_services;
DROP TABLE IF EXISTS cluster_nodes;
DROP TABLE IF EXISTS clusters;

ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_command_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_command_type_check
    CHECK (command_type IN ('default', 'custom', 'install_app'));
