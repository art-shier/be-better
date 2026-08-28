#!/bin/sh
set -eu

secret_file="${PGBACKREST_REPO1_CIPHER_PASS_FILE:-/run/secrets/pgbackrest_cipher_pass}"
if [ ! -r "$secret_file" ]; then
  echo "PGBACKREST_REPO1_CIPHER_PASS_FILE does not reference a readable file" >&2
  exit 1
fi
PGBACKREST_REPO1_CIPHER_PASS="$(tr -d '\r\n' < "$secret_file")"
if [ -z "$PGBACKREST_REPO1_CIPHER_PASS" ]; then
  echo "pgBackRest repository cipher secret is empty" >&2
  exit 1
fi
export PGBACKREST_REPO1_CIPHER_PASS
exec pgbackrest "$@"
