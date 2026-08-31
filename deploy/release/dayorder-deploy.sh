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
[[ ! "$root_input" =~ [[:cntrl:]] ]] || die "deployment root contains control characters"
[[ ! -L "$root_input" ]] || die "deployment root must not be a symbolic link"
root="$(realpath -m -- "$root_input")"
home="$(realpath -m -- "${HOME:?HOME is required}")"
[[ "$root" != / ]] || die "filesystem root cannot be the deployment root"
[[ "$root" != "$home" ]] || die "home directory cannot be the deployment root"
mkdir -p -- "$root"
[[ ! -L "$root" ]] || die "deployment root must not be a symbolic link"

for command_name in bash curl tar sha256sum flock realpath awk sed grep find mktemp cmp; do require_command "$command_name"; done
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

canonical_manifest() {
  local manifest_version="$1" revision="$2"
  printf '%s\n' \
    '{' \
    '  "schemaVersion": 1,' \
    "  \"version\": \"$manifest_version\"," \
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
    '}'
}

validate_manifest() {
  local file="$1" expected_version="$2" schema script_version manifest_version revision
  schema="$(sed -nE 's/^  "schemaVersion": ([0-9]+),$/\1/p' "$file")"
  script_version="$(sed -nE 's/^  "deployScriptVersion": ([0-9]+),$/\1/p' "$file")"
  manifest_version="$(sed -nE 's/^  "version": "([^"]+)",$/\1/p' "$file")"
  revision="$(sed -nE 's/^  "revision": "([^"]+)",$/\1/p' "$file")"
  [[ "$schema" == 1 ]] || die "unsupported manifest schema: ${schema:-missing}"
  [[ "$script_version" == "$DEPLOY_SCRIPT_VERSION" ]] || die "unsupported deployment script compatibility version"
  [[ "$manifest_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "manifest version is invalid"
  [[ -z "$expected_version" || "$manifest_version" == "$expected_version" ]] || die "manifest version does not match requested version"
  [[ "$revision" =~ ^[0-9a-f]{40}$ ]] || die "manifest revision is invalid"
  canonical_manifest "$manifest_version" "$revision" | cmp -s "$file" - || die "manifest must use the canonical schema"
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

readonly CHECKSUM_NAMES=(
  dayorder-web.tar.gz
  dayorder-server-linux-amd64.tar.gz
  dayorder-server-linux-arm64.tar.gz
  dayorder-worker-linux-amd64.tar.gz
  dayorder-worker-linux-arm64.tar.gz
  dayorder-deploy.sh
  release-manifest.json
)

is_checksum_name() {
  case "$1" in
    dayorder-web.tar.gz|dayorder-server-linux-amd64.tar.gz|dayorder-server-linux-arm64.tar.gz|dayorder-worker-linux-amd64.tar.gz|dayorder-worker-linux-arm64.tar.gz|dayorder-deploy.sh|release-manifest.json) ;;
    *) return 1 ;;
  esac
}

checksum_for() {
  local name="$1" record checksum="" record_checksum record_name count=0
  while IFS= read -r record || [[ -n "$record" ]]; do
    if [[ "$record" =~ ^([0-9a-f]{64})\ \ (.+)$ ]]; then
      record_checksum="${BASH_REMATCH[1]}"; record_name="${BASH_REMATCH[2]}"
    elif [[ "$record" =~ ^([0-9a-f]{64})\ \*(.+)$ ]]; then
      record_checksum="${BASH_REMATCH[1]}"; record_name="${BASH_REMATCH[2]}"
    else
      die "invalid SHA-256 record"
    fi
    [[ "$record_name" == "$name" ]] || continue
    count=$((count + 1))
    checksum="$record_checksum"
  done < "$work_dir/SHA256SUMS"
  [[ "$count" == 1 ]] || die "SHA-256 entry must appear exactly once: $name"
  printf '%s' "$checksum"
}

validate_checksums() {
  local record record_name count=0 name
  while IFS= read -r record || [[ -n "$record" ]]; do
    if [[ "$record" =~ ^[0-9a-f]{64}\ \ (.+)$ ]]; then
      record_name="${BASH_REMATCH[1]}"
    elif [[ "$record" =~ ^[0-9a-f]{64}\ \*(.+)$ ]]; then
      record_name="${BASH_REMATCH[1]}"
    else
      die "invalid SHA-256 record"
    fi
    is_checksum_name "$record_name" || die "unexpected SHA-256 entry: $record_name"
    count=$((count + 1))
  done < "$work_dir/SHA256SUMS"
  [[ "$count" == "${#CHECKSUM_NAMES[@]}" ]] || die "SHA-256 file must contain exactly seven records"
  for name in "${CHECKSUM_NAMES[@]}"; do checksum_for "$name" >/dev/null; done
}

verify_file() {
  local name="$1" expected
  expected="$(checksum_for "$name")"
  printf '%s  %s\n' "$expected" "$name" | (cd -- "$work_dir" && sha256sum -c - >/dev/null) || \
    die "SHA-256 verification failed: $name"
}
validate_checksums
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
  if find "$directory" -type l -o -type b -o -type c -o -type p | grep -q .; then
    die "archive contains unsafe nodes"
  fi
  case "$name" in
    web)
      [[ -f "$directory/index.html" && -d "$directory/assets" ]] || die "Web archive is missing index.html or assets"
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

managed_releases_root() {
  local releases="$root/releases" canonical
  [[ ! -L "$releases" ]] || die "managed releases directory must not be a symbolic link"
  if [[ -e "$releases" ]]; then
    [[ -d "$releases" ]] || die "managed releases path is not a directory"
  else
    mkdir -- "$releases"
  fi
  [[ ! -L "$releases" ]] || die "managed releases directory must not be a symbolic link"
  canonical="$(realpath -m -- "$releases")"
  [[ "$canonical" == "$root/releases" ]] || die "managed releases path escapes root"
  printf '%s' "$canonical"
}

managed_destination() {
  local name="$1" releases version_directory destination canonical
  releases="$(managed_releases_root)"
  version_directory="$releases/$version"
  [[ ! -L "$version_directory" ]] || die "managed version directory must not be a symbolic link"
  if [[ -e "$version_directory" ]]; then
    [[ -d "$version_directory" ]] || die "managed version path is not a directory"
  else
    mkdir -- "$version_directory"
  fi
  [[ ! -L "$version_directory" ]] || die "managed version directory must not be a symbolic link"
  canonical="$(realpath -m -- "$version_directory")"
  [[ "$canonical" == "$releases/$version" ]] || die "managed version path escapes root"
  destination="$canonical/$name"
  [[ ! -L "$destination" ]] || die "managed component directory must not be a symbolic link"
  if [[ -e "$destination" ]]; then [[ -d "$destination" ]] || die "managed component path is not a directory"; fi
  canonical="$(realpath -m -- "$destination")"
  [[ "$canonical" == "$releases/$version/$name" ]] || die "managed component path escapes root"
  printf '%s' "$canonical"
}

install_component() {
  local name="$1" asset archive checksum destination staging marker
  asset="$(asset_name "$name")"; archive="$work_dir/$asset"
  download "$release_base/$asset" "$archive"; verify_file "$asset"; checksum="$(checksum_for "$asset")"
  destination="$(managed_destination "$name")"; marker="$destination/.dayorder-release"
  if [[ -d "$destination" ]]; then
    [[ ! -L "$marker" && -f "$marker" ]] || die "existing version directory has no release marker: $destination"
    grep -Fxq "asset=$asset" "$marker" && grep -Fxq "sha256=$checksum" "$marker" || \
      die "existing version directory does not match the Release asset"
    if find "$destination" -type l -o -type b -o -type c -o -type p | grep -q .; then
      die "existing version directory contains unsafe nodes"
    fi
    return
  fi
  staging="$work_dir/unpack-$name"; mkdir -p -- "$staging"
  validate_archive "$archive"
  tar --extract --gzip --no-same-owner --no-same-permissions --file "$archive" --directory "$staging"
  validate_component_tree "$name" "$staging"
  printf 'asset=%s\nsha256=%s\n' "$asset" "$checksum" > "$staging/.dayorder-release"
  destination="$(managed_destination "$name")"
  [[ ! -e "$destination" && ! -L "$destination" ]] || die "managed component destination appeared during deployment"
  mv -T -- "$staging" "$destination"
}

current_target() {
  local link="$root/current-$1" target releases
  releases="$(managed_releases_root)"
  [[ ! -e "$link" || -L "$link" ]] || die "$link exists and is not a symbolic link"
  if [[ -L "$link" ]]; then
    target="$(realpath -m -- "$link")"
    [[ "$target" == "$releases/"* ]] || die "$link points outside the managed releases directory"
    printf '%s' "$target"
  fi
}

switch_link() {
  local name="$1" target="$2" link temporary releases
  releases="$(managed_releases_root)"
  target="$(realpath -m -- "$target")"
  [[ "$target" == "$releases/"* ]] || die "link target points outside the managed releases directory"
  link="$root/current-$name"
  temporary="$root/.current-$name.$$"
  ln -s -- "$target" "$temporary"
  mv -Tf -- "$temporary" "$link"
}

config_dir="$root/dayorder-config"
config_created=0

ensure_config() {
  local name="$1" component_name="$2" template needle='/etc/dayorder/secrets' line remaining prefix output
  template="$root/releases/$version/$component_name/config/$name.env.example"
  local destination="$config_dir/$name.env" temporary="$config_dir/.$name.env.$$"
  [[ -f "$destination" ]] && return
  mkdir -p -- "$config_dir/secrets"; chmod 0700 "$config_dir" "$config_dir/secrets"
  while IFS= read -r line || [[ -n "$line" ]]; do
    remaining="$line"; output=""
    while [[ "$remaining" == *"$needle"* ]]; do
      prefix="${remaining%%"$needle"*}"
      output+="$prefix$config_dir/secrets"
      remaining="${remaining#*"$needle"}"
    done
    printf '%s\n' "$output$remaining"
  done < "$template" > "$temporary"
  chmod 0600 "$temporary"; mv -- "$temporary" "$destination"
  config_created=1
  printf 'Created %s; fill it and the referenced secret files before retrying.\n' "$destination" >&2
}

require_config() {
  [[ -f "$config_dir/$1.env" && -r "$config_dir/$1.env" ]] || die "configuration is not readable: $config_dir/$1.env"
}

systemd_quote() {
  local value="$1"
  value="${value//\\/\\\\}"; value="${value//\"/\\\"}"; value="${value//%/%%}"
  printf '"%s"' "$value"
}

validate_systemd_value() {
  [[ "$1" =~ [[:cntrl:]] ]] && die "systemd unit value contains control characters"
  return 0
}

preflight_systemd() {
  local linger
  require_command systemctl; require_command loginctl
  systemctl --user show-environment >/dev/null || die "systemd --user manager is unavailable"
  linger="$(loginctl show-user "${USER:?USER is required}" --property=Linger --value)"
  if [[ "$linger" != yes ]]; then
    die "linger is disabled; run: sudo loginctl enable-linger \"$USER\""
  fi
}

write_unit() {
  local service="$1" current="$2" script="$3" config="$4" timeout="$5"
  local unit_dir unit
  unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
  unit="$unit_dir/$service.service"
  mkdir -p -- "$unit_dir"
  {
    printf '[Unit]\nDescription=DayOrder %s\nAfter=network-online.target\nWants=network-online.target\n\n' "$service"
    printf '[Service]\nType=simple\nWorkingDirectory=%s\n' "$(systemd_quote "$current")"
    printf 'ExecStart=%s %s\n' "$(systemd_quote "$current/scripts/$script")" "$(systemd_quote "$config")"
    printf 'Restart=on-failure\nRestartSec=5\nTimeoutStopSec=%s\n\n[Install]\nWantedBy=default.target\n' "$timeout"
  } > "$unit"
}

server_changed=0; worker_changed=0; web_changed=0
server_old=""; worker_old=""; web_old=""

activate_service() {
  local service="$1"
  systemctl --user daemon-reload || return 1
  systemctl --user enable "$service.service" || return 1
  if systemctl --user is-active --quiet "$service.service"; then
    systemctl --user restart "$service.service" || return 1
  else
    systemctl --user start "$service.service" || return 1
  fi
}

wait_for_api() {
  local url="${DAYORDER_DEPLOY_HEALTH_URL:-http://127.0.0.1:8080/health/ready}" attempt
  for attempt in $(seq 1 30); do
    if curl --fail --silent --show-error --max-time 5 "$url" >/dev/null; then return 0; fi
    sleep 2
  done
  return 1
}

restore_link() {
  local name="$1" old="$2" service="${3:-}" link
  link="$root/current-$name"
  if [[ -n "$old" ]]; then
    switch_link "$name" "$old" || { printf 'dayorder-deploy: rollback failed to restore %s link\n' "$name" >&2; return 1; }
  else
    rm -f -- "$link" || { printf 'dayorder-deploy: rollback failed to remove %s link\n' "$name" >&2; return 1; }
    [[ ! -e "$link" && ! -L "$link" ]] || { printf 'dayorder-deploy: rollback failed to remove %s link\n' "$name" >&2; return 1; }
  fi
  if [[ -n "$service" ]]; then
    if [[ -n "$old" ]]; then
      systemctl --user restart "$service.service" || { printf 'dayorder-deploy: rollback failed to restart %s\n' "$service" >&2; return 1; }
    else
      systemctl --user stop "$service.service" || { printf 'dayorder-deploy: rollback failed to stop %s\n' "$service" >&2; return 1; }
    fi
  fi
}

deploy_server() {
  local destination="$root/releases/$version/server"
  server_old="$(current_target server)" || return 1
  if [[ "$server_old" == "$destination" ]]; then printf 'Server %s is already deployed\n' "$version"; return 0; fi
  "$destination/scripts/migrate.sh" up "$config_dir/migrate.env" || return 1
  "$destination/scripts/migrate.sh" check "$config_dir/migrate.env" || return 1
  switch_link server "$destination" || return 1; server_changed=1
  if ! write_unit dayorder-api "$root/current-server" start-api.sh "$config_dir/api.env" 30 || \
    ! activate_service dayorder-api || ! wait_for_api; then
    restore_link server "$server_old" dayorder-api || return 2
    server_changed=0; return 1
  fi
  printf 'Server %s deployed\n' "$version"
}

deploy_worker() {
  local destination="$root/releases/$version/worker"
  worker_old="$(current_target worker)" || return 1
  if [[ "$worker_old" == "$destination" ]]; then printf 'Worker %s is already deployed\n' "$version"; return 0; fi
  switch_link worker "$destination" || return 1; worker_changed=1
  if ! write_unit dayorder-worker "$root/current-worker" start-worker.sh "$config_dir/worker.env" 60 || \
    ! activate_service dayorder-worker || ! systemctl --user is-active --quiet dayorder-worker.service; then
    restore_link worker "$worker_old" dayorder-worker || return 2
    worker_changed=0; return 1
  fi
  printf 'Worker %s deployed\n' "$version"
}

deploy_web() {
  local destination="$root/releases/$version/web"
  web_old="$(current_target web)" || return 1
  if [[ "$web_old" == "$destination" ]]; then printf 'Web %s is already deployed\n' "$version"; return 0; fi
  switch_link web "$destination" || return 1
  web_changed=1
  printf 'Web %s deployed at %s/current-web; configure Nginx/Caddy/CDN separately\n' "$version" "$root"
}

# Schema migrations are deliberately not rolled back; compatible releases use expand/contract migrations.
case "$component" in
  web)
    install_component web
    ;;
  server)
    install_component server
    ensure_config api server
    ensure_config migrate server
    ;;
  worker)
    install_component worker
    ensure_config worker worker
    ;;
  all)
    install_component server
    install_component worker
    install_component web
    ensure_config api server
    ensure_config migrate server
    ensure_config worker worker
    ;;
