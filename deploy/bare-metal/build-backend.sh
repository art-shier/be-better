#!/usr/bin/env bash
set -Eeuo pipefail

die() { printf 'build-backend: %s\n' "$*" >&2; exit 1; }
command -v go >/dev/null 2>&1 || die "go is required"

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
root_dir="$(cd -- "$script_dir/../.." && pwd -P)"
output="${1:-$root_dir/release/backend}"
[[ -n "$output" ]] || die "output directory must not be empty"
output="$(realpath -m -- "$output")"
[[ "$output" != "/" && "$output" != "$root_dir" && "$output" != "${HOME:-/__unset_home__}" ]] || \
  die "refusing unsafe output directory: $output"
[[ ! -L "$output" ]] || die "output directory must not be a symbolic link: $output"
[[ ! -e "$output" || -d "$output" ]] || die "output path is not a directory: $output"

target_arch="${GOARCH:-$(go env GOARCH)}"
case "$target_arch" in
  amd64|arm64) ;;
  *) die "GOARCH must be amd64 or arm64" ;;
esac

parent="$(dirname -- "$output")"
mkdir -p -- "$parent"
staging="$(mktemp -d "$parent/.dayorder-backend.XXXXXX")"
previous=""
cleanup() {
  [[ ! -d "$staging" ]] || rm -rf -- "$staging"
  if [[ -n "$previous" && -d "$previous" && ! -e "$output" ]]; then mv -- "$previous" "$output"; fi
}
trap cleanup EXIT
mkdir -p -- "$staging/bin" "$staging/scripts" "$staging/config"

cd -- "$root_dir"
CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" go -C apps/api build \
  -buildvcs=false -trimpath -ldflags="-s -w" -o "$staging/bin/dayorder-api" ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" go -C apps/api build \
  -buildvcs=false -trimpath -ldflags="-s -w" -o "$staging/bin/dayorder-worker" ./cmd/worker
CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" go -C apps/api build \
  -buildvcs=false -trimpath -ldflags="-s -w" -o "$staging/bin/dayorder-migrate" ./cmd/migrate
cp -- "$script_dir/runtime/"*.sh "$staging/scripts/"
cp -- "$script_dir/config/"*.env.example "$staging/config/"
chmod 0755 "$staging/bin/"* "$staging/scripts/"*.sh
chmod 0644 "$staging/config/"*.env.example

if [[ -e "$output" ]]; then
  previous="${output}.previous.$$"
  [[ ! -e "$previous" ]] || die "temporary previous output already exists: $previous"
  mv -- "$output" "$previous"
fi
mv -- "$staging" "$output"
[[ -z "$previous" ]] || rm -rf -- "$previous"
previous=""
printf 'Backend release for linux/%s written to %s\n' "$target_arch" "$output"
