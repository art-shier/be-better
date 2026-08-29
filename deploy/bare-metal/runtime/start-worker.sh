#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=runtime-env.sh
source "$script_dir/runtime-env.sh"

[[ $# -eq 1 ]] || dayorder_die "usage: start-worker.sh <worker.env>"
dayorder_load_environment "$1"
dayorder_load_runtime_secrets
dayorder_require_value WORKER_DATABASE_URL
dayorder_require_value DAYORDER_AUTH_HMAC_KEY

binary="$script_dir/../bin/dayorder-worker"
dayorder_require_executable "$binary"
exec "$binary"
