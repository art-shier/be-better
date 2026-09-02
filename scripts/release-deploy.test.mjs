import assert from "node:assert/strict";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  linkSync,
  lstatSync,
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
  const prefix = options.cwd ? `cd -- ${bashLiteral(shellPath(options.cwd))}; ` : "";
  return spawnSync("C:\\Windows\\System32\\wsl.exe", [
    "--exec", "/bin/bash", "-c", `${prefix}exec ${shellArgs.map(bashLiteral).join(" ")}`,
  ], {
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
  target="$(readlink -f "$PWD/current-server" 2>/dev/null || true)"
  [[ "\${DAYORDER_TEST_HEALTH_FAIL:-0}" != 1 ]] || exit 1
  [[ -z "\${DAYORDER_TEST_HEALTH_FAIL_VERSION:-}" || "$target" != *"/releases/$DAYORDER_TEST_HEALTH_FAIL_VERSION/server" ]] || exit 1
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
printf '%s\\n' "\${DAYORDER_TEST_MACHINE:-x86_64}"
`);
  writeExecutable(resolve(directory, "id"), `#!/usr/bin/env bash
case "$*" in
  -u) printf '%s\\n' "\${DAYORDER_TEST_ID_UID:-1000}" ;;
  -g) printf '%s\\n' "\${DAYORDER_TEST_ID_GID:-1000}" ;;
  -G) printf '%s\\n' "\${DAYORDER_TEST_ID_GROUPS:-1000}" ;;
  -un) printf '%s\\n' "\${DAYORDER_TEST_ID_USER:-dayorder-test}" ;;
  *) exec /usr/bin/id "$@" ;;
esac
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
source="\${@: -2:1}"; destination="\${@: -1}"
if [[ "\${DAYORDER_TEST_MV_FAIL_WEB:-0}" == 1 && "$source" == *"current-web"* && "$destination" == */current-web ]]; then exit 1; fi
if [[ "\${DAYORDER_TEST_MV_FAIL_SERVER_ROLLBACK:-0}" == 1 && "$source" == *"current-server"* && "$(readlink "$source")" == *"/releases/v1.2.3/server" ]]; then exit 1; fi
if [[ -n "\${DAYORDER_TEST_MODE_LOG:-}" ]]; then
  mode="$(awk -F '\\t' -v path="$source" '$2 == path { value = $1 } END { print value }' "$DAYORDER_TEST_MODE_LOG")"
  [[ -z "$mode" ]] || printf '%s\\t%s\\n' "$mode" "$destination" >> "$DAYORDER_TEST_MODE_LOG"
fi
exec /bin/mv "$@"
`);
  writeExecutable(resolve(directory, "ln"), `#!/usr/bin/env bash
destination="\${@: -1}"
if [[ -n "\${DAYORDER_TEST_LN_FAIL_COMPONENT:-}" && "$destination" == *"current-$DAYORDER_TEST_LN_FAIL_COMPONENT"* ]]; then
  /bin/ln -s -- "$DAYORDER_TEST_PREEXISTING_LINK_TARGET" "$destination"
  exit 73
fi
if [[ "$*" != *" -s "* && "$1" != -s && -n "\${DAYORDER_TEST_MODE_LOG:-}" ]]; then
  source="\${@: -2:1}"
  mode="$(awk -F '\\t' -v path="$source" '$2 == path { value = $1 } END { print value }' "$DAYORDER_TEST_MODE_LOG")"
  [[ -z "$mode" ]] || printf '%s\\t%s\\n' "$mode" "$destination" >> "$DAYORDER_TEST_MODE_LOG"
fi
exec /bin/ln "$@"
`);
  writeExecutable(resolve(directory, "tar"), `#!/usr/bin/env bash
if [[ "\${DAYORDER_TEST_TAR_LIST_FAIL:-0}" == 1 && "$*" == *"-tzf"* ]]; then exit 74; fi
exec /usr/bin/tar "$@"
`);
  writeExecutable(resolve(directory, "find"), `#!/usr/bin/env bash
