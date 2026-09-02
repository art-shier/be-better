#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=runtime-env.sh
source "$script_dir/runtime-env.sh"

[[ $# -eq 2 ]] || dayorder_die "usage: migrate.sh <up|check> <migrate.env>"
action="$1"
environment_file="$2"
case "$action" in
  up) migration_arguments=() ;;
  check) migration_arguments=(-check) ;;
  *) dayorder_die "usage: migrate.sh <up|check> <migrate.env>" ;;
esac

dayorder_load_environment "$environment_file"
[[ "${DAYORDER_ENV-}" == production ]] || dayorder_die "DAYORDER_ENV must be production for bare-metal migrations"
dayorder_clear_database_overrides

binary="$script_dir/../bin/dayorder-migrate"
dayorder_require_executable "$binary"
dayorder_run_with_confighub "$environment_file" "$binary" "${migration_arguments[@]}"
