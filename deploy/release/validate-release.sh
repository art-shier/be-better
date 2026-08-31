#!/usr/bin/env bash
set -Eeuo pipefail

die() { printf 'validate-release: %s\n' "$*" >&2; exit 1; }

[[ $# -ge 1 && $# -le 3 ]] || \
  die "usage: validate-release.sh <asset-directory> [expected-version [expected-revision]]"
asset_directory="$1"
expected_version="${2:-}"
expected_revision="${3:-}"
[[ -d "$asset_directory" && ! -L "$asset_directory" ]] || die "asset directory is not a real directory: $asset_directory"
asset_directory="$(cd -- "$asset_directory" && pwd -P)"
if [[ -n "$expected_version" ]]; then
  [[ "$expected_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "expected version must match vX.Y.Z"
fi
if [[ -n "$expected_revision" ]]; then
  [[ -n "$expected_version" ]] || die "expected revision requires an expected version"
  [[ "$expected_revision" =~ ^[0-9a-f]{40}$ ]] || die "expected revision must be a 40-character lowercase Git SHA"
fi

for command_name in awk file find grep mktemp python3 readelf sha256sum sort tar uniq wc; do
  command -v "$command_name" >/dev/null 2>&1 || die "$command_name is required"
done

temporary_directory="$(mktemp -d)" || die "cannot create validation workspace"
cleanup() { [[ ! -d "$temporary_directory" ]] || rm -rf -- "$temporary_directory"; }
trap cleanup EXIT

expected_assets=$'SHA256SUMS\ndayorder-deploy.sh\ndayorder-server-linux-amd64.tar.gz\ndayorder-server-linux-arm64.tar.gz\ndayorder-web.tar.gz\ndayorder-worker-linux-amd64.tar.gz\ndayorder-worker-linux-arm64.tar.gz\nrelease-manifest.json'
actual_assets="$(find "$asset_directory" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort)"
[[ "$actual_assets" == "$expected_assets" ]] || die "asset set must contain exactly eight fixed-name entries"
while IFS= read -r name; do
  [[ -f "$asset_directory/$name" && ! -L "$asset_directory/$name" ]] || \
    die "release asset must be a regular non-symlink file: $name"
done <<< "$expected_assets"

expected_checksum_names=$'dayorder-deploy.sh\ndayorder-server-linux-amd64.tar.gz\ndayorder-server-linux-arm64.tar.gz\ndayorder-web.tar.gz\ndayorder-worker-linux-amd64.tar.gz\ndayorder-worker-linux-arm64.tar.gz\nrelease-manifest.json'
checksum_names=()
checksum_count=0
while IFS= read -r record || [[ -n "$record" ]]; do
  ((checksum_count += 1))
  [[ "$record" =~ ^[0-9a-f]{64}\ ([\ \*])([^/[:space:]]+)$ ]] || die "checksum record has an invalid strict format"
  checksum_names+=("${BASH_REMATCH[2]}")
done < "$asset_directory/SHA256SUMS"
[[ "$checksum_count" -eq 7 ]] || die "SHA256SUMS must contain exactly seven checksum records"
actual_checksum_names="$(printf '%s\n' "${checksum_names[@]}" | LC_ALL=C sort)"
[[ "$actual_checksum_names" == "$expected_checksum_names" ]] || \
  die "SHA256SUMS does not name the exact seven checksummed assets"
if ! (cd -- "$asset_directory" && sha256sum --strict -c SHA256SUMS >/dev/null); then
  die "release asset checksum verification failed"
fi

if ! python3 - "$asset_directory/release-manifest.json" "$expected_version" "$expected_revision" <<'PYTHON'
import json
import re
import sys

path, expected_version, expected_revision = sys.argv[1:]

def fail(message):
    print(f"validate-release: {message}", file=sys.stderr)
    raise SystemExit(1)

try:
    with open(path, "r", encoding="utf-8") as manifest_file:
        manifest = json.load(manifest_file)
except (OSError, UnicodeError, json.JSONDecodeError):
    fail("release Manifest is not valid UTF-8 JSON")

def exact_keys(value, keys, label):
    if not isinstance(value, dict) or set(value) != set(keys):
        fail(f"{label} has unexpected or missing fields")

exact_keys(manifest, ["schemaVersion", "version", "revision", "deployScriptVersion", "assets"], "release Manifest")
if manifest["schemaVersion"] != 1 or isinstance(manifest["schemaVersion"], bool):
    fail("release Manifest schemaVersion must be 1")
if manifest["deployScriptVersion"] != 1 or isinstance(manifest["deployScriptVersion"], bool):
    fail("release Manifest deployScriptVersion must be 1")
if not isinstance(manifest["version"], str) or not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", manifest["version"]):
    fail("release Manifest version is invalid")
if not isinstance(manifest["revision"], str) or not re.fullmatch(r"[0-9a-f]{40}", manifest["revision"]):
    fail("release Manifest revision is invalid")
if expected_version and manifest["version"] != expected_version:
    fail("release Manifest version does not match the expected version")
if expected_revision and manifest["revision"] != expected_revision:
    fail("release Manifest revision does not match the expected revision")

assets = manifest["assets"]
exact_keys(assets, ["web", "server", "worker"], "release Manifest assets")
exact_keys(assets["server"], ["amd64", "arm64"], "release Manifest Server assets")
exact_keys(assets["worker"], ["amd64", "arm64"], "release Manifest Worker assets")
expected_assets = {
    "web": "dayorder-web.tar.gz",
    "server": {
        "amd64": "dayorder-server-linux-amd64.tar.gz",
        "arm64": "dayorder-server-linux-arm64.tar.gz",
    },
    "worker": {
        "amd64": "dayorder-worker-linux-amd64.tar.gz",
        "arm64": "dayorder-worker-linux-arm64.tar.gz",
    },
}
if assets != expected_assets:
    fail("release Manifest asset mapping does not match the fixed contract")
PYTHON
then
  die "release Manifest contract validation failed"
fi

archive_projection() {
  local archive="$1" label="$2" listing verbose line permissions owner path projection="" duplicates
  if ! listing="$(tar -tzf "$archive")"; then die "$label archive paths could not be listed"; fi
  [[ -n "$listing" ]] || die "$label archive is empty"
  duplicates="$(printf '%s\n' "$listing" | LC_ALL=C sort | uniq -d)"
  [[ -z "$duplicates" ]] || die "$label archive contains duplicate members"
  while IFS= read -r path; do
    [[ -n "$path" && "$path" != /* && "$path" != *$'\n'* && "$path" != *$'\r'* ]] || \
      die "$label archive contains an unsafe member path"
    case "/$path/" in *"/../"*) die "$label archive contains an unsafe member path: $path" ;; esac
    [[ "$path" != *"//"* ]] || die "$label archive contains a noncanonical member path: $path"
  done <<< "$listing"
  if ! verbose="$(LC_ALL=C tar --numeric-owner -tvzf "$archive")"; then
    die "$label archive member metadata could not be listed"
  fi
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    permissions="${line%%[[:space:]]*}"
    line="${line#*[[:space:]]}"
    line="${line#${line%%[![:space:]]*}}"
    owner="${line%%[[:space:]]*}"
    path="${line##*[[:space:]]}"
    case "${permissions:0:1}" in -|d) ;; *) die "$label archive contains a non-regular member type: $path" ;; esac
    [[ "$owner" == 0/0 ]] || die "$label archive member is not owned by numeric 0/0: $path"
    projection+="$permissions $owner $path"$'\n'
  done <<< "$verbose"
  [[ "$(printf '%s' "$projection" | wc -l)" -eq "$(printf '%s\n' "$listing" | wc -l)" ]] || \
    die "$label archive path and metadata listings disagree"
  printf '%s' "$projection"
}

validate_web_archive() {
  local archive="$1" projection line permissions owner path seen_root=0 seen_index=0 seen_assets=0 asset_files=0
  projection="$(archive_projection "$archive" "Web")"
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    permissions="${line%% *}"; line="${line#* }"; owner="${line%% *}"; path="${line#* }"
    case "$path" in
      ./)
        [[ "$permissions" == drwxr-xr-x ]] || die "Web archive root must use mode 0755"
        seen_root=1
        ;;
      ./index.html)
        [[ "$permissions" == -rw-r--r-- ]] || die "Web archive index.html must use mode 0644"
        seen_index=1
        ;;
      ./assets/)
        [[ "$permissions" == drwxr-xr-x ]] || die "Web archive assets directory must use mode 0755"
        seen_assets=1
        ;;
      ./assets/*/)
        [[ "$permissions" == drwxr-xr-x ]] || die "Web archive asset directories must use mode 0755: $path"
        ;;
      ./assets/*)
        [[ "$permissions" == -rw-r--r-- ]] || die "Web archive asset files must use mode 0644: $path"
        ((asset_files += 1))
        ;;
      *) die "Web archive member is outside the static contract: $path" ;;
    esac
  done <<< "$projection"
  (( seen_root == 1 && seen_index == 1 && seen_assets == 1 && asset_files > 0 )) || \
    die "Web archive is missing required static contract members"
}

validate_fixed_archive() {
  local archive="$1" label="$2" expected="$3" actual
  actual="$(archive_projection "$archive" "$label")"
  [[ "$actual" == "$expected" ]] || die "$label archive member and mode contract does not match"
}

server_contract=$'drwxr-xr-x 0/0 ./\ndrwxr-xr-x 0/0 ./bin/\n-rwxr-xr-x 0/0 ./bin/dayorder-api\n-rwxr-xr-x 0/0 ./bin/dayorder-migrate\ndrwxr-xr-x 0/0 ./config/\n-rw-r--r-- 0/0 ./config/api.env.example\n-rw-r--r-- 0/0 ./config/migrate.env.example\ndrwxr-xr-x 0/0 ./scripts/\n-rwxr-xr-x 0/0 ./scripts/migrate.sh\n-rwxr-xr-x 0/0 ./scripts/runtime-env.sh\n-rwxr-xr-x 0/0 ./scripts/start-api.sh'
worker_contract=$'drwxr-xr-x 0/0 ./\ndrwxr-xr-x 0/0 ./bin/\n-rwxr-xr-x 0/0 ./bin/dayorder-worker\ndrwxr-xr-x 0/0 ./config/\n-rw-r--r-- 0/0 ./config/worker.env.example\ndrwxr-xr-x 0/0 ./scripts/\n-rwxr-xr-x 0/0 ./scripts/runtime-env.sh\n-rwxr-xr-x 0/0 ./scripts/start-worker.sh'

validate_web_archive "$asset_directory/dayorder-web.tar.gz"
for arch in amd64 arm64; do
  validate_fixed_archive "$asset_directory/dayorder-server-linux-$arch.tar.gz" "Server $arch" "$server_contract"
  validate_fixed_archive "$asset_directory/dayorder-worker-linux-$arch.tar.gz" "Worker $arch" "$worker_contract"
done

validate_elf() {
  local arch="$1" path="$2" label="$3" description header programs dynamics
  description="$(file -b -- "$path")" || die "could not identify $label"
  [[ "$description" == *"ELF 64-bit LSB"* ]] || die "$label is not a 64-bit little-endian ELF executable"
  [[ "$description" == *"statically linked"* ]] || die "$label is not statically linked"
  header="$(readelf -h -- "$path")" || die "could not read the ELF header for $label"
  case "$arch" in
    amd64) [[ "$description" == *"x86-64"* && "$header" == *"Machine:"*"Advanced Micro Devices X86-64"* ]] || die "$label is not an amd64/x86-64 ELF" ;;
    arm64) [[ "$description" == *"ARM aarch64"* && "$header" == *"Machine:"*"AArch64"* ]] || die "$label is not an arm64/AArch64 ELF" ;;
  esac
  programs="$(readelf -l -- "$path")" || die "could not read ELF program headers for $label"
  [[ "$programs" != *" INTERP "* ]] || die "$label contains a dynamic interpreter"
  dynamics="$(readelf -d -- "$path" 2>&1)" || die "could not read ELF dynamic metadata for $label"
  [[ "$dynamics" != *"(NEEDED)"* ]] || die "$label has dynamic library dependencies"
}

for arch in amd64 arm64; do
  server_directory="$temporary_directory/server-$arch"
  worker_directory="$temporary_directory/worker-$arch"
  mkdir -p -- "$server_directory" "$worker_directory"
  tar -xzf "$asset_directory/dayorder-server-linux-$arch.tar.gz" -C "$server_directory" --no-same-owner --no-same-permissions
  tar -xzf "$asset_directory/dayorder-worker-linux-$arch.tar.gz" -C "$worker_directory" --no-same-owner --no-same-permissions
  validate_elf "$arch" "$server_directory/bin/dayorder-api" "Server API $arch"
  validate_elf "$arch" "$server_directory/bin/dayorder-migrate" "Server Migrator $arch"
  validate_elf "$arch" "$worker_directory/bin/dayorder-worker" "Worker $arch"
done

if ! bash -n "$asset_directory/dayorder-deploy.sh"; then die "dayorder-deploy.sh fails Bash syntax validation"; fi
printf 'Release asset validation passed: %s\n' "$asset_directory"