set -Eeuo pipefail
if [[ -n "\${DAYORDER_TEST_FIND_FAIL_PATH:-}" && "$1" == "$DAYORDER_TEST_FIND_FAIL_PATH" ]]; then exit 75; fi
exec /usr/bin/find "$@"
`);
  writeExecutable(resolve(directory, "stat"), `#!/usr/bin/env bash
format=""; path="\${@: -1}"
while [[ $# -gt 0 ]]; do
  case "$1" in -c|--format) format="$2"; shift 2 ;; *) shift ;; esac
done
if [[ -n "\${DAYORDER_TEST_BAD_OWNER_PATH:-}" && "$path" == "$DAYORDER_TEST_BAD_OWNER_PATH" && "$format" == %u ]]; then
  printf '999999\\n'
  exit
fi
case "$format" in
  %u) printf '%s\\n' "\${DAYORDER_TEST_ID_UID:-1000}" ;;
  %g) printf '%s\\n' "\${DAYORDER_TEST_ID_GID:-1000}" ;;
  %a)
    mode="$(awk -F '\\t' -v target="$path" '$2 == target { value = $1 } END { print value }' "$DAYORDER_TEST_MODE_LOG")"
    if [[ -n "$mode" ]]; then printf '%s\\n' "$mode"; else /usr/bin/stat -c %a -- "$path"; fi
    ;;
  *) exec /usr/bin/stat -c "$format" -- "$path" ;;
esac
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
  writeExecutable(resolve(directory, "confighub"), `#!/usr/bin/env bash
set -Eeuo pipefail
printf 'confighub cwd=%s args=%s\n' "$PWD" "$*" >> "$DAYORDER_TEST_LOG"
if [[ "\${DAYORDER_TEST_CONFIGHUB_FAIL:-0}" == 1 ]]; then
  printf 'confighub: access denied for shier/prod\n' >&2
  exit 77
fi
[[ "$1" == run && "$2" == --project && "$3" == shier && "$4" == --env && "$5" == prod && "$6" == -- ]] || exit 64
shift 6
exec "$@"
`);
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
  const result = runPosix("realpath", ["--", path]);
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

function deploymentPathExists(path) {
  if (process.platform === "win32") {
    return runPosix("test", ["-e", shellPath(path)]).status === 0
      || runPosix("test", ["-L", shellPath(path)]).status === 0;
  }
  try {
    lstatSync(path);
    return true;
  } catch (error) {
    if (error?.code === "ENOENT") return false;
    throw error;
  }
}

function makeDeploymentSymlink(target, link) {
  const result = runPosix("ln", ["-s", target, link]);
  assert.equal(result.status, 0, result.stderr);
}

