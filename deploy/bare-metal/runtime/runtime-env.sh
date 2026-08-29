#!/usr/bin/env bash
set -Eeuo pipefail

dayorder_die() {
  printf 'dayorder: %s\n' "$*" >&2
  exit 1
}

dayorder_load_environment() {
  local environment_file="${1:-}"
  [[ -n "$environment_file" && -f "$environment_file" && -r "$environment_file" ]] || \
    dayorder_die "environment file is not readable: ${environment_file:-<missing>}"
  set -a
  # The environment file is an operator-controlled Bash-compatible key/value file.
  # shellcheck disable=SC1090
  source "$environment_file"
  set +a
}

dayorder_load_secret() {
  local variable="$1"
  local file_variable="${variable}_FILE"
  local current_value="${!variable-}"
  local file_path="${!file_variable-}"
  local value

  if [[ -n "$current_value" && -n "$file_path" ]]; then
    dayorder_die "$variable and $file_variable cannot both be set"
  fi
  if [[ -z "$file_path" ]]; then
    return
  fi
  [[ -f "$file_path" && -r "$file_path" ]] || dayorder_die "$file_variable does not reference a readable file"
  value="$(tr -d '\r\n' < "$file_path")"
  [[ -n "$value" ]] || dayorder_die "$file_variable references an empty secret"
  printf -v "$variable" '%s' "$value"
  export "$variable"
}

dayorder_load_runtime_secrets() {
  local variable
  for variable in \
    DATABASE_URL WORKER_DATABASE_URL MIGRATION_DATABASE_URL \
    DAYORDER_AUTH_HMAC_KEY DAYORDER_SMTP_PASSWORD DAYORDER_AGENT_HTTP_KEY
  do
    dayorder_load_secret "$variable"
  done
}

dayorder_require_value() {
  local variable="$1"
  [[ -n "${!variable-}" ]] || dayorder_die "$variable is required"
}

dayorder_require_executable() {
  local executable="$1"
  [[ -f "$executable" && -x "$executable" ]] || dayorder_die "executable is missing or not executable: $executable"
}
