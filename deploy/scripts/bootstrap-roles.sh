#!/bin/sh
set -eu

: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${DAYORDER_MIGRATOR_DB_PASSWORD:?DAYORDER_MIGRATOR_DB_PASSWORD is required}"
: "${DAYORDER_API_DB_PASSWORD:?DAYORDER_API_DB_PASSWORD is required}"
: "${DAYORDER_WORKER_DB_PASSWORD:?DAYORDER_WORKER_DB_PASSWORD is required}"

psql \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --variable ON_ERROR_STOP=1 \
  --variable database_name="$POSTGRES_DB" \
  --variable migrator_password="$DAYORDER_MIGRATOR_DB_PASSWORD" \
  --variable api_password="$DAYORDER_API_DB_PASSWORD" \
  --variable worker_password="$DAYORDER_WORKER_DB_PASSWORD" \
  --file /opt/dayorder/bootstrap-roles.sql