function recordDeploymentMode(fixture, path, mode) {
  writeFileSync(fixture.modeLog, `${mode}\t${deploymentPath(path)}\n`, { flag: "a" });
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
    USER: "untrusted-environment-user",
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
  write(resolve(backend, "config/api.env.example"), "DAYORDER_ENV=production\nDAYORDER_AUTH_HMAC_KEY_FILE=/etc/dayorder/secrets/auth_hmac_key\n");
  write(resolve(backend, "config/migrate.env.example"), "DAYORDER_ENV=production\n");
  write(
    resolve(backend, "config/worker.env.example"),
    "DAYORDER_ENV=production\nDAYORDER_AUTH_HMAC_KEY_FILE=/etc/dayorder/secrets/auth_hmac_key\n"
      + "DAYORDER_SMTP_PASSWORD_FILE=/etc/dayorder/secrets/smtp_password\n"
      + "DAYORDER_AGENT_HTTP_KEY_FILE=/etc/dayorder/secrets/agent_http_key\n",
  );
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

test("deployment path existence detects valid and dangling POSIX symlinks", (t) => {
  const f = fixture(t);
  const target = resolve(f.base, "target");
  const validLink = resolve(f.base, "valid-link");
  const danglingLink = resolve(f.base, "dangling-link");
  mkdirSync(target);
  for (const [source, link] of [
    [target, validLink],
    [resolve(f.base, "missing-target"), danglingLink],
  ]) {
    const created = runPosix("ln", ["-s", source, link]);
    assert.equal(created.status, 0, created.stderr);
  }

  if (process.platform === "win32") {
    assert.equal(existsSync(validLink), false, "native Node cannot see the WSL-created valid symlink");
  }
  assert.equal(existsSync(danglingLink), false, "native existence follows a dangling symlink target");
  assert.equal(deploymentPathExists(validLink), true);
  assert.equal(deploymentPathExists(danglingLink), true);
  assert.equal(deploymentPathExists(resolve(f.base, "missing-entry")), false);
});

test("WSL launcher preserves special characters in commands, arguments, and working directories", {
  skip: process.platform !== "win32",
}, (t) => {
  const f = fixture(t);
  const workingDirectory = resolve(f.base, "working $() ; ' directory");
  const command = resolve(f.base, "command $() ; ' script.sh");
  const argument = "argument $() ; ' \" with spaces";
  mkdirSync(workingDirectory);
  writeExecutable(command, "#!/usr/bin/env bash\nprintf '%s\\n%s\\n' \"$PWD\" \"$1\"\n");

  const result = runPosix(command, [argument], { cwd: workingDirectory });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, `${shellPath(workingDirectory)}\n${argument}\n`);
  assert.equal(deploymentRealpath(workingDirectory), shellPath(workingDirectory));
});

test("deployer rejects invalid commands, versions, roots, and architectures", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  assert.match(runDeploy(f, ["api"]).stderr, /web\|server\|worker\|all/);
  assert.match(runDeploy(f, ["web", "--version", "latest"]).stderr, /vX\.Y\.Z/);
  assert.match(runDeploy(f, ["web", "--version", ""]).stderr, /version must match vX\.Y\.Z/);
  assert.match(runDeploy(f, ["upgrade", "web", "--version", ""]).stderr, /upgrade.*--version/i);
  assert.match(runDeploy(f, ["restart", "server", "--version", ""]).stderr, /restart.*--version/i);
  assert.match(runDeploy(f, ["web", "--root", f.home]).stderr, /home directory/);
  const unsupported = runDeploy(f, ["server"], { DAYORDER_TEST_MACHINE: "riscv64" });
  assert.match(unsupported.stderr, /unsupported architecture/);
});

test("service lifecycle commands manage API and Worker without downloading releases", (t) => {
  const f = fixture(t);
  const web = resolve(f.runDirectory, "releases/v1.2.3/web");
  mkdirSync(web, { recursive: true });
  makeDeploymentSymlink(web, resolve(f.runDirectory, "current-web"));
  const expectations = new Map([
    ["start", "dayorder-api.service dayorder-worker.service"],
    ["stop", "dayorder-worker.service dayorder-api.service"],
    ["restart", "dayorder-api.service dayorder-worker.service"],
    ["status", "dayorder-api.service dayorder-worker.service"],
  ]);

  for (const [action, services] of expectations) {
    writeFileSync(f.log, "", "utf8");
    const result = runDeploy(f, [action, "all", "--root", f.runDirectory]);
    assert.equal(result.status, 0, `${action}: ${result.stderr}`);
    const log = readFileSync(f.log, "utf8");
    assert.match(log, new RegExp(`systemctl --user ${action} ${services}`));
    assert.doesNotMatch(log, /curl |confighub|loginctl/);
    assert.match(result.stdout, /Web.*v1\.2\.3/i);
  }

  writeFileSync(f.log, "", "utf8");
  const untouchedRoot = resolve(f.base, "lifecycle-must-not-create-this-root");
  const failedStop = runDeploy(f, ["stop", "server", "--root", untouchedRoot], {
    DAYORDER_TEST_SYSTEMCTL_STOP_FAIL: "1",
  });
  assert.notEqual(failedStop.status, 0);
  assert.equal(existsSync(untouchedRoot), false);
  assert.match(readFileSync(f.log, "utf8"), /systemctl --user stop dayorder-api\.service/);

  writeFileSync(f.log, "", "utf8");
  const webOnly = runDeploy(f, ["restart", "web", "--root", f.runDirectory]);
  assert.equal(webOnly.status, 0, webOnly.stderr);
  assert.equal(readFileSync(f.log, "utf8"), "");
  assert.match(webOnly.stdout, /Web has no managed systemd service/i);
});

