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
