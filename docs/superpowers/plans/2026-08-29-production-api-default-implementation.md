# Production API Default Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make production Web builds default to `https://better-api.shier.art/api/v1` while development keeps using the localhost Vite proxy and `VITE_API_BASE_URL` remains an explicit override.

**Architecture:** Keep API URL selection in `apps/web/src/api/http.ts`, where Vite's `import.meta.env.PROD` selects the fallback at build time. Extract the selection into a small pure function so development, production, override, whitespace, and trailing-slash behavior are directly testable without changing any business API modules.

**Tech Stack:** TypeScript 5.9, Vite 8, Vitest 4, npm workspaces, Markdown deployment documentation

---

## File Structure

- Modify `apps/web/src/api/http.ts`: resolve the configured or environment-specific API base URL.
- Modify `apps/web/src/api/http.test.ts`: verify URL precedence and preserve the existing development request behavior.
- Modify `README.md`: document mode-specific defaults and the normal production build command.
- Modify `docs/runbooks/separate-deployment.md`: document the production default and explicit override command for operators.

### Task 1: Implement the environment-specific API base URL

**Files:**
- Modify: `apps/web/src/api/http.ts:1-2`
- Test: `apps/web/src/api/http.test.ts:1-7`

- [ ] **Step 1: Write the failing URL-resolution tests**

Change the import and add this suite before `describe("apiRequest", ...)`:

```ts
import { describe, expect, it, vi } from "vitest";
import { ApiError, apiRequest, resolveApiBaseUrl } from "./http";

describe("resolveApiBaseUrl", () => {
  it.each([
    [undefined, false, "/api/v1"],
    [undefined, true, "https://better-api.shier.art/api/v1"],
    ["   ", true, "https://better-api.shier.art/api/v1"],
    [" https://staging-api.example.com/api/v1/ ", false, "https://staging-api.example.com/api/v1"],
    [" https://staging-api.example.com/api/v1/ ", true, "https://staging-api.example.com/api/v1"],
  ])("配置为 %j、生产模式为 %j 时返回 %s", (configured, production, expected) => {
    expect(resolveApiBaseUrl(configured, production)).toBe(expected);
  });
});
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
npm run test --workspace @dayorder/web -- src/api/http.test.ts
```

Expected: FAIL because `./http` does not export `resolveApiBaseUrl`.

- [ ] **Step 3: Add the minimal resolver and use it for the exported constant**

Replace the first two lines of `apps/web/src/api/http.ts` with:

```ts
const PRODUCTION_API_BASE_URL = "https://better-api.shier.art/api/v1";

export function resolveApiBaseUrl(configuredBaseUrl: string | undefined, production: boolean): string {
  const defaultBaseUrl = production ? PRODUCTION_API_BASE_URL : "/api/v1";
  return (configuredBaseUrl?.trim() || defaultBaseUrl).replace(/\/$/, "");
}

export const API_BASE_URL = resolveApiBaseUrl(import.meta.env.VITE_API_BASE_URL, import.meta.env.PROD);
```

- [ ] **Step 4: Run the focused test and verify GREEN**

Run:

```bash
npm run test --workspace @dayorder/web -- src/api/http.test.ts
```

Expected: PASS for all `resolveApiBaseUrl` and `apiRequest` tests; the existing request test still calls `/api/v1/probe` under Vitest's non-production mode.

- [ ] **Step 5: Commit the behavior and tests**

```bash
git add apps/web/src/api/http.ts apps/web/src/api/http.test.ts
git commit -m "feat(web): set production API default"
```

### Task 2: Document development, production, and override behavior

**Files:**
- Modify: `README.md:125,136-154`
- Modify: `docs/runbooks/separate-deployment.md:17-28`

- [ ] **Step 1: Update the README configuration table**

Replace the `VITE_API_BASE_URL` row with:

```markdown
| `VITE_API_BASE_URL` | 前端 API 根地址；开发默认 `/api/v1`，生产默认 `https://better-api.shier.art/api/v1`，显式设置时覆盖默认值 |
```

- [ ] **Step 2: Update the README Web build instructions**

Replace the paragraphs and command blocks from “同域代理 `/api` 时直接构建” through the explicit URL example with:

````markdown
开发环境默认请求 `/api/v1`，由 Vite 代理到 `http://127.0.0.1:8080`。生产构建未设置 `VITE_API_BASE_URL` 时，默认请求 `https://better-api.shier.art/api/v1`：

```bash
npm run build:release:web
```

预发布环境或其他部署需要不同的 API 地址时，在构建命令中显式覆盖：

```bash
VITE_API_BASE_URL=https://staging-api.example.com/api/v1 npm run build:release:web
```
````

- [ ] **Step 3: Update the separate-deployment runbook**

Replace the paragraphs and command blocks from “同域代理 `/api` 时运行” through the explicit URL example with the same mode-specific explanation and commands used in README:

````markdown
开发环境默认请求 `/api/v1`，由 Vite 代理到 `http://127.0.0.1:8080`。生产构建未设置 `VITE_API_BASE_URL` 时，默认请求 `https://better-api.shier.art/api/v1`：

```bash
npm run build:release:web
```

预发布环境或其他部署需要不同的 API 地址时，在构建命令中显式覆盖：

```bash
VITE_API_BASE_URL=https://staging-api.example.com/api/v1 npm run build:release:web
```
````

- [ ] **Step 4: Verify documentation consistency**

Run:

```bash
rg -n "better-api\.shier\.art|VITE_API_BASE_URL|127\.0\.0\.1:8080" README.md docs/runbooks/separate-deployment.md
git diff --check
```

Expected: both documents show the production default, development proxy, and explicit override; `git diff --check` exits successfully.

- [ ] **Step 5: Commit the documentation**

```bash
git add README.md docs/runbooks/separate-deployment.md
git commit -m "docs: explain environment-specific API defaults"
```

### Task 3: Run complete verification

**Files:**
- Verify: `apps/web/src/api/http.ts`
- Verify: `apps/web/src/api/http.test.ts`
- Verify: `README.md`
- Verify: `docs/runbooks/separate-deployment.md`
- Generated and ignored: `release/web/`

- [ ] **Step 1: Run all Web tests**

Run:

```bash
npm run test:web
```

Expected: exit code 0 with no failed Vitest suites or tests.

- [ ] **Step 2: Run TypeScript type checking**

Run:

```bash
npm run typecheck
```

Expected: exit code 0 with no TypeScript errors.

- [ ] **Step 3: Build the default production Web release**

Run:

```bash
npm run build:release:web
```

Expected: exit code 0 and `release/web/index.html` exists.

- [ ] **Step 4: Inspect the production bundle and final diff**

Run:

```bash
rg -l -F "https://better-api.shier.art/api/v1" release/web/assets
git diff --check
git status --short
```

Expected: at least one built JavaScript asset contains the production API URL, `git diff --check` exits successfully, and only expected ignored build outputs or intentional changes are present.

- [ ] **Step 5: Review requirements against the final implementation**

Confirm all of the following from fresh command output and the final diff:

- Development default remains `/api/v1` and Vite continues proxying `/api` to localhost by default.
- Production default is `https://better-api.shier.art/api/v1`.
- Non-empty `VITE_API_BASE_URL` overrides either environment default.
- README and the deployment runbook describe the implemented behavior.