test("redeploy reactivates the same version and repairs units without downloading component archives", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const initial = runDeploy(f, ["all", "--version", "v1.2.3"]);
  assert.equal(initial.status, 0, initial.stderr);
  rmSync(resolve(f.home, ".config/systemd/user/dayorder-api.service"));
  rmSync(resolve(f.home, ".config/systemd/user/dayorder-worker.service"));
  writeFileSync(f.log, "", "utf8");

  const redeployed = runDeploy(f, ["redeploy", "all", "--version", "v1.2.3"]);

  assert.equal(redeployed.status, 0, redeployed.stderr);
  assert.equal(existsSync(resolve(f.home, ".config/systemd/user/dayorder-api.service")), true);
  assert.equal(existsSync(resolve(f.home, ".config/systemd/user/dayorder-worker.service")), true);
  assert.doesNotMatch(redeployed.stdout, /already deployed/);
  const log = readFileSync(f.log, "utf8");
  assert.match(log, /migrate up/);
  assert.match(log, /systemctl --user restart dayorder-api\.service/);
  assert.match(log, /systemctl --user restart dayorder-worker\.service/);
  assert.deepEqual(log.split("\n").filter((line) => line.startsWith("curl ")), [
    "curl https://github.com/art-shier/be-better/releases/download/v1.2.3/release-manifest.json",
    "curl https://github.com/art-shier/be-better/releases/download/v1.2.3/SHA256SUMS",
  ]);
});

test("upgrade resolves latest and activates the newer release", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const initial = runDeploy(f, ["all", "--version", "v1.2.3"]);
  assert.equal(initial.status, 0, initial.stderr);
  makeAssetRelease(f, "v1.2.4");
  writeFileSync(f.log, "", "utf8");

  const upgraded = runDeploy(f, ["upgrade", "all"]);

  assert.equal(upgraded.status, 0, upgraded.stderr);
  for (const name of ["server", "worker", "web"]) {
    assert.equal(
      deploymentRealpath(resolve(f.runDirectory, `current-${name}`)),
      deploymentRealpath(resolve(f.runDirectory, `releases/v1.2.4/${name}`)),
    );
  }
  assert.match(readFileSync(f.log, "utf8"), /releases\/latest\/download\/release-manifest\.json/);
  const pinned = runDeploy(f, ["upgrade", "all", "--version", "v1.2.3"]);
  assert.notEqual(pinned.status, 0);
  assert.match(pinned.stderr, /upgrade.*--version/i);
});

test("deployer derives architecture and operator identity from uname and id", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  const identityEnvironment = {
    DAYORDER_TEST_UNAME: "riscv64",
    DAYORDER_TEST_MACHINE: "aarch64",
    DAYORDER_TEST_ID_USER: "dayorder-operator",
    USER: "spoofed-user",
  };
  const first = runDeploy(f, ["all"], identityEnvironment);
  assert.notEqual(first.status, 0);
  assert.match(first.stderr, /configuration templates were created/);
  assert.match(readFileSync(f.log, "utf8"), /dayorder-server-linux-arm64\.tar\.gz/);
  writeFileSync(f.log, "", "utf8");

  const linger = runDeploy(f, ["server"], { ...identityEnvironment, DAYORDER_TEST_LINGER: "no" });

  assert.notEqual(linger.status, 0);
  assert.match(linger.stderr, /loginctl enable-linger "dayorder-operator"/);
  const log = readFileSync(f.log, "utf8");
  assert.match(log, /loginctl show-user dayorder-operator/);
  assert.doesNotMatch(log, /spoofed-user/);
});

