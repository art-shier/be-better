-- name: CreateUser :one
INSERT INTO dayorder.users (
    id, email, normalized_email, display_name, password_hash, status, email_verified_at
) VALUES (
    sqlc.arg(id),
    sqlc.arg(email),
    sqlc.arg(normalized_email),
    sqlc.arg(display_name),
    sqlc.arg(password_hash),
    sqlc.arg(status),
    sqlc.narg(email_verified_at)
)
RETURNING *;

-- name: LookupLoginAccount :one
SELECT
    result.user_id::uuid AS user_id,
    result.password_hash::text AS password_hash,
    result.user_status::varchar AS user_status,
    result.email_verified_at::timestamptz AS email_verified_at
FROM dayorder.lookup_login_account(sqlc.arg(normalized_email)) AS result;

-- name: AuthenticateSession :one
SELECT
    result.session_id::uuid AS session_id,
    result.user_id::uuid AS user_id,
    result.user_status::varchar AS user_status,
    result.display_name::varchar AS display_name,
    result.email_verified_at::timestamptz AS email_verified_at,
    result.session_expires_at::timestamptz AS session_expires_at
FROM dayorder.authenticate_session(sqlc.arg(token_hash)) AS result;

-- name: CreateSession :one
INSERT INTO dayorder.sessions (
    id, user_id, token_hash, user_agent, expires_at
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(token_hash), sqlc.arg(user_agent), sqlc.arg(expires_at)
)
RETURNING *;

-- name: RevokeSession :execrows
UPDATE dayorder.sessions
SET revoked_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(session_id)
  AND revoked_at IS NULL;

-- name: CreateAccountToken :one
INSERT INTO dayorder.account_tokens (
    id, user_id, purpose, token_hash, expires_at
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(purpose), sqlc.arg(token_hash), sqlc.arg(expires_at)
)
RETURNING *;

-- name: LookupAccountToken :one
SELECT
    result.token_id::uuid AS token_id,
    result.user_id::uuid AS user_id,
    result.purpose::varchar AS purpose,
    result.expires_at::timestamptz AS expires_at
FROM dayorder.lookup_account_token(sqlc.arg(token_hash)) AS result;

-- name: ConsumeAccountToken :execrows
UPDATE dayorder.account_tokens
SET consumed_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(token_id)
  AND consumed_at IS NULL
  AND expires_at > now();

-- name: GetUserSettings :one
SELECT * FROM dayorder.user_settings WHERE user_id = sqlc.arg(user_id);

-- name: UpsertUserSettings :one
INSERT INTO dayorder.user_settings (user_id, schema_version, version, settings)
VALUES (sqlc.arg(user_id), sqlc.arg(schema_version), 1, sqlc.arg(settings))
ON CONFLICT (user_id) DO UPDATE
SET schema_version = EXCLUDED.schema_version,
    settings = EXCLUDED.settings,
    version = dayorder.user_settings.version + 1,
    updated_at = now()
WHERE dayorder.user_settings.version = sqlc.arg(expected_version)
RETURNING *;
