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

validate_complete_asset_set() {
  local directory="$1" expected actual name
  expected=$'SHA256SUMS\ndayorder-deploy.sh\ndayorder-server-linux-amd64.tar.gz\ndayorder-server-linux-arm64.tar.gz\ndayorder-web.tar.gz\ndayorder-worker-linux-amd64.tar.gz\ndayorder-worker-linux-arm64.tar.gz\nrelease-manifest.json'
  actual="$(find "$directory" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort)"
  [[ "$actual" == "$expected" ]] || die "aggregate build did not produce the exact eight-asset contract"
  while IFS= read -r name; do
    [[ -f "$directory/$name" && ! -L "$directory/$name" ]] || \
      die "aggregate build produced a non-regular asset: $name"
  done <<< "$expected"
}

replace_output_directory() {
  local staged="$1" parent backup=""
  parent="$(dirname -- "$output")"
  if [[ -e "$output" || -L "$output" ]]; then
    [[ -d "$output" && ! -L "$output" ]] || die "release output must be a real directory: $output"
    backup="$(mktemp -d "$parent/.dayorder-release-backup.XXXXXX")"
    rmdir -- "$backup"
    mv -- "$output" "$backup" || die "could not preserve the previous release output"
  fi
  if ! mv -- "$staged" "$output"; then
    if [[ -n "$backup" ]]; then
      mv -- "$backup" "$output" || die "could not install the new release output or restore the previous output"
    fi
    die "could not install the complete release output"
  fi
  if [[ -n "$backup" ]]; then
    rm -rf -- "$backup" || printf 'build-release: warning: could not remove previous release backup: %s\n' "$backup" >&2
  fi
}

build_all() {
  local version revision final_output parent staged
  version="v$(cd -- "$root_dir" && node -p "require('./package.json').version")"
  revision="$(git -C "$root_dir" rev-parse HEAD)"
  final_output="$output"
  [[ -n "$final_output" && "$final_output" != / ]] || die "release output must not be empty or the filesystem root"
  parent="$(dirname -- "$final_output")"
  mkdir -p -- "$parent"
  staged="$(mktemp -d "$parent/.dayorder-release-staging.XXXXXX")"
  temporary_directories+=("$staged")
  output="$staged"
  build_web
  build_backend amd64
  build_backend arm64
  finalize "$version" "$revision"
  validate_complete_asset_set "$staged"
  output="$final_output"
  replace_output_directory "$staged"
}

command="${1:-}"
case "$command" in
  web) [[ $# -eq 1 ]] || die "usage: build-release.sh web"; build_web ;;
  backend) [[ $# -eq 2 ]] || die "usage: build-release.sh backend <amd64|arm64>"; build_backend "$2" ;;
  finalize) [[ $# -eq 3 ]] || die "usage: build-release.sh finalize <version> <revision>"; finalize "$2" "$3" ;;
  all)
    [[ $# -eq 1 ]] || die "usage: build-release.sh all"
    build_all
    ;;
  *) die "usage: build-release.sh <web|backend|finalize|all>" ;;
esac
