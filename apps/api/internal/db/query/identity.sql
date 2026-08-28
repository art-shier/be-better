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
    result.email::varchar AS email,
    result.normalized_email::varchar AS normalized_email,
    result.display_name::varchar AS display_name,
    result.password_hash::text AS password_hash,
    result.user_status::varchar AS user_status,
    result.email_verified_at::timestamptz AS email_verified_at,
    result.created_at::timestamptz AS created_at,
    result.updated_at::timestamptz AS updated_at
FROM dayorder.lookup_login_account(sqlc.arg(normalized_email)) AS result;

-- name: AuthenticateSession :one
SELECT
    result.session_id::uuid AS session_id,
    result.user_id::uuid AS user_id,
    result.user_status::varchar AS user_status,
    result.email::varchar AS email,
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

-- name: GetSession :one
SELECT *
FROM dayorder.sessions
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(session_id);

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

-- name: InvalidateAccountTokens :execrows
UPDATE dayorder.account_tokens
SET consumed_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND purpose = sqlc.arg(purpose)
  AND consumed_at IS NULL;

-- name: GetUserSettings :one
SELECT * FROM dayorder.user_settings WHERE user_id = sqlc.arg(user_id);

-- name: CreateUserSettings :one
INSERT INTO dayorder.user_settings (user_id, schema_version, version, settings)
VALUES (sqlc.arg(user_id), 1, 1, '{}'::jsonb)
RETURNING *;

-- name: GetUser :one
SELECT *
FROM dayorder.users
WHERE id = sqlc.arg(user_id)
  AND deleted_at IS NULL;

-- name: ActivateUser :one
UPDATE dayorder.users
SET status = 'active',
    email_verified_at = coalesce(email_verified_at, now()),
    updated_at = now()
WHERE id = sqlc.arg(user_id)
  AND status = 'pending_verification'
  AND deleted_at IS NULL
RETURNING *;

-- name: PasswordHashByUserID :one
SELECT password_hash
FROM dayorder.users
WHERE id = sqlc.arg(user_id)
  AND deleted_at IS NULL;

-- name: UpdatePasswordHash :execrows
UPDATE dayorder.users
SET password_hash = sqlc.arg(password_hash), updated_at = now()
WHERE id = sqlc.arg(user_id)
  AND deleted_at IS NULL;

-- name: UpdateDisplayName :one
UPDATE dayorder.users
SET display_name = sqlc.arg(display_name), updated_at = now()
WHERE id = sqlc.arg(user_id)
  AND deleted_at IS NULL
RETURNING *;

-- name: UpdateEmail :one
UPDATE dayorder.users
SET email = sqlc.arg(email),
    normalized_email = sqlc.arg(normalized_email),
    status = 'pending_verification',
    email_verified_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(user_id)
  AND status = 'active'
  AND deleted_at IS NULL
RETURNING *;

-- name: TouchSession :execrows
UPDATE dayorder.sessions
SET last_seen_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(session_id)
  AND revoked_at IS NULL
  AND last_seen_at < now() - sqlc.arg(touch_interval)::interval;

-- name: RevokeAllUserSessions :execrows
UPDATE dayorder.sessions
SET revoked_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL;

-- name: LoginThrottleStatus :one
SELECT
    result.failures::integer AS failures,
    result.blocked_until::timestamptz AS blocked_until
FROM dayorder.login_throttle_status(sqlc.arg(dimension), sqlc.arg(key_hash)) AS result;

-- name: RecordLoginFailure :one
SELECT
    result.failures::integer AS failures,
    result.blocked_until::timestamptz AS blocked_until
FROM dayorder.record_login_failure(sqlc.arg(dimension), sqlc.arg(key_hash)) AS result;

-- name: ClearLoginThrottle :exec
SELECT dayorder.clear_login_throttle(sqlc.arg(dimension), sqlc.arg(key_hash));

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
