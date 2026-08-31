#!/usr/bin/env bash
set -Eeuo pipefail

die() { printf 'build-release: %s\n' "$*" >&2; exit 1; }
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
root_dir="$(cd -- "$script_dir/../.." && pwd -P)"
packager="$script_dir/package-assets.sh"
output="${DAYORDER_RELEASE_OUTPUT:-$root_dir/release/github}"
temporary_directories=()
cleanup() {
  local directory
  for directory in "${temporary_directories[@]}"; do
    [[ ! -d "$directory" ]] || rm -rf -- "$directory"
  done
}
trap cleanup EXIT
new_temporary_directory() {
  local destination_variable="$1" directory
  directory="$(mktemp -d)"
  temporary_directories+=("$directory")
  printf -v "$destination_variable" '%s' "$directory"
}

build_web() {
  local staging
  new_temporary_directory staging
  "$root_dir/deploy/bare-metal/build-web.sh" "$staging/web"
  "$packager" web "$staging/web" "$output"
}

build_backend() {
  local arch="$1" staging
  case "$arch" in amd64|arm64) ;; *) die "architecture must be amd64 or arm64" ;; esac
  new_temporary_directory staging
  GOARCH="$arch" "$root_dir/deploy/bare-metal/build-backend.sh" "$staging/backend"
  "$packager" backend "$arch" "$staging/backend" "$output"
}

finalize() {
  local version="$1" revision="$2"
  "$packager" metadata "$version" "$revision" "$script_dir/dayorder-deploy.sh" "$output"
}

command="${1:-}"
case "$command" in
  web) [[ $# -eq 1 ]] || die "usage: build-release.sh web"; build_web ;;
  backend) [[ $# -eq 2 ]] || die "usage: build-release.sh backend <amd64|arm64>"; build_backend "$2" ;;
  finalize) [[ $# -eq 3 ]] || die "usage: build-release.sh finalize <version> <revision>"; finalize "$2" "$3" ;;
  all)
    [[ $# -eq 1 ]] || die "usage: build-release.sh all"
    version="v$(node -p "require('$root_dir/package.json').version")"
    revision="$(git -C "$root_dir" rev-parse HEAD)"
    build_web
    build_backend amd64
    build_backend arm64
    finalize "$version" "$revision"
    ;;
  *) die "usage: build-release.sh <web|backend|finalize|all>" ;;
esac
