#!/usr/bin/env bash
set -Eeuo pipefail

die() { printf 'build-web: %s\n' "$*" >&2; exit 1; }
command -v node >/dev/null 2>&1 || die "node is required"
command -v npm >/dev/null 2>&1 || die "npm is required"

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
root_dir="$(cd -- "$script_dir/../.." && pwd -P)"
output="${1:-$root_dir/release/web}"
[[ -n "$output" ]] || die "output directory must not be empty"
output="$(realpath -m -- "$output")"
[[ "$output" != "/" && "$output" != "$root_dir" && "$output" != "${HOME:-/__unset_home__}" ]] || \
  die "refusing unsafe output directory: $output"
[[ ! -L "$output" ]] || die "output directory must not be a symbolic link: $output"
[[ ! -e "$output" || -d "$output" ]] || die "output path is not a directory: $output"

parent="$(dirname -- "$output")"
mkdir -p -- "$parent"
staging="$(mktemp -d "$parent/.dayorder-web.XXXXXX")"
previous=""
cleanup() {
  [[ ! -d "$staging" ]] || rm -rf -- "$staging"
  if [[ -n "$previous" && -d "$previous" && ! -e "$output" ]]; then mv -- "$previous" "$output"; fi
}
trap cleanup EXIT

cd -- "$root_dir"
npm ci --include=dev --workspaces
npm run build:web
[[ -f "$root_dir/apps/web/dist/index.html" ]] || die "Web build did not produce index.html"
cp -a -- "$root_dir/apps/web/dist/." "$staging/"

if [[ -e "$output" ]]; then
  previous="${output}.previous.$$"
  [[ ! -e "$previous" ]] || die "temporary previous output already exists: $previous"
  mv -- "$output" "$previous"
fi
mv -- "$staging" "$output"
[[ -z "$previous" ]] || rm -rf -- "$previous"
previous=""
printf 'Web release written to %s\n' "$output"
