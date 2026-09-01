-- name: CreateAgentRun :one
INSERT INTO dayorder.agent_runs (
    id, user_id, intent, status, action_mode, scope, provider, model, started_at
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(intent), sqlc.arg(status),
    sqlc.arg(action_mode), sqlc.arg(scope), sqlc.narg(provider), sqlc.narg(model),
    sqlc.narg(started_at)
)
RETURNING *;

-- name: GetAgentRun :one
SELECT *
FROM dayorder.agent_runs
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id);

-- name: GetAgentRunForUpdate :one
SELECT *
FROM dayorder.agent_runs
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ListAgentRuns :many
SELECT *
FROM dayorder.agent_runs
WHERE user_id = sqlc.arg(user_id)
  AND (
      sqlc.narg(after_created_at)::timestamptz IS NULL
      OR created_at < sqlc.narg(after_created_at)::timestamptz
      OR (created_at = sqlc.narg(after_created_at)::timestamptz AND id < sqlc.narg(after_id)::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: CreateAgentStep :one
INSERT INTO dayorder.agent_steps (
    id, user_id, run_id, sequence_no, title, detail, status, metadata,
    started_at, finished_at
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(run_id), sqlc.arg(sequence_no),
    sqlc.arg(title), sqlc.arg(detail), sqlc.arg(status), sqlc.arg(metadata),
    sqlc.narg(started_at), sqlc.narg(finished_at)
)
RETURNING *;

-- name: ListAgentSteps :many
SELECT *
FROM dayorder.agent_steps
WHERE user_id = sqlc.arg(user_id) AND run_id = sqlc.arg(run_id)
ORDER BY sequence_no, id;

-- name: CreateAgentChange :one
INSERT INTO dayorder.agent_changes (
    id, user_id, run_id, change_type, target_type, target_id, base_version,
    patch, preview_before, preview_after, reason
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(run_id), sqlc.arg(change_type),
    sqlc.arg(target_type), sqlc.narg(target_id), sqlc.narg(base_version), sqlc.arg(patch),
    sqlc.narg(preview_before), sqlc.narg(preview_after), sqlc.arg(reason)
)
RETURNING *;

-- name: GetAgentChange :one
SELECT *
FROM dayorder.agent_changes
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id);

-- name: GetAgentChangeForUpdate :one
SELECT *
FROM dayorder.agent_changes
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ListAgentChanges :many
SELECT *
FROM dayorder.agent_changes
WHERE user_id = sqlc.arg(user_id) AND run_id = sqlc.arg(run_id)
ORDER BY created_at, id;

-- name: MarkAgentChangeApplied :one
UPDATE dayorder.agent_changes
SET status = 'applied',
    target_id = COALESCE(target_id, sqlc.arg(applied_target_id)),
    accepted_at = sqlc.arg(resolved_at),
    applied_at = sqlc.arg(resolved_at),
    version = version + 1,
    updated_at = sqlc.arg(resolved_at)
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND status = 'pending'
RETURNING *;

-- name: MarkAgentChangeRejected :one
UPDATE dayorder.agent_changes
SET status = 'rejected',
    version = version + 1,
    updated_at = sqlc.arg(resolved_at)
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND status = 'pending'
RETURNING *;

-- name: CompleteAgentRunIfResolved :one
UPDATE dayorder.agent_runs AS run
SET status = 'completed',
    finished_at = sqlc.arg(resolved_at),
    summary = CASE
        WHEN EXISTS (
            SELECT 1 FROM dayorder.agent_changes AS applied
            WHERE applied.user_id = run.user_id AND applied.run_id = run.id AND applied.status = 'applied'
        ) THEN '所有已确认变更均已处理。'
        ELSE '所有变更均已拒绝，没有修改业务数据。'
    END,
    version = version + 1,
    updated_at = sqlc.arg(resolved_at)
WHERE run.user_id = sqlc.arg(user_id)
  AND run.id = sqlc.arg(run_id)
  AND run.status = 'waiting'
  AND NOT EXISTS (
      SELECT 1 FROM dayorder.agent_changes AS pending
      WHERE pending.user_id = run.user_id AND pending.run_id = run.id AND pending.status = 'pending'
  )
RETURNING run.*;

-- name: StopAgentRun :one
UPDATE dayorder.agent_runs
SET status = 'stopped',
    finished_at = sqlc.arg(stopped_at),
    summary = '用户已停止运行，没有执行新的写入。',
    version = version + 1,
    updated_at = sqlc.arg(stopped_at)
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND status IN ('ready', 'reading', 'analyzing', 'waiting')
RETURNING *;

-- name: StartAgentRunAnalysis :one
UPDATE dayorder.agent_runs
SET status = 'analyzing',
    started_at = COALESCE(started_at, sqlc.arg(started_at)),
    version = version + 1,
    updated_at = sqlc.arg(started_at)
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND status = 'ready'
RETURNING *;

-- name: FinishAgentRunAnalysis :one
UPDATE dayorder.agent_runs
SET status = sqlc.arg(status),
    provider = sqlc.arg(provider),
    model = sqlc.arg(model),
    summary = sqlc.arg(summary),
    finished_at = CASE WHEN sqlc.arg(status) = 'completed' THEN sqlc.arg(finished_at) ELSE NULL END,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = sqlc.arg(finished_at)
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND status = 'analyzing'
RETURNING *;

-- name: FailAgentRun :one
UPDATE dayorder.agent_runs
SET status = 'failed',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    finished_at = sqlc.arg(finished_at),
    version = version + 1,
    updated_at = sqlc.arg(finished_at)
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND status IN ('ready', 'reading', 'analyzing')
RETURNING *;

-- name: CreateAgentSourceRef :one
INSERT INTO dayorder.agent_source_refs (
    id, user_id, run_id, entity_type, entity_id, entity_version, label_snapshot
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(run_id), sqlc.arg(entity_type),
    sqlc.arg(entity_id), sqlc.arg(entity_version), sqlc.arg(label_snapshot)
)
RETURNING *;

-- name: ListAgentSourceRefs :many
SELECT *
FROM dayorder.agent_source_refs
WHERE user_id = sqlc.arg(user_id) AND run_id = sqlc.arg(run_id)
ORDER BY created_at, id;

-- name: CreateAuditEvent :one
INSERT INTO dayorder.audit_events (
    id, user_id, actor_type, actor_id, action, request_id, before_data, after_data, metadata
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(actor_type), sqlc.narg(actor_id),
    sqlc.arg(action), sqlc.arg(request_id), sqlc.narg(before_data),
    sqlc.narg(after_data), sqlc.arg(metadata)
)
RETURNING *;

-- name: CreateAuditEventEntity :exec
INSERT INTO dayorder.audit_event_entities (
    audit_event_id, user_id, entity_type, entity_id
) VALUES (
    sqlc.arg(audit_event_id), sqlc.arg(user_id), sqlc.arg(entity_type), sqlc.arg(entity_id)
);

-- name: GetAuditEvent :one
SELECT *
FROM dayorder.audit_events
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id);

-- name: ListAuditEvents :many
SELECT *
FROM dayorder.audit_events
WHERE user_id = sqlc.arg(user_id)
  AND (
      sqlc.narg(after_created_at)::timestamptz IS NULL
      OR created_at < sqlc.narg(after_created_at)::timestamptz
      OR (created_at = sqlc.narg(after_created_at)::timestamptz AND id < sqlc.narg(after_id)::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: ListAuditEventEntities :many
SELECT *
FROM dayorder.audit_event_entities
WHERE user_id = sqlc.arg(user_id) AND audit_event_id = sqlc.arg(audit_event_id)
ORDER BY entity_type, entity_id;

-- name: CreateOutboxEvent :exec
INSERT INTO dayorder.outbox_events (
    id, user_id, event_type, aggregate_type, aggregate_id, payload, available_at
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(event_type), sqlc.arg(aggregate_type),
    sqlc.arg(aggregate_id), sqlc.arg(payload), sqlc.arg(available_at)
);
