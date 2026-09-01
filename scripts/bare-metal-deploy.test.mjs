import assert from "node:assert/strict";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

const root = resolve(import.meta.dirname, "..");
const runtimeSource = resolve(root, "deploy/bare-metal/runtime");
const bash = process.platform === "win32"
  ? resolve(process.env.ProgramFiles ?? "C:\\Program Files", "Git/bin/bash.exe")
  : "bash";

function writeExecutable(path, content) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content, "utf8");
  chmodSync(path, 0o755);
}

function writeProtected(path, content, mode = 0o600) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content, "utf8");
  chmodSync(path, mode);
}

function createRuntimeFixture() {
  const fixture = mkdtempSync(resolve(tmpdir(), "dayorder-bare-metal-"));
  const scripts = resolve(fixture, "scripts");
  const bin = resolve(fixture, "bin");
  const commands = resolve(fixture, "commands");
  const configHubCommands = resolve(fixture, "confighub-commands");
  mkdirSync(scripts, { recursive: true });
  mkdirSync(bin, { recursive: true });
  mkdirSync(commands, { recursive: true });
  mkdirSync(configHubCommands, { recursive: true });
  for (const name of ["runtime-env.sh", "start-api.sh", "start-worker.sh", "migrate.sh"]) {
    const source = resolve(runtimeSource, name);
    assert.equal(existsSync(source), true, `${name} must exist`);
    copyFileSync(source, resolve(scripts, name));
    chmodSync(resolve(scripts, name), 0o755);
  }
  writeExecutable(resolve(commands, "id"), `#!/usr/bin/env bash
case "$*" in
  -u) printf '%s\\n' "\${DAYORDER_TEST_ID_UID:-1000}" ;;
  -G) printf '%s\\n' "\${DAYORDER_TEST_ID_GROUPS:-1000 2000}" ;;
  *) exec /usr/bin/id "$@" ;;
esac
`);
  writeExecutable(resolve(commands, "stat"), `#!/usr/bin/env bash
format=""; path="\${@: -1}"
while [[ $# -gt 0 ]]; do
  case "$1" in -c|--format) format="$2"; shift 2 ;; *) shift ;; esac
done
uid="\${DAYORDER_TEST_DEFAULT_UID:-1000}"
gid="\${DAYORDER_TEST_DEFAULT_GID:-1000}"
mode="\${DAYORDER_TEST_DEFAULT_MODE:-600}"
if [[ -n "\${DAYORDER_TEST_STAT_BASENAME:-}" && "$(basename -- "$path")" == "$DAYORDER_TEST_STAT_BASENAME" ]]; then
  uid="\${DAYORDER_TEST_STAT_UID:-$uid}"
  gid="\${DAYORDER_TEST_STAT_GID:-$gid}"
  mode="\${DAYORDER_TEST_STAT_MODE:-$mode}"
fi
case "$format" in
  %u) printf '%s\\n' "$uid" ;;
  %g) printf '%s\\n' "$gid" ;;
  %a) printf '%s\\n' "$mode" ;;
  *) exit 64 ;;
esac
`);
  writeExecutable(resolve(configHubCommands, "confighub"), `#!/usr/bin/env bash
set -Eeuo pipefail
if [[ -n "\${DAYORDER_TEST_CONFIGHUB_LOG:-}" ]]; then
  printf '%s\t%s\n' "$PWD" "$*" >> "$DAYORDER_TEST_CONFIGHUB_LOG"
fi
if [[ "\${DAYORDER_TEST_CONFIGHUB_FAIL:-0}" == 1 ]]; then
  printf 'confighub: access denied for shier/prod\n' >&2
  exit 77
fi
[[ "$1" == run && "$2" == --project && "$3" == shier && "$4" == --env && "$5" == prod && "$6" == -- ]] || exit 64
[[ -f "$PWD/api.env" || -f "$PWD/worker.env" || -f "$PWD/migrate.env" ]] || exit 65
shift 6
export db_address=db.example.invalid db_port=5432 db_username=dayorder_admin
export db_password=admin-password db_migrator_password=migrator-password
export db_api_password=api-password db_worker_password=worker-password
exec "$@"
`);
  return fixture;
}