test("backend deployment stops before configuration when the ConfigHub CLI is missing", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  rmSync(resolve(f.commands, "confighub"));

  const result = runDeploy(f, ["all"]);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /confighub is required/i);
  assert.equal(existsSync(resolve(f.runDirectory, "dayorder-config")), false);
  assert.doesNotMatch(readFileSync(f.log, "utf8"), /migrate|systemctl/);
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

test("archive listing errors fail closed before extraction or link switching", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");

  const result = runDeploy(f, ["web"], { DAYORDER_TEST_TAR_LIST_FAIL: "1" });

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /list archive/i);
  assert.equal(deploymentPathExists(resolve(f.runDirectory, "current-web")), false);
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

test("cached component reuse rejects multiple unsafe nodes", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  const first = runDeploy(f, ["web"]);
  assert.equal(first.status, 0, first.stderr);
  const cachedWeb = resolve(f.runDirectory, "releases/v1.2.3/web");
  makeDeploymentSymlink("missing-one", resolve(cachedWeb, "unsafe-one"));
  makeDeploymentSymlink("missing-two", resolve(cachedWeb, "unsafe-two"));

  const reused = runDeploy(f, ["web"]);

  assert.notEqual(reused.status, 0);
  assert.match(reused.stderr, /existing version directory contains unsafe nodes/i);
});

test("cached component reuse fails closed when its safety scan fails", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  const first = runDeploy(f, ["web"]);
  assert.equal(first.status, 0, first.stderr);
  const cachedWeb = deploymentPath(resolve(f.runDirectory, "releases/v1.2.3/web"));

  const reused = runDeploy(f, ["web"], { DAYORDER_TEST_FIND_FAIL_PATH: cachedWeb });

  assert.notEqual(reused.status, 0);
  assert.match(reused.stderr, /could not inspect existing version directory/i);
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

test("deployment locking never follows a mutable lock symlink", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  const outside = resolve(f.base, "outside-lock");
  writeFileSync(outside, "outside-lock-sentinel\n", "utf8");
  makeDeploymentSymlink(outside, resolve(f.runDirectory, ".dayorder-deploy.lock"));

  const result = runDeploy(f, ["web"]);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /lock.*symbolic link/i);
  assert.equal(readFileSync(outside, "utf8"), "outside-lock-sentinel\n");
  assert.equal(deploymentPathExists(resolve(f.runDirectory, "current-web")), false);
});

test("configuration directory symlinks are rejected without external writes", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  const outside = resolve(f.base, "outside-config");
  mkdirSync(outside);
  makeDeploymentSymlink(outside, resolve(f.runDirectory, "dayorder-config"));

  const result = runDeploy(f, ["all"]);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /configuration directory.*symbolic link/i);
  assert.deepEqual(readFileSync(f.log, "utf8").match(/migrate|systemctl/g), null);
  assert.deepEqual(runPosix("find", [outside, "-mindepth", "1", "-print"]).stdout, "");
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
    assert.doesNotMatch(readFileSync(path, "utf8"), /(?:DATABASE_URL|WORKER_DATABASE_URL|MIGRATION_DATABASE_URL)(?:_FILE)?=/);
  }
  const secretsPath = new RegExp(deploymentPath(resolve(f.runDirectory, "dayorder-config/secrets")).replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  assert.match(readFileSync(resolve(f.runDirectory, "dayorder-config/api.env"), "utf8"), secretsPath);
  assert.match(readFileSync(resolve(f.runDirectory, "dayorder-config/worker.env"), "utf8"), secretsPath);
  assert.equal(existsSync(resolve(f.runDirectory, "dayorder-config/.confighub.yaml")), false);
  assert.equal(existsSync(resolve(f.runDirectory, "current-server")), false);
  assert.equal(existsSync(resolve(f.runDirectory, "current-worker")), false);
  assert.doesNotMatch(readFileSync(f.log, "utf8"), /migrate|systemctl/);
});

