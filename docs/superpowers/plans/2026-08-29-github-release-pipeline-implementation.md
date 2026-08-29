# DayOrder GitHub Release Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish versioned Web, Server, and Worker assets to GitHub Releases and provide one standalone Bash command that securely installs or updates any component on a Docker-free Linux host.

**Architecture:** Existing bare-metal builders remain the source of Web and Go files. Focused scripts under `deploy/release/` turn those directories into a fixed five-archive contract, generate a versioned manifest and checksums, and implement a standalone pull deployer using immutable version directories plus atomic `current-*` links. A tag-only GitHub Actions workflow tests and builds assets, creates a Draft Release, verifies the complete asset set, and only then publishes it.

**Tech Stack:** Bash 4+, GNU `tar`/`sha256sum`/`flock`, `curl`, `systemd --user`, Node.js 24/`node:test`, npm workspaces, Vite 8, Go 1.25, GitHub Actions and GitHub CLI.

---

## File map

- Create `deploy/release/package-assets.sh`: validate and package already-built Web/backend directories, generate `release-manifest.json`, and generate `SHA256SUMS`.
- Create `deploy/release/build-release.sh`: call existing bare-metal builders for one CI component or for all local release assets.
- Create `deploy/release/dayorder-deploy.sh`: standalone public deployment client; no repository-relative imports at runtime.
- Create `scripts/release-assets.test.mjs`: executable archive, permission, manifest, and checksum contract tests.
- Create `scripts/release-deploy.test.mjs`: hermetic deployment tests with fake GitHub downloads, `systemctl`, `loginctl`, and health responses.
- Create `scripts/release-workflow.test.mjs`: static contract test for stable tags, permissions, pinned actions, matrix targets, Draft-first publishing, and complete assets.
- Create `.github/workflows/release.yml`: stable-tag GitHub Release workflow.
- Modify `package.json`: expose release tests and the complete local asset build.
- Modify `scripts/bare-metal-deploy.test.mjs`: keep the old directory-release contract while asserting the new Release entry points are discoverable.
- Modify `README.md`: replace manual upload as the primary split-deployment path with GitHub Release commands.
- Modify `docs/runbooks/separate-deployment.md`: document first install, configuration, systemd user services, updates, logs, rollback semantics, and Web server boundary.

## Stable interfaces to preserve across tasks

The asset names are exact and never contain the version:

```text
dayorder-web.tar.gz
dayorder-server-linux-amd64.tar.gz
dayorder-server-linux-arm64.tar.gz
dayorder-worker-linux-amd64.tar.gz
dayorder-worker-linux-arm64.tar.gz
dayorder-deploy.sh
release-manifest.json
SHA256SUMS
```

The deployer CLI is exact:

```text
dayorder-deploy.sh <web|server|worker|all> [--version vX.Y.Z] [--root ABSOLUTE_OR_RELATIVE_PATH]
```

The deployment environment has one supported override:

```text
DAYORDER_DEPLOY_HEALTH_URL=http://127.0.0.1:8080/health/ready
```

Tests may replace commands through `PATH`, but production code must always construct downloads below these HTTPS bases:

```text
https://github.com/art-shier/be-better/releases/latest/download
https://github.com/art-shier/be-better/releases/download/<version>
```

### Task 1: Component archive and metadata contract

**Files:**
- Create: `scripts/release-assets.test.mjs`
- Create: `deploy/release/package-assets.sh`

- [ ] **Step 1: Write the failing archive contract tests**

Create `scripts/release-assets.test.mjs`. The fixture must use fake files so this test remains fast and does not invoke npm or Go:

```javascript
import assert from "node:assert/strict";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

const root = resolve(import.meta.dirname, "..");
const packager = resolve(root, "deploy/release/package-assets.sh");

function write(path, content, mode = 0o644) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content, "utf8");
  chmodSync(path, mode);
}

function run(args, cwd = root) {
  return spawnSync("bash", [packager, ...args], { cwd, encoding: "utf8" });
}

function listArchive(path) {
  const result = spawnSync("tar", ["-tzf", path], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim().split("\n").map((entry) => entry.replace(/^\.\//, "")).filter(Boolean).sort();
}

function fixture(t) {
  const base = mkdtempSync(resolve(tmpdir(), "dayorder-release-assets-"));
  t.after(() => rmSync(base, { recursive: true, force: true }));
  const web = resolve(base, "web");
  const backend = resolve(base, "backend");
  const assets = resolve(base, "assets");
  write(resolve(web, "index.html"), "<main>dayorder</main>\n");
  write(resolve(web, "assets/app.js"), "console.log('dayorder')\n");
  for (const name of ["dayorder-api", "dayorder-worker", "dayorder-migrate"]) {
    write(resolve(backend, `bin/${name}`), "#!/usr/bin/env bash\nexit 0\n", 0o755);
  }
  for (const name of ["runtime-env.sh", "start-api.sh", "start-worker.sh", "migrate.sh"]) {
    write(resolve(backend, `scripts/${name}`), "#!/usr/bin/env bash\nexit 0\n", 0o755);
  }
  for (const name of ["api.env.example", "worker.env.example", "migrate.env.example"]) {
    write(resolve(backend, `config/${name}`), `${name}=fixture\n`);
  }
  return { base, web, backend, assets };
}

test("packager emits the exact Web, Server, and Worker archive contracts", (t) => {
  const f = fixture(t);
  for (const command of [["web", f.web, f.assets], ["backend", "amd64", f.backend, f.assets]]) {
    const result = run(command);
    assert.equal(result.status, 0, result.stderr);
  }
  assert.deepEqual(listArchive(resolve(f.assets, "dayorder-web.tar.gz")), ["assets/", "assets/app.js", "index.html"]);
  assert.deepEqual(listArchive(resolve(f.assets, "dayorder-server-linux-amd64.tar.gz")), [
    "bin/", "bin/dayorder-api", "bin/dayorder-migrate", "config/", "config/api.env.example",
    "config/migrate.env.example", "scripts/", "scripts/migrate.sh", "scripts/runtime-env.sh", "scripts/start-api.sh",
  ]);
  assert.deepEqual(listArchive(resolve(f.assets, "dayorder-worker-linux-amd64.tar.gz")), [
    "bin/", "bin/dayorder-worker", "config/", "config/worker.env.example", "scripts/",
    "scripts/runtime-env.sh", "scripts/start-worker.sh",
  ]);
  const extracted = resolve(f.base, "extracted-server");
  mkdirSync(extracted);
  const unpack = spawnSync("tar", ["-xzf", resolve(f.assets, "dayorder-server-linux-amd64.tar.gz"), "-C", extracted], { encoding: "utf8" });
  assert.equal(unpack.status, 0, unpack.stderr);
  assert.equal(statSync(resolve(extracted, "bin/dayorder-api")).mode & 0o777, 0o755);
  assert.equal(statSync(resolve(extracted, "scripts/start-api.sh")).mode & 0o777, 0o755);
  assert.equal(statSync(resolve(extracted, "config/api.env.example")).mode & 0o777, 0o644);
});

test("packager rejects missing inputs and unsupported architectures", (t) => {
  const f = fixture(t);
  const missingIndex = run(["web", resolve(f.base, "missing"), f.assets]);
  assert.notEqual(missingIndex.status, 0);
  assert.match(missingIndex.stderr, /index\.html/);
  const unsupported = run(["backend", "386", f.backend, f.assets]);
  assert.notEqual(unsupported.status, 0);
  assert.match(unsupported.stderr, /amd64 or arm64/);
});

test("metadata names every asset and SHA256SUMS verifies all seven inputs", (t) => {
  const f = fixture(t);
  for (const arch of ["amd64", "arm64"]) {
    assert.equal(run(["backend", arch, f.backend, f.assets]).status, 0);
  }
  assert.equal(run(["web", f.web, f.assets]).status, 0);
  const deployScript = resolve(f.base, "dayorder-deploy.sh");
  write(deployScript, "#!/usr/bin/env bash\nexit 0\n", 0o755);
  const metadata = run([
    "metadata", "v1.2.3", "0123456789abcdef0123456789abcdef01234567", deployScript, f.assets,
  ]);
  assert.equal(metadata.status, 0, metadata.stderr);

  assert.deepEqual(JSON.parse(readFileSync(resolve(f.assets, "release-manifest.json"), "utf8")), {
    schemaVersion: 1,
    version: "v1.2.3",
    revision: "0123456789abcdef0123456789abcdef01234567",
    deployScriptVersion: 1,
    assets: {
      web: "dayorder-web.tar.gz",
      server: { amd64: "dayorder-server-linux-amd64.tar.gz", arm64: "dayorder-server-linux-arm64.tar.gz" },
      worker: { amd64: "dayorder-worker-linux-amd64.tar.gz", arm64: "dayorder-worker-linux-arm64.tar.gz" },
    },
  });
  const verify = spawnSync("sha256sum", ["-c", "SHA256SUMS"], { cwd: f.assets, encoding: "utf8" });
  assert.equal(verify.status, 0, verify.stderr);
  assert.equal(verify.stdout.trim().split("\n").length, 7);
  assert.doesNotMatch(readFileSync(resolve(f.assets, "SHA256SUMS"), "utf8"), /SHA256SUMS/);
});
```

