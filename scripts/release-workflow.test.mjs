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

function jobSections(workflow) {
  const jobsStart = workflow.indexOf("jobs:\n");
  assert.notEqual(jobsStart, -1, "workflow must define jobs");
  const jobs = workflow.slice(jobsStart + "jobs:\n".length);
  const headers = [...jobs.matchAll(/^  ([A-Za-z_][A-Za-z0-9_-]*):\n/gm)];
  return headers.map((header, index) => ({
    name: header[1],
    section: jobs.slice(header.index + header[0].length, headers[index + 1]?.index),
  }));
}

function jobSection(workflow, name) {
  const section = jobSections(workflow).find((job) => job.name === name)?.section;
  assert.ok(section, `workflow must define the ${name} job`);
  return section;
}

function namedRunBlock(job, name) {
  const step = `      - name: ${name}\n`;
  const start = job.indexOf(step);
  assert.notEqual(start, -1, `release job must define the ${name} step`);
  const afterStep = job.slice(start + step.length);
  const nextStep = afterStep.search(/\n      - /);
  const stepSection = afterStep.slice(0, nextStep === -1 ? undefined : nextStep);
  const runMarker = "        run: |\n";
  const runStart = stepSection.indexOf(runMarker);
  assert.notEqual(runStart, -1, `${name} step must define a literal run block`);
  return stepSection.slice(runStart + runMarker.length);
}

function stripShellComment(line) {
  let quote;
  let escaped = false;
  for (let index = 0; index < line.length; index += 1) {
    const character = line[index];
    if (escaped) {
      escaped = false;
    } else if (character === "\\" && quote !== "'") {
      escaped = true;
    } else if (quote) {
      if (character === quote) quote = undefined;
    } else if (character === "'" || character === '"') {
      quote = character;
    } else if (character === "#" && (index === 0 || /[\s;&|()]/.test(line[index - 1]))) {
      return line.slice(0, index);
    }
  }
  return line;
}

