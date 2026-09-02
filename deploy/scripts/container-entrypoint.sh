#!/bin/sh
set -eu

load_secret() {
  variable="$1"
  file_variable="${variable}_FILE"
  file_path="$(printenv "$file_variable" 2>/dev/null || true)"
  current_value="$(printenv "$variable" 2>/dev/null || true)"
  if [ -n "$current_value" ] && [ -n "$file_path" ]; then
    echo "$variable and $file_variable cannot both be set" >&2
    exit 1
  fi
  if [ -n "$file_path" ]; then
    if [ ! -r "$file_path" ]; then
      echo "$file_variable does not reference a readable file" >&2
      exit 1
    fi
    value="$(tr -d '\r\n' < "$file_path")"
    if [ -z "$value" ]; then
      echo "$file_variable references an empty secret" >&2
      exit 1
    fi
    export "$variable=$value"
  fi
}

for variable in \
  DATABASE_URL WORKER_DATABASE_URL MIGRATION_DATABASE_URL \
  DAYORDER_AUTH_HMAC_KEY DAYORDER_SMTP_PASSWORD
do
  load_secret "$variable"
done

exec "$@"