- [ ] **Step 2: Run the archive tests and verify RED**

Run:

```bash
node --test scripts/release-assets.test.mjs
```

Expected: FAIL because `deploy/release/package-assets.sh` does not exist.

- [ ] **Step 3: Implement the deterministic packager**

Create `deploy/release/package-assets.sh` with these public commands and no others:

```bash
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
  make_archive "$server" "$output_dir/dayorder-server-linux-$arch.tar.gz"
  make_archive "$worker" "$output_dir/dayorder-worker-linux-$arch.tar.gz"
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
```

- [ ] **Step 4: Verify GREEN and shell syntax**

Run:

```bash
chmod 0755 deploy/release/package-assets.sh
bash -n deploy/release/package-assets.sh
node --test scripts/release-assets.test.mjs
```

Expected: Bash exits 0; all three Node subtests pass.

- [ ] **Step 5: Commit the archive contract**

```bash
git add deploy/release/package-assets.sh scripts/release-assets.test.mjs
git commit -m "feat(release): package component assets"
```

### Task 2: Real source-build orchestration

**Files:**
- Create: `deploy/release/build-release.sh`
- Modify: `scripts/release-assets.test.mjs`
- Modify: `package.json`

- [ ] **Step 1: Add a failing orchestration contract test**

Append this test to `scripts/release-assets.test.mjs`:

```javascript
test("release builder exposes isolated CI targets and one complete local build", () => {
  const builder = readFileSync(resolve(root, "deploy/release/build-release.sh"), "utf8");
  const packageJson = JSON.parse(readFileSync(resolve(root, "package.json"), "utf8"));
  assert.match(builder, /build-web\.sh/);
  assert.match(builder, /build-backend\.sh/);
  assert.match(builder, /web\|backend\|finalize\|all/);
  assert.equal(packageJson.scripts["build:release:assets"], "bash deploy/release/build-release.sh all");
  assert.equal(packageJson.scripts["test:release"], "node --test scripts/release-*.test.mjs");
});
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
node --test scripts/release-assets.test.mjs
```

Expected: FAIL because `deploy/release/build-release.sh` is missing and the package commands are undefined.

- [ ] **Step 3: Implement the orchestration script**

Create `deploy/release/build-release.sh`:

```bash
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
```

- [ ] **Step 4: Add package commands**

Add these entries to the root `package.json` `scripts` object without changing the existing `build:release:web` or `build:release:backend` commands:

```json
"test:release": "node --test scripts/release-*.test.mjs",
"build:release:assets": "bash deploy/release/build-release.sh all"
```

- [ ] **Step 5: Run focused verification**

```bash
chmod 0755 deploy/release/build-release.sh
bash -n deploy/release/build-release.sh
node --test scripts/release-assets.test.mjs
npm run test:deploy:bare
```

Expected: all commands exit 0. Do not run the full real build until Task 7.

- [ ] **Step 6: Commit the build entry points**

```bash
git add deploy/release/build-release.sh package.json scripts/release-assets.test.mjs
git commit -m "feat(release): orchestrate release builds"
```

### Task 3: Deployer input, downloads, validation, and Web installation

**Files:**
- Create: `scripts/release-deploy.test.mjs`
- Create: `deploy/release/dayorder-deploy.sh`

- [ ] **Step 1: Create the hermetic command fixture**

Create `scripts/release-deploy.test.mjs` with this complete fixture before adding the test cases in Step 2:

```javascript
import assert from "node:assert/strict";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

const root = resolve(import.meta.dirname, "..");
const deployer = resolve(root, "deploy/release/dayorder-deploy.sh");
const packager = resolve(root, "deploy/release/package-assets.sh");
const checksumNames = [
  "dayorder-web.tar.gz",
  "dayorder-server-linux-amd64.tar.gz",
  "dayorder-server-linux-arm64.tar.gz",
  "dayorder-worker-linux-amd64.tar.gz",
  "dayorder-worker-linux-arm64.tar.gz",
  "dayorder-deploy.sh",
  "release-manifest.json",
];

function write(path, content, mode = 0o644) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content, "utf8");
  chmodSync(path, mode);
}

function writeExecutable(path, content) {
  write(path, content, 0o755);
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { encoding: "utf8", ...options });
  assert.equal(result.status, 0, result.stderr);
  return result;
}

function makeCommands(directory) {
  writeExecutable(resolve(directory, "curl"), `#!/usr/bin/env bash
