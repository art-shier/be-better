import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import test from "node:test";

const root = resolve(import.meta.dirname, "..");

test("Release workflow gates stable tags and publishes a complete Draft atomically", () => {
  const workflow = readFileSync(resolve(root, ".github/workflows/release.yml"), "utf8");
  assert.match(workflow, /tags:\s*\["v\*"\]/);
  assert.match(workflow, /\^v\[0-9\]\+\\\.\[0-9\]\+\\\.\[0-9\]\+\$/);
  assert.match(workflow, /fetch-depth:\s*0/);
  assert.match(workflow, /merge-base --is-ancestor[\s\S]*origin\/main/);
  assert.match(workflow, /permissions:\s*\n\s*contents:\s*read/);
  assert.match(workflow, /release:[\s\S]*permissions:\s*\n\s*contents:\s*write/);
  assert.match(workflow, /cancel-in-progress:\s*false/);
  assert.match(workflow, /matrix:[\s\S]*arch:\s*\[amd64, arm64\]/);
  assert.match(workflow, /gh release create[\s\S]*--draft/);
  assert.match(workflow, /gh release upload[\s\S]*--clobber/);
  assert.ok(workflow.indexOf("gh release upload") < workflow.indexOf("--draft=false"));
  for (const asset of [
    "dayorder-web.tar.gz", "dayorder-server-linux-amd64.tar.gz", "dayorder-server-linux-arm64.tar.gz",
    "dayorder-worker-linux-amd64.tar.gz", "dayorder-worker-linux-arm64.tar.gz", "dayorder-deploy.sh",
    "release-manifest.json", "SHA256SUMS",
  ]) assert.match(workflow, new RegExp(asset.replaceAll(".", "\\.")));
  for (const reference of workflow.matchAll(/uses:\s*([^\s#]+)/g)) {
    assert.match(reference[1], /@[0-9a-f]{40}$/, `Action is not pinned: ${reference[1]}`);
  }
});
