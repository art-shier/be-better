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
