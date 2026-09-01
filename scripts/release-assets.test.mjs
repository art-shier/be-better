import assert from "node:assert/strict";
import {
  chmodSync,
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

const root = resolve(import.meta.dirname, "..");
const packager = resolve(root, "deploy/release/package-assets.sh");
const releaseAssetNames = [
  "SHA256SUMS",
  "dayorder-deploy.sh",
  "dayorder-server-linux-amd64.tar.gz",
  "dayorder-server-linux-arm64.tar.gz",
  "dayorder-web.tar.gz",
  "dayorder-worker-linux-amd64.tar.gz",
  "dayorder-worker-linux-arm64.tar.gz",
  "release-manifest.json",
];

function write(path, content, mode = 0o644) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content, "utf8");
  chmodSync(path, mode);
}

function run(args, cwd = root) {
  return spawnSync("bash", [packager, ...args], { cwd, encoding: "utf8" });
}

function tarPath(path) {
  if (process.platform !== "win32") return path;
  const converted = spawnSync("cygpath", ["-u", path], { encoding: "utf8" });
  assert.equal(converted.status, 0, converted.stderr);
  return converted.stdout.trim();
}

function listArchive(path) {
  const result = spawnSync("tar", ["-tzf", tarPath(path)], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim().split("\n").map((entry) => entry.replace(/^\.\//, "")).filter(Boolean).sort();
}

function archiveMode(path, entry) {
  const result = spawnSync("tar", ["-tvzf", tarPath(path)], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  const line = result.stdout.split(/\r?\n/).find((candidate) => {
    const normalized = candidate.trimEnd();
    if (entry === ".") return normalized.endsWith(" ./");
    return normalized.endsWith(` ${entry}`) || normalized.endsWith(` ./${entry}`);
  });
  assert.ok(line, `archive entry is missing: ${entry}`);
  return line.trim().split(/\s+/, 1)[0];
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
    // Real ELF binaries have no shebang for Git/MSYS to infer executability from.
    write(resolve(backend, `bin/${name}`), "\x7fELF fixture\n", 0o755);
  }
  for (const name of ["runtime-env.sh", "start-api.sh", "start-worker.sh", "migrate.sh"]) {
    write(resolve(backend, `scripts/${name}`), "#!/usr/bin/env bash\nexit 0\n", 0o755);
  }
  for (const name of ["api.env.example", "worker.env.example", "migrate.env.example"]) {
    write(resolve(backend, `config/${name}`), `${name}=fixture\n`);
  }
  return { base, web, backend, assets };
}

function builderFixture(t) {
  const base = mkdtempSync(resolve(tmpdir(), "dayorder-release-builder-"));
  t.after(() => rmSync(base, { recursive: true, force: true }));
  const deployRelease = resolve(base, "deploy/release");
  const bareMetal = resolve(base, "deploy/bare-metal");
  const commands = resolve(base, "commands");
  const output = resolve(base, "release/github");
  const log = resolve(base, "build.log");
  mkdirSync(deployRelease, { recursive: true });
  mkdirSync(bareMetal, { recursive: true });
  mkdirSync(commands, { recursive: true });
  copyFileSync(resolve(root, "deploy/release/build-release.sh"), resolve(deployRelease, "build-release.sh"));
  chmodSync(resolve(deployRelease, "build-release.sh"), 0o755);
  write(resolve(base, "package.json"), '{"version":"1.2.3"}\n');
  write(resolve(deployRelease, "dayorder-deploy.sh"), "#!/usr/bin/env bash\nexit 0\n", 0o755);
  write(resolve(commands, "git"), "#!/usr/bin/env bash\nprintf '0123456789abcdef0123456789abcdef01234567\\n'\n", 0o755);
  write(resolve(bareMetal, "build-web.sh"), `#!/usr/bin/env bash
printf 'web-builder\\t%s\\n' "$1" >> "$DAYORDER_TEST_BUILD_LOG"
[[ "\${DAYORDER_TEST_FAIL_COMPONENT:-}" != web ]] || exit 71
mkdir -p -- "$1/assets"
printf 'web\\n' > "$1/index.html"
printf 'asset\\n' > "$1/assets/app.js"
`, 0o755);
  write(resolve(bareMetal, "build-backend.sh"), `#!/usr/bin/env bash
printf 'backend-%s\\t%s\\n' "$GOARCH" "$1" >> "$DAYORDER_TEST_BUILD_LOG"
[[ "\${DAYORDER_TEST_FAIL_ARCH:-}" != "$GOARCH" ]] || exit 72
mkdir -p -- "$1"
printf '%s\\n' "$GOARCH" > "$1/architecture"
`, 0o755);
  write(resolve(deployRelease, "package-assets.sh"), `#!/usr/bin/env bash
set -Eeuo pipefail
command="$1"; output="\${@: -1}"
printf 'package-%s\\t%s\\n' "$command" "$output" >> "$DAYORDER_TEST_BUILD_LOG"
mkdir -p -- "$output"
case "$command" in
  web) printf 'new web\\n' > "$output/dayorder-web.tar.gz" ;;
  backend)
    arch="$2"
    printf 'new server %s\\n' "$arch" > "$output/dayorder-server-linux-$arch.tar.gz"
    printf 'new worker %s\\n' "$arch" > "$output/dayorder-worker-linux-$arch.tar.gz"
    ;;
  metadata)
    printf 'new deployer\\n' > "$output/dayorder-deploy.sh"
    printf '{"schemaVersion":1}\\n' > "$output/release-manifest.json"
    printf 'new checksums\\n' > "$output/SHA256SUMS"
    ;;
  *) exit 64 ;;
esac
`, 0o755);
  writeFileSync(log, "", "utf8");
  return { base, builder: resolve(deployRelease, "build-release.sh"), commands, output, log };
}

function runBuilder(fixture, extraEnvironment = {}) {
  return spawnSync("bash", [
    "-c",
    'PATH="$DAYORDER_TEST_COMMANDS:$PATH"; export PATH; exec bash "$@"',
    "dayorder-release-builder-test",
    tarPath(fixture.builder),
    "all",
  ], {
    cwd: root,
    encoding: "utf8",
    env: {
      ...process.env,
      DAYORDER_RELEASE_OUTPUT: tarPath(fixture.output),
      DAYORDER_TEST_BUILD_LOG: tarPath(fixture.log),
      DAYORDER_TEST_COMMANDS: tarPath(fixture.commands),
      ...extraEnvironment,
    },
  });
}

function directorySnapshot(directory) {
  return Object.fromEntries(readdirSync(directory).sort().map((name) => [
    name,
    readFileSync(resolve(directory, name), "utf8"),
  ]));
}

test("packager emits the exact Web, Server, and Worker archive contracts", (t) => {
  const f = fixture(t);
  chmodSync(f.web, 0o700);
  chmodSync(resolve(f.web, "index.html"), 0o700);
  chmodSync(resolve(f.web, "assets"), 0o700);
  chmodSync(resolve(f.web, "assets/app.js"), 0o755);
  for (const command of [["web", f.web, f.assets], ["backend", "amd64", f.backend, f.assets]]) {
    const result = run(command);
    assert.equal(result.status, 0, result.stderr);
  }
  assert.deepEqual(listArchive(resolve(f.assets, "dayorder-web.tar.gz")), ["assets/", "assets/app.js", "index.html"]);
  assert.equal(archiveMode(resolve(f.assets, "dayorder-web.tar.gz"), "."), "drwxr-xr-x");
  assert.equal(archiveMode(resolve(f.assets, "dayorder-web.tar.gz"), "assets/"), "drwxr-xr-x");
  assert.equal(archiveMode(resolve(f.assets, "dayorder-web.tar.gz"), "assets/app.js"), "-rw-r--r--");
  assert.equal(archiveMode(resolve(f.assets, "dayorder-web.tar.gz"), "index.html"), "-rw-r--r--");
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
  const unpack = spawnSync("tar", ["-xzf", tarPath(resolve(f.assets, "dayorder-server-linux-amd64.tar.gz")), "-C", tarPath(extracted)], { encoding: "utf8" });
  assert.equal(unpack.status, 0, unpack.stderr);
  assert.equal(statSync(resolve(extracted, "bin/dayorder-api")).isFile(), true);
  assert.equal(statSync(resolve(extracted, "scripts/start-api.sh")).isFile(), true);
  assert.equal(statSync(resolve(extracted, "config/api.env.example")).isFile(), true);
  if (process.platform === "win32") {
    const archive = resolve(f.assets, "dayorder-server-linux-amd64.tar.gz");
    assert.equal(archiveMode(archive, "bin/dayorder-api"), "-rwxr-xr-x");
    assert.equal(archiveMode(archive, "scripts/start-api.sh"), "-rwxr-xr-x");
    assert.equal(archiveMode(archive, "config/api.env.example"), "-rw-r--r--");
  } else {
    assert.equal(statSync(resolve(extracted, "bin/dayorder-api")).mode & 0o777, 0o755);
    assert.equal(statSync(resolve(extracted, "scripts/start-api.sh")).mode & 0o777, 0o755);
    assert.equal(statSync(resolve(extracted, "config/api.env.example")).mode & 0o777, 0o644);
  }
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

test("release builder exposes isolated CI targets and one complete local build", () => {
  const builder = readFileSync(resolve(root, "deploy/release/build-release.sh"), "utf8");
  const packageJson = JSON.parse(readFileSync(resolve(root, "package.json"), "utf8"));
  assert.match(builder, /build-web\.sh/);
  assert.match(builder, /build-backend\.sh/);
  assert.match(builder, /web\|backend\|finalize\|all/);
  assert.equal(packageJson.scripts["build:release:assets"], "bash deploy/release/build-release.sh all");
  assert.equal(packageJson.scripts["test:release"], "node --test scripts/release-*.test.mjs");
});

test("release build entrypoints are executable after a Git checkout", () => {
  for (const path of ["deploy/bare-metal/build-web.sh", "deploy/bare-metal/build-backend.sh"]) {
    const result = spawnSync("git", ["ls-files", "--stage", "--", path], {
      cwd: root,
      encoding: "utf8",
    });
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /^100755\s/, `${path} is not executable in the Git index`);
  }
});

test("aggregate release builder installs one exact staged asset set and removes stale output", (t) => {
  const f = builderFixture(t);
  mkdirSync(f.output, { recursive: true });
  for (const name of releaseAssetNames) write(resolve(f.output, name), `previous ${name}\n`);
  write(resolve(f.output, "stale-asset.txt"), "stale\n");

  const result = runBuilder(f);

  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(readdirSync(f.output).sort(), releaseAssetNames);
  assert.equal(readFileSync(resolve(f.output, "dayorder-web.tar.gz"), "utf8"), "new web\n");
  const finalOutput = tarPath(f.output);
  for (const line of readFileSync(f.log, "utf8").trim().split(/\r?\n/)) {
    const operationOutput = line.split("\t").at(-1);
    assert.notEqual(operationOutput, finalOutput, `aggregate build wrote directly to ${finalOutput}`);
  }
});

test("aggregate release builder preserves the previous asset set when a component fails", (t) => {
  const f = builderFixture(t);
  mkdirSync(f.output, { recursive: true });
  for (const name of releaseAssetNames) write(resolve(f.output, name), `preserved ${name}\n`);
  write(resolve(f.output, "preserved-extra.txt"), "preserved extra\n");
  const before = directorySnapshot(f.output);

  const result = runBuilder(f, { DAYORDER_TEST_FAIL_ARCH: "arm64" });

  assert.notEqual(result.status, 0);
  assert.deepEqual(directorySnapshot(f.output), before);
});

test("release builder resolves package metadata from the repository working directory", (t) => {
  const f = fixture(t);
  const commands = resolve(f.base, "commands");
  write(resolve(commands, "node"), `#!/usr/bin/env bash
if [[ "$*" == *"$DAYORDER_TEST_ROOT"* ]]; then
  printf 'node cannot resolve package metadata outside its path namespace: %s\\n' "$*" >&2
  exit 91
fi
[[ "$PWD" == "$DAYORDER_TEST_ROOT" ]] || exit 91
printf 'relative metadata probe passed\\n' >&2
exit 92
`, 0o755);

  const result = spawnSync("bash", [resolve(root, "deploy/release/build-release.sh"), "all"], {
    cwd: root,
    encoding: "utf8",
    env: {
      ...process.env,
      DAYORDER_TEST_ROOT: tarPath(root),
      PATH: `${commands}${process.platform === "win32" ? ";" : ":"}${process.env.PATH}`,
    },
  });

  assert.equal(result.status, 92, result.stderr);
  assert.match(result.stderr, /relative metadata probe passed/);
});

test("gitignore tracks release source scripts while ignoring root release output", () => {
  const sourceProbe = spawnSync("git", ["check-ignore", "--no-index", "deploy/release/future-release-helper.sh"], {
    cwd: root,
    encoding: "utf8",
  });
  const outputProbe = spawnSync("git", ["check-ignore", "--no-index", "release/future-release.tar.gz"], {
    cwd: root,
    encoding: "utf8",
  });
  assert.equal(sourceProbe.status, 1, sourceProbe.stdout || sourceProbe.stderr);
  assert.equal(outputProbe.status, 0, outputProbe.stderr);
});
