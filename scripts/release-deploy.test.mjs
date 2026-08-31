import assert from "node:assert/strict";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  linkSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
  symlinkSync,
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

function runPosix(command, args, options = {}) {
  if (process.platform !== "win32") return spawnSync(command, args, { encoding: "utf8", ...options });
  const shellArgs = [command, ...args].map((argument) => (
    /^[A-Za-z]:\\/.test(argument) ? shellPath(argument) : argument
  ));
  const prefix = options.cwd ? `cd ${JSON.stringify(shellPath(options.cwd))}; ` : "";
  return spawnSync("C:\\Windows\\System32\\bash.exe", ["-c", `${prefix}exec ${shellArgs.map(JSON.stringify).join(" ")}`], {
    encoding: "utf8",
    env: options.env,
  });
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
printf 'curl %s\\n' "$url" >> "$DAYORDER_TEST_LOG"
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
if [[ "$*" == *show-environment* ]]; then exit 0; fi
printf 'systemctl %s\\n' "$*" >> "$DAYORDER_TEST_LOG"
if [[ "$*" == *restart*dayorder-api.service* && "\${DAYORDER_TEST_SYSTEMCTL_RESTART_FAIL:-0}" == 1 ]]; then exit 1; fi
if [[ "$*" == *stop*dayorder-api.service* && "\${DAYORDER_TEST_SYSTEMCTL_STOP_FAIL:-0}" == 1 ]]; then exit 1; fi
if [[ "$*" == *is-active*dayorder-worker.service* && "\${DAYORDER_TEST_WORKER_FAIL:-0}" == 1 ]]; then exit 1; fi
exit 0
`);
  writeExecutable(resolve(directory, "mv"), `#!/usr/bin/env bash
if [[ "\${DAYORDER_TEST_MV_FAIL_WEB:-0}" == 1 && "$*" == *"/.current-web."* ]]; then exit 1; fi
if [[ "\${DAYORDER_TEST_MV_FAIL_SERVER_ROLLBACK:-0}" == 1 && "$*" == *"/.current-server."* && "$(readlink "\${@: -2:1}")" == *"/releases/v1.2.3/server" ]]; then exit 1; fi
if [[ -n "\${DAYORDER_TEST_MODE_LOG:-}" ]]; then
  source="\${@: -2:1}"; destination="\${@: -1}"
  mode="$(awk -F '\\t' -v path="$source" '$2 == path { value = $1 } END { print value }' "$DAYORDER_TEST_MODE_LOG")"
  [[ -z "$mode" ]] || printf '%s\\t%s\\n' "$mode" "$destination" >> "$DAYORDER_TEST_MODE_LOG"
fi
exec /bin/mv "$@"
`);
  writeExecutable(resolve(directory, "chmod"), `#!/usr/bin/env bash
mode="$1"; shift
if [[ -n "\${DAYORDER_TEST_MODE_LOG:-}" ]]; then
  for path in "$@"; do printf '%s\\t%s\\n' "$mode" "$path" >> "$DAYORDER_TEST_MODE_LOG"; done
fi
exec /bin/chmod "$mode" "$@"
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
    modeLog: resolve(base, "modes.log"),
    latest: "v1.2.3",
  };
  for (const directory of [value.releases, value.runDirectory, value.commands, value.home]) {
    mkdirSync(directory, { recursive: true });
  }
  writeFileSync(value.log, "", "utf8");
  writeFileSync(value.modeLog, "", "utf8");
  makeCommands(value.commands);
  return value;
}

function shellPath(path) {
  if (process.platform !== "win32") return path;
  // This fixture runs the POSIX deployer under WSL on Windows. Adapt only
  // fixture command paths; deployed links remain real POSIX symlinks.
  const match = /^([A-Za-z]):\\([\s\S]*)$/.exec(path);
  assert.ok(match, `expected Windows path: ${path}`);
  return `/mnt/${match[1].toLowerCase()}/${match[2].replaceAll("\\", "/")}`;
}