test("first-run output names every one-line secret and exact permission commands", (t) => {
  const f = fixture(t); makeAssetRelease(f, "v1.2.3");

  const result = runDeploy(f, ["all"]);

  assert.notEqual(result.status, 0);
  for (const secret of ["auth_hmac_key", "smtp_password", "agent_http_key"]) {
    assert.match(result.stderr, new RegExp(`secrets/${secret}`));
  }
  for (const removed of ["api_database_url", "worker_database_url", "migration_database_url"]) {
    assert.doesNotMatch(result.stderr, new RegExp(`secrets/${removed}`));
  }
  assert.match(result.stderr, /dayorder-config\/\.confighub\.yaml/);
  assert.match(result.stderr, /single-line/i);
  assert.match(result.stderr, /chmod 0700/);
  assert.match(result.stderr, /chmod 0600/);
});

test("ConfigHub authorization failure is printed and stops deployment before migration or systemd", (t) => {
  const f = configuredFixture(t, "v1.2.3");

  const result = runDeploy(f, ["all"], { DAYORDER_TEST_CONFIGHUB_FAIL: "1" });

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /confighub: access denied for shier\/prod/i);
  assert.match(result.stderr, /ConfigHub preflight failed/i);
  assert.equal(existsSync(resolve(f.runDirectory, "current-server")), false);
  assert.equal(existsSync(resolve(f.runDirectory, "current-worker")), false);
  assert.doesNotMatch(readFileSync(f.log, "utf8"), /migrate|systemctl/);
});

test("configuration file symlinks are rejected before migration", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const config = resolve(f.runDirectory, "dayorder-config/api.env");
  const outside = resolve(f.base, "outside-api.env");
  writeFileSync(outside, "outside-config-sentinel\n", "utf8");
  assert.equal(runPosix("rm", ["-f", config]).status, 0);
  makeDeploymentSymlink(outside, config);
  writeFileSync(f.log, "", "utf8");

  const result = runDeploy(f, ["server"]);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /configuration file.*symbolic link/i);
  assert.equal(readFileSync(outside, "utf8"), "outside-config-sentinel\n");
  assert.doesNotMatch(readFileSync(f.log, "utf8"), /migrate|systemctl/);
});

test("existing configuration must be owned by the operator with mode 0600", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const config = resolve(f.runDirectory, "dayorder-config/api.env");
  recordDeploymentMode(f, config, "0644");

  const loose = runDeploy(f, ["server"]);

  assert.notEqual(loose.status, 0);
  assert.match(loose.stderr, /mode 0600/i);
  assert.doesNotMatch(readFileSync(f.log, "utf8"), /migrate|systemctl/);

  recordDeploymentMode(f, config, "0600");
  const foreign = runDeploy(f, ["server"], { DAYORDER_TEST_BAD_OWNER_PATH: deploymentPath(config) });
  assert.notEqual(foreign.status, 0);
  assert.match(foreign.stderr, /owned by the deployment user/i);
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
  assert.match(log, /confighub cwd=.*dayorder-config args=run --project shier --env prod -- true/);
  assert.ok(log.indexOf("confighub cwd=") < log.indexOf("migrate up"));
  assert.ok(log.indexOf("migrate up") < log.indexOf("migrate check"));
  assert.ok(log.indexOf("migrate check") < log.indexOf("systemctl --user daemon-reload"));
  assert.equal(deploymentRealpath(resolve(f.runDirectory, "current-server")), deploymentRealpath(resolve(f.runDirectory, "releases/v1.2.3/server")));
  const unit = readFileSync(resolve(f.home, ".config/systemd/user/dayorder-api.service"), "utf8");
  assert.match(
    unit,
    new RegExp(`^WorkingDirectory=${deploymentPath(resolve(f.runDirectory, "current-server")).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`, "m"),
  );
  assert.match(unit, /Restart=on-failure/);
  assert.match(unit, /TimeoutStopSec=30/);
  assert.match(
    unit,
    new RegExp(`^Environment="DAYORDER_CONFIGHUB_EXECUTABLE=${deploymentPath(resolve(f.commands, "confighub")).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}"$`, "m"),
  );
  assert.match(unit, new RegExp(deploymentPath(resolve(f.runDirectory, "current-server/scripts/start-api.sh")).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
});

test("Worker is a separate enabled user service with a 60 second stop timeout", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const result = runDeploy(f, ["worker"]);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(deploymentRealpath(resolve(f.runDirectory, "current-worker")), deploymentRealpath(resolve(f.runDirectory, "releases/v1.2.3/worker")));
  const unit = readFileSync(resolve(f.home, ".config/systemd/user/dayorder-worker.service"), "utf8");
  assert.match(
    unit,
    new RegExp(`^WorkingDirectory=${deploymentPath(resolve(f.runDirectory, "current-worker")).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`, "m"),
  );
  assert.match(unit, /TimeoutStopSec=60/);
  assert.match(unit, /worker\.env/);
});

