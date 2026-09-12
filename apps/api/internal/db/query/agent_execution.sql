-- name: LockAgentExecutionAccount :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(user_id)::text, 0));

-- name: CreateReadonlyAgentRun :exec
INSERT INTO dayorder.agent_runs (
    id, user_id, intent, status, action_mode, scope, provider, model, started_at,
    finished_at, summary, error_code, error_message
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(intent), sqlc.arg(status),
    sqlc.arg(action_mode), sqlc.arg(scope), sqlc.narg(provider), sqlc.narg(model),
    sqlc.narg(started_at), sqlc.narg(finished_at), sqlc.narg(summary),
    sqlc.narg(error_code), sqlc.narg(error_message)
);

-- name: CreateReadonlyExecution :exec
INSERT INTO dayorder.agent_run_executions (
    user_id, run_id, token, execution_mode, protocol_version, runtime_version,
    model_profile, timezone, capabilities, budget, deadline, known_usage,
    reserved_tokens, usage_complete, result_origin
) VALUES (
    sqlc.arg(user_id), sqlc.arg(run_id), sqlc.arg(token), sqlc.arg(execution_mode),
    sqlc.arg(protocol_version), sqlc.arg(runtime_version), sqlc.arg(model_profile),
    sqlc.arg(timezone), sqlc.arg(capabilities), sqlc.arg(budget), sqlc.arg(deadline),
    sqlc.arg(known_usage), sqlc.arg(reserved_tokens), sqlc.arg(usage_complete),
    sqlc.arg(result_origin)
);

-- name: GetReadonlyExecution :one
SELECT * FROM dayorder.agent_run_executions
WHERE user_id = sqlc.arg(user_id) AND run_id = sqlc.arg(run_id);

-- name: GetReadonlyExecutionForUpdate :one
SELECT * FROM dayorder.agent_run_executions
WHERE user_id = sqlc.arg(user_id) AND run_id = sqlc.arg(run_id)
FOR UPDATE;

-- name: ListActiveReadonlyExecutionIDs :many
SELECT execution.run_id
FROM dayorder.agent_run_executions AS execution
JOIN dayorder.agent_runs AS run
  ON run.user_id = execution.user_id AND run.id = execution.run_id
WHERE execution.user_id = sqlc.arg(user_id)
  AND run.status IN ('ready', 'reading', 'analyzing', 'waiting', 'applying')
ORDER BY run.created_at, run.id;

-- name: CountReadonlyExecutionsCreatedSince :one
SELECT count(*)
FROM dayorder.agent_run_executions AS execution
JOIN dayorder.agent_runs AS run
  ON run.user_id = execution.user_id AND run.id = execution.run_id
WHERE execution.user_id = sqlc.arg(user_id)
  AND run.created_at >= sqlc.arg(created_since);

-- name: SaveReadonlyExecution :execrows
WITH saved_run AS (
    UPDATE dayorder.agent_runs AS run
    SET status = sqlc.arg(status),
        provider = sqlc.narg(provider),
        model = sqlc.narg(model),
        started_at = sqlc.narg(started_at),
        finished_at = sqlc.narg(finished_at),
        summary = sqlc.narg(summary),
        error_code = sqlc.narg(error_code),
        error_message = sqlc.narg(error_message),
        version = version + 1,
        updated_at = statement_timestamp()
    WHERE run.user_id = sqlc.arg(user_id)
      AND run.id = sqlc.arg(run_id)
      AND run.version = sqlc.arg(expected_version)
      AND EXISTS (
          SELECT 1 FROM dayorder.agent_run_executions AS execution
          WHERE execution.user_id = run.user_id
            AND execution.run_id = run.id
            AND execution.token = sqlc.arg(expected_token)
      )
    RETURNING run.id
)
UPDATE dayorder.agent_run_executions AS execution
SET token = sqlc.arg(new_token),
    known_usage = sqlc.arg(known_usage),
    reserved_tokens = sqlc.arg(reserved_tokens),
    usage_complete = sqlc.arg(usage_complete),
    result_origin = sqlc.arg(result_origin)
WHERE execution.user_id = sqlc.arg(user_id)
  AND execution.run_id = sqlc.arg(run_id)
  AND execution.token = sqlc.arg(expected_token)
  AND EXISTS (SELECT 1 FROM saved_run);

-- name: ListReadonlyOperations :many
SELECT * FROM dayorder.agent_run_operations
WHERE user_id = sqlc.arg(user_id) AND run_id = sqlc.arg(run_id)
ORDER BY started_at, kind, operation_id;

-- name: InsertReadonlyOperation :exec
INSERT INTO dayorder.agent_run_operations (
    user_id, run_id, kind, operation_id, state, operation_hash, attempts,
    reserved_tokens, usage, usage_complete, error_code, started_at, finished_at
) VALUES (
    sqlc.arg(user_id), sqlc.arg(run_id), sqlc.arg(kind), sqlc.arg(operation_id),
    sqlc.arg(state), sqlc.arg(operation_hash), sqlc.arg(attempts),
    sqlc.arg(reserved_tokens), sqlc.arg(usage), sqlc.arg(usage_complete),
    sqlc.arg(error_code), sqlc.arg(started_at), sqlc.narg(finished_at)
);

-- name: UpdateReadonlyOperation :execrows
UPDATE dayorder.agent_run_operations
SET state = sqlc.arg(state),
    attempts = sqlc.arg(attempts),
    reserved_tokens = sqlc.arg(reserved_tokens),
    usage = sqlc.arg(usage),
    usage_complete = sqlc.arg(usage_complete),
    error_code = sqlc.arg(error_code),
    finished_at = sqlc.narg(finished_at)
WHERE user_id = sqlc.arg(user_id)
  AND run_id = sqlc.arg(run_id)
  AND kind = sqlc.arg(kind)
  AND operation_id = sqlc.arg(operation_id)
  AND state = sqlc.arg(expected_state);

-- name: InsertReadonlySourceRef :exec
INSERT INTO dayorder.agent_source_refs (
    id, user_id, run_id, entity_type, entity_id, entity_version, label_snapshot
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(run_id), sqlc.arg(entity_type),
    sqlc.arg(entity_id), sqlc.arg(entity_version), sqlc.arg(label_snapshot)
)
ON CONFLICT (user_id, run_id, entity_type, entity_id, entity_version) DO NOTHING;

-- name: NextReadonlyAgentStepSequence :one
SELECT (coalesce(max(sequence_no), 0) + 1)::integer
FROM dayorder.agent_steps
WHERE user_id = sqlc.arg(user_id) AND run_id = sqlc.arg(run_id);
