BEGIN;

CREATE SCHEMA IF NOT EXISTS dayorder;

CREATE TABLE dayorder.users (
    id UUID PRIMARY KEY,
    email VARCHAR(254) NOT NULL,
    normalized_email VARCHAR(254) NOT NULL,
    display_name VARCHAR(80) NOT NULL,
    password_hash TEXT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending_verification',
    email_verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT users_status_check CHECK (status IN ('pending_verification', 'active', 'disabled', 'deletion_pending')),
    CONSTRAINT users_email_check CHECK (length(btrim(email)) BETWEEN 3 AND 254),
    CONSTRAINT users_normalized_email_check CHECK (normalized_email = lower(btrim(normalized_email))),
    CONSTRAINT users_active_verified_check CHECK (status <> 'active' OR email_verified_at IS NOT NULL)
);

CREATE UNIQUE INDEX users_normalized_email_active_uidx
    ON dayorder.users (normalized_email)
    WHERE deleted_at IS NULL;
CREATE INDEX users_status_idx ON dayorder.users (status) WHERE deleted_at IS NULL;

CREATE TABLE dayorder.sessions (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    user_agent TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    CONSTRAINT sessions_expiry_check CHECK (expires_at > created_at),
    CONSTRAINT sessions_user_pair_unique UNIQUE (user_id, id)
);

CREATE INDEX sessions_user_expires_idx ON dayorder.sessions (user_id, expires_at DESC);
CREATE INDEX sessions_active_expiry_idx ON dayorder.sessions (expires_at) WHERE revoked_at IS NULL;

CREATE TABLE dayorder.account_tokens (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    purpose VARCHAR(32) NOT NULL,
    token_hash BYTEA NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT account_tokens_purpose_check CHECK (purpose IN ('verify_email', 'reset_password', 'change_email')),
    CONSTRAINT account_tokens_expiry_check CHECK (expires_at > created_at)
);

CREATE INDEX account_tokens_user_purpose_idx
    ON dayorder.account_tokens (user_id, purpose, expires_at DESC);
CREATE INDEX account_tokens_active_expiry_idx
    ON dayorder.account_tokens (expires_at)
    WHERE consumed_at IS NULL;

CREATE TABLE dayorder.login_throttles (
    dimension VARCHAR(16) NOT NULL,
    key_hash BYTEA NOT NULL,
    window_started_at TIMESTAMPTZ NOT NULL,
    failures INTEGER NOT NULL DEFAULT 0,
    blocked_until TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (dimension, key_hash),
    CONSTRAINT login_throttles_dimension_check CHECK (dimension IN ('email', 'ip')),
    CONSTRAINT login_throttles_failures_check CHECK (failures >= 0)
);

CREATE INDEX login_throttles_cleanup_idx ON dayorder.login_throttles (updated_at);

CREATE TABLE dayorder.user_settings (
    user_id UUID PRIMARY KEY REFERENCES dayorder.users(id) ON DELETE CASCADE,
    schema_version INTEGER NOT NULL DEFAULT 1,
    version BIGINT NOT NULL DEFAULT 1,
    settings JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT user_settings_schema_version_check CHECK (schema_version > 0),
    CONSTRAINT user_settings_version_check CHECK (version > 0),
    CONSTRAINT user_settings_object_check CHECK (jsonb_typeof(settings) = 'object')
);

CREATE TABLE dayorder.user_devices (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    device_name VARCHAR(120) NOT NULL,
    platform VARCHAR(40) NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_sync_cursor BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,
    CONSTRAINT user_devices_cursor_check CHECK (last_sync_cursor >= 0),
    CONSTRAINT user_devices_user_pair_unique UNIQUE (user_id, id)
);

CREATE INDEX user_devices_user_active_idx
    ON dayorder.user_devices (user_id, last_seen_at DESC)
    WHERE revoked_at IS NULL;

CREATE TABLE dayorder.account_deletions (
    user_id UUID PRIMARY KEY REFERENCES dayorder.users(id) ON DELETE CASCADE,
    requested_at TIMESTAMPTZ NOT NULL,
    scheduled_for TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    request_hash BYTEA NOT NULL,
    CONSTRAINT account_deletions_schedule_check CHECK (scheduled_for >= requested_at)
);

COMMIT;
