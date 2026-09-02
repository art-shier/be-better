#!/usr/bin/env bash
set -Eeuo pipefail

readonly DEPLOY_SCRIPT_VERSION=1
readonly REPOSITORY="art-shier/be-better"
readonly LATEST_BASE="https://github.com/$REPOSITORY/releases/latest/download"
readonly VERSION_BASE="https://github.com/$REPOSITORY/releases/download"

die() { printf 'dayorder-deploy: %s\n' "$*" >&2; exit 1; }
usage() {
  printf '%s\n' \
    'usage: dayorder-deploy.sh <web|server|worker|all> [--version vX.Y.Z] [--root PATH]' \
    '       dayorder-deploy.sh upgrade <web|server|worker|all> [--root PATH]' \
    '       dayorder-deploy.sh redeploy <web|server|worker|all> [--version vX.Y.Z] [--root PATH]' \
    '       dayorder-deploy.sh <start|stop|restart|status> <server|worker|web|all> [--root PATH]' >&2
}
require_command() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

[[ $# -ge 1 ]] || { usage; exit 1; }
action=deploy
case "$1" in
  web|server|worker|all) component="$1"; shift ;;
  start|stop|restart|status|upgrade|redeploy)
    action="$1"; shift
    [[ $# -ge 1 ]] || { usage; exit 1; }
    component="$1"; shift
    ;;
  *) usage; exit 1 ;;
esac
case "$component" in web|server|worker|all) ;; *) usage; exit 1 ;; esac
requested_version=""
version_specified=0
root_input="$PWD"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) [[ $# -ge 2 ]] || die "--version requires a value"; requested_version="$2"; version_specified=1; shift 2 ;;
    --root) [[ $# -ge 2 ]] || die "--root requires a value"; root_input="$2"; shift 2 ;;
    *) die "unknown argument: $1" ;;
  esac
done
[[ "$action" != upgrade || "$version_specified" == 0 ]] || die "upgrade does not accept --version; it always resolves the latest Release"
case "$action" in
  start|stop|restart|status)
    [[ "$version_specified" == 0 ]] || die "$action does not accept --version"
    ;;
esac
[[ "$version_specified" == 0 || "$requested_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "version must match vX.Y.Z"
[[ -n "$root_input" ]] || die "deployment root must not be empty"
[[ ! "$root_input" =~ [[:cntrl:]] ]] || die "deployment root contains control characters"
[[ ! -L "$root_input" ]] || die "deployment root must not be a symbolic link"
root="$(realpath -m -- "$root_input")"
home="$(realpath -m -- "${HOME:?HOME is required}")"
[[ "$root" != / ]] || die "filesystem root cannot be the deployment root"
[[ "$root" != "$home" ]] || die "home directory cannot be the deployment root"

report_web_status() {
  local link="$root/current-web" target
  if [[ -L "$link" ]]; then
    target="$(realpath -m -- "$link")"
    printf 'Web current: %s\n' "$target"
  elif [[ -e "$link" ]]; then
    die "Web current path is not a symbolic link: $link"
  else
    printf 'Web is not deployed under %s\n' "$root"
  fi
}

manage_services() {
  local result=0
  local -a services
  if [[ "$component" == web ]]; then
    [[ "$action" == status ]] || printf 'Web has no managed systemd service; %s did not change current-web.\n' "$action"
    report_web_status
    return
  fi
  require_command systemctl
  case "$component" in
    server) services=(dayorder-api.service) ;;
    worker) services=(dayorder-worker.service) ;;
    all)
      if [[ "$action" == stop ]]; then
        services=(dayorder-worker.service dayorder-api.service)
      else
        services=(dayorder-api.service dayorder-worker.service)
      fi
      ;;
  esac
  systemctl --user "$action" "${services[@]}" || result=$?
  if [[ "$component" == all ]]; then
    [[ "$action" == status ]] || printf 'Web has no managed systemd service; %s did not change current-web.\n' "$action"
    report_web_status
  fi
  return "$result"
}