test("service units preserve custom roots with spaces and literal percent signs", (t) => {
  const f = fixture(t);
  makeAssetRelease(f, "v1.2.3");
  const customRoot = resolve(f.base, "custom root%files");
  const first = runDeploy(f, ["all", "--root", customRoot]);
  assert.notEqual(first.status, 0);

  const result = runDeploy(f, ["server", "--root", customRoot]);
  assert.equal(result.status, 0, result.stderr);
  const unit = readFileSync(resolve(f.home, ".config/systemd/user/dayorder-api.service"), "utf8");
  const workingDirectory = deploymentPath(resolve(customRoot, "current-server")).replaceAll("%", "%%");
  assert.match(
    unit,
    new RegExp(`^WorkingDirectory=${workingDirectory.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`, "m"),
  );
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
  const failed = runDeploy(f, ["server", "--version", "v1.2.4"], { DAYORDER_TEST_HEALTH_FAIL_VERSION: "v1.2.4" });
  assert.notEqual(failed.status, 0);
  assert.equal(deploymentRealpath(resolve(f.runDirectory, "current-server")), deploymentRealpath(resolve(f.runDirectory, "releases/v1.2.3/server")));
  assert.match(readFileSync(f.log, "utf8"), /systemctl --user restart dayorder-api\.service/);
});

test("restored API readiness failure reports manual intervention", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const initial = runDeploy(f, ["server", "--version", "v1.2.3"]);
  assert.equal(initial.status, 0, initial.stderr);
  makeAssetRelease(f, "v1.2.4");

  const failed = runDeploy(f, ["server", "--version", "v1.2.4"], { DAYORDER_TEST_HEALTH_FAIL: "1" });

  assert.notEqual(failed.status, 0);
  assert.match(failed.stderr, /restored API.*readiness.*manual intervention/i);
  assert.equal(deploymentRealpath(resolve(f.runDirectory, "current-server")), deploymentRealpath(resolve(f.runDirectory, "releases/v1.2.3/server")));
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
  assert.deepEqual(repeatLog.split("\n").filter((line) => line.startsWith("curl ")), [
    "curl https://github.com/art-shier/be-better/releases/latest/download/release-manifest.json",
    "curl https://github.com/art-shier/be-better/releases/download/v1.2.3/release-manifest.json",
    "curl https://github.com/art-shier/be-better/releases/download/v1.2.3/SHA256SUMS",
  ]);
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

test("failed temporary link creation never installs a pre-existing link", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const initial = runDeploy(f, ["all", "--version", "v1.2.3"]);
  assert.equal(initial.status, 0, initial.stderr);
  makeAssetRelease(f, "v1.2.4");
  const attackerTarget = resolve(f.base, "attacker-link-target");
  mkdirSync(attackerTarget);

  const failed = runDeploy(f, ["all", "--version", "v1.2.4"], {
    DAYORDER_TEST_LN_FAIL_COMPONENT: "web",
    DAYORDER_TEST_PREEXISTING_LINK_TARGET: deploymentPath(attackerTarget),
  });

  assert.notEqual(failed.status, 0);
  assert.match(failed.stderr, /create temporary web link/i);
  assert.equal(deploymentRealpath(resolve(f.runDirectory, "current-web")), deploymentRealpath(resolve(f.runDirectory, "releases/v1.2.3/web")));
});

