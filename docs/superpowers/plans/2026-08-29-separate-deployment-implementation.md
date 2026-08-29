# DayOrder Separate Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Docker-independent Linux release flow that emits frontend static assets plus independently runnable API, Worker, and Migrator backend artifacts.

**Architecture:** Bash build scripts under `deploy/bare-metal/` produce replaceable `release/web` and `release/backend` directories through temporary staging directories. The backend release carries relocatable foreground runtime wrappers and three least-privilege environment templates; Node's built-in test runner exercises wrapper behavior without requiring a live database, while final acceptance performs real Web and Go builds.

**Tech Stack:** Bash 4+, Node.js 22+/`node:test`, npm workspaces, Vite 8, TypeScript 5.9, Go 1.25, PostgreSQL migration binary.

---

## File map

- Create `scripts/bare-metal-deploy.test.mjs`: behavioral tests for runtime wrappers and static contract tests for build scripts, templates, and documentation.
- Create `deploy/bare-metal/build-web.sh`: reproducible static Web release builder.
- Create `deploy/bare-metal/build-backend.sh`: reproducible Linux API/Worker/Migrator release builder.
- Create `deploy/bare-metal/runtime/runtime-env.sh`: shared environment, secret-file, validation, and binary helpers.
- Create `deploy/bare-metal/runtime/start-api.sh`: foreground API launcher.
- Create `deploy/bare-metal/runtime/start-worker.sh`: foreground Worker launcher.
- Create `deploy/bare-metal/runtime/migrate.sh`: `up`/`check` migration dispatcher.
- Create `deploy/bare-metal/config/api.env.example`: API-only production configuration.
- Create `deploy/bare-metal/config/worker.env.example`: Worker-only production configuration.
- Create `deploy/bare-metal/config/migrate.env.example`: migrator-only production configuration.
- Create `docs/runbooks/separate-deployment.md`: end-to-end Linux split-deployment runbook.
- Modify `.gitignore`: ignore reproducible root `release/` output.
- Modify `package.json`: expose bare-metal test and release build commands.
- Modify `README.md`: make the Docker-independent deployment route discoverable.

### Task 1: Runtime wrapper behavior

**Files:**
- Create: `scripts/bare-metal-deploy.test.mjs`
- Create: `deploy/bare-metal/runtime/runtime-env.sh`
- Create: `deploy/bare-metal/runtime/start-api.sh`
- Create: `deploy/bare-metal/runtime/start-worker.sh`
- Create: `deploy/bare-metal/runtime/migrate.sh`

- [ ] **Step 1: Write the failing runtime tests**

Create `scripts/bare-metal-deploy.test.mjs` with:

```javascript
import assert from "node:assert/strict";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

const root = resolve(import.meta.dirname, "..");
const runtimeSource = resolve(root, "deploy/bare-metal/runtime");

function writeExecutable(path, content) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content, "utf8");
  chmodSync(path, 0o755);
}

function createRuntimeFixture() {
  const fixture = mkdtempSync(resolve(tmpdir(), "dayorder-bare-metal-"));
  const scripts = resolve(fixture, "scripts");
  const bin = resolve(fixture, "bin");
  mkdirSync(scripts, { recursive: true });
  mkdirSync(bin, { recursive: true });
  for (const name of ["runtime-env.sh", "start-api.sh", "start-worker.sh", "migrate.sh"]) {
    const source = resolve(runtimeSource, name);
    assert.equal(existsSync(source), true, `${name} must exist`);
    copyFileSync(source, resolve(scripts, name));
    chmodSync(resolve(scripts, name), 0o755);
  }
  return fixture;
}

function runScript(script, args, environment = {}) {
  return spawnSync("bash", [script, ...args], {
    cwd: root,
    encoding: "utf8",
    env: { ...process.env, ...environment },
  });
}

test("runtime scripts pass Bash syntax validation", () => {
  for (const name of ["runtime-env.sh", "start-api.sh", "start-worker.sh", "migrate.sh"]) {
    const result = spawnSync("bash", ["-n", resolve(runtimeSource, name)], { encoding: "utf8" });
    assert.equal(result.status, 0, result.stderr);
  }
});

test("API wrapper loads a secret file and execs the API binary without leaking the secret", (t) => {
  const fixture = createRuntimeFixture();
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const secret = "api-hmac-secret-with-at-least-32-bytes";
  const secretPath = resolve(fixture, "secrets/api_hmac");
  const capturePath = resolve(fixture, "api.capture");
  mkdirSync(dirname(secretPath), { recursive: true });
  writeFileSync(secretPath, `${secret}\n`, "utf8");
  const envPath = resolve(fixture, "api.env");
  writeFileSync(
    envPath,
    `DATABASE_URL='postgres://api@db/dayorder'\nDAYORDER_AUTH_HMAC_KEY_FILE='${secretPath}'\n`,
    "utf8",
  );
  writeExecutable(
    resolve(fixture, "bin/dayorder-api"),
    "#!/usr/bin/env bash\nprintf '%s\\n%s\\n' \"$DATABASE_URL\" \"$DAYORDER_AUTH_HMAC_KEY\" > \"$CAPTURE_PATH\"\n",
  );

  const result = runScript(resolve(fixture, "scripts/start-api.sh"), [envPath], { CAPTURE_PATH: capturePath });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(capturePath, "utf8"), `postgres://api@db/dayorder\n${secret}\n`);
  assert.doesNotMatch(`${result.stdout}${result.stderr}`, new RegExp(secret));
});

