BEGIN;

DO $dayorder_roles$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'dayorder_api') THEN
        RAISE EXCEPTION 'database role dayorder_api is missing; run deploy/scripts/bootstrap-roles.sql first';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'dayorder_worker') THEN
        RAISE EXCEPTION 'database role dayorder_worker is missing; run deploy/scripts/bootstrap-roles.sql first';
    END IF;
END
$dayorder_roles$;

CREATE FUNCTION dayorder.current_user_id()
RETURNS UUID
LANGUAGE sql
STABLE
PARALLEL SAFE
AS $function$
    SELECT nullif(pg_catalog.current_setting('dayorder.user_id', true), '')::uuid
$function$;

CREATE FUNCTION dayorder.set_user_context(p_user_id UUID)
RETURNS VOID
LANGUAGE sql
VOLATILE
AS $function$
    SELECT pg_catalog.set_config('dayorder.user_id', p_user_id::text, true)::void
$function$;

CREATE FUNCTION dayorder.authenticate_session(p_token_hash BYTEA)
RETURNS TABLE (
    session_id UUID,
    user_id UUID,
    user_status VARCHAR(32),
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
    password_hash TEXT,
    user_status VARCHAR(32),
    email_verified_at TIMESTAMPTZ
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, dayorder
AS $function$
    SELECT account.id, account.password_hash, account.status, account.email_verified_at
    FROM dayorder.users AS account
    WHERE account.normalized_email = lower(btrim(p_normalized_email))
      AND account.deleted_at IS NULL
    LIMIT 1
$function$;

CREATE FUNCTION dayorder.lookup_account_token(p_token_hash BYTEA)
RETURNS TABLE (
    token_id UUID,
    user_id UUID,
    purpose VARCHAR(32),
    expires_at TIMESTAMPTZ
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, dayorder
AS $function$
    SELECT token.id, token.user_id, token.purpose, token.expires_at
    FROM dayorder.account_tokens AS token
    JOIN dayorder.users AS account ON account.id = token.user_id
    WHERE token.token_hash = p_token_hash
      AND token.consumed_at IS NULL
      AND token.expires_at > statement_timestamp()
      AND account.deleted_at IS NULL
    LIMIT 1
$function$;

CREATE FUNCTION dayorder.claim_outbox_events(
    p_limit INTEGER,
    p_lock_token UUID,
    p_stale_after INTERVAL DEFAULT INTERVAL '5 minutes'
)
RETURNS TABLE (
    id UUID,
    user_id UUID,
    event_type VARCHAR(120),
    aggregate_type VARCHAR(32),
    aggregate_id UUID,
    payload JSONB,
    attempts INTEGER,
    lock_token UUID
)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, dayorder
AS $function$
BEGIN
    IF p_limit < 1 OR p_limit > 100 THEN
        RAISE EXCEPTION 'outbox claim limit must be between 1 and 100' USING ERRCODE = '22023';
    END IF;
    IF p_lock_token IS NULL THEN
        RAISE EXCEPTION 'outbox lock token is required' USING ERRCODE = '22023';
    END IF;
    IF p_stale_after < INTERVAL '30 seconds' OR p_stale_after > INTERVAL '1 hour' THEN
        RAISE EXCEPTION 'outbox stale interval must be between 30 seconds and 1 hour' USING ERRCODE = '22023';
    END IF;

    RETURN QUERY
    WITH candidates AS (
        SELECT event.id
        FROM dayorder.outbox_events AS event
        WHERE (
                event.status = 'pending'
                AND event.available_at <= statement_timestamp()
              )
           OR (
                event.status = 'processing'
                AND event.locked_at < statement_timestamp() - p_stale_after
              )
        ORDER BY event.available_at, event.created_at, event.id
        FOR UPDATE SKIP LOCKED
        LIMIT p_limit
    )
    UPDATE dayorder.outbox_events AS event
    SET status = 'processing',
        attempts = event.attempts + 1,
        locked_at = statement_timestamp(),
        lock_token = p_lock_token,
        last_error = NULL
    FROM candidates
    WHERE event.id = candidates.id
    RETURNING
        event.id,
        event.user_id,
        event.event_type,
        event.aggregate_type,
        event.aggregate_id,
        event.payload,
        event.attempts,
        event.lock_token;
END
$function$;

CREATE FUNCTION dayorder.complete_outbox_event(p_id UUID, p_lock_token UUID)
RETURNS BOOLEAN
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, dayorder
AS $function$
BEGIN
    UPDATE dayorder.outbox_events AS event
    SET status = 'processed',
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

CREATE FUNCTION dayorder.retry_outbox_event(
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

ALTER TABLE dayorder.users ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON dayorder.users
    USING (id = dayorder.current_user_id())
    WITH CHECK (id = dayorder.current_user_id());

DO $tenant_rls$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'sessions',
        'account_tokens',
        'user_settings',
        'user_devices',
        'account_deletions',
        'goals',
        'goal_milestones',
        'tasks',
        'calendar_events',
        'calendar_event_reminders',
        'records',
        'notes',
        'daily_reviews',
        'tags',
        'record_tags',
        'note_tags',
        'entity_links',
        'agent_runs',
        'agent_steps',
        'agent_changes',
        'agent_source_refs',
        'audit_events',
        'audit_event_entities',
        'sync_changes',
        'client_mutations',
        'outbox_events'
    ]
    LOOP
        EXECUTE format('ALTER TABLE dayorder.%I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON dayorder.%I USING (user_id = dayorder.current_user_id()) WITH CHECK (user_id = dayorder.current_user_id())',
            table_name
        );
    END LOOP;
END
$tenant_rls$;

REVOKE ALL ON SCHEMA dayorder FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA dayorder FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA dayorder FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA dayorder FROM PUBLIC;

GRANT USAGE ON SCHEMA dayorder TO dayorder_api, dayorder_worker;

GRANT SELECT, INSERT, UPDATE, DELETE ON
    dayorder.users,
    dayorder.sessions,
    dayorder.account_tokens,
    dayorder.user_settings,
    dayorder.user_devices,
    dayorder.account_deletions,
    dayorder.goals,
    dayorder.goal_milestones,
    dayorder.tasks,
    dayorder.calendar_events,
    dayorder.calendar_event_reminders,
    dayorder.records,
    dayorder.notes,
    dayorder.daily_reviews,
    dayorder.tags,
    dayorder.record_tags,
    dayorder.note_tags,
    dayorder.entity_links,
    dayorder.agent_runs,
    dayorder.agent_steps,
    dayorder.agent_changes,
    dayorder.agent_source_refs,
    dayorder.client_mutations
TO dayorder_api;

GRANT SELECT, INSERT ON
    dayorder.audit_events,
    dayorder.audit_event_entities,
    dayorder.sync_changes
TO dayorder_api;

GRANT INSERT ON dayorder.outbox_events TO dayorder_api;
GRANT USAGE, SELECT ON SEQUENCE dayorder.sync_changes_sequence_seq TO dayorder_api;

GRANT SELECT ON
    dayorder.user_settings,
    dayorder.goals,
    dayorder.tasks,
    dayorder.calendar_events,
    dayorder.records,
    dayorder.notes,
    dayorder.agent_runs,
    dayorder.agent_steps,
    dayorder.agent_changes,
    dayorder.agent_source_refs
TO dayorder_worker;

GRANT INSERT, UPDATE ON
    dayorder.agent_runs,
    dayorder.agent_steps,
    dayorder.agent_changes,
    dayorder.agent_source_refs
TO dayorder_worker;

GRANT INSERT ON
    dayorder.audit_events,
    dayorder.audit_event_entities,
    dayorder.sync_changes
TO dayorder_worker;

GRANT USAGE, SELECT ON SEQUENCE dayorder.sync_changes_sequence_seq TO dayorder_worker;

GRANT EXECUTE ON FUNCTION dayorder.current_user_id() TO dayorder_api, dayorder_worker;
GRANT EXECUTE ON FUNCTION dayorder.set_user_context(UUID) TO dayorder_api, dayorder_worker;
GRANT EXECUTE ON FUNCTION dayorder.authenticate_session(BYTEA) TO dayorder_api;
GRANT EXECUTE ON FUNCTION dayorder.lookup_login_account(VARCHAR) TO dayorder_api;
GRANT EXECUTE ON FUNCTION dayorder.lookup_account_token(BYTEA) TO dayorder_api;
GRANT EXECUTE ON FUNCTION dayorder.claim_outbox_events(INTEGER, UUID, INTERVAL) TO dayorder_worker;
GRANT EXECUTE ON FUNCTION dayorder.complete_outbox_event(UUID, UUID) TO dayorder_worker;
GRANT EXECUTE ON FUNCTION dayorder.retry_outbox_event(UUID, UUID, TIMESTAMPTZ, TEXT, BOOLEAN) TO dayorder_worker;

ALTER DEFAULT PRIVILEGES IN SCHEMA dayorder REVOKE ALL ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA dayorder REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA dayorder REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;

COMMIT;