set -Eeuo pipefail
output=""
url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output|-o) output="$2"; shift 2 ;;
    --proto|--max-time) shift 2 ;;
    --*) shift ;;
    *) url="$1"; shift ;;
  esac
done
if [[ "$url" == */health/ready ]]; then
  [[ "\${DAYORDER_TEST_HEALTH_FAIL:-0}" != 1 ]]
  exit
fi
name="\${url##*/}"
if [[ "$url" == */releases/latest/download/* ]]; then
  source="$DAYORDER_TEST_RELEASES/$DAYORDER_TEST_LATEST/$name"
else
  tag="\${url#*/releases/download/}"
  tag="\${tag%%/*}"
  source="$DAYORDER_TEST_RELEASES/$tag/$name"
fi
cp -- "$source" "$output"
`);
  writeExecutable(resolve(directory, "uname"), `#!/usr/bin/env bash
printf '%s\\n' "\${DAYORDER_TEST_UNAME:-x86_64}"
`);
  writeExecutable(resolve(directory, "loginctl"), `#!/usr/bin/env bash
printf 'loginctl %s\\n' "$*" >> "$DAYORDER_TEST_LOG"
if [[ "$*" == *show-user* ]]; then printf '%s\\n' "\${DAYORDER_TEST_LINGER:-yes}"; fi
`);
  writeExecutable(resolve(directory, "systemctl"), `#!/usr/bin/env bash
printf 'systemctl %s\\n' "$*" >> "$DAYORDER_TEST_LOG"
if [[ "$*" == *is-active*dayorder-worker.service* && "\${DAYORDER_TEST_WORKER_FAIL:-0}" == 1 ]]; then exit 1; fi
exit 0
`);
  writeExecutable(resolve(directory, "sleep"), "#!/usr/bin/env bash\nexit 0\n");
}

function fixture(t) {
  const base = mkdtempSync(resolve(tmpdir(), "dayorder-release-deploy-"));
  t.after(() => rmSync(base, { recursive: true, force: true }));
  const value = {
    base,
    releases: resolve(base, "releases"),
    runDirectory: resolve(base, "run"),
    commands: resolve(base, "commands"),
    home: resolve(base, "home"),
    log: resolve(base, "commands.log"),
    latest: "v1.2.3",
  };
  for (const directory of [value.releases, value.runDirectory, value.commands, value.home]) {
    mkdirSync(directory, { recursive: true });
  }
  writeFileSync(value.log, "", "utf8");
  makeCommands(value.commands);
  return value;
}

function runDeploy(fixture, args, extraEnvironment = {}) {
  return spawnSync("bash", [deployer, ...args], {
    cwd: fixture.runDirectory,
    encoding: "utf8",
    env: {
      ...process.env,
      PATH: `${fixture.commands}:${process.env.PATH}`,
      DAYORDER_TEST_RELEASES: fixture.releases,
      DAYORDER_TEST_LATEST: fixture.latest,
      DAYORDER_TEST_LOG: fixture.log,
      HOME: fixture.home,
      USER: "dayorder-test",
      XDG_CONFIG_HOME: resolve(fixture.home, ".config"),
      ...extraEnvironment,
    },
  });
}

function refreshChecksums(releaseDirectory) {
  const result = spawnSync("sha256sum", checksumNames, { cwd: releaseDirectory, encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  writeFileSync(resolve(releaseDirectory, "SHA256SUMS"), result.stdout, "utf8");
}

function makeAssetRelease(f, tag) {
  const source = resolve(f.base, "source", tag);
  const web = resolve(source, "web");
  const backend = resolve(source, "backend");
  const release = resolve(f.releases, tag);
  rmSync(source, { recursive: true, force: true });
  rmSync(release, { recursive: true, force: true });
  write(resolve(web, "index.html"), `<main>${tag}</main>\n`);
  write(resolve(web, "assets/app.js"), `console.log(${JSON.stringify(tag)})\n`);
  for (const name of ["dayorder-api", "dayorder-worker", "dayorder-migrate"]) {
    writeExecutable(resolve(backend, "bin", name), "#!/usr/bin/env bash\nexit 0\n");
  }
  writeExecutable(resolve(backend, "scripts/runtime-env.sh"), "#!/usr/bin/env bash\nexit 0\n");
  writeExecutable(resolve(backend, "scripts/start-api.sh"), "#!/usr/bin/env bash\nexit 0\n");
  writeExecutable(resolve(backend, "scripts/start-worker.sh"), "#!/usr/bin/env bash\nexit 0\n");
  writeExecutable(resolve(backend, "scripts/migrate.sh"), `#!/usr/bin/env bash
printf 'migrate %s %s\\n' "$1" "$2" >> "$DAYORDER_TEST_LOG"
[[ "\${DAYORDER_TEST_MIGRATE_FAIL:-0}" != 1 ]]
`);
  write(resolve(backend, "config/api.env.example"), "DATABASE_URL_FILE=/etc/dayorder/secrets/api_database_url\n");
  write(resolve(backend, "config/migrate.env.example"), "MIGRATION_DATABASE_URL_FILE=/etc/dayorder/secrets/migration_database_url\n");
  write(resolve(backend, "config/worker.env.example"), "WORKER_DATABASE_URL_FILE=/etc/dayorder/secrets/worker_database_url\n");
  run("bash", [packager, "web", web, release]);
  for (const arch of ["amd64", "arm64"]) run("bash", [packager, "backend", arch, backend, release]);
  run("bash", [
    packager,
    "metadata",
    tag,
    "0123456789abcdef0123456789abcdef01234567",
    deployer,
    release,
  ]);
  f.latest = tag;
  return release;
}

function configuredFixture(t, tag) {
  const f = fixture(t);
  makeAssetRelease(f, tag);
  const first = runDeploy(f, ["all"]);
  assert.notEqual(first.status, 0);
  writeFileSync(f.log, "", "utf8");
  return f;
}
```

- [ ] **Step 2: Add failing CLI, checksum, archive-safety, and Web tests**

Add these tests using the helpers above:

```javascript
test("deployer rejects invalid commands, versions, roots, and architectures", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  assert.match(runDeploy(f, ["api"]).stderr, /web\|server\|worker\|all/);
  assert.match(runDeploy(f, ["web", "--version", "latest"]).stderr, /vX\.Y\.Z/);
  assert.match(runDeploy(f, ["web", "--root", f.home]).stderr, /home directory/);
  const unsupported = runDeploy(f, ["server"], { DAYORDER_TEST_UNAME: "riscv64" });
  assert.match(unsupported.stderr, /unsupported architecture/);
});

test("Web deploy defaults to latest, uses the invocation directory, and is idempotent", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  const first = runDeploy(f, ["web"]);
  assert.equal(first.status, 0, first.stderr);
  assert.equal(realpathSync(resolve(f.runDirectory, "current-web")), resolve(f.runDirectory, "releases/v1.2.3/web"));
  assert.equal(readFileSync(resolve(f.runDirectory, "current-web/index.html"), "utf8"), "<main>v1.2.3</main>\n");
  const second = runDeploy(f, ["web"]);
  assert.equal(second.status, 0, second.stderr);
  assert.match(second.stdout, /already deployed/);
});

test("explicit version and root select an immutable version directory", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  makeAssetRelease(f, "v1.2.4");
  const target = resolve(f.base, "target");
  const result = runDeploy(f, ["web", "--version", "v1.2.3", "--root", target]);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(realpathSync(resolve(target, "current-web")), resolve(target, "releases/v1.2.3/web"));
});

test("checksum mismatch and unsafe tar members fail before current-web changes", (t) => {
  const f = fixture(t);
  const release = makeAssetRelease(f, "v1.2.3");
  writeFileSync(resolve(release, "dayorder-web.tar.gz"), "tampered", "utf8");
  const checksumFailure = runDeploy(f, ["web"]);
  assert.notEqual(checksumFailure.status, 0);
  assert.match(checksumFailure.stderr, /SHA-256/);
  assert.equal(existsSync(resolve(f.runDirectory, "current-web")), false);

  makeAssetRelease(f, "v1.2.4");
  const unsafe = resolve(f.base, "unsafe");
  write(resolve(unsafe, "payload"), "unsafe\n");
  const archive = resolve(f.releases, "v1.2.4/dayorder-web.tar.gz");
  const tar = spawnSync("tar", ["-czf", archive, "--transform=s#payload#../escape#", "-C", unsafe, "payload"], { encoding: "utf8" });
  assert.equal(tar.status, 0, tar.stderr);
  refreshChecksums(resolve(f.releases, "v1.2.4"));
  const unsafeResult = runDeploy(f, ["web", "--version", "v1.2.4"]);
  assert.notEqual(unsafeResult.status, 0);
  assert.match(unsafeResult.stderr, /unsafe archive/);
  assert.equal(existsSync(resolve(f.runDirectory, "escape")), false);
});
```

- [ ] **Step 3: Run deploy tests and verify RED**

```bash
node --test scripts/release-deploy.test.mjs
```

Expected: FAIL because `deploy/release/dayorder-deploy.sh` does not exist.

- [ ] **Step 4: Implement CLI/root/dependency/lock handling**

Start `deploy/release/dayorder-deploy.sh` with:

```bash
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
```

- [ ] **Step 5: Implement Manifest and checksum validation**

Add functions that download only HTTPS Release URLs, parse the controlled Manifest, reject incompatible schema/script versions, pin latest to the resolved explicit version URL, and verify every selected file before use:

```bash
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
  count="$(awk -v name="$name" '$2 == name { count++ } END { print count + 0 }' "$work_dir/SHA256SUMS")"
  [[ "$count" == 1 ]] || die "SHA-256 entry must appear exactly once: $name"
  checksum="$(awk -v name="$name" '$2 == name { print $1 }' "$work_dir/SHA256SUMS")"
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
```

- [ ] **Step 6: Implement safe archive installation and atomic Web switching**

Add these complete `asset_name`, archive validation, installation, and link-switching function bodies:

```bash
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
      find "$directory" -type l -o -type b -o -type c -o -type p | grep -q . && die "Web archive contains unsafe nodes"
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
  local name="$1" target="$2" link="$root/current-$name" temporary="$root/.current-$name.$$"
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
```

At the bottom, prepare only requested components, then call `deploy_web` for `web`. `all` is wired in Task 4 after all preflights exist.

- [ ] **Step 7: Verify Web behavior is GREEN**

```bash
chmod 0755 deploy/release/dayorder-deploy.sh
bash -n deploy/release/dayorder-deploy.sh
node --test --test-name-pattern='deployer rejects|Web deploy|explicit version|checksum mismatch' scripts/release-deploy.test.mjs
```

Expected: all selected tests pass; no file named `escape` appears outside an extraction directory.

- [ ] **Step 8: Commit secure Web deployment**

```bash
git add deploy/release/dayorder-deploy.sh scripts/release-deploy.test.mjs
git commit -m "feat(deploy): install verified Web releases"
```

### Task 4: Configuration, migration, systemd services, and rollback

**Files:**
- Modify: `scripts/release-deploy.test.mjs`
- Modify: `deploy/release/dayorder-deploy.sh`

- [ ] **Step 1: Add failing first-configuration and systemd preflight tests**

Add tests that assert:

```javascript
test("first all deployment creates persistent templates and stops before migration or switching", (t) => {
  const f = fixture(t); makeAssetRelease(f, "v1.2.3");
  const result = runDeploy(f, ["all"]);
  assert.notEqual(result.status, 0);
  for (const name of ["api.env", "migrate.env", "worker.env"]) {
    const path = resolve(f.runDirectory, "dayorder-config", name);
    assert.equal(existsSync(path), true);
    assert.equal(statSync(path).mode & 0o777, 0o600);
    assert.match(readFileSync(path, "utf8"), new RegExp(resolve(f.runDirectory, "dayorder-config/secrets").replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  assert.equal(existsSync(resolve(f.runDirectory, "current-server")), false);
  assert.equal(existsSync(resolve(f.runDirectory, "current-worker")), false);
  assert.doesNotMatch(readFileSync(f.log, "utf8"), /migrate|systemctl/);
});

test("missing linger stops Server before migration and prints the enable command", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const result = runDeploy(f, ["server"], { DAYORDER_TEST_LINGER: "no" });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /sudo loginctl enable-linger/);
  assert.equal(existsSync(resolve(f.runDirectory, "current-server")), false);
});
```

`configuredFixture` performs the expected first run, leaves the generated templates in place, and clears the log; do not bypass the production first-run behavior by writing unrelated fixture configs.

- [ ] **Step 2: Add failing Server and Worker lifecycle tests**

Add tests for these exact observable sequences:

```javascript
test("Server migrates up and checks before activation, then passes readiness", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const result = runDeploy(f, ["server"]);
  assert.equal(result.status, 0, result.stderr);
  const log = readFileSync(f.log, "utf8");
  assert.ok(log.indexOf("migrate up") < log.indexOf("migrate check"));
  assert.ok(log.indexOf("migrate check") < log.indexOf("systemctl --user daemon-reload"));
  assert.equal(realpathSync(resolve(f.runDirectory, "current-server")), resolve(f.runDirectory, "releases/v1.2.3/server"));
  const unit = readFileSync(resolve(f.home, ".config/systemd/user/dayorder-api.service"), "utf8");
  assert.match(unit, /Restart=on-failure/);
  assert.match(unit, /TimeoutStopSec=30/);
  assert.match(unit, new RegExp(resolve(f.runDirectory, "current-server/scripts/start-api.sh").replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
});

test("Worker is a separate enabled user service with a 60 second stop timeout", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const result = runDeploy(f, ["worker"]);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(realpathSync(resolve(f.runDirectory, "current-worker")), resolve(f.runDirectory, "releases/v1.2.3/worker"));
  const unit = readFileSync(resolve(f.home, ".config/systemd/user/dayorder-worker.service"), "utf8");
  assert.match(unit, /TimeoutStopSec=60/);
  assert.match(unit, /worker\.env/);
});
```

- [ ] **Step 3: Add failing failure/rollback and all-order tests**

Append these four cases:

```javascript
test("migration failure leaves the Server link unchanged", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const result = runDeploy(f, ["server"], { DAYORDER_TEST_MIGRATE_FAIL: "1" });
  assert.notEqual(result.status, 0);
  assert.equal(existsSync(resolve(f.runDirectory, "current-server")), false);
  const log = readFileSync(f.log, "utf8");
  assert.match(log, /migrate up/);
  assert.doesNotMatch(log, /systemctl/);
});

test("health failure restores the previous Server link and restarts the old API", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const initial = runDeploy(f, ["server", "--version", "v1.2.3"]);
  assert.equal(initial.status, 0, initial.stderr);
  makeAssetRelease(f, "v1.2.4");
  writeFileSync(f.log, "", "utf8");
  const failed = runDeploy(f, ["server", "--version", "v1.2.4"], { DAYORDER_TEST_HEALTH_FAIL: "1" });
  assert.notEqual(failed.status, 0);
  assert.equal(realpathSync(resolve(f.runDirectory, "current-server")), resolve(f.runDirectory, "releases/v1.2.3/server"));
  assert.match(readFileSync(f.log, "utf8"), /systemctl --user restart dayorder-api\.service/);
});

test("Worker failure during all restores Server and Worker while Web stays old", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const initial = runDeploy(f, ["all", "--version", "v1.2.3"]);
  assert.equal(initial.status, 0, initial.stderr);
  makeAssetRelease(f, "v1.2.4");
  writeFileSync(f.log, "", "utf8");
  const failed = runDeploy(f, ["all", "--version", "v1.2.4"], { DAYORDER_TEST_WORKER_FAIL: "1" });
  assert.notEqual(failed.status, 0);
  for (const name of ["server", "worker", "web"]) {
    assert.equal(realpathSync(resolve(f.runDirectory, `current-${name}`)), resolve(f.runDirectory, `releases/v1.2.3/${name}`));
  }
  const log = readFileSync(f.log, "utf8");
  assert.match(log, /systemctl --user restart dayorder-api\.service/);
  assert.match(log, /systemctl --user restart dayorder-worker\.service/);
});

test("Successful all activates Server then Worker then Web and a repeat is a no-op", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const first = runDeploy(f, ["all"]);
  assert.equal(first.status, 0, first.stderr);
  const log = readFileSync(f.log, "utf8");
  assert.ok(log.indexOf("migrate check") < log.indexOf("dayorder-worker.service"));
  assert.ok(first.stdout.indexOf("Server v1.2.3 deployed") < first.stdout.indexOf("Worker v1.2.3 deployed"));
  assert.ok(first.stdout.indexOf("Worker v1.2.3 deployed") < first.stdout.indexOf("Web v1.2.3 deployed"));
  for (const name of ["server", "worker", "web"]) {
    assert.equal(realpathSync(resolve(f.runDirectory, `current-${name}`)), resolve(f.runDirectory, `releases/v1.2.3/${name}`));
  }
  const logBeforeRepeat = readFileSync(f.log, "utf8");
  const repeat = runDeploy(f, ["all"]);
  assert.equal(repeat.status, 0, repeat.stderr);
  assert.equal((repeat.stdout.match(/already deployed/g) ?? []).length, 3);
  const repeatLog = readFileSync(f.log, "utf8").slice(logBeforeRepeat.length);
  assert.doesNotMatch(repeatLog, /migrate|daemon-reload| enable | restart | start /);
});
```

- [ ] **Step 4: Run the new lifecycle tests and verify RED**

```bash
node --test --test-name-pattern='first all|missing linger|Server migrates|Worker is|migration failure|health failure|Worker failure|Successful all' scripts/release-deploy.test.mjs
```

Expected: FAIL because configuration and service lifecycle functions do not exist.

- [ ] **Step 5: Implement persistent configuration and systemd preflight**

Add these functions to `dayorder-deploy.sh`:

```bash
config_dir="$root/dayorder-config"
config_created=0

ensure_config() {
  local name="$1" component_name="$2" template="$root/releases/$version/$component_name/config/$name.env.example"
  local destination="$config_dir/$name.env" temporary="$config_dir/.$name.env.$$"
  [[ -f "$destination" ]] && return
  mkdir -p -- "$config_dir/secrets"; chmod 0700 "$config_dir" "$config_dir/secrets"
  sed "s#/etc/dayorder/secrets#$config_dir/secrets#g" "$template" > "$temporary"
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

preflight_systemd() {
  require_command systemctl; require_command loginctl
  systemctl --user show-environment >/dev/null || die "systemd --user manager is unavailable"
  linger="$(loginctl show-user "${USER:?USER is required}" --property=Linger --value)"
  if [[ "$linger" != yes ]]; then
    die "linger is disabled; run: sudo loginctl enable-linger \"$USER\""
  fi
}

write_unit() {
  local service="$1" current="$2" script="$3" config="$4" timeout="$5"
  local unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user" unit="$unit_dir/$service.service"
  mkdir -p -- "$unit_dir"
  {
    printf '[Unit]\nDescription=DayOrder %s\nAfter=network-online.target\nWants=network-online.target\n\n' "$service"
    printf '[Service]\nType=simple\nWorkingDirectory=%s\n' "$(systemd_quote "$current")"
    printf 'ExecStart=%s %s\n' "$(systemd_quote "$current/scripts/$script")" "$(systemd_quote "$config")"
    printf 'Restart=on-failure\nRestartSec=5\nTimeoutStopSec=%s\n\n[Install]\nWantedBy=default.target\n' "$timeout"
  } > "$unit"
}
```

Add this preparation block before component activation. It deliberately installs and verifies every selected archive before it checks all selected configurations:

```bash
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
    require_config api; require_config migrate; preflight_systemd
    ;;
  worker)
    require_config worker; preflight_systemd
    ;;
  all)
    require_config api; require_config migrate; require_config worker; preflight_systemd
    ;;
esac
```

- [ ] **Step 6: Implement lifecycle, readiness, and rollback functions**

Add exact state variables and behavior:

```bash
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
  local name="$1" old="$2" service="${3:-}"
  if [[ -n "$old" ]]; then switch_link "$name" "$old"; else rm -f -- "$root/current-$name"; fi
  if [[ -n "$service" ]]; then
    if [[ -n "$old" ]]; then systemctl --user restart "$service.service"; else systemctl --user stop "$service.service" || true; fi
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
    restore_link server "$server_old" dayorder-api; server_changed=0; return 1
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
    restore_link worker "$worker_old" dayorder-worker; worker_changed=0; return 1
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
```

Replace the temporary Task 3 dispatch with these explicit error branches:

```bash
case "$component" in
  server) deploy_server || die "Server deployment failed; the previous application link was restored" ;;
  worker) deploy_worker || die "Worker deployment failed; the previous application link was restored" ;;
  web) deploy_web ;;
  all)
    if ! deploy_server; then die "Server deployment failed before later components were activated"; fi
    if ! deploy_worker; then
      (( server_changed == 0 )) || restore_link server "$server_old" dayorder-api
      die "Worker deployment failed; activated application links were restored"
    fi
    if ! deploy_web; then
      (( worker_changed == 0 )) || restore_link worker "$worker_old" dayorder-worker
      (( server_changed == 0 )) || restore_link server "$server_old" dayorder-api
      die "Web deployment failed; activated application links were restored"
    fi
    ;;
esac
```

Before this case, validate every selected config and run `preflight_systemd` once for Server/Worker. Document in an adjacent comment that schema migration is deliberately not rolled back; compatible releases must use expand/contract migrations.

- [ ] **Step 7: Run the complete deployer suite**

```bash
bash -n deploy/release/dayorder-deploy.sh
node --test scripts/release-deploy.test.mjs
```

Expected: all tests pass. Inspect the failure tests' temporary cleanup through their assertions; no test may use the host's real systemd manager.

- [ ] **Step 8: Commit service deployment**

```bash
git add deploy/release/dayorder-deploy.sh scripts/release-deploy.test.mjs
git commit -m "feat(deploy): manage API and Worker releases"
```

### Task 5: Stable-tag GitHub Release workflow

**Files:**
- Create: `scripts/release-workflow.test.mjs`
- Create: `.github/workflows/release.yml`

- [ ] **Step 1: Write the failing static workflow contract**

Create `scripts/release-workflow.test.mjs`:

```javascript
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import test from "node:test";

const root = resolve(import.meta.dirname, "..");

test("Release workflow gates stable tags and publishes a complete Draft atomically", () => {
  const workflow = readFileSync(resolve(root, ".github/workflows/release.yml"), "utf8");
  assert.match(workflow, /tags:\s*\["v\*"\]/);
  assert.match(workflow, /\^v\[0-9\]\+\\\.\[0-9\]\+\\\.\[0-9\]\+\$/);
  assert.match(workflow, /fetch-depth:\s*0/);
  assert.match(workflow, /merge-base --is-ancestor[\s\S]*origin\/main/);
  assert.match(workflow, /permissions:\s*\n\s*contents:\s*read/);
  assert.match(workflow, /release:[\s\S]*permissions:\s*\n\s*contents:\s*write/);
  assert.match(workflow, /cancel-in-progress:\s*false/);
  assert.match(workflow, /matrix:[\s\S]*arch:\s*\[amd64, arm64\]/);
  assert.match(workflow, /gh release create[\s\S]*--draft/);
  assert.match(workflow, /gh release upload[\s\S]*--clobber/);
  assert.ok(workflow.indexOf("gh release upload") < workflow.indexOf("--draft=false"));
  for (const asset of [
    "dayorder-web.tar.gz", "dayorder-server-linux-amd64.tar.gz", "dayorder-server-linux-arm64.tar.gz",
    "dayorder-worker-linux-amd64.tar.gz", "dayorder-worker-linux-arm64.tar.gz", "dayorder-deploy.sh",
    "release-manifest.json", "SHA256SUMS",
  ]) assert.match(workflow, new RegExp(asset.replaceAll(".", "\\.")));
  for (const reference of workflow.matchAll(/uses:\s*([^\s#]+)/g)) {
    assert.match(reference[1], /@[0-9a-f]{40}$/, `Action is not pinned: ${reference[1]}`);
  }
});
```

- [ ] **Step 2: Run the workflow test and verify RED**

```bash
node --test scripts/release-workflow.test.mjs
```

Expected: FAIL because `.github/workflows/release.yml` does not exist.

- [ ] **Step 3: Implement the complete Draft-first workflow**

Create `.github/workflows/release.yml`:

```yaml
name: Release

on:
  push:
    tags: ["v*"]

permissions:
  contents: read

concurrency:
  group: release-${{ github.ref }}
  cancel-in-progress: false

jobs:
  validate:
    runs-on: ubuntu-latest
    outputs:
      commit: ${{ steps.tag.outputs.commit }}
    steps:
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
        with:
          fetch-depth: 0
      - uses: actions/setup-node@49933ea5288caeca8642d1e84afbd3f7d6820020 # v4
        with:
          node-version: "24.7.0"
          cache: npm
      - uses: actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5
        with:
          go-version: "1.25.0"
          cache-dependency-path: apps/api/go.sum
      - name: Validate stable tag and main ancestry
        id: tag
        shell: bash
        run: |
          set -Eeuo pipefail
          tag="$GITHUB_REF_NAME"
          [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
            printf 'invalid stable tag: %s\n' "$tag" >&2
            exit 1
          }
          git fetch --no-tags origin main:refs/remotes/origin/main
          commit="$(git rev-parse "$tag^{}")"
          git merge-base --is-ancestor "$commit" origin/main || {
            printf 'tag commit is not on origin/main\n' >&2
            exit 1
          }
          printf 'commit=%s\n' "$commit" >> "$GITHUB_OUTPUT"
      - run: npm ci
      - run: npm run typecheck
      - run: npm run test:web
      - run: npm run test:api
      - run: go vet ./apps/api/...
      - run: npm run test:architecture
      - run: npm run test:deploy
      - run: npm run test:deploy:bare
      - run: npm run test:release

  web:
    runs-on: ubuntu-latest
    needs: validate
    steps:
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
      - uses: actions/setup-node@49933ea5288caeca8642d1e84afbd3f7d6820020 # v4
        with:
          node-version: "24.7.0"
          cache: npm
      - name: Build Web Release asset
        shell: bash
        run: DAYORDER_RELEASE_OUTPUT="$PWD/release/github" bash deploy/release/build-release.sh web
      - uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4
        with:
          name: release-web
          path: release/github/dayorder-web.tar.gz
          if-no-files-found: error
          retention-days: 1

  backend:
    runs-on: ubuntu-latest
    needs: validate
    strategy:
      fail-fast: false
      matrix:
        arch: [amd64, arm64]
    steps:
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
      - uses: actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5
        with:
          go-version: "1.25.0"
          cache-dependency-path: apps/api/go.sum
      - name: Build backend Release assets
        shell: bash
        run: DAYORDER_RELEASE_OUTPUT="$PWD/release/github" bash deploy/release/build-release.sh backend "${{ matrix.arch }}"
      - uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4
        with:
          name: release-backend-${{ matrix.arch }}
          path: |
            release/github/dayorder-server-linux-${{ matrix.arch }}.tar.gz
            release/github/dayorder-worker-linux-${{ matrix.arch }}.tar.gz
          if-no-files-found: error
          retention-days: 1

  release:
    runs-on: ubuntu-latest
    needs: [validate, web, backend]
    permissions:
      contents: write
    env:
      GH_TOKEN: ${{ github.token }}
    steps:
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
      - uses: actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093 # v4
        with:
          pattern: release-*
          path: release/github
          merge-multiple: true
      - name: Generate Manifest and checksums
        shell: bash
        run: |
          DAYORDER_RELEASE_OUTPUT="$PWD/release/github" bash deploy/release/build-release.sh finalize \
            "$GITHUB_REF_NAME" "${{ needs.validate.outputs.commit }}"
      - name: Validate local asset set
        shell: bash
        run: |
          set -Eeuo pipefail
          expected=$'SHA256SUMS\ndayorder-deploy.sh\ndayorder-server-linux-amd64.tar.gz\ndayorder-server-linux-arm64.tar.gz\ndayorder-web.tar.gz\ndayorder-worker-linux-amd64.tar.gz\ndayorder-worker-linux-arm64.tar.gz\nrelease-manifest.json'
          actual="$(find release/github -maxdepth 1 -type f -printf '%f\n' | sort)"
          [[ "$actual" == "$expected" ]] || {
            printf 'local release asset set is incomplete\n%s\n' "$actual" >&2
            exit 1
          }
          (cd release/github && sha256sum -c SHA256SUMS)
      - name: Publish verified GitHub Release
        shell: bash
        run: |
          set -Eeuo pipefail
          tag="$GITHUB_REF_NAME"
          draft_state="$RUNNER_TEMP/dayorder-release-draft"
          if gh release view "$tag" --json isDraft --jq .isDraft > "$draft_state" 2>/dev/null; then
            [[ "$(<"$draft_state")" == true ]] || {
              printf 'release %s is already public; refusing to replace it\n' "$tag" >&2
              exit 1
            }
          else
            gh release create "$tag" --verify-tag --draft --generate-notes --title "$tag"
          fi
          gh release upload "$tag" release/github/* --clobber
          expected=$'SHA256SUMS\ndayorder-deploy.sh\ndayorder-server-linux-amd64.tar.gz\ndayorder-server-linux-arm64.tar.gz\ndayorder-web.tar.gz\ndayorder-worker-linux-amd64.tar.gz\ndayorder-worker-linux-arm64.tar.gz\nrelease-manifest.json'
          actual="$(gh release view "$tag" --json assets --jq '.assets[].name' | sort)"
          [[ "$actual" == "$expected" ]] || {
            printf 'release asset set is incomplete\n%s\n' "$actual" >&2
            exit 1
          }
          gh release edit "$tag" --draft=false
```

- [ ] **Step 4: Inspect the failure semantics in the completed workflow**

Run:

```bash
rg -n "gh release (create|upload|edit)|--draft|--draft=false|permissions:|cancel-in-progress" .github/workflows/release.yml
```

Expected: `create --draft` precedes upload, exact asset validation precedes `edit --draft=false`, only the `release` job grants `contents: write`, and no command deletes a failed Draft.

- [ ] **Step 5: Verify the workflow contract**

```bash
node --test scripts/release-workflow.test.mjs
npm run test:release
```

Expected: all release tests pass, and every `uses:` line is pinned to 40 hexadecimal characters.

- [ ] **Step 6: Commit the workflow**

```bash
git add .github/workflows/release.yml scripts/release-workflow.test.mjs
git commit -m "ci: publish GitHub Release assets"
```

### Task 6: Operator documentation and discoverability

**Files:**
- Modify: `scripts/bare-metal-deploy.test.mjs`
- Modify: `README.md`
- Modify: `docs/runbooks/separate-deployment.md`

- [ ] **Step 1: Make documentation requirements fail first**

Extend the existing documentation test with:

```javascript
for (const phrase of [
  "dayorder-deploy.sh all",
  "releases/latest/download/dayorder-deploy.sh",
  "dayorder-config",
  "current-web",
  "systemctl --user",
  "loginctl enable-linger",
  "journalctl --user",
  "--version v0.3.0",
  "数据库 migration 不会回退",
]) {
  assert.match(`${readme}\n${runbook}`, new RegExp(phrase.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
}
assert.match(runbook, /Web.*不.*Nginx|不.*启动.*Nginx/s);
assert.match(runbook, /Server.*Worker.*独立/s);
```

- [ ] **Step 2: Run the documentation test and verify RED**

```bash
npm run test:deploy:bare
```

Expected: FAIL on the new GitHub Release deployment phrases.

- [ ] **Step 3: Update the README deployment entry point**

Retain the existing manual build commands as a “本地构建/离线传输” subsection, but make this the primary quick start:

```bash
mkdir -p ~/a
cd ~/a
curl -fsSLO https://github.com/art-shier/be-better/releases/latest/download/dayorder-deploy.sh
chmod 0755 dayorder-deploy.sh
./dayorder-deploy.sh all
```

Explain that the first Server/Worker run creates `~/a/dayorder-config/{api.env,migrate.env,worker.env,secrets/}` and stops; the operator fills those files, runs `sudo loginctl enable-linger "$USER"` once if requested, then repeats `./dayorder-deploy.sh all`. State explicitly that Web only updates `~/a/current-web`, while API and Worker become two separate `systemd --user` services.

- [ ] **Step 4: Rewrite the runbook around first install and update operations**

Document all of these exact commands:

```bash
./dayorder-deploy.sh web
./dayorder-deploy.sh server
./dayorder-deploy.sh worker
./dayorder-deploy.sh all
./dayorder-deploy.sh all --version v0.3.0
./dayorder-deploy.sh server --root /srv/dayorder
systemctl --user status dayorder-api.service dayorder-worker.service
journalctl --user -u dayorder-api.service -f
journalctl --user -u dayorder-worker.service -f
```

Include the exact directory tree from the design. State that `--root` defaults to the command's `$PWD`, not the script location; existing config is never overwritten; history is not auto-deleted; Nginx/Caddy must point at `<root>/current-web`; Web deployment starts no static server; `all` orders Server migration/readiness, Worker activation, then Web; and application links are restored on activation failure. Include this warning verbatim: `数据库 migration 不会回退；指定旧版本只回退应用链接，因此版本必须使用 expand/contract migration 保持相邻版本兼容。`

- [ ] **Step 5: Verify documentation and all release tests**

```bash
npm run test:deploy:bare
npm run test:release
```

Expected: all tests pass.

- [ ] **Step 6: Commit documentation**

```bash
git add README.md docs/runbooks/separate-deployment.md scripts/bare-metal-deploy.test.mjs
git commit -m "docs: explain GitHub Release deployment"
```

### Task 7: Full build and local no-privilege acceptance

**Files:**
- Modify: `scripts/release-deploy.test.mjs`
- Modify only when verification exposes a defect: files from Tasks 1–6.

- [ ] **Step 1: Run all fast/static validation from a clean command invocation**

```bash
npm run typecheck
npm run test:web
npm run test:api
go vet ./apps/api/...
npm run test:architecture
npm run test:deploy
npm run test:deploy:bare
npm run test:release
```

Expected: every command exits 0. If a command fails, use `superpowers:systematic-debugging`, add or refine the reproducing test, then make the smallest implementation correction before continuing.

- [ ] **Step 2: Build all real release assets**

```bash
npm run build:release:assets
```

Expected: `release/github/` contains exactly the eight public assets. Both backend architectures compile with `CGO_ENABLED=0 GOOS=linux`; `SHA256SUMS` verifies.

- [ ] **Step 3: Inspect actual archive contracts and executable formats**

```bash
tar -tzf release/github/dayorder-web.tar.gz
tar -tzf release/github/dayorder-server-linux-amd64.tar.gz
tar -tzf release/github/dayorder-server-linux-arm64.tar.gz
tar -tzf release/github/dayorder-worker-linux-amd64.tar.gz
tar -tzf release/github/dayorder-worker-linux-arm64.tar.gz
(cd release/github && sha256sum -c SHA256SUMS)
acceptance_dir="$(mktemp -d)"
trap 'rm -rf -- "$acceptance_dir"' EXIT
mkdir -p "$acceptance_dir/amd64" "$acceptance_dir/arm64"
tar -xzf release/github/dayorder-server-linux-amd64.tar.gz -C "$acceptance_dir/amd64"
tar -xzf release/github/dayorder-server-linux-arm64.tar.gz -C "$acceptance_dir/arm64"
file "$acceptance_dir/amd64/bin/dayorder-api" "$acceptance_dir/arm64/bin/dayorder-api"
```

Expected: archive members match Task 1, all seven checksum lines report `OK`, neither archive includes another component's binary or configuration, and `file` identifies x86-64 and ARM aarch64 statically linked Linux executables.

- [ ] **Step 4: Run an unprivileged deployment rehearsal from the generated assets**

Append this opt-in test to `scripts/release-deploy.test.mjs`. It exercises real archives and checks the safe first-run boundary without connecting the real Migrator binary to an operator database:

```javascript
test("real generated assets install without privilege and stop at first configuration", {
  skip: !process.env.DAYORDER_TEST_REAL_ASSETS,
}, (t) => {
  const source = resolve(process.env.DAYORDER_TEST_REAL_ASSETS);
  const manifest = JSON.parse(readFileSync(resolve(source, "release-manifest.json"), "utf8"));
  const f = fixture(t);
  const destination = resolve(f.releases, manifest.version);
  mkdirSync(destination, { recursive: true });
  for (const name of [...checksumNames, "SHA256SUMS"]) {
    copyFileSync(resolve(source, name), resolve(destination, name));
  }
  f.latest = manifest.version;

  const firstAll = runDeploy(f, ["all"]);
  assert.notEqual(firstAll.status, 0);
  assert.match(firstAll.stderr, /configuration templates were created/);
  for (const name of ["api.env", "migrate.env", "worker.env"]) {
    assert.equal(existsSync(resolve(f.runDirectory, "dayorder-config", name)), true);
  }
  assert.equal(existsSync(resolve(f.runDirectory, "current-server")), false);
  assert.equal(existsSync(resolve(f.runDirectory, "current-worker")), false);
  assert.equal(existsSync(resolve(f.runDirectory, "current-web")), false);
  assert.doesNotMatch(readFileSync(f.log, "utf8"), /migrate|systemctl/);

  const web = runDeploy(f, ["web"]);
  assert.equal(web.status, 0, web.stderr);
  assert.equal(realpathSync(resolve(f.runDirectory, "current-web")), resolve(f.runDirectory, `releases/${manifest.version}/web`));
  assert.equal(existsSync(resolve(f.runDirectory, "current-web/index.html")), true);
});
```

Run:

```bash
DAYORDER_TEST_REAL_ASSETS="$PWD/release/github" node --test --test-name-pattern='real generated assets' scripts/release-deploy.test.mjs
```

Expected: PASS. The test skips only when `DAYORDER_TEST_REAL_ASSETS` is absent; in this explicit command it executes. The fake-release lifecycle tests from Task 4 remain responsible for successful migration/service activation and rollback behavior without a live database.

- [ ] **Step 5: Check the final diff and repository state**

```bash
git diff --check
git status --short
git log --oneline -8
```

Expected: no whitespace errors; only an intentional verification correction may be uncommitted.

- [ ] **Step 6: Commit the real-asset acceptance**

Stage the opt-in acceptance test:

```bash
git add scripts/release-deploy.test.mjs
git commit -m "test(release): exercise generated assets"
```

## Final acceptance checklist

- [ ] A pushed `vX.Y.Z` tag whose commit is on `main` can publish; malformed or prerelease tags cannot.
- [ ] A Release is not public until all eight fixed-name assets are present.
- [ ] Web contains only static files; Server contains API, Migrator, scripts, and two configs; Worker contains only Worker, scripts, and its config.
- [ ] `amd64` and `arm64` backend artifacts are real Linux binaries.
- [ ] The deployer requires HTTPS GitHub downloads, compatible Manifest versions, exact checksums, and safe archive entries.
- [ ] Default root is invocation `$PWD`; config stays in `<root>/dayorder-config`; historical versions remain under `<root>/releases`.
- [ ] First missing config run never migrates, switches, or starts a service.
- [ ] API migration/check precede API activation; API readiness and Worker active state gate success.
- [ ] Web does not install or start Nginx/Caddy; API and Worker are independent user services.
- [ ] Re-running the deployer updates to latest; `--version` selects a stable Release; same-version components are skipped.
- [ ] Failed activation restores application links and services where a previous version exists; database migration is never reversed.
- [ ] README and runbook contain first-install, update, status, log, linger, and downgrade-safety instructions.