test("Worker wrapper selects the Worker binary", (t) => {
  const fixture = createRuntimeFixture();
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const envPath = resolve(fixture, "worker.env");
  const capturePath = resolve(fixture, "worker.capture");
  writeFileSync(
    envPath,
    "WORKER_DATABASE_URL='postgres://worker@db/dayorder'\nDAYORDER_AUTH_HMAC_KEY='worker-hmac-secret-with-at-least-32-bytes'\n",
    "utf8",
  );
  writeExecutable(
    resolve(fixture, "bin/dayorder-worker"),
    "#!/usr/bin/env bash\nprintf '%s' worker > \"$CAPTURE_PATH\"\n",
  );

  const result = runScript(resolve(fixture, "scripts/start-worker.sh"), [envPath], { CAPTURE_PATH: capturePath });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(capturePath, "utf8"), "worker");
});

test("migration wrapper maps up and check to the embedded migrator", (t) => {
  const fixture = createRuntimeFixture();
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const envPath = resolve(fixture, "migrate.env");
  writeFileSync(envPath, "MIGRATION_DATABASE_URL='postgres://migrator@db/dayorder'\n", "utf8");
  writeExecutable(
    resolve(fixture, "bin/dayorder-migrate"),
    "#!/usr/bin/env bash\nprintf '%s' \"$*\" > \"$CAPTURE_PATH\"\n",
  );

  const upCapture = resolve(fixture, "up.capture");
  const up = runScript(resolve(fixture, "scripts/migrate.sh"), ["up", envPath], { CAPTURE_PATH: upCapture });
  assert.equal(up.status, 0, up.stderr);
  assert.equal(readFileSync(upCapture, "utf8"), "");

  const checkCapture = resolve(fixture, "check.capture");
  const check = runScript(resolve(fixture, "scripts/migrate.sh"), ["check", envPath], {
    CAPTURE_PATH: checkCapture,
  });
  assert.equal(check.status, 0, check.stderr);
  assert.equal(readFileSync(checkCapture, "utf8"), "-check");

  const invalid = runScript(resolve(fixture, "scripts/migrate.sh"), ["down", envPath]);
  assert.notEqual(invalid.status, 0);
  assert.match(invalid.stderr, /usage/i);
});

