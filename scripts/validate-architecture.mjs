import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";

import { validateAgentArchitecture } from "./lib/agent-architecture-rules.mjs";

const root = resolve(import.meta.dirname, "..");
const failures = [];

function read(relativePath) {
  return readFileSync(resolve(root, relativePath), "utf8");
}

function requireAbsentFile(relativePath, reason) {
  if (existsSync(resolve(root, relativePath))) {
    failures.push(`${relativePath}: ${reason}`);
  }
}

function requireAbsentText(relativePath, pattern, reason) {
  const content = read(relativePath);
  if (pattern.test(content)) {
    failures.push(`${relativePath}: ${reason}`);
  }
}

function readTypeScriptSources(relativeDirectory) {
  const directory = resolve(root, relativeDirectory);
  const sources = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) {
      sources.push(...readTypeScriptSources(`${relativeDirectory}/${entry.name}`));
    } else if (entry.isFile() && /\.tsx?$/.test(entry.name)) {
      sources.push(readFileSync(path, "utf8"));
    }
  }
  return sources;
}

function readProductionGoSources(relativeDirectory) {
  const directory = resolve(root, relativeDirectory);
  const sources = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const relativePath = `${relativeDirectory}/${entry.name}`;
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) {
      sources.push(...readProductionGoSources(relativePath));
    } else if (entry.isFile() && entry.name.endsWith(".go") && !entry.name.endsWith("_test.go")) {
      sources.push({ path: relativePath, source: readFileSync(path, "utf8") });
    }
  }
  return sources;
}

for (const relativePath of [
  "apps/api/internal/store/sqlite.go",
  "apps/api/internal/store/sqlite_test.go",
  "apps/api/internal/httpapi/server.go",
  "apps/api/internal/httpapi/server_test.go",
]) {
  requireAbsentFile(relativePath, "legacy SQLite snapshot implementation must be deleted");
}

for (const relativePath of ["apps/api/go.mod", "apps/api/go.sum"]) {
  requireAbsentText(relativePath, /modernc\.org\/sqlite/i, "SQLite must not be a runtime or transitive dependency");
}

for (const relativePath of [
  "package.json",
  "scripts/runtime-acceptance.ps1",
  "scripts/runtime-postgres-acceptance.ps1",
  "README.md",
  "docs/dayorder-product-spec.md",
]) {
  requireAbsentText(relativePath, /DAYORDER_DB_PATH/, "production and acceptance paths must use DATABASE_URL");
  requireAbsentText(relativePath, /\/api\/v1\/state|(?:^|\s)\/state(?:\s|`|$)/m, "the snapshot state API is not part of the PostgreSQL architecture");
}

for (const relativePath of [
  "apps/web/src/domain/types.ts",
  "apps/web/src/domain/seed.ts",
  "apps/web/src/store/AppStore.tsx",
  "apps/web/src/store/reducer.ts",
  "apps/web/src/store/selectors.ts",
  "apps/web/src/auth/guest-migration.ts",
]) {
  requireAbsentText(relativePath, /\bagentRuns\s*[:;,]/, "AppData must not embed server-owned agent runs");
  requireAbsentText(relativePath, /\baudit\s*[:;,]/, "AppData must not embed server-owned audit events");
}

for (const relativePath of [
  "apps/web/src/store/storage.ts",
  "apps/web/src/store/AppStore.tsx",
  "apps/web/src/auth/AuthProvider.tsx",
]) {
  requireAbsentText(
    relativePath,
    /LEGACY_SYNC_KEY|LEGACY_CONFLICT_KEY|SYNC_META_KEY|CONFLICT_KEY|migrateLegacyStorage|dayorder\.(?:sync|conflict)\.v1/,
    "legacy whole-document synchronization compatibility must be removed",
  );
}

requireAbsentText(
  "apps/web/src/store/reducer.ts",
  /type:\s*"undo"|\bwithAudit\b|function\s+audit\s*\(/,
  "guest AppData must not maintain a second local audit/undo system",
);

const configHubIgnoreCheck = spawnSync("git", ["check-ignore", "-q", "--", ".confighub.yaml"], {
  cwd: root,
  stdio: "ignore",
});
if (configHubIgnoreCheck.status !== 0) {
  failures.push(".confighub.yaml: local ConfigHub Machine Token file must be ignored by Git");
}

failures.push(...validateAgentArchitecture({
  webPackage: JSON.parse(read("apps/web/package.json")),
  webAgentSources: readTypeScriptSources("apps/web/src/agent"),
  appSource: read("apps/web/src/App.tsx"),
  agentHandlerSource: read("apps/api/internal/httpapi/agent_handlers.go"),
  workerMainSource: read("apps/api/cmd/worker/main.go"),
  productionGoSources: readProductionGoSources("apps/api"),
}));

if (failures.length > 0) {
  console.error("Architecture validation failed:\n");
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}

console.log("Architecture validation passed: PostgreSQL resource model has no active snapshot/SQLite compatibility path; Agent foundation has no Provider credentials, vendor SDKs, or production wiring.");