function releaseCommands(publishRun) {
  return publishRun
    .split("\n")
    .map((line) => stripShellComment(line).trim())
    .filter(Boolean)
    .flatMap((line) => [...line.matchAll(/\bgh release (?:view|create|upload|edit)\b[^\n]*/g)].map((match) => match[0]));
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
  const jobs = jobSections(workflow);
  assert.deepEqual(jobs.map((job) => job.name), ["validate", "web", "backend", "release"], "workflow job set must be fixed");
  for (const job of jobs.filter((job) => job.name !== "release")) {
    assert.doesNotMatch(job.section, /^    permissions:/m, "non-release job permissions must remain default-safe");
  }
  const releaseJob = jobSection(workflow, "release");
  const releasePermissions = releaseJob.match(/^    permissions:\n((?:      [^\n]+\n)+)/m)?.[1];
  assert.equal(releasePermissions?.trim(), "contents: write", "release job must grant only contents: write");
  assert.match(workflow, /cancel-in-progress:\s*false/);
  assert.match(workflow, /matrix:[\s\S]*arch:\s*\[amd64, arm64\]/);
  const localValidation = namedRunBlock(releaseJob, "Validate local asset set");
  assert.match(
    localValidation,
    /bash deploy\/release\/validate-release\.sh release\/github[\s\\]*"\$GITHUB_REF_NAME"[\s\\]*"\$\{\{ needs\.validate\.outputs\.commit \}\}"/,
  );

  const publishRun = namedRunBlock(releaseJob, "Publish verified GitHub Release");
  assert.match(publishRun, /gh release create[\s\S]*--draft/);
  assert.match(publishRun, /gh release view "\$tag" --json isDraft --jq \.isDraft/);
  assert.match(publishRun, /\[\[ "\$\(<"\$draft_state"\)" == true \]\]/);
  assert.match(publishRun, /release %s is already public; refusing to replace it/);
  assert.doesNotMatch(publishRun, /gh release delete/);
  const commands = releaseCommands(publishRun);
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
  for (const asset of assetNames) assert.match(publishRun, new RegExp(`release/github/${asset.replaceAll(".", "\\.")}`));
  assert.match(publishRun, /gh release upload "\$tag" "\$\{assets\[@\]\}" --clobber/);
  assert.doesNotMatch(publishRun, /gh release upload[^\n]*release\/github\/\*/);
  const remoteValidation = publishRun.slice(publishRun.indexOf("gh release upload"));
  assert.match(remoteValidation, /gh release view "\$tag" --json assets --jq '\.assets\[\]\.name'/);
  assert.match(remoteValidation, /\[\[ "\$actual" == "\$expected" \]\]/);
  assert.match(remoteValidation, /release asset set is incomplete/);
  for (const asset of assetNames) assert.match(remoteValidation, new RegExp(asset.replaceAll(".", "\\.")));
  assert.ok(publishRun.indexOf("gh release upload") < publishRun.indexOf("--draft=false"));
  const validatorCall = workflow.indexOf("bash deploy/release/validate-release.sh release/github");
  assert.notEqual(validatorCall, -1, "workflow must invoke the reusable release validator");
  assert.ok(validatorCall < workflow.indexOf("gh release create"), "release validation must precede draft creation");
  assert.ok(publishRun.indexOf("gh release upload") < publishRun.indexOf("gh release view \"$tag\" --json assets"));
  assert.ok(publishRun.indexOf("gh release view \"$tag\" --json assets") < publishRun.indexOf("--draft=false"));
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

test("Release workflow rejects a new non-release job with release-write permission", () => {
  const workflow = readFileSync(resolve(root, ".github/workflows/release.yml"), "utf8");
  const mutated = `${workflow}\n  Rogue:\n    runs-on: ubuntu-latest\n    permissions:\n      contents: write\n    steps: []\n`;
  assert.throws(() => assertReleaseWorkflowContract(mutated), /job set|non-release job permissions/);
});

test("Release workflow binds the publish run block to the named step", () => {
  const workflow = readFileSync(resolve(root, ".github/workflows/release.yml"), "utf8");
  const mutated = workflow.replace(
    "      - name: Publish verified GitHub Release\n        shell: bash\n        run: |",
    "      - name: Publish verified GitHub Release\n        shell: bash\n      - name: Unrelated later step\n        shell: bash\n        run: |",
  );
  assert.throws(() => assertReleaseWorkflowContract(mutated), /literal run block/);
});

test("Release workflow rejects an upload after remote asset verification", () => {
  const workflow = readFileSync(resolve(root, ".github/workflows/release.yml"), "utf8");
  const mutated = workflow.replace(
    '          gh release edit "$tag" --draft=false',
    '          gh release upload "$tag" "${assets[@]}" --clobber\n          gh release edit "$tag" --draft=false',
  );
  assert.throws(() => assertReleaseWorkflowContract(mutated), /release command sequence/);
});

test("Release workflow rejects a commented upload in place of the publish command", () => {
  const workflow = readFileSync(resolve(root, ".github/workflows/release.yml"), "utf8");
  const mutated = workflow.replace(
    '          gh release upload "$tag" "${assets[@]}" --clobber',
    '          # gh release upload "$tag" "${assets[@]}" --clobber\n          printf \'upload skipped\\n\'',
  );
  assert.throws(() => assertReleaseWorkflowContract(mutated), /publish step|release command sequence/);
});

test("Release workflow rejects an inline-commented upload in place of the publish command", () => {
  const workflow = readFileSync(resolve(root, ".github/workflows/release.yml"), "utf8");
  const mutated = workflow.replace(
    '          gh release upload "$tag" "${assets[@]}" --clobber',
    '          :; # gh release upload "$tag" "${assets[@]}" --clobber',
  );
  assert.throws(() => assertReleaseWorkflowContract(mutated), /publish step|release command sequence/);
});

test("Release workflow rejects a missing reusable validator call", () => {
  const workflow = readFileSync(resolve(root, ".github/workflows/release.yml"), "utf8");
  const mutated = workflow.replace(
    /          bash deploy\/release\/validate-release\.sh release\/github \\\n+            "\$GITHUB_REF_NAME" "\$\{\{ needs\.validate\.outputs\.commit \}\}"\n/,
    "          printf 'validation skipped\\n'\n",
  );
  assert.throws(() => assertReleaseWorkflowContract(mutated), /release validator|validate-release/);
});

test("Release workflow rejects moving reusable validation after draft creation", () => {
  const workflow = readFileSync(resolve(root, ".github/workflows/release.yml"), "utf8");
  const validatorLine = [
    "          bash deploy/release/validate-release.sh release/github \\",
    '            "$GITHUB_REF_NAME" "${{ needs.validate.outputs.commit }}"',
    "",
  ].join("\n");
  const withoutValidator = workflow.replace(validatorLine, "          printf 'validation moved\\n'\n");
  const mutated = withoutValidator.replace(
    '            gh release create "$tag" --verify-tag --draft --generate-notes --title "$tag"\n',
    `            gh release create "$tag" --verify-tag --draft --generate-notes --title "$tag"\n${validatorLine}`,
  );
  assert.throws(() => assertReleaseWorkflowContract(mutated), /release validator|validate-release|precede draft creation/);
});
