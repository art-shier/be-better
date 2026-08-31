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

test("Release workflow gates stable tags and publishes a complete Draft atomically", () => {
  const workflow = readFileSync(resolve(root, ".github/workflows/release.yml"), "utf8");
  assert.match(workflow, /tags:\s*\["v\*"\]/);
  assert.match(workflow, /\^v\[0-9\]\+\\\.\[0-9\]\+\\\.\[0-9\]\+\$/);
  assert.match(workflow, /fetch-depth:\s*0/);
  assert.match(workflow, /merge-base --is-ancestor[\s\S]*origin\/main/);
  assert.match(workflow, /permissions:\s*\n\s*contents:\s*read/);
  const releaseJob = workflow.match(/\n  release:\n[\s\S]*$/)?.[0] ?? "";
  assert.match(releaseJob, /permissions:\s*\n\s*contents:\s*write/);
  assert.equal((workflow.match(/contents:\s*write/g) ?? []).length, 1);
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
});