function runScript(script, args, environment = {}) {
  const fixture = resolve(dirname(script), "..");
  const useFakeMetadata = process.platform === "win32" || environment.DAYORDER_TEST_USE_FAKE_METADATA === "1";
  const childEnvironment = { ...process.env, ...environment };
  const pathPrefix = useFakeMetadata
    ? "$DAYORDER_TEST_COMMANDS:$DAYORDER_TEST_CONFIGHUB_COMMANDS"
    : "$DAYORDER_TEST_CONFIGHUB_COMMANDS";
  const command = [
    "-c",
    `PATH="${pathPrefix}:$PATH"; export PATH; exec bash "$@"`,
    "dayorder-runtime-test",
    script,
    ...args,
  ];
  childEnvironment.DAYORDER_TEST_CONFIGHUB_COMMANDS = gitShellPath(resolve(fixture, "confighub-commands"));
  if (useFakeMetadata) childEnvironment.DAYORDER_TEST_COMMANDS = gitShellPath(resolve(fixture, "commands"));
  return spawnSync(bash, command, {
    cwd: root,
    encoding: "utf8",
    env: childEnvironment,
  });
}

function shellPath(path) {
  if (process.platform !== "win32") return path;
  const match = /^([A-Za-z]):\\([\s\S]*)$/.exec(path);
  assert.ok(match, `expected Windows path: ${path}`);
  return `/mnt/${match[1].toLowerCase()}/${match[2].replaceAll("\\", "/")}`;
}

function gitShellPath(path) {
  if (process.platform !== "win32") return path;
  const match = /^([A-Za-z]):\\([\s\S]*)$/.exec(path);
  assert.ok(match, `expected Windows path: ${path}`);
  return `/${match[1].toLowerCase()}/${match[2].replaceAll("\\", "/")}`;
}

function makeRuntimeSymlink(target, link) {
  if (process.platform !== "win32") {
    symlinkSync(target, link);
    return;
  }
  const result = spawnSync("C:\\Windows\\System32\\wsl.exe", [
    "--exec", "/bin/ln", "-s", shellPath(target), shellPath(link),
  ], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
}

test("runtime scripts pass Bash syntax validation", () => {
  for (const name of ["runtime-env.sh", "start-api.sh", "start-worker.sh", "migrate.sh"]) {
    const result = spawnSync(bash, ["-n", resolve(runtimeSource, name)], { encoding: "utf8" });
    assert.equal(result.status, 0, result.stderr);
  }
});

test("API wrapper loads non-database secrets and launches through ConfigHub from the configuration directory", (t) => {
  const fixture = createRuntimeFixture();
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const secret = "api-hmac-secret-with-at-least-32-bytes";
  const secretPath = resolve(fixture, "secrets/api_hmac");
  const capturePath = resolve(fixture, "api.capture");
  const configHubLog = resolve(fixture, "confighub.log");
  mkdirSync(dirname(secretPath), { recursive: true });
  writeProtected(secretPath, `${secret}\n`);
  const envPath = resolve(fixture, "api.env");
  writeProtected(
    envPath,
    `DATABASE_URL='postgres://legacy-api@db/dayorder'\nDAYORDER_AUTH_HMAC_KEY_FILE='${secretPath}'\n`,
  );
  writeExecutable(
    resolve(fixture, "bin/dayorder-api"),
    "#!/usr/bin/env bash\nprintf '%s\\n%s\\n%s\\n' \"$db_address\" \"${DATABASE_URL-unset}\" \"$DAYORDER_AUTH_HMAC_KEY\" > \"$CAPTURE_PATH\"\n",
  );

  const result = runScript(resolve(fixture, "scripts/start-api.sh"), [envPath], {
    CAPTURE_PATH: capturePath,
    DAYORDER_TEST_CONFIGHUB_LOG: configHubLog,
  });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(capturePath, "utf8"), `db.example.invalid\nunset\n${secret}\n`);
  assert.match(
    readFileSync(configHubLog, "utf8"),
    /\trun --project shier --env prod -- .*\/scripts\/\.\.\/bin\/dayorder-api\n$/,
  );
  assert.doesNotMatch(`${result.stdout}${result.stderr}`, new RegExp(secret));
});

