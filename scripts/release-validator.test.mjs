import assert from "node:assert/strict";
import {
  chmodSync,
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import test, { after, before } from "node:test";

const root = resolve(import.meta.dirname, "..");
const packager = resolve(root, "deploy/release/package-assets.sh");
const validator = resolve(root, "deploy/release/validate-release.sh");
const revision = "0123456789abcdef0123456789abcdef01234567";
const checksumNames = [
  "dayorder-web.tar.gz",
  "dayorder-server-linux-amd64.tar.gz",
  "dayorder-server-linux-arm64.tar.gz",
  "dayorder-worker-linux-amd64.tar.gz",
  "dayorder-worker-linux-arm64.tar.gz",
  "dayorder-deploy.sh",
  "release-manifest.json",
];
let binaryCache;

function write(path, content, mode = 0o644) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content, "utf8");
  chmodSync(path, mode);
}

function linuxPath(path) {
  if (process.platform !== "win32") return path;
  const match = /^([A-Za-z]):\\([\s\S]*)$/.exec(path);
  assert.ok(match, `expected Windows path: ${path}`);
  return `/mnt/${match[1].toLowerCase()}/${match[2].replaceAll("\\", "/")}`;
}

function linuxArgument(argument) {
  return process.platform === "win32" && /^[A-Za-z]:\\/.test(argument) ? linuxPath(argument) : argument;
}

function runLinux(command, args, options = {}) {
  const linuxArgs = args.map(linuxArgument);
  if (process.platform !== "win32") {
    return spawnSync(command, linuxArgs, { encoding: "utf8", ...options });
  }
  return spawnSync("C:\\Windows\\System32\\wsl.exe", ["--exec", command, ...linuxArgs], {
    encoding: "utf8",
    env: options.env,
  });
}

function runBash(args, options = {}) {
  return runLinux("/bin/bash", args, options);
}

function requireSuccess(result, context) {
  assert.equal(result.status, 0, `${context}:\n${result.stdout}${result.stderr}`);
}

before(() => {
  binaryCache = mkdtempSync(resolve(tmpdir(), "dayorder-validator-binaries-"));
  const source = resolve(binaryCache, "main.go");
  write(source, "package main\nfunc main() {}\n");
  for (const arch of ["amd64", "arm64"]) {
    const result = spawnSync("go", ["build", "-trimpath", "-ldflags=-s -w", "-o", resolve(binaryCache, arch), source], {
      encoding: "utf8",
      env: { ...process.env, CGO_ENABLED: "0", GOOS: "linux", GOARCH: arch },
    });
    requireSuccess(result, `cross-compile ${arch} fixture`);
  }
});

after(() => {
  if (binaryCache) rmSync(binaryCache, { recursive: true, force: true });
});

function releaseFixture(t) {
  const base = mkdtempSync(resolve(tmpdir(), "dayorder-release-validator-"));
  t.after(() => rmSync(base, { recursive: true, force: true }));
  const web = resolve(base, "web");
  const assets = resolve(base, "assets");
  write(resolve(web, "index.html"), "<main>validator fixture</main>\n");
  write(resolve(web, "assets/app.js"), "console.log('validator fixture')\n");
  requireSuccess(runBash([packager, "web", web, assets]), "package Web fixture");

  for (const arch of ["amd64", "arm64"]) {
    const backend = resolve(base, `backend-${arch}`);
    for (const name of ["dayorder-api", "dayorder-worker", "dayorder-migrate"]) {
      mkdirSync(resolve(backend, "bin"), { recursive: true });
      copyFileSync(resolve(binaryCache, arch), resolve(backend, `bin/${name}`));
      chmodSync(resolve(backend, `bin/${name}`), 0o755);
    }
    for (const name of ["runtime-env.sh", "start-api.sh", "start-worker.sh", "migrate.sh"]) {
      write(resolve(backend, `scripts/${name}`), "#!/usr/bin/env bash\nexit 0\n", 0o755);
    }
    for (const name of ["api.env.example", "worker.env.example", "migrate.env.example"]) {
      write(resolve(backend, `config/${name}`), `${name}=fixture\n`);
    }
    requireSuccess(runBash([packager, "backend", arch, backend, assets]), `package ${arch} fixture`);
  }

  const deployer = resolve(base, "dayorder-deploy.sh");
  write(deployer, "#!/usr/bin/env bash\nset -Eeuo pipefail\n", 0o755);
  requireSuccess(
    runBash([packager, "metadata", "v1.2.3", revision, deployer, assets]),
    "package fixture metadata",
  );
  return { base, web, assets };
}

function refreshChecksums(assets) {
  const command = `cd -- "$1" && sha256sum ${checksumNames.map((name) => `'${name}'`).join(" ")} > SHA256SUMS`;
  requireSuccess(runBash(["-c", command, "dayorder-validator-checksums", assets]), "refresh fixture checksums");
}

function runValidator(fixture, expectedVersion = "v1.2.3", expectedRevision = revision) {
  return runBash([validator, fixture.assets, expectedVersion, expectedRevision]);
}

test("release validator accepts the complete real Linux asset contract", (t) => {
  const fixture = releaseFixture(t);

  const result = runValidator(fixture);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /release asset validation passed/i);

  const checksums = readFileSync(resolve(fixture.assets, "SHA256SUMS"), "utf8");
  writeFileSync(
    resolve(fixture.assets, "SHA256SUMS"),
    checksums.replace(/^([0-9a-f]{64})  /gm, "$1 *"),
    "utf8",
  );
  const binaryModeRecords = runValidator(fixture);
  assert.equal(binaryModeRecords.status, 0, binaryModeRecords.stderr);
});

