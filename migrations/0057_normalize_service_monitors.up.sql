-- Bring monitors saved before the API validated their settings into the accepted ranges, so editing or
-- toggling them does not fail. Each value keeps the behaviour the agent already applied to it.
UPDATE service_monitors
SET interval_seconds = CASE
        WHEN interval_seconds <= 0 THEN 30
        WHEN interval_seconds < 5 THEN 5
        ELSE 3600
    END
WHERE interval_seconds < 5 OR interval_seconds > 3600;

UPDATE service_monitors
SET failure_threshold = CASE WHEN failure_threshold <= 0 THEN 3 ELSE 20 END
WHERE failure_threshold < 1 OR failure_threshold > 20;

UPDATE service_monitors
SET http_expected_status = 200
WHERE http_expected_status IS NULL OR http_expected_status NOT BETWEEN 100 AND 599;

-- The agent ran any unknown check type as a container check.
UPDATE service_monitors
SET check_type = 'process'
WHERE check_type NOT IN ('process', 'tcp', 'http');
