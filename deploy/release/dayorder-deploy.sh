#!/usr/bin/env bash
set -Eeuo pipefail

readonly DEPLOY_SCRIPT_VERSION=1
readonly REPOSITORY="art-shier/be-better"
readonly LATEST_BASE="https://github.com/$REPOSITORY/releases/latest/download"
readonly VERSION_BASE="https://github.com/$REPOSITORY/releases/download"

die() { printf 'dayorder-deploy: %s\n' "$*" >&2; exit 1; }
usage() { printf 'usage: dayorder-deploy.sh <web|server|worker|all> [--version vX.Y.Z] [--root PATH]\n' >&2; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

[[ $# -ge 1 ]] || { usage; exit 1; }
component="$1"; shift
case "$component" in web|server|worker|all) ;; *) usage; exit 1 ;; esac
requested_version=""
root_input="$PWD"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) [[ $# -ge 2 ]] || die "--version requires a value"; requested_version="$2"; shift 2 ;;
    --root) [[ $# -ge 2 ]] || die "--root requires a value"; root_input="$2"; shift 2 ;;
    *) die "unknown argument: $1" ;;
  esac
done
[[ -z "$requested_version" || "$requested_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "version must match vX.Y.Z"
[[ -n "$root_input" ]] || die "deployment root must not be empty"
[[ ! -L "$root_input" ]] || die "deployment root must not be a symbolic link"
root="$(realpath -m -- "$root_input")"
home="$(realpath -m -- "${HOME:?HOME is required}")"
[[ "$root" != / ]] || die "filesystem root cannot be the deployment root"
[[ "$root" != "$home" ]] || die "home directory cannot be the deployment root"
mkdir -p -- "$root"
[[ ! -L "$root" ]] || die "deployment root must not be a symbolic link"

for command_name in bash curl tar sha256sum flock realpath awk sed grep find mktemp; do require_command "$command_name"; done
if [[ "$component" != web ]]; then
  machine="${DAYORDER_TEST_UNAME:-$(uname -m)}"
  case "$machine" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) die "unsupported architecture: $machine" ;; esac
fi

exec 9>"$root/.dayorder-deploy.lock"
flock -n 9 || die "another deployment is running for $root"
work_dir="$(mktemp -d "$root/.dayorder-deploy.XXXXXX")"
cleanup() { [[ ! -d "$work_dir" ]] || rm -rf -- "$work_dir"; }
trap cleanup EXIT

download() {
  local url="$1" output="$2"
  [[ "$url" == https://github.com/* ]] || die "download URL must use the expected GitHub HTTPS origin"
  curl --proto '=https' --tlsv1.2 --fail --silent --show-error --location --output "$output" "$url"
}

manifest_value() {
  local key="$1" file="$2"
  sed -nE "s/^[[:space:]]*\"$key\":[[:space:]]*(\"[^\"]*\"|[0-9]+),?$/\\1/p" "$file" | tr -d '"'
}

validate_manifest() {
  local file="$1" expected_version="$2" schema script_version manifest_version revision
  schema="$(manifest_value schemaVersion "$file")"
  script_version="$(manifest_value deployScriptVersion "$file")"
  manifest_version="$(manifest_value version "$file")"
  revision="$(manifest_value revision "$file")"
  [[ "$schema" == 1 ]] || die "unsupported manifest schema: ${schema:-missing}"
  [[ "$script_version" == "$DEPLOY_SCRIPT_VERSION" ]] || die "unsupported deployment script compatibility version"
  [[ "$manifest_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "manifest version is invalid"
  [[ -z "$expected_version" || "$manifest_version" == "$expected_version" ]] || die "manifest version does not match requested version"
  [[ "$revision" =~ ^[0-9a-f]{40}$ ]] || die "manifest revision is invalid"
  grep -Fq '"web": "dayorder-web.tar.gz"' "$file" || die "manifest Web asset is invalid"
  for expected in dayorder-server-linux-amd64.tar.gz dayorder-server-linux-arm64.tar.gz \
    dayorder-worker-linux-amd64.tar.gz dayorder-worker-linux-arm64.tar.gz; do
    grep -Fq "\"$expected\"" "$file" || die "manifest asset is missing: $expected"
  done
  printf '%s' "$manifest_version"
}

if [[ -n "$requested_version" ]]; then
  release_base="$VERSION_BASE/$requested_version"
  download "$release_base/release-manifest.json" "$work_dir/release-manifest.json"
  version="$(validate_manifest "$work_dir/release-manifest.json" "$requested_version")"
else
  download "$LATEST_BASE/release-manifest.json" "$work_dir/latest-manifest.json"
  version="$(validate_manifest "$work_dir/latest-manifest.json" "")"
  release_base="$VERSION_BASE/$version"
  download "$release_base/release-manifest.json" "$work_dir/release-manifest.json"
  validate_manifest "$work_dir/release-manifest.json" "$version" >/dev/null
fi
download "$release_base/SHA256SUMS" "$work_dir/SHA256SUMS"

checksum_for() {
  local name="$1" count checksum
  count="$(awk -v name="$name" '$2 == name || $2 == "*" name { count++ } END { print count + 0 }' "$work_dir/SHA256SUMS")"
  [[ "$count" == 1 ]] || die "SHA-256 entry must appear exactly once: $name"
  checksum="$(awk -v name="$name" '$2 == name || $2 == "*" name { print $1 }' "$work_dir/SHA256SUMS")"
  [[ "$checksum" =~ ^[0-9a-f]{64}$ ]] || die "invalid SHA-256 entry: $name"
  printf '%s' "$checksum"
}

verify_file() {
  local name="$1" expected
  expected="$(checksum_for "$name")"
  printf '%s  %s\n' "$expected" "$name" | (cd -- "$work_dir" && sha256sum -c - >/dev/null) || \
    die "SHA-256 verification failed: $name"
}
verify_file release-manifest.json

asset_name() {
  case "$1" in
    web) printf 'dayorder-web.tar.gz' ;;
    server) printf 'dayorder-server-linux-%s.tar.gz' "$arch" ;;
    worker) printf 'dayorder-worker-linux-%s.tar.gz' "$arch" ;;
  esac
}

validate_archive() {
  local archive="$1" entry listing normalized
  while IFS= read -r entry; do
    normalized="${entry#./}"
    [[ -n "$normalized" ]] || continue
    [[ -n "$normalized" && "$normalized" != /* ]] || die "unsafe archive path: $entry"
    case "/$normalized/" in *"/../"*) die "unsafe archive path: $entry" ;; esac
  done < <(tar -tzf "$archive")
  while IFS= read -r listing; do
    case "${listing:0:1}" in -|d) ;; *) die "unsafe archive member type" ;; esac
  done < <(tar -tvzf "$archive")
}

validate_component_tree() {
  local name="$1" directory="$2" actual expected
  case "$name" in
    web)
      [[ -f "$directory/index.html" && -d "$directory/assets" ]] || die "Web archive is missing index.html or assets"
      if find "$directory" -type l -o -type b -o -type c -o -type p | grep -q .; then
        die "Web archive contains unsafe nodes"
      fi
      ;;
    server)
      expected=$'bin/dayorder-api\nbin/dayorder-migrate\nconfig/api.env.example\nconfig/migrate.env.example\nscripts/migrate.sh\nscripts/runtime-env.sh\nscripts/start-api.sh'
      actual="$(find "$directory" -type f -printf '%P\n' | sort)"
      [[ "$actual" == "$expected" ]] || die "Server archive content does not match the contract"
      ;;
    worker)
      expected=$'bin/dayorder-worker\nconfig/worker.env.example\nscripts/runtime-env.sh\nscripts/start-worker.sh'
      actual="$(find "$directory" -type f -printf '%P\n' | sort)"
      [[ "$actual" == "$expected" ]] || die "Worker archive content does not match the contract"
      ;;
  esac
}

install_component() {
  local name="$1" asset archive checksum destination staging marker
  asset="$(asset_name "$name")"; archive="$work_dir/$asset"
  download "$release_base/$asset" "$archive"; verify_file "$asset"; checksum="$(checksum_for "$asset")"
  destination="$root/releases/$version/$name"; marker="$destination/.dayorder-release"
  if [[ -d "$destination" ]]; then
    [[ -f "$marker" ]] || die "existing version directory has no release marker: $destination"
    grep -Fxq "asset=$asset" "$marker" && grep -Fxq "sha256=$checksum" "$marker" || \
      die "existing version directory does not match the Release asset"
    return
  fi
  staging="$work_dir/unpack-$name"; mkdir -p -- "$staging"
  validate_archive "$archive"
  tar --extract --gzip --no-same-owner --no-same-permissions --file "$archive" --directory "$staging"
  validate_component_tree "$name" "$staging"
  printf 'asset=%s\nsha256=%s\n' "$asset" "$checksum" > "$staging/.dayorder-release"
  mkdir -p -- "$root/releases/$version"
  mv -- "$staging" "$destination"
}

current_target() {
  local link="$root/current-$1" target
  [[ ! -e "$link" || -L "$link" ]] || die "$link exists and is not a symbolic link"
  if [[ -L "$link" ]]; then
    target="$(realpath -m -- "$link")"
    [[ "$target" == "$root/releases/"* ]] || die "$link points outside the managed releases directory"
    printf '%s' "$target"
  fi
}

switch_link() {
  local name="$1" target="$2" link temporary
  link="$root/current-$name"
  temporary="$root/.current-$name.$$"
  ln -s -- "$target" "$temporary"
  mv -Tf -- "$temporary" "$link"
}

deploy_web() {
  local destination="$root/releases/$version/web" old
  old="$(current_target web)"
  if [[ "$old" == "$destination" ]]; then printf 'Web %s is already deployed\n' "$version"; return; fi
  switch_link web "$destination"
  printf 'Web %s deployed at %s/current-web; configure Nginx/Caddy/CDN separately\n' "$version" "$root"
}

case "$component" in
  web) install_component web; deploy_web ;;
  server) install_component server ;;
  worker) install_component worker ;;
  all) die "all deployment is not available until service preflights are configured" ;;
esac
