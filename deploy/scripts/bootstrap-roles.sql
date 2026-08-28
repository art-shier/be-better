\set ON_ERROR_STOP on
\getenv database_name POSTGRES_DB
\getenv migrator_password DAYORDER_MIGRATOR_DB_PASSWORD
\getenv api_password DAYORDER_API_DB_PASSWORD
\getenv worker_password DAYORDER_WORKER_DB_PASSWORD
\getenv backup_password DAYORDER_BACKUP_DB_PASSWORD
\getenv monitor_password DAYORDER_MONITOR_DB_PASSWORD

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

SELECT format(
    'CREATE ROLE dayorder_backup LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE REPLICATION NOBYPASSRLS PASSWORD %L',
    :'backup_password'
)
WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'dayorder_backup')
\gexec

SELECT format(
    'CREATE ROLE dayorder_monitor LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L',
    :'monitor_password'
)
WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'dayorder_monitor')
\gexec

ALTER ROLE dayorder_migrator PASSWORD :'migrator_password';
ALTER ROLE dayorder_api PASSWORD :'api_password';
ALTER ROLE dayorder_worker PASSWORD :'worker_password';
ALTER ROLE dayorder_backup PASSWORD :'backup_password';
ALTER ROLE dayorder_monitor PASSWORD :'monitor_password';

ALTER ROLE dayorder_migrator WITH LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 3;
ALTER ROLE dayorder_api WITH LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 25;
ALTER ROLE dayorder_worker WITH LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 10;
ALTER ROLE dayorder_backup WITH LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE REPLICATION NOBYPASSRLS CONNECTION LIMIT 3;
ALTER ROLE dayorder_monitor WITH LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 5;

ALTER ROLE dayorder_migrator SET timezone = 'UTC';
ALTER ROLE dayorder_api SET timezone = 'UTC';
ALTER ROLE dayorder_worker SET timezone = 'UTC';
ALTER ROLE dayorder_backup SET timezone = 'UTC';
ALTER ROLE dayorder_monitor SET timezone = 'UTC';
ALTER ROLE dayorder_migrator SET statement_timeout = '10min';
ALTER ROLE dayorder_api SET statement_timeout = '5s';
ALTER ROLE dayorder_worker SET statement_timeout = '30s';
ALTER ROLE dayorder_monitor SET statement_timeout = '5s';
ALTER ROLE dayorder_api SET idle_in_transaction_session_timeout = '10s';
ALTER ROLE dayorder_worker SET idle_in_transaction_session_timeout = '10s';

REVOKE CONNECT, TEMPORARY ON DATABASE :"database_name" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"database_name" TO dayorder_migrator, dayorder_api, dayorder_worker, dayorder_backup, dayorder_monitor;
GRANT CREATE ON DATABASE :"database_name" TO dayorder_migrator;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT pg_monitor TO dayorder_monitor;

SELECT 'CREATE SCHEMA dayorder AUTHORIZATION dayorder_migrator'
WHERE NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = 'dayorder'
)
\gexec

ALTER SCHEMA dayorder OWNER TO dayorder_migrator;
REVOKE ALL ON SCHEMA dayorder FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA dayorder TO dayorder_migrator;
