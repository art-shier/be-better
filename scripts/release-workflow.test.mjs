import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import test from "node:test";

const root = resolve(import.meta.dirname, "..");
const assetNames = [
  "dayorder-web.tar.gz", "dayorder-server-linux-amd64.tar.gz", "dayorder-server-linux-arm64.tar.gz",
  "dayorder-worker-linux-amd64.tar.gz", "dayorder-worker-linux-arm64.tar.gz", "dayorder-deploy.sh",
  "release-manifest.json", "SHA256SUMS",
];

function jobSection(workflow, name) {
  const jobsStart = workflow.indexOf("jobs:\n");
  assert.notEqual(jobsStart, -1, "workflow must define jobs");
  const header = `  ${name}:\n`;
  const start = workflow.indexOf(header, jobsStart);
  assert.notEqual(start, -1, `workflow must define the ${name} job`);
  const remaining = workflow.slice(start + header.length);
  const nextJob = remaining.search(/\n  [a-z][a-z0-9_-]*:\n/);
  return remaining.slice(0, nextJob === -1 ? undefined : nextJob);
}

function releaseCommands(releaseJob) {
  return [...releaseJob.matchAll(/\bgh release (?:view|create|upload|edit)\b[^\n]*/g)].map((match) => match[0]);
}

function assertReleaseWorkflowContract(workflow) {
  assert.match(workflow, /tags:\s*\["v\*"\]/);
  assert.match(workflow, /\^v\[0-9\]\+\\\.\[0-9\]\+\\\.\[0-9\]\+\$/);
  assert.match(workflow, /fetch-depth:\s*0/);
  assert.match(workflow, /merge-base --is-ancestor[\s\S]*origin\/main/);
  const globalConfig = workflow.slice(0, workflow.indexOf("jobs:\n"));
  const globalPermissions = globalConfig.match(/^permissions:\n((?:  [^\n]+\n)+)/m)?.[1];
  assert.equal(globalPermissions?.trim(), "contents: read", "global permissions must be contents: read only");
  assert.doesNotMatch(workflow, /^\s*permissions:\s*write-all\s*$/m, "workflow must not grant write-all");
  for (const job of ["validate", "web", "backend"]) {
    assert.doesNotMatch(jobSection(workflow, job), /^    permissions:/m, "non-release job permissions must remain default-safe");
  }
  const releaseJob = jobSection(workflow, "release");
  const releasePermissions = releaseJob.match(/^    permissions:\n((?:      [^\n]+\n)+)/m)?.[1];
  assert.equal(releasePermissions?.trim(), "contents: write", "release job must grant only contents: write");
  assert.match(workflow, /cancel-in-progress:\s*false/);
  assert.match(workflow, /matrix:[\s\S]*arch:\s*\[amd64, arm64\]/);
  const localValidation = workflow.match(/- name: Validate local asset set[\s\S]*?(?=\n      - name: Publish verified GitHub Release)/)?.[0] ?? "";
  assert.match(localValidation, /find release\/github -maxdepth 1 -mindepth 1 -printf '%f\\n' \| sort/);
  assert.doesNotMatch(localValidation, /-type f/);
  assert.match(localValidation, /\[\[ "\$actual" == "\$expected" \]\]/);
  assert.match(localValidation, /sha256sum -c SHA256SUMS/);
  for (const asset of assetNames) assert.match(localValidation, new RegExp(asset.replaceAll(".", "\\.")));

  assert.match(releaseJob, /gh release create[\s\S]*--draft/);
  assert.match(releaseJob, /gh release view "\$tag" --json isDraft --jq \.isDraft/);
  assert.match(releaseJob, /\[\[ "\$\(<"\$draft_state"\)" == true \]\]/);
  assert.match(releaseJob, /release %s is already public; refusing to replace it/);
  assert.doesNotMatch(releaseJob, /gh release delete/);
  const commands = releaseCommands(releaseJob);
  assert.deepEqual(
    commands.map((command) => command.match(/^gh release ([a-z]+)/)?.[1]),
    ["view", "create", "upload", "view", "edit"],
    "release command sequence must be draft view, create, one upload, remote validation, and publication",
  );
  assert.match(commands[0], /^gh release view "\$tag" --json isDraft --jq \.isDraft/);
  assert.match(commands[1], /^gh release create[\s\S]*--draft/);
  assert.equal(commands[2], 'gh release upload "$tag" "${assets[@]}" --clobber');
  assert.match(commands[3], /^gh release view "\$tag" --json assets --jq '\.assets\[\]\.name'/);
  assert.equal(commands[4], 'gh release edit "$tag" --draft=false');
  for (const asset of assetNames) assert.match(releaseJob, new RegExp(`release/github/${asset.replaceAll(".", "\\.")}`));
  assert.match(releaseJob, /gh release upload "\$tag" "\$\{assets\[@\]\}" --clobber/);
  assert.doesNotMatch(releaseJob, /gh release upload[^\n]*release\/github\/\*/);
  const remoteValidation = releaseJob.slice(releaseJob.indexOf("gh release upload"));
  assert.match(remoteValidation, /gh release view "\$tag" --json assets --jq '\.assets\[\]\.name'/);
  assert.match(remoteValidation, /\[\[ "\$actual" == "\$expected" \]\]/);
  assert.match(remoteValidation, /release asset set is incomplete/);
  for (const asset of assetNames) assert.match(remoteValidation, new RegExp(asset.replaceAll(".", "\\.")));
  assert.ok(workflow.indexOf("gh release upload") < workflow.indexOf("--draft=false"));
  assert.ok(workflow.indexOf("sha256sum -c SHA256SUMS") < workflow.indexOf("gh release create"));
  assert.ok(workflow.indexOf("gh release upload") < workflow.indexOf("gh release view \"$tag\" --json assets"));
  assert.ok(workflow.indexOf("gh release view \"$tag\" --json assets") < workflow.indexOf("--draft=false"));
  for (const reference of workflow.matchAll(/uses:\s*([^\s#]+)/g)) {
    assert.match(reference[1], /@[0-9a-f]{40}$/, `Action is not pinned: ${reference[1]}`);
  }
}

test("Release workflow gates stable tags and publishes a complete Draft atomically", () => {
  assertReleaseWorkflowContract(readFileSync(resolve(root, ".github/workflows/release.yml"), "utf8"));
});

test("Release workflow rejects a broader permission grant in a non-release job", () => {
  const workflow = readFileSync(resolve(root, ".github/workflows/release.yml"), "utf8");
  const mutated = workflow.replace(
    "  validate:\n    runs-on: ubuntu-latest",
    "  validate:\n    permissions: write-all\n    runs-on: ubuntu-latest",
  );
  assert.throws(() => assertReleaseWorkflowContract(mutated), /write-all|non-release job permissions/);
});

test("Release workflow rejects an upload after remote asset verification", () => {
  const workflow = readFileSync(resolve(root, ".github/workflows/release.yml"), "utf8");
  const mutated = workflow.replace(
    '          gh release edit "$tag" --draft=false',
    '          gh release upload "$tag" "${assets[@]}" --clobber\n          gh release edit "$tag" --draft=false',
  );
  assert.throws(() => assertReleaseWorkflowContract(mutated), /release command sequence/);
});