test("release validator rejects extra top-level entries and an incomplete checksum record set", (t) => {
  const extra = releaseFixture(t);
  write(resolve(extra.assets, "unexpected.txt"), "unexpected\n");
  const extraResult = runValidator(extra);
  assert.notEqual(extraResult.status, 0);
  assert.match(extraResult.stderr, /exactly eight|asset set/i);

  const missingChecksum = releaseFixture(t);
  const checksumLines = readFileSync(resolve(missingChecksum.assets, "SHA256SUMS"), "utf8").trimEnd().split(/\r?\n/);
  writeFileSync(resolve(missingChecksum.assets, "SHA256SUMS"), `${checksumLines.slice(0, -1).join("\n")}\n`, "utf8");
  const checksumResult = runValidator(missingChecksum);
  assert.notEqual(checksumResult.status, 0);
  assert.match(checksumResult.stderr, /exactly seven|checksum record/i);

  const mismatch = releaseFixture(t);
  writeFileSync(resolve(mismatch.assets, "dayorder-deploy.sh"), "# tampered after checksumming\n", { flag: "a" });
  const mismatchResult = runValidator(mismatch);
  assert.notEqual(mismatchResult.status, 0);
  assert.match(mismatchResult.stderr, /checksum verification failed/i);
});

test("release validator rejects archive contract drift even with refreshed checksums", (t) => {
  const fixture = releaseFixture(t);
  const mutatedWeb = resolve(fixture.base, "mutated-web");
  write(resolve(mutatedWeb, "index.html"), "<main>mutated</main>\n");
  write(resolve(mutatedWeb, "assets/app.js"), "console.log('mutated')\n");
  write(resolve(mutatedWeb, "unexpected.txt"), "unexpected top-level member\n");
  const archive = resolve(fixture.assets, "dayorder-web.tar.gz");
  const uncompressedArchive = resolve(fixture.base, "mutated-web.tar");
  requireSuccess(runBash([
    "-c",
    'tar --mtime=@0 --owner=0 --group=0 --numeric-owner --no-recursion --mode=0755 -C "$1" -cf "$3" . && tar --mtime=@0 --owner=0 --group=0 --numeric-owner --no-recursion --mode=0755 -C "$1" -rf "$3" ./assets && tar --mtime=@0 --owner=0 --group=0 --numeric-owner --no-recursion --mode=0644 -C "$1" -rf "$3" ./index.html ./assets/app.js ./unexpected.txt && gzip -n < "$3" > "$2"',
    "dayorder-mutated-web",
    mutatedWeb,
    archive,
    uncompressedArchive,
  ]), "create mutated Web fixture");
  refreshChecksums(fixture.assets);

  const result = runValidator(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /web archive.*contract|archive member/i);
});

test("release validator rejects whitespace members that spoof contract paths", (t) => {
  const fixture = releaseFixture(t);
  const mutatedWeb = resolve(fixture.base, "whitespace-web");
  write(resolve(mutatedWeb, "index.html"), "<main>mutated</main>\n");
  write(resolve(mutatedWeb, "assets/app.js"), "console.log('expected')\n");
  write(resolve(mutatedWeb, "unexpected ./assets/app.js"), "console.log('unexpected')\n");
  const archive = resolve(fixture.assets, "dayorder-web.tar.gz");
  const uncompressedArchive = resolve(fixture.base, "whitespace-web.tar");
  requireSuccess(runBash([
    "-c",
    'tar --mtime=@0 --owner=0 --group=0 --numeric-owner --no-recursion --mode=0755 -C "$1" -cf "$3" . && tar --mtime=@0 --owner=0 --group=0 --numeric-owner --no-recursion --mode=0755 -C "$1" -rf "$3" ./assets && tar --mtime=@0 --owner=0 --group=0 --numeric-owner --no-recursion --mode=0644 -C "$1" -rf "$3" ./index.html ./assets/app.js "./unexpected ./assets/app.js" && gzip -n < "$3" > "$2"',
    "dayorder-whitespace-web",
    mutatedWeb,
    archive,
    uncompressedArchive,
  ]), "create whitespace-spoofing Web fixture");
  refreshChecksums(fixture.assets);

  const result = runValidator(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /whitespace|unsafe member path|archive member/i);
});

test("release validator rejects a backend archive with the wrong ELF architecture", (t) => {
  const fixture = releaseFixture(t);
  copyFileSync(
    resolve(fixture.assets, "dayorder-server-linux-arm64.tar.gz"),
    resolve(fixture.assets, "dayorder-server-linux-amd64.tar.gz"),
  );
  refreshChecksums(fixture.assets);

  const result = runValidator(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /amd64|x86-64|machine/i);
});

test("release validator binds the Manifest to the expected tag and commit", (t) => {
  const fixture = releaseFixture(t);

  const wrongVersion = runValidator(fixture, "v9.9.9", revision);
  assert.notEqual(wrongVersion.status, 0);
  assert.match(wrongVersion.stderr, /manifest version|expected version/i);

  const wrongRevision = runValidator(fixture, "v1.2.3", "ffffffffffffffffffffffffffffffffffffffff");
  assert.notEqual(wrongRevision.status, 0);
  assert.match(wrongRevision.stderr, /manifest revision|expected revision/i);
});