test("Worker wrapper selects the Worker binary", (t) => {
  const fixture = createRuntimeFixture();
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const envPath = resolve(fixture, "worker.env");
  const capturePath = resolve(fixture, "worker.capture");
  const configHubLog = resolve(fixture, "confighub.log");
  writeProtected(
    envPath,
    "WORKER_DATABASE_URL='postgres://legacy-worker@db/dayorder'\nDAYORDER_AUTH_HMAC_KEY='worker-hmac-secret-with-at-least-32-bytes'\n",
  );
  writeExecutable(
    resolve(fixture, "bin/dayorder-worker"),
    "#!/usr/bin/env bash\nprintf '%s\\n%s\\n' \"$db_address\" \"${WORKER_DATABASE_URL-unset}\" > \"$CAPTURE_PATH\"\n",
  );

  const result = runScript(resolve(fixture, "scripts/start-worker.sh"), [envPath], {
    CAPTURE_PATH: capturePath,
    DAYORDER_TEST_CONFIGHUB_LOG: configHubLog,
  });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(capturePath, "utf8"), "db.example.invalid\nunset\n");
  assert.match(
    readFileSync(configHubLog, "utf8"),
    /\trun --project shier --env prod -- .*\/scripts\/\.\.\/bin\/dayorder-worker\n$/,
  );
});

test("migration wrapper maps up and check to the embedded migrator", (t) => {
  const fixture = createRuntimeFixture();
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const envPath = resolve(fixture, "migrate.env");
  const configHubLog = resolve(fixture, "confighub.log");
  writeProtected(
    envPath,
    "DAYORDER_ENV=production\nMIGRATION_DATABASE_URL='postgres://legacy-migrator@db/dayorder'\n",
  );
  writeExecutable(
    resolve(fixture, "bin/dayorder-migrate"),
    "#!/usr/bin/env bash\nprintf '%s\\n%s\\n%s\\n' \"$*\" \"$db_address\" \"${MIGRATION_DATABASE_URL-unset}\" > \"$CAPTURE_PATH\"\n",
  );

  const upCapture = resolve(fixture, "up.capture");
  const up = runScript(resolve(fixture, "scripts/migrate.sh"), ["up", envPath], {
    CAPTURE_PATH: upCapture,
    DAYORDER_TEST_CONFIGHUB_LOG: configHubLog,
  });
  assert.equal(up.status, 0, up.stderr);
  assert.equal(readFileSync(upCapture, "utf8"), "\ndb.example.invalid\nunset\n");

  const checkCapture = resolve(fixture, "check.capture");
  const check = runScript(resolve(fixture, "scripts/migrate.sh"), ["check", envPath], {
    CAPTURE_PATH: checkCapture,
    DAYORDER_TEST_CONFIGHUB_LOG: configHubLog,
  });
  assert.equal(check.status, 0, check.stderr);
  assert.equal(readFileSync(checkCapture, "utf8"), "-check\ndb.example.invalid\nunset\n");
  const configHubInvocations = readFileSync(configHubLog, "utf8").trimEnd().split("\n");
  assert.equal(configHubInvocations.length, 2);
  assert.match(configHubInvocations[0], /\trun --project shier --env prod -- .*\/scripts\/\.\.\/bin\/dayorder-migrate$/);
  assert.match(configHubInvocations[1], /\trun --project shier --env prod -- .*\/scripts\/\.\.\/bin\/dayorder-migrate -check$/);

  const invalid = runScript(resolve(fixture, "scripts/migrate.sh"), ["down", envPath]);
  assert.notEqual(invalid.status, 0);
  assert.match(invalid.stderr, /usage/i);
});

test("migration wrapper rejects a missing or non-production environment before ConfigHub", (t) => {
  const fixture = createRuntimeFixture();
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const envPath = resolve(fixture, "migrate.env");
  const capturePath = resolve(fixture, "migrate.capture");
  const configHubLog = resolve(fixture, "confighub.log");
  writeExecutable(
    resolve(fixture, "bin/dayorder-migrate"),
    "#!/usr/bin/env bash\nprintf started > \"$CAPTURE_PATH\"\n",
  );

  for (const content of [
    "MIGRATION_DATABASE_URL='postgres://legacy-migrator@db/dayorder'\n",
    "DAYORDER_ENV=development\n",
  ]) {
    writeProtected(envPath, content);
    const result = runScript(resolve(fixture, "scripts/migrate.sh"), ["up", envPath], {
      CAPTURE_PATH: capturePath,
      DAYORDER_TEST_CONFIGHUB_LOG: configHubLog,
    });

    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /DAYORDER_ENV.*production/i);
    assert.equal(existsSync(capturePath), false);
    assert.equal(existsSync(configHubLog), false);
  }
});

