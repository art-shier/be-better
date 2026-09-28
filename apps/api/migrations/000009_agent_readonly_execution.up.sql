BEGIN;

CREATE TABLE dayorder.agent_run_executions (
    user_id UUID NOT NULL,
    run_id UUID NOT NULL,
    token UUID NOT NULL,
    execution_mode VARCHAR(16) NOT NULL,
    protocol_version VARCHAR(32) NOT NULL,
    runtime_version VARCHAR(32) NOT NULL,
    model_profile VARCHAR(120) NOT NULL,
    timezone VARCHAR(80) NOT NULL,
    capabilities JSONB NOT NULL,
    budget JSONB NOT NULL,
    deadline TIMESTAMPTZ NOT NULL,
    known_usage JSONB NOT NULL DEFAULT '{}'::jsonb,
    reserved_tokens INTEGER NOT NULL DEFAULT 0,
    usage_complete BOOLEAN NOT NULL DEFAULT false,
    result_origin VARCHAR(32) NOT NULL,
    PRIMARY KEY (user_id, run_id),
    CONSTRAINT agent_run_executions_run_fk FOREIGN KEY (user_id, run_id)
        REFERENCES dayorder.agent_runs(user_id, id) ON DELETE CASCADE,
    CONSTRAINT agent_run_executions_mode_check CHECK (execution_mode IN ('foreground', 'background')),
    CONSTRAINT agent_run_executions_protocol_check CHECK (length(btrim(protocol_version)) > 0),
    CONSTRAINT agent_run_executions_runtime_check CHECK (length(btrim(runtime_version)) > 0),
    CONSTRAINT agent_run_executions_profile_check CHECK (length(btrim(model_profile)) > 0),
    CONSTRAINT agent_run_executions_timezone_check CHECK (length(btrim(timezone)) > 0),
    CONSTRAINT agent_run_executions_capabilities_check CHECK (jsonb_typeof(capabilities) = 'object'),
    CONSTRAINT agent_run_executions_budget_check CHECK (jsonb_typeof(budget) = 'object'),
    CONSTRAINT agent_run_executions_usage_check CHECK (jsonb_typeof(known_usage) = 'object'),
    CONSTRAINT agent_run_executions_reserved_tokens_check CHECK (reserved_tokens >= 0),
    CONSTRAINT agent_run_executions_result_origin_check CHECK (result_origin IN ('client_reported', 'server_runtime'))
);

CREATE INDEX agent_run_executions_user_deadline_idx
    ON dayorder.agent_run_executions (user_id, deadline, run_id);

CREATE TABLE dayorder.agent_run_operations (
    user_id UUID NOT NULL,
    run_id UUID NOT NULL,
    kind VARCHAR(32) NOT NULL,
    operation_id VARCHAR(240) NOT NULL,
    state VARCHAR(24) NOT NULL,
    operation_hash BYTEA NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    reserved_tokens INTEGER NOT NULL DEFAULT 0,
    usage JSONB NOT NULL DEFAULT '{}'::jsonb,
    usage_complete BOOLEAN NOT NULL DEFAULT false,
    error_code VARCHAR(80) NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    PRIMARY KEY (user_id, run_id, kind, operation_id),
    CONSTRAINT agent_run_operations_execution_fk FOREIGN KEY (user_id, run_id)
        REFERENCES dayorder.agent_run_executions(user_id, run_id) ON DELETE CASCADE,
    CONSTRAINT agent_run_operations_kind_check CHECK (kind IN ('provider_turn', 'calendar_read')),
    CONSTRAINT agent_run_operations_id_check CHECK (length(btrim(operation_id)) BETWEEN 1 AND 240),
    CONSTRAINT agent_run_operations_state_check CHECK (state IN ('running', 'completed', 'failed', 'unknown')),
    CONSTRAINT agent_run_operations_hash_check CHECK (octet_length(operation_hash) = 32),
    CONSTRAINT agent_run_operations_attempts_check CHECK (attempts >= 0),
    CONSTRAINT agent_run_operations_reserved_tokens_check CHECK (reserved_tokens >= 0),
    CONSTRAINT agent_run_operations_usage_check CHECK (jsonb_typeof(usage) = 'object'),
    CONSTRAINT agent_run_operations_times_check CHECK (finished_at IS NULL OR finished_at >= started_at)
);

CREATE INDEX agent_run_operations_run_started_idx
    ON dayorder.agent_run_operations (user_id, run_id, started_at, kind, operation_id);

ALTER TABLE dayorder.agent_run_executions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON dayorder.agent_run_executions
USING (user_id = dayorder.current_user_id())
WITH CHECK (user_id = dayorder.current_user_id());
ALTER TABLE dayorder.agent_run_operations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON dayorder.agent_run_operations
USING (user_id = dayorder.current_user_id())
WITH CHECK (user_id = dayorder.current_user_id());

GRANT SELECT, INSERT ON dayorder.agent_run_executions, dayorder.agent_run_operations
TO dayorder_api, dayorder_worker;

REVOKE UPDATE ON dayorder.agent_runs FROM dayorder_api, dayorder_worker;
GRANT UPDATE (
    status, provider, model, started_at, finished_at, summary,
    error_code, error_message, version, updated_at
) ON dayorder.agent_runs TO dayorder_api, dayorder_worker;

GRANT UPDATE (token, known_usage, reserved_tokens, usage_complete, result_origin)
ON dayorder.agent_run_executions TO dayorder_api, dayorder_worker;

GRANT UPDATE (state, attempts, reserved_tokens, usage, usage_complete, error_code, finished_at)
ON dayorder.agent_run_operations TO dayorder_api, dayorder_worker;

COMMIT;
