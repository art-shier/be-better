#!/usr/bin/env bash
set -Eeuo pipefail

dayorder_die() {
  printf 'dayorder: %s\n' "$*" >&2
  exit 1
}

dayorder_normalize_mode() {
  local mode="$1"
  while [[ "$mode" == 0* && "$mode" != 0 ]]; do mode="${mode#0}"; done
  printf '%s' "$mode"
}

dayorder_validate_protected_file() {
  local path="$1" label="$2" owner group mode current_uid current_groups
  [[ ! -L "$path" ]] || dayorder_die "$label must not be a symbolic link: $path"
  [[ -f "$path" && -r "$path" ]] || dayorder_die "$label is not a readable regular file: $path"

  owner="$(stat -c %u -- "$path")" || dayorder_die "cannot read $label owner: $path"
  group="$(stat -c %g -- "$path")" || dayorder_die "cannot read $label group: $path"
  mode="$(stat -c %a -- "$path")" || dayorder_die "cannot read $label mode: $path"
  mode="$(dayorder_normalize_mode "$mode")"
  current_uid="$(id -u)" || dayorder_die "cannot determine the runtime user"

  if [[ "$owner" == "$current_uid" ]]; then
    [[ "$mode" == 400 || "$mode" == 600 ]] || \
      dayorder_die "$label has unsafe mode $mode; current-user files must use mode 0400 or 0600: $path"
    return
  fi
  [[ "$owner" == 0 ]] || dayorder_die "$label has an unapproved owner: $path"

  current_groups="$(id -G)" || dayorder_die "cannot determine runtime groups"
  [[ " $current_groups " == *" $group "* ]] || \
    dayorder_die "$label is root-owned but its group is not assigned to the runtime user: $path"
  [[ "$mode" == 440 || "$mode" == 640 ]] || \
    dayorder_die "$label has unsafe mode $mode; root-owned group-readable files must use mode 0440 or 0640: $path"
}

dayorder_load_environment() {
  local environment_file="${1:-}"
  local pinned_confighub_executable="${DAYORDER_CONFIGHUB_EXECUTABLE-}"
  [[ -n "$environment_file" ]] || dayorder_die "environment file is not readable: <missing>"
  dayorder_validate_protected_file "$environment_file" "environment file"
  set -a
  # The environment file is an operator-controlled Bash-compatible key/value file.
  # shellcheck disable=SC1090
  source "$environment_file"
  set +a
  if [[ -n "$pinned_confighub_executable" ]]; then
    DAYORDER_CONFIGHUB_EXECUTABLE="$pinned_confighub_executable"
    export DAYORDER_CONFIGHUB_EXECUTABLE
  fi
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
  dayorder_validate_protected_file "$file_path" "secret file referenced by $file_variable"
  local line_count
  line_count="$(awk 'END { print NR }' "$file_path")" || dayorder_die "cannot read secret file referenced by $file_variable"
  [[ "$line_count" == 1 ]] || dayorder_die "$file_variable secret file must contain exactly one non-empty line"
  value=""
  IFS= read -r value < "$file_path" || [[ -n "$value" ]] || \
    dayorder_die "$file_variable secret file must contain exactly one non-empty line"
  value="${value%$'\r'}"
  [[ -n "$value" && "$value" != *$'\r'* ]] || \
    dayorder_die "$file_variable secret file must contain exactly one non-empty line"
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

dayorder_clear_database_overrides() {
  unset DATABASE_URL DATABASE_URL_FILE
  unset WORKER_DATABASE_URL WORKER_DATABASE_URL_FILE
  unset MIGRATION_DATABASE_URL MIGRATION_DATABASE_URL_FILE
}

dayorder_run_with_confighub() {
  local environment_file="$1"
  shift
  local configuration_directory confighub_executable
  configuration_directory="$(cd -- "$(dirname -- "$environment_file")" && pwd -P)" || \
    dayorder_die "cannot enter the configuration directory"
  confighub_executable="${DAYORDER_CONFIGHUB_EXECUTABLE:-confighub}"
  cd -- "$configuration_directory" || dayorder_die "cannot enter the configuration directory"
  exec "$confighub_executable" run --project shier --env prod -- "$@"
}

dayorder_require_value() {
  local variable="$1"
  [[ -n "${!variable-}" ]] || dayorder_die "$variable is required"
}

dayorder_require_executable() {
  local executable="$1"
  [[ -f "$executable" && -x "$executable" ]] || dayorder_die "executable is missing or not executable: $executable"
}