test("runtime wrapper preserves ConfigHub authorization errors and does not start the binary", (t) => {
  const fixture = createRuntimeFixture();
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const envPath = resolve(fixture, "api.env");
  const capturePath = resolve(fixture, "api.capture");
  writeProtected(
    envPath,
    "DATABASE_URL='postgres://legacy-api@db/dayorder'\nDAYORDER_AUTH_HMAC_KEY='direct-secret-with-at-least-32-bytes'\n",
  );
  writeExecutable(
    resolve(fixture, "bin/dayorder-api"),
    "#!/usr/bin/env bash\nprintf started > \"$CAPTURE_PATH\"\n",
  );

  const result = runScript(resolve(fixture, "scripts/start-api.sh"), [envPath], {
    CAPTURE_PATH: capturePath,
    DAYORDER_TEST_CONFIGHUB_FAIL: "1",
  });

  assert.equal(result.status, 77);
  assert.match(result.stderr, /confighub: access denied for shier\/prod/i);
  assert.equal(existsSync(capturePath), false);
});

test("runtime wrapper uses the pinned ConfigHub executable instead of PATH", (t) => {
  const fixture = createRuntimeFixture();
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const envPath = resolve(fixture, "api.env");
  const capturePath = resolve(fixture, "api.capture");
  const pinnedConfigHub = resolve(fixture, "pinned/confighub");
  writeProtected(
    envPath,
    "DAYORDER_ENV=production\nDAYORDER_CONFIGHUB_EXECUTABLE=confighub\nDAYORDER_AUTH_HMAC_KEY='direct-secret-with-at-least-32-bytes'\n",
  );
  writeExecutable(
    resolve(fixture, "bin/dayorder-api"),
    "#!/usr/bin/env bash\nprintf started > \"$CAPTURE_PATH\"\n",
  );
  writeExecutable(pinnedConfigHub, `#!/usr/bin/env bash
set -Eeuo pipefail
[[ "$1" == run && "$2" == --project && "$3" == shier && "$4" == --env && "$5" == prod && "$6" == -- ]] || exit 64
shift 6
exec "$@"
`);

  const result = runScript(resolve(fixture, "scripts/start-api.sh"), [envPath], {
    CAPTURE_PATH: capturePath,
    DAYORDER_CONFIGHUB_EXECUTABLE: gitShellPath(pinnedConfigHub),
    DAYORDER_TEST_CONFIGHUB_FAIL: "1",
  });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(capturePath, "utf8"), "started");
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
  writeProtected(secretPath, "file-secret-with-at-least-thirty-two-bytes\n");
  writeProtected(
    conflictEnv,
    `DATABASE_URL='postgres://api@db/dayorder'\nDAYORDER_AUTH_HMAC_KEY='direct-secret-with-at-least-32-bytes'\nDAYORDER_AUTH_HMAC_KEY_FILE='${secretPath}'\n`,
  );
  const conflict = runScript(apiScript, [conflictEnv]);
  assert.notEqual(conflict.status, 0);
  assert.match(conflict.stderr, /cannot both be set/i);

  const validEnv = resolve(fixture, "valid.env");
  writeProtected(
    validEnv,
    "DATABASE_URL='postgres://api@db/dayorder'\nDAYORDER_AUTH_HMAC_KEY='direct-secret-with-at-least-32-bytes'\n",
  );
  const missingBinary = runScript(apiScript, [validEnv]);
  assert.notEqual(missingBinary.status, 0);
  assert.match(missingBinary.stderr, /executable.*dayorder-api/i);
});

test("runtime wrappers reject environment and secret symbolic links", (t) => {
  const fixture = createRuntimeFixture();
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const apiScript = resolve(fixture, "scripts/start-api.sh");
  const capturePath = resolve(fixture, "api.capture");
  writeExecutable(
    resolve(fixture, "bin/dayorder-api"),
    "#!/usr/bin/env bash\nprintf '%s' started > \"$CAPTURE_PATH\"\n",
  );

  const environmentTarget = resolve(fixture, "environment-target.env");
  const environmentLink = resolve(fixture, "api.env");
  writeProtected(
    environmentTarget,
    "DATABASE_URL='postgres://api@db/dayorder'\nDAYORDER_AUTH_HMAC_KEY='direct-secret-with-at-least-32-bytes'\n",
  );
  makeRuntimeSymlink(environmentTarget, environmentLink);

  const linkedEnvironment = runScript(apiScript, [environmentLink], { CAPTURE_PATH: capturePath });
  assert.notEqual(linkedEnvironment.status, 0);
  assert.match(linkedEnvironment.stderr, /environment file.*symbolic link/i);
  assert.equal(existsSync(capturePath), false);

  const secretTarget = resolve(fixture, "secret-target");
  const secretLink = resolve(fixture, "api_hmac");
  const regularEnvironment = resolve(fixture, "regular.env");
  writeProtected(secretTarget, "file-secret-with-at-least-thirty-two-bytes\n");
  makeRuntimeSymlink(secretTarget, secretLink);
  writeProtected(
    regularEnvironment,
    `DATABASE_URL='postgres://api@db/dayorder'\nDAYORDER_AUTH_HMAC_KEY_FILE='${secretLink}'\n`,
  );

  const linkedSecret = runScript(apiScript, [regularEnvironment], { CAPTURE_PATH: capturePath });
  assert.notEqual(linkedSecret.status, 0);
  assert.match(linkedSecret.stderr, /secret file.*symbolic link/i);
  assert.equal(existsSync(capturePath), false);
});