function deploymentRealpath(path) {
  if (process.platform !== "win32") return realpathSync(path);
  const result = spawnSync("C:\\Windows\\System32\\bash.exe", ["-c", `realpath -- ${JSON.stringify(shellPath(path))}`], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

function deploymentPath(path) {
  return process.platform === "win32" ? shellPath(path) : path;
}

function bashLiteral(value) {
  return `'${value.replaceAll("'", "'\"'\"'")}'`;
}

function deploymentMode(fixture, path) {
  if (process.platform !== "win32") return (statSync(path).mode & 0o777).toString(8);
  const target = shellPath(path);
  const record = readFileSync(fixture.modeLog, "utf8").trim().split("\n").reverse()
    .find((line) => line.endsWith(`\t${target}`));
  assert.ok(record, `missing virtual POSIX mode for ${target}`);
  return Number.parseInt(record.split("\t", 1)[0], 8).toString(8);
}

function readDeploymentFile(path) {
  if (process.platform !== "win32") return readFileSync(path, "utf8");
  const result = runPosix("cat", ["--", shellPath(path)]);
  assert.equal(result.status, 0, result.stderr);
  return result.stdout;
}

function deploymentFileExists(path) {
  if (process.platform !== "win32") return existsSync(path);
  return runPosix("test", ["-f", shellPath(path)]).status === 0;
}

function runDeploy(fixture, args, extraEnvironment = {}) {
  const commandPath = process.platform === "win32"
    ? `${shellPath(fixture.commands)}:/usr/bin:/bin`
    : `${fixture.commands}:${process.env.PATH}`;
  const shellArgs = args.map((argument, index) => (
    process.platform === "win32" && args[index - 1] === "--root" ? shellPath(argument) : argument
  ));
  const bash = process.platform === "win32" ? "C:\\Windows\\System32\\bash.exe" : "bash";
  const deploymentEnvironment = {
    DAYORDER_TEST_RELEASES: shellPath(fixture.releases),
    DAYORDER_TEST_LATEST: fixture.latest,
    DAYORDER_TEST_LOG: shellPath(fixture.log),
    DAYORDER_TEST_MODE_LOG: shellPath(fixture.modeLog),
    HOME: shellPath(fixture.home),
    USER: "dayorder-test",
    XDG_CONFIG_HOME: shellPath(resolve(fixture.home, ".config")),
    ...extraEnvironment,
  };
  const exports = Object.entries(deploymentEnvironment)
    .map(([name, value]) => `export ${name}=${bashLiteral(value)}`).join("; ");
  const launcher = `PATH=${bashLiteral(commandPath)}; export PATH; ${exports}; exec /bin/bash ${[
    shellPath(deployer), ...shellArgs,
  ].map(bashLiteral).join(" ")}`;
  return spawnSync(bash, ["-c", launcher], {
    cwd: fixture.runDirectory,
    encoding: "utf8",
    env: {
      ...process.env,
    },
  });
}

function refreshChecksums(releaseDirectory) {
  const result = runPosix("sha256sum", checksumNames, { cwd: releaseDirectory });
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
  // Git for Windows' Bash requires POSIX paths for fixture helper scripts.
  const shellPackager = shellPath(packager);
  const shellRelease = shellPath(release);
  run("bash", [shellPackager, "web", shellPath(web), shellRelease]);
  for (const arch of ["amd64", "arm64"]) run("bash", [shellPackager, "backend", arch, shellPath(backend), shellRelease]);
  run("bash", [
    shellPackager,
    "metadata",
    tag,
    "0123456789abcdef0123456789abcdef01234567",
    shellPath(deployer),
    shellRelease,
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
  assert.equal(readDeploymentFile(resolve(f.runDirectory, "current-web/index.html")), "<main>v1.2.3</main>\n");
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
  const tar = runPosix("tar", [
    "-czf", shellPath(archive), "--transform=s#payload#../escape#", "-C", shellPath(unsafe), "payload",
  ], { encoding: "utf8" });
  assert.equal(tar.status, 0, tar.stderr);
  refreshChecksums(resolve(f.releases, "v1.2.4"));
  const unsafeResult = runDeploy(f, ["web", "--version", "v1.2.4"]);
  assert.notEqual(unsafeResult.status, 0);
  assert.match(unsafeResult.stderr, /unsafe archive/);
  assert.equal(existsSync(resolve(f.runDirectory, "escape")), false);
});

test("deployer rejects a noncanonical Manifest with assets outside the assets object", (t) => {
  const f = fixture(t);
  const release = makeAssetRelease(f, "v1.2.3");
  writeFileSync(resolve(release, "release-manifest.json"), [
    "{",
    '  "schemaVersion": 1,',
    '  "version": "v1.2.3",',
    '  "revision": "0123456789abcdef0123456789abcdef01234567",',
    '  "deployScriptVersion": 1,',
    '  "assets": {},',
    '  "web": "dayorder-web.tar.gz",',
    '  "serverAmd64": "dayorder-server-linux-amd64.tar.gz",',
    '  "serverArm64": "dayorder-server-linux-arm64.tar.gz",',
    '  "workerAmd64": "dayorder-worker-linux-amd64.tar.gz",',
    '  "workerArm64": "dayorder-worker-linux-arm64.tar.gz"',
    "}\n",
  ].join("\n"), "utf8");
  refreshChecksums(release);
  const result = runDeploy(f, ["web"]);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /manifest/i);
  assert.equal(existsSync(resolve(f.runDirectory, "current-web")), false);
});

test("deployer rejects checksum records with trailing fields", (t) => {
  const f = fixture(t);
  const release = makeAssetRelease(f, "v1.2.3");
  const sums = readFileSync(resolve(release, "SHA256SUMS"), "utf8");
  writeFileSync(resolve(release, "SHA256SUMS"), sums.replace(
    /^([0-9a-f]{64}) [ *]release-manifest\.json$/m,
    "$1  release-manifest.json trailing-junk",
  ), "utf8");
  const result = runDeploy(f, ["web"]);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /SHA-256/);
  assert.equal(existsSync(resolve(f.runDirectory, "current-web")), false);
});

test("deployer rejects a managed releases path that resolves outside the root", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  const outside = resolve(f.base, "outside");
  mkdirSync(outside);
  symlinkSync(outside, resolve(f.runDirectory, "releases"), process.platform === "win32" ? "junction" : "dir");
  const result = runDeploy(f, ["web"]);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /managed releases|symbolic link|escapes root/);
  assert.equal(existsSync(resolve(outside, "v1.2.3")), false);
});

