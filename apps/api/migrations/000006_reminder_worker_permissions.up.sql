BEGIN;

DO $roles$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'dayorder_worker') THEN
        RAISE EXCEPTION 'database role dayorder_worker is missing; run deploy/scripts/bootstrap-roles.sql first';
    END IF;
END
$roles$;

ALTER TABLE dayorder.audit_event_entities
    DROP CONSTRAINT audit_event_entities_type_check;
ALTER TABLE dayorder.audit_event_entities
    ADD CONSTRAINT audit_event_entities_type_check CHECK (
        entity_type IN (
            'goal', 'milestone', 'task', 'calendar_event', 'reminder',
            'record', 'note', 'daily_review', 'agent_run'
        )
    );

GRANT SELECT ON
    dayorder.users,
    dayorder.calendar_events,
    dayorder.calendar_event_reminders
TO dayorder_worker;

GRANT UPDATE (status, delivered_at, attempts, version, updated_at)
ON dayorder.calendar_event_reminders
TO dayorder_worker;

GRANT SELECT, INSERT ON
    dayorder.audit_events,
    dayorder.audit_event_entities,
    dayorder.sync_changes
TO dayorder_worker;

GRANT USAGE, SELECT ON SEQUENCE dayorder.sync_changes_sequence_seq TO dayorder_worker;

COMMIT;