test("runtime protected files enforce the approved owner and mode matrix", (t) => {
  const fixture = createRuntimeFixture();
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const apiScript = resolve(fixture, "scripts/start-api.sh");
  const environmentPath = resolve(fixture, "api.env");
  const secretPath = resolve(fixture, "api_hmac");
  const capturePath = resolve(fixture, "api.capture");
  writeProtected(secretPath, "file-secret-with-at-least-thirty-two-bytes\n");
  writeProtected(
    environmentPath,
    `DATABASE_URL='postgres://api@db/dayorder'\nDAYORDER_AUTH_HMAC_KEY_FILE='${secretPath}'\n`,
  );
  writeExecutable(
    resolve(fixture, "bin/dayorder-api"),
    "#!/usr/bin/env bash\nprintf '%s' started > \"$CAPTURE_PATH\"\n",
  );
  const metadata = (overrides) => ({
    CAPTURE_PATH: capturePath,
    DAYORDER_TEST_USE_FAKE_METADATA: "1",
    DAYORDER_TEST_STAT_BASENAME: "api_hmac",
    ...overrides,
  });

  for (const allowed of [
    { DAYORDER_TEST_STAT_UID: "1000", DAYORDER_TEST_STAT_GID: "1000", DAYORDER_TEST_STAT_MODE: "400" },
    { DAYORDER_TEST_STAT_UID: "1000", DAYORDER_TEST_STAT_GID: "1000", DAYORDER_TEST_STAT_MODE: "600" },
    { DAYORDER_TEST_STAT_UID: "0", DAYORDER_TEST_STAT_GID: "2000", DAYORDER_TEST_STAT_MODE: "440" },
    { DAYORDER_TEST_STAT_UID: "0", DAYORDER_TEST_STAT_GID: "2000", DAYORDER_TEST_STAT_MODE: "640" },
  ]) {
    const result = runScript(apiScript, [environmentPath], metadata(allowed));
    assert.equal(result.status, 0, result.stderr);
  }

  for (const rejected of [
    { name: "foreign owner", values: { DAYORDER_TEST_STAT_UID: "2001", DAYORDER_TEST_STAT_MODE: "600" }, message: /owner/i },
    { name: "user-readable by others", values: { DAYORDER_TEST_STAT_UID: "1000", DAYORDER_TEST_STAT_MODE: "604" }, message: /mode|permissions/i },
    { name: "user group-writable", values: { DAYORDER_TEST_STAT_UID: "1000", DAYORDER_TEST_STAT_MODE: "660" }, message: /mode|permissions/i },
    { name: "root group-writable", values: { DAYORDER_TEST_STAT_UID: "0", DAYORDER_TEST_STAT_GID: "2000", DAYORDER_TEST_STAT_MODE: "660" }, message: /mode|permissions/i },
    { name: "root foreign group", values: { DAYORDER_TEST_STAT_UID: "0", DAYORDER_TEST_STAT_GID: "3000", DAYORDER_TEST_STAT_MODE: "640" }, message: /group/i },
  ]) {
    const result = runScript(apiScript, [environmentPath], metadata(rejected.values));
    assert.notEqual(result.status, 0, `${rejected.name} was accepted`);
    assert.match(result.stderr, rejected.message, rejected.name);
  }

  const looseEnvironment = runScript(apiScript, [environmentPath], {
    ...metadata({}),
    DAYORDER_TEST_STAT_BASENAME: "api.env",
    DAYORDER_TEST_STAT_MODE: "644",
  });
  assert.notEqual(looseEnvironment.status, 0);
  assert.match(looseEnvironment.stderr, /environment file.*mode|permissions/i);
});