test("Server and Worker prepare verified architecture-specific release trees", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  const first = runDeploy(f, ["all"]);
  assert.notEqual(first.status, 0);
  const server = runDeploy(f, ["server"]);
  assert.equal(server.status, 0, server.stderr);
  const worker = runDeploy(f, ["worker"]);
  assert.equal(worker.status, 0, worker.stderr);
  assert.equal(existsSync(resolve(f.runDirectory, "releases/v1.2.3/server/bin/dayorder-api")), true);
  assert.equal(existsSync(resolve(f.runDirectory, "releases/v1.2.3/worker/bin/dayorder-worker")), true);
});

test("deployer pins latest through the exact GitHub HTTPS release bases", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  const result = runDeploy(f, ["server"]);
  assert.notEqual(result.status, 0);
  assert.deepEqual(readFileSync(f.log, "utf8").trim().split("\n"), [
    "curl https://github.com/art-shier/be-better/releases/latest/download/release-manifest.json",
    "curl https://github.com/art-shier/be-better/releases/download/v1.2.3/release-manifest.json",
    "curl https://github.com/art-shier/be-better/releases/download/v1.2.3/SHA256SUMS",
    "curl https://github.com/art-shier/be-better/releases/download/v1.2.3/dayorder-server-linux-amd64.tar.gz",
  ]);
});

