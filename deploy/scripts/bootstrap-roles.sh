#!/bin/sh
set -eu

: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"

read_secret() {
  name="$1"
  value="$2"
  file_path="$3"
  if [ -n "$value" ] && [ -n "$file_path" ]; then
    echo "$name and ${name}_FILE cannot both be set" >&2
    exit 1
  fi
  if [ -n "$file_path" ]; then
    if [ ! -r "$file_path" ]; then
      echo "${name}_FILE does not reference a readable file" >&2
      exit 1
    fi
    tr -d '\r\n' < "$file_path"
    return
  fi
  if [ -z "$value" ]; then
    echo "$name is required" >&2
    exit 1
  fi
  printf '%s' "$value"
}

export DAYORDER_MIGRATOR_DB_PASSWORD="$(read_secret DAYORDER_MIGRATOR_DB_PASSWORD "${DAYORDER_MIGRATOR_DB_PASSWORD:-}" "${DAYORDER_MIGRATOR_DB_PASSWORD_FILE:-}")"
export DAYORDER_API_DB_PASSWORD="$(read_secret DAYORDER_API_DB_PASSWORD "${DAYORDER_API_DB_PASSWORD:-}" "${DAYORDER_API_DB_PASSWORD_FILE:-}")"
export DAYORDER_WORKER_DB_PASSWORD="$(read_secret DAYORDER_WORKER_DB_PASSWORD "${DAYORDER_WORKER_DB_PASSWORD:-}" "${DAYORDER_WORKER_DB_PASSWORD_FILE:-}")"
export DAYORDER_BACKUP_DB_PASSWORD="$(read_secret DAYORDER_BACKUP_DB_PASSWORD "${DAYORDER_BACKUP_DB_PASSWORD:-}" "${DAYORDER_BACKUP_DB_PASSWORD_FILE:-}")"
export DAYORDER_MONITOR_DB_PASSWORD="$(read_secret DAYORDER_MONITOR_DB_PASSWORD "${DAYORDER_MONITOR_DB_PASSWORD:-}" "${DAYORDER_MONITOR_DB_PASSWORD_FILE:-}")"

psql \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --file /opt/dayorder/bootstrap-roles.sql
