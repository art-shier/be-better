import assert from "node:assert/strict";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
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
  // Git for Windows does not provide flock; the fixture models uncontended locking.
  writeExecutable(resolve(directory, "flock"), "#!/usr/bin/env bash\nexit 0\n");
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

function shellPath(path) {
  if (process.platform !== "win32") return path;
  const result = spawnSync("cygpath", ["-u", path], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

function deploymentRealpath(path) {
  if (process.platform !== "win32") return realpathSync(path);
  const result = spawnSync("bash", ["-c", 'realpath -- "$1"', "bash", shellPath(path)], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

function runDeploy(fixture, args, extraEnvironment = {}) {
  const commandPath = process.platform === "win32"
    ? `${shellPath(fixture.commands)}:/usr/bin:/bin`
    : `${fixture.commands}:${process.env.PATH}`;
  const shellArgs = args.map((argument, index) => (
    process.platform === "win32" && args[index - 1] === "--root" ? shellPath(argument) : argument
  ));
  return spawnSync("bash", [
    "-c",
    'PATH="$1"; export PATH; shift; exec bash "$@"',
    "bash",
    commandPath,
    deployer,
    ...shellArgs,
  ], {
    cwd: fixture.runDirectory,
    encoding: "utf8",
    env: {
      ...process.env,
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
  assert.equal(deploymentRealpath(resolve(f.runDirectory, "current-web")), deploymentRealpath(resolve(f.runDirectory, "releases/v1.2.3/web")));
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
  assert.equal(deploymentRealpath(resolve(target, "current-web")), deploymentRealpath(resolve(target, "releases/v1.2.3/web")));
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
  const tar = spawnSync("tar", [
    "-czf", shellPath(archive), "--transform=s#payload#../escape#", "-C", shellPath(unsafe), "payload",
  ], { encoding: "utf8" });
  assert.equal(tar.status, 0, tar.stderr);
  refreshChecksums(resolve(f.releases, "v1.2.4"));
  const unsafeResult = runDeploy(f, ["web", "--version", "v1.2.4"]);
  assert.notEqual(unsafeResult.status, 0);
  assert.match(unsafeResult.stderr, /unsafe archive/);
  assert.equal(existsSync(resolve(f.runDirectory, "escape")), false);
});