case "$action" in
  start|stop|restart|status)
    manage_services
    exit
    ;;
esac

mkdir -p -- "$root"
[[ ! -L "$root" ]] || die "deployment root must not be a symbolic link"

for command_name in bash curl tar sha256sum flock realpath awk sed grep find mktemp cmp id stat; do require_command "$command_name"; done
deployment_uid="$(id -u)"
deployment_user="$(id -un)"
[[ "$deployment_uid" =~ ^[0-9]+$ && -n "$deployment_user" && ! "$deployment_user" =~ [[:cntrl:]] ]] || \
  die "could not determine a safe deployment identity"
confighub_path=""
if [[ "$component" != web ]]; then
  confighub_path="$(type -P confighub)" || die "confighub is required"
  confighub_path="$(realpath -e -- "$confighub_path")" || die "cannot resolve the ConfigHub executable"
  [[ "$confighub_path" == /* && -f "$confighub_path" && -x "$confighub_path" && ! "$confighub_path" =~ [[:cntrl:]] ]] || \
    die "ConfigHub executable must be an absolute executable file"
  machine="$(uname -m)"
  case "$machine" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) die "unsupported architecture: $machine" ;; esac
fi

legacy_lock="$root/.dayorder-deploy.lock"
[[ ! -L "$legacy_lock" ]] || die "lock path must not be a symbolic link: $legacy_lock"
[[ ! -e "$legacy_lock" || -f "$legacy_lock" ]] || die "lock path is not a regular file: $legacy_lock"
exec 9<"$root" || die "cannot open deployment root for locking: $root"
flock -n 9 || die "another deployment is running for $root"
work_dir="$(mktemp -d "$root/.dayorder-deploy.XXXXXX")" || die "cannot create deployment workspace"
chmod 0700 "$work_dir" || die "cannot restrict deployment workspace permissions"
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
  local archive="$1" entry listing normalized entries listings
  if ! entries="$(tar -tzf "$archive")"; then
    die "could not list archive paths: $archive"
  fi
  while IFS= read -r entry; do
    normalized="${entry#./}"
    [[ -n "$normalized" ]] || continue
    [[ -n "$normalized" && "$normalized" != /* ]] || die "unsafe archive path: $entry"
    case "/$normalized/" in *"/../"*) die "unsafe archive path: $entry" ;; esac
  done <<< "$entries"
  if ! listings="$(tar -tvzf "$archive")"; then
    die "could not list archive member types: $archive"
  fi
  while IFS= read -r listing; do
    [[ -n "$listing" ]] || continue
    case "${listing:0:1}" in -|d) ;; *) die "unsafe archive member type" ;; esac
  done <<< "$listings"
}

validate_no_unsafe_nodes() {
  local directory="$1" label="$2" unsafe
  if ! unsafe="$(find "$directory" \( -type l -o -type b -o -type c -o -type p -o -type s \) -print -quit)"; then
    die "could not inspect $label for unsafe nodes"
  fi
  [[ -z "$unsafe" ]] || die "$label contains unsafe nodes"
}

validate_component_tree() {
  local name="$1" directory="$2" actual expected
  validate_no_unsafe_nodes "$directory" "archive"
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
  asset="$(asset_name "$name")"; checksum="$(checksum_for "$asset")"
  destination="$(managed_destination "$name")"; marker="$destination/.dayorder-release"
  if [[ -d "$destination" ]]; then
    [[ ! -L "$marker" && -f "$marker" ]] || die "existing version directory has no release marker: $destination"
    grep -Fxq "asset=$asset" "$marker" && grep -Fxq "sha256=$checksum" "$marker" || \
      die "existing version directory does not match the Release asset"
    validate_no_unsafe_nodes "$destination" "existing version directory"
    return
  fi
  archive="$work_dir/$asset"
  download "$release_base/$asset" "$archive"; verify_file "$asset"
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
  local name="$1" target="$2" link temporary releases staging
  releases="$(managed_releases_root)"
  target="$(realpath -m -- "$target")"
  [[ "$target" == "$releases/"* ]] || die "link target points outside the managed releases directory"
  link="$root/current-$name"
  staging="$(mktemp -d "$root/.dayorder-link-$name.XXXXXX")" || {
    printf 'dayorder-deploy: failed to create secure %s link workspace\n' "$name" >&2
    return 1
  }
  chmod 0700 "$staging" || {
    rmdir -- "$staging" 2>/dev/null || true
    printf 'dayorder-deploy: failed to restrict secure %s link workspace\n' "$name" >&2
    return 1
  }
  temporary="$staging/current-$name"
  if ! ln -s -- "$target" "$temporary"; then
    rm -f -- "$temporary"
    rmdir -- "$staging" 2>/dev/null || true
    printf 'dayorder-deploy: failed to create temporary %s link\n' "$name" >&2
    return 1
  fi
  if ! mv -Tf -- "$temporary" "$link"; then
    rm -f -- "$temporary"
    rmdir -- "$staging" 2>/dev/null || true
    printf 'dayorder-deploy: failed to activate %s link\n' "$name" >&2
    return 1
  fi
  rmdir -- "$staging" 2>/dev/null || printf 'dayorder-deploy: warning: could not remove secure %s link workspace\n' "$name" >&2
}

config_dir="$root/dayorder-config"
config_created=0

normalized_mode() {
  local mode="$1"
  mode="${mode#0}"
  printf '%s' "${mode:-0}"
}

ensure_owned_directory() {
  local path="$1" label="$2" expected_mode="$3" mode_policy="${4:-exact}" created=0 owner actual_mode logical physical
  [[ ! -L "$path" ]] || die "$label must not be a symbolic link: $path"
  logical="$(realpath -ms -- "$path")"
  physical="$(realpath -m -- "$path")"
  [[ "$logical" == "$physical" ]] || die "$label path must not traverse symbolic links: $path"
  if [[ -e "$path" ]]; then
    [[ -d "$path" ]] || die "$label is not a directory: $path"
  else
    mkdir -p -- "$path" || die "cannot create $label: $path"
    chmod "0$expected_mode" "$path" || die "cannot restrict $label permissions: $path"
    created=1
  fi
  [[ ! -L "$path" && -d "$path" ]] || die "$label must be a real directory: $path"
  owner="$(stat -c %u -- "$path")" || die "cannot read $label ownership: $path"
  [[ "$owner" == "$deployment_uid" ]] || die "$label must be owned by the deployment user: $path"
  actual_mode="$(stat -c %a -- "$path")" || die "cannot read $label mode: $path"
  actual_mode="$(normalized_mode "$actual_mode")"
  if [[ "$mode_policy" == no_external_write ]]; then
    (( (8#$actual_mode & 0022) == 0 )) || die "$label must not be group- or other-writable: $path"
  else
    [[ "$actual_mode" == "$expected_mode" ]] || die "$label must use mode 0$expected_mode: $path"
  fi
  (( created == 0 )) || return 0
}

validate_owned_file() {
  local path="$1" label="$2" expected_mode="$3" owner actual_mode
  [[ ! -L "$path" ]] || die "$label must not be a symbolic link: $path"
  [[ -f "$path" && -r "$path" ]] || die "$label is not a readable regular file: $path"
  owner="$(stat -c %u -- "$path")" || die "cannot read $label ownership: $path"
  [[ "$owner" == "$deployment_uid" ]] || die "$label must be owned by the deployment user: $path"
  actual_mode="$(stat -c %a -- "$path")" || die "cannot read $label mode: $path"
  actual_mode="$(normalized_mode "$actual_mode")"
  [[ "$actual_mode" == "$expected_mode" ]] || die "$label must use mode 0$expected_mode: $path"
}

ensure_config_directories() {
  ensure_owned_directory "$config_dir" "configuration directory" 700
  ensure_owned_directory "$config_dir/secrets" "secrets directory" 700
}

ensure_config() {
  local name="$1" component_name="$2" template needle='/etc/dayorder/secrets' line remaining prefix output
  template="$root/releases/$version/$component_name/config/$name.env.example"
  local destination="$config_dir/$name.env" temporary
  ensure_config_directories
  if [[ -e "$destination" || -L "$destination" ]]; then
    validate_owned_file "$destination" "configuration file" 600
    return
  fi
  temporary="$(mktemp "$config_dir/.$name.env.XXXXXX")" || die "cannot create temporary configuration file"
  if ! while IFS= read -r line || [[ -n "$line" ]]; do
    remaining="$line"; output=""
    while [[ "$remaining" == *"$needle"* ]]; do
      prefix="${remaining%%"$needle"*}"
      output+="$prefix$config_dir/secrets"
      remaining="${remaining#*"$needle"}"
    done
    printf '%s\n' "$output$remaining"
  done < "$template" > "$temporary"; then
    rm -f -- "$temporary"
    die "cannot render configuration template: $template"
  fi
  chmod 0600 "$temporary" || { rm -f -- "$temporary"; die "cannot restrict temporary configuration permissions"; }
  if ! ln -- "$temporary" "$destination"; then
    rm -f -- "$temporary"
    die "configuration destination appeared while installing: $destination"
  fi
  rm -f -- "$temporary" || die "cannot clean temporary configuration file"
  validate_owned_file "$destination" "configuration file" 600
  config_created=1
  printf 'Created %s; fill it and the referenced secret files before retrying.\n' "$destination" >&2
}

require_config() {
  ensure_config_directories
  validate_owned_file "$config_dir/$1.env" "configuration file" 600
}

print_configuration_instructions() {
  local -a environment_files=() secret_files=()
  local path
  case "$component" in
    server)
      environment_files=(api.env migrate.env)
      secret_files=(auth_hmac_key)
      ;;
    worker)
      environment_files=(worker.env)
      secret_files=(auth_hmac_key smtp_password agent_http_key)
      ;;
    all)
      environment_files=(api.env migrate.env worker.env)
      secret_files=(auth_hmac_key smtp_password agent_http_key)
      ;;
  esac
  printf 'ConfigHub CLI reads %s/.confighub.yaml; ConfigHub errors stop deployment.\n' "$config_dir" >&2
  printf 'Required secret files (each must contain exactly one non-empty single-line value):\n' >&2
  for path in "${secret_files[@]}"; do printf '  %s/secrets/%s\n' "$config_dir" "$path" >&2; done
  printf 'Create/edit the files, then enforce these permissions before retrying:\n  touch' >&2
  for path in "${secret_files[@]}"; do printf ' %q' "$config_dir/secrets/$path" >&2; done
  printf '\n  chmod 0700 %q %q\n  chmod 0600' "$config_dir" "$config_dir/secrets" >&2
  for path in "${environment_files[@]}"; do printf ' %q' "$config_dir/$path" >&2; done
  for path in "${secret_files[@]}"; do printf ' %q' "$config_dir/secrets/$path" >&2; done
  printf '\n' >&2
}

preflight_confighub() {
  if ! (cd -- "$config_dir" && "$confighub_path" run --project shier --env prod -- true); then
    die "ConfigHub preflight failed"
  fi
}

systemd_quote() {
  local value="$1"
  value="${value//\\/\\\\}"; value="${value//\"/\\\"}"; value="${value//%/%%}"
  printf '"%s"' "$value"
}

systemd_path() {
  local value="$1"
  value="${value//%/%%}"
  printf '%s' "$value"
}

validate_systemd_value() {
  [[ "$1" =~ [[:cntrl:]] ]] && die "systemd unit value contains control characters"
  return 0
}

preflight_systemd() {
  local linger
  require_command systemctl; require_command loginctl
  systemctl --user show-environment >/dev/null || die "systemd --user manager is unavailable"
  linger="$(loginctl show-user "$deployment_user" --property=Linger --value)"
  if [[ "$linger" != yes ]]; then
    die "linger is disabled; run: sudo loginctl enable-linger \"$deployment_user\""
  fi
}

unit_directory() {
  printf '%s/systemd/user' "${XDG_CONFIG_HOME:-$HOME/.config}"
}

preflight_unit() {
  local service="$1" unit_dir unit
  unit_dir="$(unit_directory)"
  [[ "$unit_dir" == /* && ! "$unit_dir" =~ [[:cntrl:]] ]] || die "systemd unit directory must be an absolute safe path"
  ensure_owned_directory "$unit_dir" "systemd unit directory" 700 no_external_write
  unit="$unit_dir/$service.service"
  if [[ -e "$unit" || -L "$unit" ]]; then
    validate_owned_file "$unit" "systemd unit file" 644
  fi
}

write_unit() {
  local service="$1" current="$2" script="$3" config="$4" timeout="$5"
  local unit_dir unit temporary
  unit_dir="$(unit_directory)"
  unit="$unit_dir/$service.service"
  [[ ! -L "$unit" ]] || { printf 'dayorder-deploy: systemd unit file became a symbolic link: %s\n' "$unit" >&2; return 1; }
  temporary="$(mktemp "$unit_dir/.$service.service.XXXXXX")" || return 1
  if ! {
    printf '[Unit]\nDescription=DayOrder %s\nAfter=network-online.target\nWants=network-online.target\n\n' "$service"
    printf '[Service]\nType=simple\nWorkingDirectory=%s\n' "$(systemd_path "$current")"
    printf 'Environment=%s\n' "$(systemd_quote "DAYORDER_CONFIGHUB_EXECUTABLE=$confighub_path")"
    printf 'ExecStart=%s %s\n' "$(systemd_quote "$current/scripts/$script")" "$(systemd_quote "$config")"
    printf 'Restart=on-failure\nRestartSec=5\nTimeoutStopSec=%s\n\n[Install]\nWantedBy=default.target\n' "$timeout"
  } > "$temporary"; then
    rm -f -- "$temporary"
    return 1
  fi
  chmod 0644 "$temporary" || { rm -f -- "$temporary"; return 1; }
  if ! mv -Tf -- "$temporary" "$unit"; then
    rm -f -- "$temporary"
    return 1
  fi
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
      if [[ "$service" == dayorder-api ]] && ! wait_for_api; then
        printf 'dayorder-deploy: restored API failed readiness; manual intervention required\n' >&2
        return 1
      fi
    else
      systemctl --user stop "$service.service" || { printf 'dayorder-deploy: rollback failed to stop %s\n' "$service" >&2; return 1; }
    fi
  fi
}

deploy_server() {
  local destination="$root/releases/$version/server"
  server_old="$(current_target server)" || return 1
  if [[ "$action" != redeploy && "$server_old" == "$destination" ]]; then printf 'Server %s is already deployed\n' "$version"; return 0; fi
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
  if [[ "$action" != redeploy && "$worker_old" == "$destination" ]]; then printf 'Worker %s is already deployed\n' "$version"; return 0; fi
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
  if [[ "$action" != redeploy && "$web_old" == "$destination" ]]; then printf 'Web %s is already deployed\n' "$version"; return 0; fi
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
  print_configuration_instructions
  die "configuration templates were created; complete them and rerun the same deployment command"
fi
case "$component" in
  server)
    require_config api; require_config migrate; preflight_confighub; preflight_systemd; validate_systemd_value "$root"; preflight_unit dayorder-api
    ;;
  worker)
    require_config worker; preflight_confighub; preflight_systemd; validate_systemd_value "$root"; preflight_unit dayorder-worker
    ;;
  all)
    require_config api; require_config migrate; require_config worker; preflight_confighub; preflight_systemd; validate_systemd_value "$root"
    preflight_unit dayorder-api; preflight_unit dayorder-worker
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
