#!/usr/bin/env bash
set -Eeuo pipefail

die() { printf 'package-assets: %s\n' "$*" >&2; exit 1; }
[[ $# -ge 1 ]] || die "usage: package-assets.sh <web|backend|metadata> ..."
for command_name in tar gzip install mktemp sha256sum; do
  command -v "$command_name" >/dev/null 2>&1 || die "$command_name is required"
done

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

require_file() { [[ -f "$1" ]] || die "required file is missing: $1"; }
make_archive() {
  local source="$1" output="$2"
  mkdir -p -- "$(dirname -- "$output")"
  local temporary="${output}.tmp.$$"
  tar --sort=name --mtime="@${SOURCE_DATE_EPOCH:-0}" --owner=0 --group=0 --numeric-owner \
    -C "$source" -cf - . | gzip -n > "$temporary"
  mv -f -- "$temporary" "$output"
}

make_contract_archive() {
  local source="$1" output="$2" staging archive operation path mode
  shift 2
  [[ $(( $# % 2 )) -eq 0 ]] || die "internal archive contract must contain path/mode pairs"
  mkdir -p -- "$(dirname -- "$output")"
  new_temporary_directory staging
  archive="$staging/archive.tar"
  operation=-cf
  while [[ $# -gt 0 ]]; do
    path="$1" mode="$2"
    shift 2
    tar --mtime="@${SOURCE_DATE_EPOCH:-0}" --owner=0 --group=0 --numeric-owner \
      --no-recursion --mode="$mode" -C "$source" "$operation" "$archive" "$path"
    operation=-rf
  done
  gzip -n < "$archive" > "${output}.tmp.$$"
  mv -f -- "${output}.tmp.$$" "$output"
}

package_web() {
  local source="$1" output_dir="$2" staging
  require_file "$source/index.html"
  [[ -d "$source/assets" ]] || die "Web assets directory is missing: $source/assets"
  new_temporary_directory staging
  cp -a -- "$source/index.html" "$source/assets" "$staging/"
  make_archive "$staging" "$output_dir/dayorder-web.tar.gz"
}

package_backend() {
  local arch="$1" source="$2" output_dir="$3" server worker path
  case "$arch" in amd64|arm64) ;; *) die "architecture must be amd64 or arm64" ;; esac
  new_temporary_directory server
  new_temporary_directory worker
  for path in bin/dayorder-api bin/dayorder-migrate scripts/runtime-env.sh scripts/start-api.sh \
    scripts/migrate.sh config/api.env.example config/migrate.env.example; do
    require_file "$source/$path"
    install -D -m "$( [[ "$path" == bin/* || "$path" == scripts/* ]] && printf 0755 || printf 0644 )" \
      "$source/$path" "$server/$path"
  done
  for path in bin/dayorder-worker scripts/runtime-env.sh scripts/start-worker.sh config/worker.env.example; do
    require_file "$source/$path"
    install -D -m "$( [[ "$path" == bin/* || "$path" == scripts/* ]] && printf 0755 || printf 0644 )" \
      "$source/$path" "$worker/$path"
  done
  make_contract_archive "$server" "$output_dir/dayorder-server-linux-$arch.tar.gz" \
    . 0755 \
    ./bin 0755 \
    ./bin/dayorder-api 0755 \
    ./bin/dayorder-migrate 0755 \
    ./config 0755 \
    ./config/api.env.example 0644 \
    ./config/migrate.env.example 0644 \
    ./scripts 0755 \
    ./scripts/migrate.sh 0755 \
    ./scripts/runtime-env.sh 0755 \
    ./scripts/start-api.sh 0755
  make_contract_archive "$worker" "$output_dir/dayorder-worker-linux-$arch.tar.gz" \
    . 0755 \
    ./bin 0755 \
    ./bin/dayorder-worker 0755 \
    ./config 0755 \
    ./config/worker.env.example 0644 \
    ./scripts 0755 \
    ./scripts/runtime-env.sh 0755 \
    ./scripts/start-worker.sh 0755
}

write_metadata() {
  local version="$1" revision="$2" deploy_script="$3" output_dir="$4" name
  [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "version must match vX.Y.Z"
  [[ "$revision" =~ ^[0-9a-f]{40}$ ]] || die "revision must be a 40-character lowercase Git SHA"
  [[ -x "$deploy_script" ]] || die "deployment script is missing or not executable: $deploy_script"
  install -m 0755 "$deploy_script" "$output_dir/dayorder-deploy.sh"
  for name in dayorder-web.tar.gz \
    dayorder-server-linux-amd64.tar.gz dayorder-server-linux-arm64.tar.gz \
    dayorder-worker-linux-amd64.tar.gz dayorder-worker-linux-arm64.tar.gz; do
    require_file "$output_dir/$name"
  done
  printf '%s\n' \
    '{' \
    '  "schemaVersion": 1,' \
    "  \"version\": \"$version\"," \
    "  \"revision\": \"$revision\"," \
    '  "deployScriptVersion": 1,' \
    '  "assets": {' \
    '    "web": "dayorder-web.tar.gz",' \
    '    "server": {' \
    '      "amd64": "dayorder-server-linux-amd64.tar.gz",' \
    '      "arm64": "dayorder-server-linux-arm64.tar.gz"' \
    '    },' \
    '    "worker": {' \
    '      "amd64": "dayorder-worker-linux-amd64.tar.gz",' \
    '      "arm64": "dayorder-worker-linux-arm64.tar.gz"' \
    '    }' \
    '  }' \
    '}' > "$output_dir/release-manifest.json"
  (
    cd -- "$output_dir"
    sha256sum dayorder-web.tar.gz \
      dayorder-server-linux-amd64.tar.gz dayorder-server-linux-arm64.tar.gz \
      dayorder-worker-linux-amd64.tar.gz dayorder-worker-linux-arm64.tar.gz \
      dayorder-deploy.sh release-manifest.json > SHA256SUMS
  )
}

case "$1" in
  web) [[ $# -eq 3 ]] || die "usage: package-assets.sh web <web-dir> <output-dir>"; package_web "$2" "$3" ;;
  backend) [[ $# -eq 4 ]] || die "usage: package-assets.sh backend <amd64|arm64> <backend-dir> <output-dir>"; package_backend "$2" "$3" "$4" ;;
  metadata) [[ $# -eq 5 ]] || die "usage: package-assets.sh metadata <version> <revision> <deploy-script> <output-dir>"; write_metadata "$2" "$3" "$4" "$5" ;;
  *) die "usage: package-assets.sh <web|backend|metadata> ..." ;;
esac
