ALTER TABLE agent_runtime DROP CONSTRAINT IF EXISTS agent_runtime_status_check;

ALTER TABLE agent_runtime ADD CONSTRAINT agent_runtime_status_check
    CHECK (status IN ('online', 'offline', 'disabled'));
