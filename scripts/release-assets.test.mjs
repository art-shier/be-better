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