test("runtime wrappers reject missing configuration, conflicting secrets, and missing binaries", (t) => {
  const fixture = createRuntimeFixture();
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const apiScript = resolve(fixture, "scripts/start-api.sh");

  const missingEnvironment = runScript(apiScript, [resolve(fixture, "missing.env")]);
  assert.notEqual(missingEnvironment.status, 0);
  assert.match(missingEnvironment.stderr, /environment file.*readable/i);

  const conflictEnv = resolve(fixture, "conflict.env");
  const secretPath = resolve(fixture, "api_hmac");
  writeFileSync(secretPath, "file-secret-with-at-least-thirty-two-bytes\n", "utf8");
  writeFileSync(
    conflictEnv,
    `DATABASE_URL='postgres://api@db/dayorder'\nDAYORDER_AUTH_HMAC_KEY='direct-secret-with-at-least-32-bytes'\nDAYORDER_AUTH_HMAC_KEY_FILE='${secretPath}'\n`,
    "utf8",
  );
  const conflict = runScript(apiScript, [conflictEnv]);
  assert.notEqual(conflict.status, 0);
  assert.match(conflict.stderr, /cannot both be set/i);

  const validEnv = resolve(fixture, "valid.env");
  writeFileSync(
    validEnv,
    "DATABASE_URL='postgres://api@db/dayorder'\nDAYORDER_AUTH_HMAC_KEY='direct-secret-with-at-least-32-bytes'\n",
    "utf8",
  );
  const missingBinary = runScript(apiScript, [validEnv]);
  assert.notEqual(missingBinary.status, 0);
  assert.match(missingBinary.stderr, /executable.*dayorder-api/i);
});
```

- [ ] **Step 2: Run the runtime tests and confirm the expected red state**

Run:

```bash
node --test scripts/bare-metal-deploy.test.mjs
```

Expected: FAIL because `deploy/bare-metal/runtime/runtime-env.sh` and the three wrappers do not exist.

- [ ] **Step 3: Implement the shared runtime helper**

Create `deploy/bare-metal/runtime/runtime-env.sh`:

```bash
#!/usr/bin/env bash
set -Eeuo pipefail

dayorder_die() {
  printf 'dayorder: %s\n' "$*" >&2
  exit 1
}

dayorder_load_environment() {
  local environment_file="${1:-}"
  [[ -n "$environment_file" && -f "$environment_file" && -r "$environment_file" ]] || \
    dayorder_die "environment file is not readable: ${environment_file:-<missing>}"
  set -a
  # The environment file is an operator-controlled Bash-compatible key/value file.
  # shellcheck disable=SC1090
  source "$environment_file"
  set +a
}

dayorder_load_secret() {
  local variable="$1"
  local file_variable="${variable}_FILE"
  local current_value="${!variable-}"
  local file_path="${!file_variable-}"
  local value

  if [[ -n "$current_value" && -n "$file_path" ]]; then
    dayorder_die "$variable and $file_variable cannot both be set"
  fi
  if [[ -z "$file_path" ]]; then
    return
  fi
  [[ -f "$file_path" && -r "$file_path" ]] || dayorder_die "$file_variable does not reference a readable file"
  value="$(tr -d '\r\n' < "$file_path")"
  [[ -n "$value" ]] || dayorder_die "$file_variable references an empty secret"
  printf -v "$variable" '%s' "$value"
  export "$variable"
}

dayorder_load_runtime_secrets() {
  local variable
  for variable in \
    DATABASE_URL WORKER_DATABASE_URL MIGRATION_DATABASE_URL \
    DAYORDER_AUTH_HMAC_KEY DAYORDER_SMTP_PASSWORD DAYORDER_AGENT_HTTP_KEY
  do
    dayorder_load_secret "$variable"
  done
}

dayorder_require_value() {
  local variable="$1"
  [[ -n "${!variable-}" ]] || dayorder_die "$variable is required"
}

dayorder_require_executable() {
  local executable="$1"
  [[ -f "$executable" && -x "$executable" ]] || dayorder_die "executable is missing or not executable: $executable"
}
```

- [ ] **Step 4: Implement the API and Worker foreground launchers**

Create `deploy/bare-metal/runtime/start-api.sh`:

```bash
#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=runtime-env.sh
source "$script_dir/runtime-env.sh"