test("deployer rejects unsafe hard-link archive members before installation", (t) => {
  const f = fixture(t);
  const release = makeAssetRelease(f, "v1.2.3");
  const source = resolve(f.base, "hard-link-web");
  write(resolve(source, "index.html"), "<main>hard-link</main>\n");
  write(resolve(source, "assets/app.js"), "console.log('hard-link')\n");
  linkSync(resolve(source, "index.html"), resolve(source, "linked-index.html"));
  const archive = resolve(release, "dayorder-web.tar.gz");
  const tar = runPosix("tar", ["-czf", shellPath(archive), "-C", shellPath(source), "."]);
  assert.equal(tar.status, 0, tar.stderr);
  refreshChecksums(release);
  const result = runDeploy(f, ["web"]);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /unsafe archive member type/);
  assert.equal(existsSync(resolve(f.runDirectory, "current-web")), false);
});

test("first all deployment creates persistent templates and stops before migration or switching", (t) => {
  const f = fixture(t); makeAssetRelease(f, "v1.2.3");
  const result = runDeploy(f, ["all"]);
  assert.notEqual(result.status, 0);
  for (const name of ["api.env", "migrate.env", "worker.env"]) {
    const path = resolve(f.runDirectory, "dayorder-config", name);
    assert.equal(existsSync(path), true);
    assert.equal(deploymentMode(f, path), "600");
    assert.match(readFileSync(path, "utf8"), new RegExp(deploymentPath(resolve(f.runDirectory, "dayorder-config/secrets")).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
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

test("Server migrates up and checks before activation, then passes readiness", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const result = runDeploy(f, ["server"]);
  assert.equal(result.status, 0, result.stderr);
  const log = readFileSync(f.log, "utf8");
  assert.ok(log.indexOf("migrate up") < log.indexOf("migrate check"));
  assert.ok(log.indexOf("migrate check") < log.indexOf("systemctl --user daemon-reload"));
  assert.equal(deploymentRealpath(resolve(f.runDirectory, "current-server")), deploymentRealpath(resolve(f.runDirectory, "releases/v1.2.3/server")));
  const unit = readFileSync(resolve(f.home, ".config/systemd/user/dayorder-api.service"), "utf8");
  assert.match(unit, /Restart=on-failure/);
  assert.match(unit, /TimeoutStopSec=30/);
  assert.match(unit, new RegExp(deploymentPath(resolve(f.runDirectory, "current-server/scripts/start-api.sh")).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
});

test("Worker is a separate enabled user service with a 60 second stop timeout", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const result = runDeploy(f, ["worker"]);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(deploymentRealpath(resolve(f.runDirectory, "current-worker")), deploymentRealpath(resolve(f.runDirectory, "releases/v1.2.3/worker")));
  const unit = readFileSync(resolve(f.home, ".config/systemd/user/dayorder-worker.service"), "utf8");
  assert.match(unit, /TimeoutStopSec=60/);
  assert.match(unit, /worker\.env/);
});

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
  assert.equal(deploymentRealpath(resolve(f.runDirectory, "current-server")), deploymentRealpath(resolve(f.runDirectory, "releases/v1.2.3/server")));
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
    assert.equal(deploymentRealpath(resolve(f.runDirectory, `current-${name}`)), deploymentRealpath(resolve(f.runDirectory, `releases/v1.2.3/${name}`)));
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
    assert.equal(deploymentRealpath(resolve(f.runDirectory, `current-${name}`)), deploymentRealpath(resolve(f.runDirectory, `releases/v1.2.3/${name}`)));
  }
  const logBeforeRepeat = readFileSync(f.log, "utf8");
  const repeat = runDeploy(f, ["all"]);
  assert.equal(repeat.status, 0, repeat.stderr);
  assert.equal((repeat.stdout.match(/already deployed/g) ?? []).length, 3);
  const repeatLog = readFileSync(f.log, "utf8").slice(logBeforeRepeat.length);
  assert.doesNotMatch(repeatLog, /migrate|daemon-reload| enable | restart | start /);
});

test("Web link failure during all restores Server and Worker while Web stays old", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const initial = runDeploy(f, ["all", "--version", "v1.2.3"]);
  assert.equal(initial.status, 0, initial.stderr);
  makeAssetRelease(f, "v1.2.4");
  writeFileSync(f.log, "", "utf8");
  const failed = runDeploy(f, ["all", "--version", "v1.2.4"], { DAYORDER_TEST_MV_FAIL_WEB: "1" });
  assert.notEqual(failed.status, 0);
  for (const name of ["server", "worker", "web"]) {
    assert.equal(deploymentRealpath(resolve(f.runDirectory, `current-${name}`)), deploymentRealpath(resolve(f.runDirectory, `releases/v1.2.3/${name}`)));
  }
  assert.doesNotMatch(failed.stdout, /Web v1\.2\.4 deployed/);
  const log = readFileSync(f.log, "utf8");
  assert.match(log, /systemctl --user restart dayorder-api\.service/);
  assert.match(log, /systemctl --user restart dayorder-worker\.service/);
});

test("rollback restart failure reports manual intervention while restoring the old Server link", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const initial = runDeploy(f, ["server", "--version", "v1.2.3"]);
  assert.equal(initial.status, 0, initial.stderr);
  makeAssetRelease(f, "v1.2.4");
  const failed = runDeploy(f, ["server", "--version", "v1.2.4"], { DAYORDER_TEST_SYSTEMCTL_RESTART_FAIL: "1" });
  assert.notEqual(failed.status, 0);
  assert.match(failed.stderr, /rollback failed.*manual intervention/i);
  assert.equal(deploymentRealpath(resolve(f.runDirectory, "current-server")), deploymentRealpath(resolve(f.runDirectory, "releases/v1.2.3/server")));
});

