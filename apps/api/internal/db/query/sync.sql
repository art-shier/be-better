-- name: SetUserContext :exec
SELECT dayorder.set_user_context(sqlc.arg(user_id));

-- name: AppendSyncChange :one
INSERT INTO dayorder.sync_changes (
    user_id, entity_type, entity_id, operation, entity_version
) VALUES (
    sqlc.arg(user_id), sqlc.arg(entity_type), sqlc.arg(entity_id),
    sqlc.arg(operation), sqlc.arg(entity_version)
)
RETURNING *;

-- name: CurrentSyncCursor :one
SELECT coalesce(max(sequence), 0)::bigint AS sequence
FROM dayorder.sync_changes
WHERE user_id = sqlc.arg(user_id);

-- name: ListSyncChanges :many
SELECT *
FROM dayorder.sync_changes
WHERE user_id = sqlc.arg(user_id)
  AND sequence > sqlc.arg(after_sequence)
ORDER BY sequence
LIMIT sqlc.arg(page_size);

-- name: GetClientMutation :one
SELECT *
FROM dayorder.client_mutations
WHERE user_id = sqlc.arg(user_id)
  AND device_id = sqlc.arg(device_id)
  AND mutation_id = sqlc.arg(mutation_id)
  AND expires_at > now();

-- name: CreateClientMutation :one
INSERT INTO dayorder.client_mutations (
    id, user_id, device_id, mutation_id, request_hash, expires_at
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(device_id), sqlc.arg(mutation_id),
    sqlc.arg(request_hash), sqlc.arg(expires_at)
)
RETURNING *;

-- name: CompleteClientMutation :one
UPDATE dayorder.client_mutations
SET response_status = sqlc.arg(response_status),
    response_body = sqlc.arg(response_body)
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND response_status IS NULL
RETURNING *;
