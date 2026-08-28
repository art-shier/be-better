\set ON_ERROR_STOP on

SELECT format(
    'CREATE ROLE dayorder_migrator LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L',
    :'migrator_password'
)
WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'dayorder_migrator')
\gexec

SELECT format(
    'CREATE ROLE dayorder_api LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L',
    :'api_password'
)
WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'dayorder_api')
\gexec

SELECT format(
    'CREATE ROLE dayorder_worker LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L',
    :'worker_password'
)
WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'dayorder_worker')
\gexec

ALTER ROLE dayorder_migrator PASSWORD :'migrator_password';
ALTER ROLE dayorder_api PASSWORD :'api_password';
ALTER ROLE dayorder_worker PASSWORD :'worker_password';

ALTER ROLE dayorder_migrator SET timezone = 'UTC';
ALTER ROLE dayorder_api SET timezone = 'UTC';
ALTER ROLE dayorder_worker SET timezone = 'UTC';

GRANT CONNECT ON DATABASE :"database_name" TO dayorder_migrator, dayorder_api, dayorder_worker;
GRANT CREATE ON DATABASE :"database_name" TO dayorder_migrator;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

SELECT 'CREATE SCHEMA dayorder AUTHORIZATION dayorder_migrator'
WHERE NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = 'dayorder'
)
\gexec

ALTER SCHEMA dayorder OWNER TO dayorder_migrator;
REVOKE ALL ON SCHEMA dayorder FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA dayorder TO dayorder_migrator;