test("rollback link failure reports manual intervention and leaves the new Server link visible", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const initial = runDeploy(f, ["server", "--version", "v1.2.3"]);
  assert.equal(initial.status, 0, initial.stderr);
  makeAssetRelease(f, "v1.2.4");
  const failed = runDeploy(f, ["server", "--version", "v1.2.4"], {
    DAYORDER_TEST_HEALTH_FAIL: "1",
    DAYORDER_TEST_MV_FAIL_SERVER_ROLLBACK: "1",
  });
  assert.notEqual(failed.status, 0);
  assert.match(failed.stderr, /rollback failed.*manual intervention/i);
  assert.equal(deploymentRealpath(resolve(f.runDirectory, "current-server")), deploymentRealpath(resolve(f.runDirectory, "releases/v1.2.4/server")));
});

test("rollback service-stop failure reports manual intervention after removing a new Server link", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const failed = runDeploy(f, ["server"], {
    DAYORDER_TEST_HEALTH_FAIL: "1",
    DAYORDER_TEST_SYSTEMCTL_STOP_FAIL: "1",
  });
  assert.notEqual(failed.status, 0);
  assert.match(failed.stderr, /rollback failed.*manual intervention/i);
  assert.equal(existsSync(resolve(f.runDirectory, "current-server")), false);
});

test("service deployment rejects control characters in the root before migration", (t) => {
  const f = fixture(t); makeAssetRelease(f, "v1.2.3");
  const unsafeRoot = resolve(f.base, "unsafe\nroot");
  const result = runDeploy(f, ["server", "--root", unsafeRoot]);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /control character/i);
  assert.doesNotMatch(readFileSync(f.log, "utf8"), /migrate|systemctl/);
});

test("configuration templates preserve roots with sed-special characters", (t) => {
  const f = fixture(t); makeAssetRelease(f, "v1.2.3");
  const specialRoot = resolve(f.base, process.platform === "win32" ? "root&hash#backslash" : "root&hash#back\\slash");
  const result = runDeploy(f, ["all", "--root", specialRoot]);
  assert.notEqual(result.status, 0);
  const secrets = deploymentPath(resolve(specialRoot, "dayorder-config/secrets"));
  for (const name of ["api.env", "migrate.env", "worker.env"]) {
    assert.match(readFileSync(resolve(specialRoot, "dayorder-config", name), "utf8"), new RegExp(secrets.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
});

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
  assert.equal(deploymentRealpath(resolve(f.runDirectory, "current-web")), deploymentRealpath(resolve(f.runDirectory, `releases/${manifest.version}/web`)));
  assert.equal(deploymentFileExists(resolve(f.runDirectory, "current-web/index.html")), true);
});
