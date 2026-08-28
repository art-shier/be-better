BEGIN;

DROP FUNCTION dayorder.authenticate_session(BYTEA);
DROP FUNCTION dayorder.lookup_login_account(VARCHAR);

CREATE FUNCTION dayorder.authenticate_session(p_token_hash BYTEA)
RETURNS TABLE (
    session_id UUID,
    user_id UUID,
    user_status VARCHAR(32),
    email VARCHAR(254),
    display_name VARCHAR(80),
    email_verified_at TIMESTAMPTZ,
    session_expires_at TIMESTAMPTZ
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, dayorder
AS $function$
    SELECT
        session.id,
        account.id,
        account.status,
        account.email,
        account.display_name,
        account.email_verified_at,
        session.expires_at
    FROM dayorder.sessions AS session
    JOIN dayorder.users AS account ON account.id = session.user_id
    WHERE session.token_hash = p_token_hash
      AND session.revoked_at IS NULL
      AND session.expires_at > statement_timestamp()
      AND account.deleted_at IS NULL
    LIMIT 1
$function$;

CREATE FUNCTION dayorder.lookup_login_account(p_normalized_email VARCHAR)
RETURNS TABLE (
    user_id UUID,
    email VARCHAR(254),
    normalized_email VARCHAR(254),
    display_name VARCHAR(80),
    password_hash TEXT,
    user_status VARCHAR(32),
    email_verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, dayorder
AS $function$
    SELECT
        account.id,
        account.email,
        account.normalized_email,
        account.display_name,
        account.password_hash,
        account.status,
        account.email_verified_at,
        account.created_at,
        account.updated_at
    FROM dayorder.users AS account
    WHERE account.normalized_email = lower(btrim(p_normalized_email))
      AND account.deleted_at IS NULL
    LIMIT 1
$function$;

CREATE FUNCTION dayorder.login_throttle_status(
    p_dimension VARCHAR,
    p_key_hash BYTEA
)
RETURNS TABLE (
    failures INTEGER,
    blocked_until TIMESTAMPTZ
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, dayorder
AS $function$
    SELECT throttle.failures, throttle.blocked_until
    FROM dayorder.login_throttles AS throttle
    WHERE throttle.dimension = p_dimension
      AND throttle.key_hash = p_key_hash
    LIMIT 1
$function$;

CREATE FUNCTION dayorder.record_login_failure(
    p_dimension VARCHAR,
    p_key_hash BYTEA,
    p_window INTERVAL DEFAULT INTERVAL '15 minutes',
    p_max_failures INTEGER DEFAULT 5,
    p_block_duration INTERVAL DEFAULT INTERVAL '15 minutes'
)
RETURNS TABLE (
    failures INTEGER,
    blocked_until TIMESTAMPTZ
)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, dayorder
AS $function$
BEGIN
    IF p_dimension NOT IN ('email', 'ip') THEN
        RAISE EXCEPTION 'invalid login throttle dimension' USING ERRCODE = '22023';
    END IF;
    IF p_window < INTERVAL '1 minute' OR p_window > INTERVAL '24 hours' THEN
        RAISE EXCEPTION 'invalid login throttle window' USING ERRCODE = '22023';
    END IF;
    IF p_max_failures < 1 OR p_max_failures > 100 THEN
        RAISE EXCEPTION 'invalid login throttle maximum' USING ERRCODE = '22023';
    END IF;
    IF p_block_duration < INTERVAL '1 minute' OR p_block_duration > INTERVAL '24 hours' THEN
        RAISE EXCEPTION 'invalid login throttle block duration' USING ERRCODE = '22023';
    END IF;

    RETURN QUERY
    INSERT INTO dayorder.login_throttles AS throttle (
        dimension, key_hash, window_started_at, failures, blocked_until, updated_at
    ) VALUES (
        p_dimension,
        p_key_hash,
        statement_timestamp(),
        1,
        CASE WHEN p_max_failures = 1 THEN statement_timestamp() + p_block_duration END,
        statement_timestamp()
    )
    ON CONFLICT (dimension, key_hash) DO UPDATE
    SET window_started_at = CASE
            WHEN throttle.window_started_at <= statement_timestamp() - p_window
                THEN statement_timestamp()
            ELSE throttle.window_started_at
        END,
        failures = CASE
            WHEN throttle.window_started_at <= statement_timestamp() - p_window
                THEN 1
            ELSE throttle.failures + 1
        END,
        blocked_until = CASE
            WHEN (
                CASE
                    WHEN throttle.window_started_at <= statement_timestamp() - p_window
                        THEN 1
                    ELSE throttle.failures + 1
                END
            ) >= p_max_failures
                THEN statement_timestamp() + p_block_duration
            WHEN throttle.blocked_until > statement_timestamp()
                THEN throttle.blocked_until
            ELSE NULL
        END,
        updated_at = statement_timestamp()
    RETURNING throttle.failures, throttle.blocked_until;
END
$function$;

CREATE FUNCTION dayorder.clear_login_throttle(
    p_dimension VARCHAR,
    p_key_hash BYTEA
)
RETURNS VOID
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, dayorder
AS $function$
    DELETE FROM dayorder.login_throttles
    WHERE dimension = p_dimension AND key_hash = p_key_hash
$function$;

CREATE OR REPLACE FUNCTION dayorder.complete_outbox_event(p_id UUID, p_lock_token UUID)
RETURNS BOOLEAN
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, dayorder
AS $function$
BEGIN
    UPDATE dayorder.outbox_events AS event
    SET status = 'processed',
        payload = CASE
            WHEN event.event_type IN ('email.verification.requested', 'email.password_reset.requested')
                THEN '{}'::jsonb
            ELSE event.payload
        END,
        processed_at = statement_timestamp(),
        locked_at = NULL,
        lock_token = NULL,
        last_error = NULL
    WHERE event.id = p_id
      AND event.status = 'processing'
      AND event.lock_token = p_lock_token;
    RETURN FOUND;
END
$function$;

CREATE OR REPLACE FUNCTION dayorder.retry_outbox_event(
    p_id UUID,
    p_lock_token UUID,
    p_available_at TIMESTAMPTZ,
    p_last_error TEXT,
    p_terminal BOOLEAN DEFAULT false
)
RETURNS BOOLEAN
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, dayorder
AS $function$
BEGIN
    UPDATE dayorder.outbox_events AS event
    SET status = CASE WHEN p_terminal THEN 'dead' ELSE 'pending' END,
        payload = CASE
            WHEN p_terminal AND event.event_type IN ('email.verification.requested', 'email.password_reset.requested')
                THEN '{}'::jsonb
            ELSE event.payload
        END,
        available_at = CASE
            WHEN p_terminal THEN event.available_at
            ELSE greatest(p_available_at, statement_timestamp())
        END,
        locked_at = NULL,
        lock_token = NULL,
        last_error = left(coalesce(p_last_error, ''), 4000),
        processed_at = NULL
    WHERE event.id = p_id
      AND event.status = 'processing'
      AND event.lock_token = p_lock_token;
    RETURN FOUND;
END
$function$;

REVOKE ALL ON FUNCTION dayorder.authenticate_session(BYTEA) FROM PUBLIC;
REVOKE ALL ON FUNCTION dayorder.lookup_login_account(VARCHAR) FROM PUBLIC;
REVOKE ALL ON FUNCTION dayorder.login_throttle_status(VARCHAR, BYTEA) FROM PUBLIC;
REVOKE ALL ON FUNCTION dayorder.record_login_failure(VARCHAR, BYTEA, INTERVAL, INTEGER, INTERVAL) FROM PUBLIC;
REVOKE ALL ON FUNCTION dayorder.clear_login_throttle(VARCHAR, BYTEA) FROM PUBLIC;

GRANT EXECUTE ON FUNCTION dayorder.authenticate_session(BYTEA) TO dayorder_api;
GRANT EXECUTE ON FUNCTION dayorder.lookup_login_account(VARCHAR) TO dayorder_api;
GRANT EXECUTE ON FUNCTION dayorder.login_throttle_status(VARCHAR, BYTEA) TO dayorder_api;
GRANT EXECUTE ON FUNCTION dayorder.record_login_failure(VARCHAR, BYTEA, INTERVAL, INTEGER, INTERVAL) TO dayorder_api;
GRANT EXECUTE ON FUNCTION dayorder.clear_login_throttle(VARCHAR, BYTEA) TO dayorder_api;
GRANT SELECT ON dayorder.schema_migrations TO dayorder_api;

COMMIT;