esac
if (( config_created != 0 )); then
  die "configuration templates were created; complete them and rerun the same deployment command"
fi
case "$component" in
  server)
    require_config api; require_config migrate; preflight_systemd; validate_systemd_value "$root"
    ;;
  worker)
    require_config worker; preflight_systemd; validate_systemd_value "$root"
    ;;
  all)
    require_config api; require_config migrate; require_config worker; preflight_systemd; validate_systemd_value "$root"
    ;;
esac
rollback_failed=0
case "$component" in
  server)
    if ! deploy_server; then
      (( server_changed == 0 )) || die "Server deployment failed; rollback failed; manual intervention required"
      die "Server deployment failed; previous application link was preserved or restored"
    fi
    ;;
  worker)
    if ! deploy_worker; then
      (( worker_changed == 0 )) || die "Worker deployment failed; rollback failed; manual intervention required"
      die "Worker deployment failed; previous application link was preserved or restored"
    fi
    ;;
  web) deploy_web ;;
  all)
    if ! deploy_server; then
      (( server_changed == 0 )) || die "Server deployment failed; rollback failed; manual intervention required"
      die "Server deployment failed before later components were activated"
    fi
    if ! deploy_worker; then
      (( worker_changed == 0 )) || rollback_failed=1
      if (( server_changed != 0 )); then
        restore_link server "$server_old" dayorder-api || rollback_failed=1
        (( rollback_failed != 0 )) || server_changed=0
      fi
      (( rollback_failed == 0 )) || die "Worker deployment failed; rollback failed; manual intervention required"
      die "Worker deployment failed; activated application links were restored"
    fi
    if ! deploy_web; then
      if (( worker_changed != 0 )); then
        restore_link worker "$worker_old" dayorder-worker || rollback_failed=1
        (( rollback_failed != 0 )) || worker_changed=0
      fi
      if (( server_changed != 0 )); then
        restore_link server "$server_old" dayorder-api || rollback_failed=1
        (( rollback_failed != 0 )) || server_changed=0
      fi
      (( rollback_failed == 0 )) || die "Web deployment failed; rollback failed; manual intervention required"
      die "Web deployment failed; activated application links were restored"
    fi
    ;;
esac
