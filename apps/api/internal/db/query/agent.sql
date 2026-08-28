-- name: CreateAgentRun :one
INSERT INTO dayorder.agent_runs (
    id, user_id, intent, status, action_mode, scope, provider, model, started_at
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(intent), sqlc.arg(status),
    sqlc.arg(action_mode), sqlc.arg(scope), sqlc.narg(provider), sqlc.narg(model),
    sqlc.narg(started_at)
)
RETURNING *;
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

-- name: CreateOutboxEvent :one
INSERT INTO dayorder.outbox_events (
    id, user_id, event_type, aggregate_type, aggregate_id, payload, available_at
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(event_type), sqlc.arg(aggregate_type),
    sqlc.arg(aggregate_id), sqlc.arg(payload), sqlc.arg(available_at)
)
RETURNING *;
