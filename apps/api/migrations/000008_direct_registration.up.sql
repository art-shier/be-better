BEGIN;

ALTER TABLE dayorder.users
    DROP CONSTRAINT users_active_verified_check;

CREATE OR REPLACE FUNCTION dayorder.reconcile_direct_registration()
RETURNS VOID
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, dayorder
AS $dayorder_reconcile$
BEGIN
    UPDATE dayorder.users
    SET status = 'active',
        updated_at = statement_timestamp()
    WHERE status = 'pending_verification'
      AND deleted_at IS NULL;

    UPDATE dayorder.account_tokens
    SET consumed_at = statement_timestamp()
    WHERE purpose IN ('verify_email', 'reset_password')
      AND consumed_at IS NULL;

    UPDATE dayorder.outbox_events
    SET status = 'processed',
        payload = '{}'::jsonb,
        locked_at = NULL,
        lock_token = NULL,
        last_error = NULL,
        processed_at = statement_timestamp()
    WHERE event_type IN (
            'email.verification.requested',
            'email.password_reset.requested',
            'agent.run.requested'
        )
      AND status IN ('pending', 'processing', 'dead');
END
$dayorder_reconcile$;

REVOKE ALL ON FUNCTION dayorder.reconcile_direct_registration() FROM PUBLIC;

SELECT dayorder.reconcile_direct_registration();

COMMIT;
