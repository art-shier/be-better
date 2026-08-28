BEGIN;

CREATE FUNCTION dayorder.outbox_metrics()
RETURNS TABLE (
    backlog BIGINT,
    oldest_age_seconds DOUBLE PRECISION,
    dead_total BIGINT
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, dayorder
AS $function$
    SELECT
        count(*) FILTER (WHERE status IN ('pending', 'processing')) AS backlog,
        coalesce(
            extract(epoch FROM (statement_timestamp() - min(created_at) FILTER (WHERE status = 'pending'))),
            0
        )::DOUBLE PRECISION AS oldest_age_seconds,
        count(*) FILTER (WHERE status = 'dead') AS dead_total
    FROM dayorder.outbox_events;
$function$;

REVOKE ALL ON FUNCTION dayorder.outbox_metrics() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION dayorder.outbox_metrics() TO dayorder_worker;

DO $grant_monitor$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'dayorder_monitor') THEN
        GRANT USAGE ON SCHEMA dayorder TO dayorder_monitor;
        GRANT EXECUTE ON FUNCTION dayorder.outbox_metrics() TO dayorder_monitor;
    END IF;
END
$grant_monitor$;

COMMIT;