test("runtime secret files contain exactly one non-empty line", (t) => {
  const fixture = createRuntimeFixture();
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const apiScript = resolve(fixture, "scripts/start-api.sh");
  const environmentPath = resolve(fixture, "api.env");
  const secretPath = resolve(fixture, "api_hmac");
  const capturePath = resolve(fixture, "api.capture");
  writeProtected(
    environmentPath,
    `DATABASE_URL='postgres://api@db/dayorder'\nDAYORDER_AUTH_HMAC_KEY_FILE='${secretPath}'\n`,
  );
  writeExecutable(
    resolve(fixture, "bin/dayorder-api"),
    "#!/usr/bin/env bash\nprintf '%s' \"$DAYORDER_AUTH_HMAC_KEY\" > \"$CAPTURE_PATH\"\n",
  );

  writeProtected(secretPath, "first-line\nsecond-line\n");
  const multiline = runScript(apiScript, [environmentPath], { CAPTURE_PATH: capturePath });
  assert.notEqual(multiline.status, 0);
  assert.match(multiline.stderr, /exactly one.*line|single-line/i);

  writeProtected(secretPath, "\n");
  const empty = runScript(apiScript, [environmentPath], { CAPTURE_PATH: capturePath });
  assert.notEqual(empty.status, 0);
  assert.match(empty.stderr, /non-empty|empty secret/i);

  writeProtected(secretPath, "valid-secret-with-windows-ending\r\n");
  const crlf = runScript(apiScript, [environmentPath], { CAPTURE_PATH: capturePath });
  assert.equal(crlf.status, 0, crlf.stderr);
  assert.equal(readFileSync(capturePath, "utf8"), "valid-secret-with-windows-ending");
});

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

test("service configuration templates delegate database configuration to ConfigHub", () => {
  const api = readFileSync(resolve(root, "deploy/bare-metal/config/api.env.example"), "utf8");
  const worker = readFileSync(resolve(root, "deploy/bare-metal/config/worker.env.example"), "utf8");
  const migrate = readFileSync(resolve(root, "deploy/bare-metal/config/migrate.env.example"), "utf8");

  assert.match(api, /^DAYORDER_AUTH_HMAC_KEY_FILE=/m);
  assert.doesNotMatch(api, /^(?:DATABASE_URL|WORKER_DATABASE_URL|MIGRATION_DATABASE_URL)(?:_FILE)?=/m);

  assert.match(worker, /^DAYORDER_AUTH_HMAC_KEY_FILE=/m);
  assert.match(worker, /^DAYORDER_SMTP_PASSWORD_FILE=/m);
  assert.match(worker, /^DAYORDER_AGENT_HTTP_KEY_FILE=/m);
  assert.doesNotMatch(worker, /^(?:DATABASE_URL|WORKER_DATABASE_URL|MIGRATION_DATABASE_URL)(?:_FILE)?=/m);

  assert.doesNotMatch(migrate, /^(?:DATABASE_URL|WORKER_DATABASE_URL|MIGRATION_DATABASE_URL)(?:_FILE)?=/m);
  assert.doesNotMatch(`${api}\n${worker}\n${migrate}`, /development-only|replace-with|change-me/i);
});

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
});

test("release documentation states the ConfigHub, remaining secret, schema, and rollback safety contract", () => {
  const documents = [
    readFileSync(resolve(root, "README.md"), "utf8"),
    readFileSync(resolve(root, "docs/runbooks/separate-deployment.md"), "utf8"),
  ];
  for (const document of documents) {
    for (const secret of [
      "auth_hmac_key",
      "smtp_password",
      "agent_http_key",
    ]) assert.match(document, new RegExp(`secrets/${secret}`));
    for (const removed of ["api_database_url", "worker_database_url", "migration_database_url"]) {
      assert.doesNotMatch(document, new RegExp(`secrets/${removed}`));
    }
    assert.match(document, /\.confighub\.yaml/);
    assert.match(document, /confighub run --project shier --env prod/);
    assert.match(document, /exactly one non-empty single-line value/);
    assert.match(document, /chmod 0700/);
    assert.match(document, /chmod 0600/);
    assert.match(document, /clean schema at or above the embedded migration floor/);
    assert.match(document, /adjacent-release only/);
    assert.match(document, /restored API failed readiness; manual intervention required/);
  }
});