test("systemd unit symlinks are rejected without overwriting external files", (t) => {
  const f = configuredFixture(t, "v1.2.3");
  const unitDirectory = resolve(f.home, ".config/systemd/user");
  const unit = resolve(unitDirectory, "dayorder-api.service");
  const outside = resolve(f.base, "outside-api.service");
  mkdirSync(unitDirectory, { recursive: true });
  recordDeploymentMode(f, unitDirectory, "0700");
  writeFileSync(outside, "outside-unit-sentinel\n", "utf8");
  makeDeploymentSymlink(outside, unit);

  const result = runDeploy(f, ["server"]);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /systemd unit.*symbolic link/i);
  assert.equal(readFileSync(outside, "utf8"), "outside-unit-sentinel\n");
});

test("systemd unit directories allow read access but reject group or other writers", (t) => {
  for (const mode of ["0750", "0755"]) {
    const safe = configuredFixture(t, "v1.2.3");
    const unitDirectory = resolve(safe.home, ".config/systemd/user");
    mkdirSync(unitDirectory, { recursive: true });
    recordDeploymentMode(safe, unitDirectory, mode);

    const result = runDeploy(safe, ["server"]);

    assert.equal(result.status, 0, `mode ${mode}: ${result.stderr}`);
  }

  const unsafe = configuredFixture(t, "v1.2.3");
  const unsafeUnitDirectory = resolve(unsafe.home, ".config/systemd/user");
  mkdirSync(unsafeUnitDirectory, { recursive: true });
  recordDeploymentMode(unsafe, unsafeUnitDirectory, "0775");

  const unsafeResult = runDeploy(unsafe, ["server"]);

  assert.notEqual(unsafeResult.status, 0);
  assert.match(unsafeResult.stderr, /systemd unit directory.*group|other.*writ/i);
  assert.doesNotMatch(readFileSync(unsafe.log, "utf8"), /migrate|systemctl --user daemon-reload/);
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
  for (const name of ["api.env", "worker.env"]) {
    assert.match(readFileSync(resolve(specialRoot, "dayorder-config", name), "utf8"), new RegExp(secrets.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  assert.doesNotMatch(
    readFileSync(resolve(specialRoot, "dayorder-config/migrate.env"), "utf8"),
    /(?:DATABASE_URL|WORKER_DATABASE_URL|MIGRATION_DATABASE_URL)(?:_FILE)?=/,
  );
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
  assert.equal(deploymentPathExists(resolve(f.runDirectory, "current-server")), false);
  assert.equal(deploymentPathExists(resolve(f.runDirectory, "current-worker")), false);
  assert.equal(deploymentPathExists(resolve(f.runDirectory, "current-web")), false);
  assert.doesNotMatch(readFileSync(f.log, "utf8"), /migrate|systemctl/);

  const web = runDeploy(f, ["web"]);
  assert.equal(web.status, 0, web.stderr);
  assert.equal(deploymentRealpath(resolve(f.runDirectory, "current-web")), deploymentRealpath(resolve(f.runDirectory, `releases/${manifest.version}/web`)));
  assert.equal(deploymentFileExists(resolve(f.runDirectory, "current-web/index.html")), true);
});