[[ $# -eq 1 ]] || dayorder_die "usage: start-api.sh <api.env>"
dayorder_load_environment "$1"
dayorder_load_runtime_secrets
dayorder_require_value DATABASE_URL
dayorder_require_value DAYORDER_AUTH_HMAC_KEY

binary="$script_dir/../bin/dayorder-api"
dayorder_require_executable "$binary"
exec "$binary"
```

Create `deploy/bare-metal/runtime/start-worker.sh`:

```bash
#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=runtime-env.sh
source "$script_dir/runtime-env.sh"

[[ $# -eq 1 ]] || dayorder_die "usage: start-worker.sh <worker.env>"
dayorder_load_environment "$1"
dayorder_load_runtime_secrets
dayorder_require_value WORKER_DATABASE_URL
dayorder_require_value DAYORDER_AUTH_HMAC_KEY

binary="$script_dir/../bin/dayorder-worker"
dayorder_require_executable "$binary"
exec "$binary"
```

- [ ] **Step 5: Implement the migration dispatcher**

Create `deploy/bare-metal/runtime/migrate.sh`:

```bash
#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=runtime-env.sh
source "$script_dir/runtime-env.sh"

[[ $# -eq 2 ]] || dayorder_die "usage: migrate.sh <up|check> <migrate.env>"
action="$1"
environment_file="$2"
case "$action" in
  up) migration_arguments=() ;;
  check) migration_arguments=(-check) ;;
  *) dayorder_die "usage: migrate.sh <up|check> <migrate.env>" ;;
esac

dayorder_load_environment "$environment_file"
dayorder_load_runtime_secrets
dayorder_require_value MIGRATION_DATABASE_URL

binary="$script_dir/../bin/dayorder-migrate"
dayorder_require_executable "$binary"
exec "$binary" "${migration_arguments[@]}"
```

- [ ] **Step 6: Run the runtime tests and confirm green**

Run:

```bash
node --test scripts/bare-metal-deploy.test.mjs
```

Expected: 5 tests pass, 0 fail.

- [ ] **Step 7: Commit the runtime slice**

```bash
git add scripts/bare-metal-deploy.test.mjs deploy/bare-metal/runtime
git commit -m "feat(deploy): add bare-metal runtime launchers"
```

### Task 2: Reproducible Web and backend release builders

**Files:**
- Modify: `scripts/bare-metal-deploy.test.mjs`
- Create: `deploy/bare-metal/build-web.sh`
- Create: `deploy/bare-metal/build-backend.sh`
- Modify: `.gitignore`
- Modify: `package.json`

- [ ] **Step 1: Append failing build-contract tests**

Append to `scripts/bare-metal-deploy.test.mjs`:

```javascript
test("release build scripts and package commands expose the agreed contract", () => {
  const webBuilder = readFileSync(resolve(root, "deploy/bare-metal/build-web.sh"), "utf8");
  const backendBuilder = readFileSync(resolve(root, "deploy/bare-metal/build-backend.sh"), "utf8");
  const packageManifest = JSON.parse(readFileSync(resolve(root, "package.json"), "utf8"));
  const ignore = readFileSync(resolve(root, ".gitignore"), "utf8");

  assert.match(webBuilder, /npm ci/);
  assert.match(webBuilder, /npm run build:web/);
  assert.match(webBuilder, /apps\/web\/dist/);
  assert.match(backendBuilder, /CGO_ENABLED=0/);
  assert.match(backendBuilder, /GOOS=linux/);
  for (const command of ["cmd/server", "cmd/worker", "cmd/migrate"]) assert.match(backendBuilder, new RegExp(command));
  assert.equal(packageManifest.scripts["test:deploy:bare"], "node --test scripts/bare-metal-deploy.test.mjs");
  assert.equal(packageManifest.scripts["build:release:web"], "bash deploy/bare-metal/build-web.sh");
  assert.equal(packageManifest.scripts["build:release:backend"], "bash deploy/bare-metal/build-backend.sh");
  assert.match(ignore, /^release\/$/m);
});
```

- [ ] **Step 2: Run the focused test and confirm it fails because builders are absent**

Run:

```bash
node --test --test-name-pattern="release build scripts" scripts/bare-metal-deploy.test.mjs
```

Expected: FAIL with `ENOENT` for `deploy/bare-metal/build-web.sh`.

- [ ] **Step 3: Implement the Web release builder**

Create `deploy/bare-metal/build-web.sh`:

```bash
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
npm ci
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
```

- [ ] **Step 4: Implement the backend release builder**

Create `deploy/bare-metal/build-backend.sh`:

```bash
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
```

- [ ] **Step 5: Expose release commands and ignore generated output**

Add this line to `.gitignore` after `apps/web/dist/`:

```gitignore
release/
```

Add these keys to the root `package.json` `scripts` object after `test:deploy`:

```json
"test:deploy:bare": "node --test scripts/bare-metal-deploy.test.mjs",
"build:release:web": "bash deploy/bare-metal/build-web.sh",
"build:release:backend": "bash deploy/bare-metal/build-backend.sh",
```

- [ ] **Step 6: Run the build-contract and existing runtime tests**

Run:

```bash
npm run test:deploy:bare
```

Expected: 6 tests pass, 0 fail.

- [ ] **Step 7: Commit the build slice**

```bash
git add .gitignore package.json scripts/bare-metal-deploy.test.mjs deploy/bare-metal/build-web.sh deploy/bare-metal/build-backend.sh
git commit -m "feat(deploy): build split Linux release artifacts"
```

### Task 3: Least-privilege service configuration templates

**Files:**
- Modify: `scripts/bare-metal-deploy.test.mjs`
- Create: `deploy/bare-metal/config/api.env.example`
- Create: `deploy/bare-metal/config/worker.env.example`
- Create: `deploy/bare-metal/config/migrate.env.example`

- [ ] **Step 1: Append failing template-isolation tests**

Append to `scripts/bare-metal-deploy.test.mjs`:

```javascript
test("service configuration templates keep database roles isolated", () => {
  const api = readFileSync(resolve(root, "deploy/bare-metal/config/api.env.example"), "utf8");
  const worker = readFileSync(resolve(root, "deploy/bare-metal/config/worker.env.example"), "utf8");
  const migrate = readFileSync(resolve(root, "deploy/bare-metal/config/migrate.env.example"), "utf8");

  assert.match(api, /^DATABASE_URL_FILE=/m);
  assert.match(api, /^DAYORDER_AUTH_HMAC_KEY_FILE=/m);
  assert.doesNotMatch(api, /^WORKER_DATABASE_URL(?:_FILE)?=/m);
  assert.doesNotMatch(api, /^MIGRATION_DATABASE_URL(?:_FILE)?=/m);

  assert.match(worker, /^WORKER_DATABASE_URL_FILE=/m);
  assert.match(worker, /^DAYORDER_AUTH_HMAC_KEY_FILE=/m);
  assert.match(worker, /^DAYORDER_SMTP_PASSWORD_FILE=/m);
  assert.match(worker, /^DAYORDER_AGENT_HTTP_KEY_FILE=/m);
  assert.doesNotMatch(worker, /^DATABASE_URL(?:_FILE)?=/m);
  assert.doesNotMatch(worker, /^MIGRATION_DATABASE_URL(?:_FILE)?=/m);

  assert.match(migrate, /^MIGRATION_DATABASE_URL_FILE=/m);
  assert.doesNotMatch(migrate, /^DATABASE_URL(?:_FILE)?=/m);
  assert.doesNotMatch(migrate, /^WORKER_DATABASE_URL(?:_FILE)?=/m);
  assert.doesNotMatch(`${api}\n${worker}\n${migrate}`, /development-only|replace-with|change-me/i);
});
```

- [ ] **Step 2: Run the focused test and confirm the templates are missing**

Run:

```bash
node --test --test-name-pattern="configuration templates" scripts/bare-metal-deploy.test.mjs
```

Expected: FAIL with `ENOENT` for `deploy/bare-metal/config/api.env.example`.

- [ ] **Step 3: Create the API template**

Create `deploy/bare-metal/config/api.env.example`:

```bash
DAYORDER_ENV=production
DAYORDER_ADDR=0.0.0.0:8080
DAYORDER_METRICS_ADDR=127.0.0.1:9090
DAYORDER_PUBLIC_URL=https://api.example.invalid
DAYORDER_ALLOWED_ORIGINS=https://app.example.invalid
DATABASE_URL_FILE=/etc/dayorder/secrets/api_database_url
DAYORDER_AUTH_HMAC_KEY_FILE=/etc/dayorder/secrets/auth_hmac_key
DAYORDER_DB_MAX_CONNS=20
DAYORDER_DB_MIN_CONNS=2
DAYORDER_DB_MAX_CONN_LIFETIME=30m
DAYORDER_DB_MAX_CONN_IDLE_TIME=5m
DAYORDER_DB_STATEMENT_TIMEOUT=5s
DAYORDER_DB_LOCK_TIMEOUT=2s
DAYORDER_DB_IDLE_TX_TIMEOUT=10s
DAYORDER_DB_HEALTH_TIMEOUT=3s
```

- [ ] **Step 4: Create the Worker template**

Create `deploy/bare-metal/config/worker.env.example`:

```bash
DAYORDER_ENV=production
DAYORDER_PUBLIC_URL=https://api.example.invalid
DAYORDER_WORKER_METRICS_ADDR=127.0.0.1:9091
WORKER_DATABASE_URL_FILE=/etc/dayorder/secrets/worker_database_url
DAYORDER_AUTH_HMAC_KEY_FILE=/etc/dayorder/secrets/auth_hmac_key
DAYORDER_WORKER_DB_MAX_CONNS=5
DAYORDER_WORKER_DB_MIN_CONNS=1
DAYORDER_WORKER_POLL_RATE=1s
DAYORDER_DB_MAX_CONN_LIFETIME=30m
DAYORDER_DB_MAX_CONN_IDLE_TIME=5m
DAYORDER_DB_STATEMENT_TIMEOUT=30s
DAYORDER_DB_LOCK_TIMEOUT=2s
DAYORDER_DB_IDLE_TX_TIMEOUT=10s
DAYORDER_DB_HEALTH_TIMEOUT=3s
DAYORDER_MAIL_SINK=smtp
DAYORDER_SMTP_ADDRESS=smtp.example.invalid:587
DAYORDER_SMTP_FROM='DayOrder <noreply@example.invalid>'
DAYORDER_SMTP_USERNAME=dayorder
DAYORDER_SMTP_PASSWORD_FILE=/etc/dayorder/secrets/smtp_password
DAYORDER_SMTP_TLS_MODE=starttls
DAYORDER_SMTP_TIMEOUT=15s
DAYORDER_AGENT_PROVIDER=http
DAYORDER_AGENT_HTTP_URL=https://agent.example.invalid/v1/analyze
DAYORDER_AGENT_HTTP_KEY_FILE=/etc/dayorder/secrets/agent_http_key
DAYORDER_AGENT_MODEL=production-model
DAYORDER_AGENT_TIMEOUT=30s
```

- [ ] **Step 5: Create the Migrator template**

Create `deploy/bare-metal/config/migrate.env.example`:

```bash
MIGRATION_DATABASE_URL_FILE=/etc/dayorder/secrets/migration_database_url
```

- [ ] **Step 6: Run the template test, then the complete bare-metal suite**

Run:

```bash
node --test --test-name-pattern="configuration templates" scripts/bare-metal-deploy.test.mjs
npm run test:deploy:bare
```

Expected: focused template test passes; complete suite reports 7 pass, 0 fail.

- [ ] **Step 7: Commit the configuration slice**

```bash
git add scripts/bare-metal-deploy.test.mjs deploy/bare-metal/config
git commit -m "docs(deploy): add isolated backend environment templates"
```

### Task 4: Separate deployment runbook and project entry points

**Files:**
- Modify: `scripts/bare-metal-deploy.test.mjs`
- Create: `docs/runbooks/separate-deployment.md`
- Modify: `README.md`

- [ ] **Step 1: Append a failing documentation coverage test**

Append to `scripts/bare-metal-deploy.test.mjs`:

```javascript
test("project documentation covers the Docker-independent release path", () => {
  const readme = readFileSync(resolve(root, "README.md"), "utf8");
  const runbook = readFileSync(resolve(root, "docs/runbooks/separate-deployment.md"), "utf8");

  assert.match(readme, /前后端分离部署/);
  assert.match(readme, /build:release:web/);
  assert.match(readme, /build:release:backend/);
  for (const phrase of [
    "VITE_API_BASE_URL",
    "migrate.sh up",
    "migrate.sh check",
    "start-api.sh",
    "start-worker.sh",
    "/health/ready",
    "systemd",
  ]) {
    assert.match(runbook, new RegExp(phrase.replace("/", "\\/")));
  }
});
```

- [ ] **Step 2: Run the focused test and confirm the runbook is missing**

Run:

```bash
node --test --test-name-pattern="documentation" scripts/bare-metal-deploy.test.mjs
```

Expected: FAIL with `ENOENT` for `docs/runbooks/separate-deployment.md`.

- [ ] **Step 3: Add the README entry point**

Insert the following section before the existing `## 单机生产部署` section in `README.md`:

````markdown
## Linux 前后端分离部署

项目提供不依赖 Docker 的 Linux 发布脚本：

```bash
npm run build:release:web
npm run build:release:backend
```

- `release/web/` 是可直接部署到 Nginx、Caddy、对象存储或 CDN 的静态资源。
- `release/backend/` 包含独立的 API、Worker、Migrator 二进制、启动脚本和配置模板。
- API、Worker 使用独立进程和数据库账号；发布时先执行 migration，再分别重启两个服务。

完整的服务器依赖、跨域配置、上传、迁移、启动和健康检查命令见 [前后端分离部署手册](docs/runbooks/separate-deployment.md)。原有 Docker Compose 部署路径继续保留，但不是该流程的依赖。
````

- [ ] **Step 4: Write the complete runbook**

Create `docs/runbooks/separate-deployment.md` with these exact sections and commands:

````markdown
# Linux 前后端分离部署手册

本手册用于将 DayOrder Web、API、Worker 和数据库迁移器分别部署到 Linux。该流程不构建或运行 Docker 容器，也不负责安装 PostgreSQL、反向代理或进程管理器。

## 依赖与服务边界

构建机需要 Node.js 22.22+（或 24.15+）、npm、Go 1.25+、Bash、`realpath` 和常见 GNU 工具。后端运行机只需要 Linux、CA 证书、时区数据、Bash 以及可访问的 PostgreSQL。

- Web 是纯静态资源。
- API 是长期运行的 HTTP 服务。
- Worker 是长期运行的异步任务服务。
- Migrator 是发布期间执行一次的数据库任务。

## 构建 Web

同域代理 `/api` 时运行：

```bash
npm run build:release:web
```

前端和 API 使用不同 Origin 时，在构建时固定 API 地址：

```bash
VITE_API_BASE_URL=https://api.example.com/api/v1 npm run build:release:web
```

产物位于 `release/web/`。把该目录的内容同步到静态服务器站点根目录；SPA 服务必须把未知前端路由回退到 `index.html`。跨域部署还需将 Web Origin 写入 API 的 `DAYORDER_ALLOWED_ORIGINS`，并保持 HTTPS 与凭据请求配置一致。

## 构建 Backend

默认构建当前机器架构的 Linux 二进制：

```bash
npm run build:release:backend
```

也可以显式构建目标架构：

```bash
GOARCH=amd64 npm run build:release:backend
GOARCH=arm64 npm run build:release:backend
```

产物位于 `release/backend/`。上传整个目录，不要只上传 `bin/`，因为运行脚本通过相对路径寻找二进制。

## 安装目录与配置

以下示例把版本产物安装到 `/opt/dayorder/releases/0.2.0`，配置和密钥放到版本目录之外：

```bash
sudo install -d -o dayorder -g dayorder /opt/dayorder/releases/0.2.0
sudo cp -a release/backend/. /opt/dayorder/releases/0.2.0/
sudo install -d -m 0750 -o root -g dayorder /etc/dayorder /etc/dayorder/secrets
sudo install -m 0640 -o root -g dayorder deploy/bare-metal/config/api.env.example /etc/dayorder/api.env
sudo install -m 0640 -o root -g dayorder deploy/bare-metal/config/worker.env.example /etc/dayorder/worker.env
sudo install -m 0640 -o root -g dayorder deploy/bare-metal/config/migrate.env.example /etc/dayorder/migrate.env
```

编辑三个环境文件中的域名、SMTP、Agent 和容量设置。分别创建以下密钥文件并保持 `0640 root:dayorder` 权限：

- `api_database_url`
- `worker_database_url`
- `migration_database_url`
- `auth_hmac_key`
- `smtp_password`
- `agent_http_key`

数据库 URL 应使用三个不同的 PostgreSQL 账号。API 和 Worker 不得使用 migrator 账号。

## 执行数据库迁移

每次启动新应用版本前执行：

```bash
cd /opt/dayorder/releases/0.2.0
sudo -u dayorder ./scripts/migrate.sh up /etc/dayorder/migrate.env
sudo -u dayorder ./scripts/migrate.sh check /etc/dayorder/migrate.env
```

任一命令失败都应停止发布。迁移只向前执行，不提供自动降级。

## 启动 API 与 Worker

启动脚本保持前台并转发 `SIGTERM`，应由 systemd、Supervisor 或同类工具管理：

```bash
cd /opt/dayorder/releases/0.2.0
sudo -u dayorder ./scripts/start-api.sh /etc/dayorder/api.env
sudo -u dayorder ./scripts/start-worker.sh /etc/dayorder/worker.env
```

为 API 和 Worker 创建两个独立服务单元，分别设置工作目录、`ExecStart`、非 root 用户、重启策略和停止超时。不要在启动脚本外再套 `nohup` 或 `&`。

## 反向代理与静态站点

公网反向代理只需要把 `/api/*` 和 `/health/*` 转发到 API 的 `DAYORDER_ADDR`。API 和 Worker 指标地址默认绑定回环接口，不应直接暴露公网。Worker 没有业务 HTTP 入口。

如果 Web 与 API 分属不同域名，认证 Cookie 请求依赖浏览器凭据模式和 API CORS 白名单；两端都必须使用 HTTPS。

## 发布后检查

```bash
curl --fail --silent --show-error https://api.example.com/health/live
curl --fail --silent --show-error https://api.example.com/health/ready
curl --fail --silent --show-error http://127.0.0.1:9090/metrics >/dev/null
curl --fail --silent --show-error http://127.0.0.1:9091/metrics >/dev/null
```

随后检查 API 与 Worker 日志，并人工完成注册、邮箱验证、登录、资源写入和同步。API `/health/ready` 会拒绝 schema 版本落后的数据库。

## 升级与回退

新版本先上传到新的版本目录，执行 migration 和检查后再切换两个服务。保留上一版完整后端目录和 Web 产物，便于回退应用二进制。

数据库 migration 不会随应用回退而降级。涉及 schema 的版本必须采用 expand/contract，保证相邻应用版本能在新 schema 上短期运行。回退后重新检查 `/health/ready`、Worker 日志和 Outbox 堆积。
````

- [ ] **Step 5: Run the documentation test and full deployment test suite**

Run:

```bash
node --test --test-name-pattern="documentation" scripts/bare-metal-deploy.test.mjs
npm run test:deploy:bare
```

Expected: focused documentation test passes; full suite reports 8 pass, 0 fail.

- [ ] **Step 6: Commit the documentation slice**

```bash
git add README.md docs/runbooks/separate-deployment.md scripts/bare-metal-deploy.test.mjs
git commit -m "docs(deploy): document split Linux deployment"
```

### Task 5: Real artifact builds and complete regression verification

**Files:**
- Verify only; repair the task-owned files above if a command exposes a defect.

- [ ] **Step 1: Run static and behavioral deployment validation**

```bash
npm run test:deploy:bare
find deploy/bare-metal -type f -name '*.sh' -print0 | xargs -0 -n1 bash -n
```

Expected: 8 Node tests pass and every Bash syntax check exits 0.

- [ ] **Step 2: Run existing application regression tests**

```bash
npm run typecheck
npm test
go vet ./apps/api/...
npm run test:architecture
```

Expected: all commands exit 0 with no test, type, vet, or architecture failures.

- [ ] **Step 3: Perform a real Web release build into an isolated temporary directory**

```bash
dayorder_web_artifact="$(mktemp -d)/web"
bash deploy/bare-metal/build-web.sh "$dayorder_web_artifact"
test -f "$dayorder_web_artifact/index.html"
find "$dayorder_web_artifact/assets" -type f -print -quit | grep -q .
```

Expected: the builder exits 0, prints its output path, and the isolated directory contains `index.html` plus at least one asset.

- [ ] **Step 4: Perform a real Backend release build into an isolated temporary directory**

```bash
dayorder_backend_artifact="$(mktemp -d)/backend"
bash deploy/bare-metal/build-backend.sh "$dayorder_backend_artifact"
test -x "$dayorder_backend_artifact/bin/dayorder-api"
test -x "$dayorder_backend_artifact/bin/dayorder-worker"
test -x "$dayorder_backend_artifact/bin/dayorder-migrate"
file "$dayorder_backend_artifact/bin/dayorder-api" "$dayorder_backend_artifact/bin/dayorder-worker" "$dayorder_backend_artifact/bin/dayorder-migrate"
```

Expected: the builder exits 0; all three files are executable Linux binaries; scripts and templates are present beside them.

- [ ] **Step 5: Confirm the generated root release directory stays untracked and inspect the final diff**

```bash
npm run build:release:web
npm run build:release:backend
git status --short
git diff --check
git diff --stat HEAD~4..HEAD
```

Expected: `release/` does not appear in Git status, `git diff --check` exits 0, and the diff contains only the deployment scripts, tests, templates, documentation, package commands, and ignore rule from this plan.

- [ ] **Step 6: Record the verification result**

If a verification-driven repair was required, commit only the repaired task-owned files:

```bash
git add .gitignore package.json README.md scripts/bare-metal-deploy.test.mjs deploy/bare-metal docs/runbooks/separate-deployment.md
git commit -m "fix(deploy): resolve bare-metal release verification issues"
```

If no repair was required, do not create an empty commit. Report the exact passing command results and any environment-dependent verification that could not run.
