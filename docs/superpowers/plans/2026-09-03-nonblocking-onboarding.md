# Non-blocking Onboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let new users enter the Today page immediately, while keeping the four-step onboarding available from an inline starter card and Settings.

**Architecture:** `UiProvider` becomes the sole owner of onboarding visibility and exposes `openOnboarding(): void`. `OnboardingDialog` becomes a controlled, dismissible dialog whose drafts reset on every open; `TodayPage` and `SettingsDialog` only invoke the shared action. The existing `complete-onboarding` reducer action and persisted `onboardingCompleted` field remain unchanged.

**Tech Stack:** React 19, TypeScript 5.9, Radix Dialog through the existing `Modal`, Vitest, Testing Library, CSS custom properties.

**Spec:** `docs/superpowers/specs/2026-09-03-nonblocking-onboarding-design.md`

## Global Constraints

- First entry must never open onboarding automatically or block navigation.
- The starter card is visible exactly when `data.goals.length === 0 && data.tasks.length === 0`.
- Closing onboarding through its close button, Escape, or backdrop must not dispatch a store mutation.
- Every opening starts at step one with default drafts; completion still dispatches the existing `complete-onboarding` action.
- Settings must close before onboarding opens; nested dialogs are forbidden.
- Preserve `AppSettings.onboardingCompleted` and all API/database formats.
- Reuse the shared `Modal`, button classes, CSS tokens, 44px touch targets, visible focus, and existing reduced-motion behavior.
- Do not use browser `alert()`, `confirm()`, or `prompt()` for new interactions.

---

### Task 1: Controlled and resettable onboarding dialog

**Files:**
- Modify: `apps/web/src/App.test.tsx:330`
- Modify: `apps/web/src/ui/UiProvider.tsx:10-75`
- Modify: `apps/web/src/components/OnboardingDialog.tsx:1-52`

**Interfaces:**
- Produces: `UiContextValue.openOnboarding(): void`
- Produces: `OnboardingDialog({ open, onClose }: { open: boolean; onClose(): void }): JSX.Element`
- Preserves: reducer action `{ type: "complete-onboarding"; goals: Goal[]; focusAreas: Area[]; dataMode: DataMode }`

- [x] **Step 1: Replace the automatic-open test with failing controlled-dialog tests**

In `apps/web/src/App.test.tsx`, replace the existing `新用户可以创建目标并生成第一个今日行动` test with tests that first establish non-blocking entry, explicit open/cancel/reset, and successful completion:

```tsx
it("新用户首次进入时不再被入门向导阻断", async () => {
  localStorage.clear();
  render(<GuestHarness />);

  expect(screen.queryByRole("dialog", { name: "把接下来想发生的变化放进日序" })).not.toBeInTheDocument();
  expect(screen.getByRole("heading", { name: /早上好/ })).toBeInTheDocument();
  expect(screen.getByTestId("onboarding-state")).toHaveTextContent("false:0:0");
});

it("取消入门向导不会写入数据且重新打开会重置草稿", async () => {
  localStorage.clear();
  const user = userEvent.setup();
  render(<GuestHarness />);

  await user.click(screen.getByRole("button", { name: "使用入门向导" }));
  await user.click(screen.getByRole("button", { name: /下一步/ }));
  await user.type(screen.getByLabelText("目标名称"), "未提交目标");
  await user.click(screen.getByRole("button", { name: "关闭" }));

  expect(screen.getByTestId("onboarding-state")).toHaveTextContent("false:0:0");
  await user.click(screen.getByRole("button", { name: "使用入门向导" }));
  expect(screen.getByText("最近最想照顾哪些部分？")).toBeInTheDocument();
  expect(screen.queryByDisplayValue("未提交目标")).not.toBeInTheDocument();
});

it("主动完成入门向导仍会创建目标和第一个今日行动", async () => {
  localStorage.clear();
  const user = userEvent.setup();
  render(<GuestHarness />);

  await user.click(screen.getByRole("button", { name: "使用入门向导" }));
  await user.click(screen.getByRole("button", { name: /下一步/ }));
  await user.type(screen.getByLabelText("目标名称"), "完成作品集");
  await user.type(screen.getByLabelText("为什么重要"), "形成可以展示的真实成果");
  await user.click(screen.getByRole("button", { name: /下一步/ }));
  await user.click(screen.getByRole("button", { name: /下一步/ }));
  await user.click(screen.getByRole("button", { name: /开始使用/ }));

  await waitFor(() => expect(screen.queryByRole("dialog", { name: "把接下来想发生的变化放进日序" })).not.toBeInTheDocument());
  expect(screen.getByTestId("onboarding-state")).toHaveTextContent("true:1:1");
});
```

- [x] **Step 2: Run the focused tests and verify RED**

Run:

```powershell
npm run test --workspace @dayorder/web -- src/App.test.tsx -t "新用户首次进入|取消入门向导|主动完成入门向导"
```

Expected: FAIL because onboarding still opens automatically and `使用入门向导` does not exist.

- [x] **Step 3: Add provider-owned visibility and a controlled dialog**

In `UiProvider.tsx`, add the context action and state, include it in the memoized value, and pass controlled props:

```tsx
interface UiContextValue {
  openOnboarding(): void;
  // existing actions remain unchanged
}

const [onboardingOpen, setOnboardingOpen] = useState(false);

const value = useMemo<UiContextValue>(() => ({
  openOnboarding: () => setOnboardingOpen(true),
  // existing actions remain unchanged
}), [toast]);

<OnboardingDialog open={onboardingOpen} onClose={() => setOnboardingOpen(false)} />
```

In `OnboardingDialog.tsx`, remove `useAuth`, `syncStatus`, and the derived `open`. Accept controlled props, reset all local drafts whenever `open` becomes true, and close after completion:

```tsx
import { useEffect, useMemo, useState } from "react";

export function OnboardingDialog({ open, onClose }: { open: boolean; onClose(): void }) {
  const { dispatch } = useAppStore();
  // existing draft state

  useEffect(() => {
    if (!open) return;
    setStep(0);
    setFocusAreas(["工作"]);
    setGoals([newGoal("工作")]);
    setDataMode("local");
  }, [open]);

  const finish = () => {
    if (!goalsValid) return;
    // preserve the existing Goal mapping
    dispatch({ type: "complete-onboarding", goals: created, focusAreas, dataMode });
    onClose();
    toast("首次设置已完成，已生成第一个今日行动");
  };

  return <Modal open={open} title="把接下来想发生的变化放进日序" onClose={onClose} size="large" footer={footer}>...</Modal>;
}
```

- [x] **Step 4: Run typecheck to expose missing callers before adding the card**

Run:

```powershell
npm run typecheck --workspace @dayorder/web
```

Expected: PASS for the controlled signature; the interaction tests still fail only because no explicit trigger exists yet.

- [x] **Step 5: Commit the controlled-dialog slice**

```powershell
git add apps/web/src/ui/UiProvider.tsx apps/web/src/components/OnboardingDialog.tsx apps/web/src/App.test.tsx
git commit -m "refactor(web): make onboarding optional and controlled"
```

### Task 2: Empty-data starter card on Today

**Files:**
- Modify: `apps/web/src/App.test.tsx`
- Modify: `apps/web/src/pages/TodayPage.tsx:10-70`
- Modify: `apps/web/src/styles.css:180-407,715-750`

**Interfaces:**
- Consumes: `useUi().editGoal(value?: Goal): void`
- Consumes: `useUi().openOnboarding(): void`
- Produces: semantic `<section className="starter-card" aria-labelledby="starter-title">`

- [x] **Step 1: Add failing starter-card behavior tests**

Add:

```tsx
it("空数据的今天页提供两条非阻断起步路径", () => {
  localStorage.clear();
  render(<GuestHarness />);

  const starter = screen.getByRole("region", { name: "开始安排你的日序" });
  expect(within(starter).getByRole("button", { name: "创建第一个目标" })).toBeInTheDocument();
  expect(within(starter).getByRole("button", { name: "使用入门向导" })).toBeInTheDocument();
});

it("直接创建第一个目标后起步卡片消失", async () => {
  localStorage.clear();
  const user = userEvent.setup();
  render(<GuestHarness />);

  await user.click(screen.getByRole("button", { name: "创建第一个目标" }));
  await user.type(screen.getByLabelText("目标名称"), "建立晨间习惯");
  await user.click(screen.getByRole("button", { name: "创建更改" }));

  await waitFor(() => expect(screen.queryByRole("region", { name: "开始安排你的日序" })).not.toBeInTheDocument());
  expect(screen.getByTestId("onboarding-state")).toHaveTextContent("false:1:0");
});
```

Before running, verify the actual create-goal submit accessible name generated by `Footer`; if it is `创建更改`, keep the assertion above.

- [x] **Step 2: Run focused tests and verify RED**

Run:

```powershell
npm run test --workspace @dayorder/web -- src/App.test.tsx -t "空数据的今天页|直接创建第一个目标"
```

Expected: FAIL because the starter region does not exist.

- [x] **Step 3: Render the card with existing actions**

Extend the Today page action destructure and render the card between `.view-head` and `.day-board`:

```tsx
const { openCapture, openReview, openOnboarding, editGoal, editTask, editEvent, toast } = useUi();
const showStarter = data.goals.length === 0 && data.tasks.length === 0;

{showStarter && (
  <section className="starter-card" aria-labelledby="starter-title">
    <div>
      <p className="eyebrow">从这里开始</p>
      <h2 id="starter-title">开始安排你的日序</h2>
      <p>先创建一个真实目标，或用四步向导一起整理关注领域和第一个今日行动。</p>
    </div>
    <div className="starter-actions">
      <button className="button primary" type="button" onClick={() => editGoal()}>创建第一个目标</button>
      <button className="button secondary" type="button" onClick={openOnboarding}>使用入门向导</button>
    </div>
  </section>
)}
```

Add token-based styling with no new colors:

```css
.starter-card { display: flex; align-items: center; justify-content: space-between; gap: 24px; margin-bottom: 18px; border: 1px solid var(--line); border-left: 3px solid var(--primary); border-radius: var(--r-panel); background: var(--surface); padding: 18px 20px; }
.starter-card h2 { margin: 2px 0 5px; font-size: 18px; }
.starter-card p:last-child { margin: 0; color: var(--ink-2); font-size: 11px; line-height: 1.6; }
.starter-actions { display: flex; flex: 0 0 auto; flex-wrap: wrap; gap: 8px; }

@media (max-width: 580px) {
  .starter-card { align-items: stretch; flex-direction: column; padding: 16px; }
  .starter-actions { display: grid; grid-template-columns: 1fr; }
  .starter-actions .button { width: 100%; min-height: 44px; }
}
```

- [x] **Step 4: Run focused tests and verify GREEN**

Run:

```powershell
npm run test --workspace @dayorder/web -- src/App.test.tsx -t "新用户首次进入|取消入门向导|主动完成入门向导|空数据的今天页|直接创建第一个目标"
```

Expected: all five onboarding tests PASS.

- [x] **Step 5: Commit the starter-card slice**

```powershell
git add apps/web/src/pages/TodayPage.tsx apps/web/src/styles.css apps/web/src/App.test.tsx
git commit -m "feat(web): add nonblocking onboarding entry"
```

### Task 3: Settings entry without nested dialogs

**Files:**
- Modify: `apps/web/src/App.test.tsx`
- Modify: `apps/web/src/components/SettingsDialog.tsx:1-65`

**Interfaces:**
- Consumes: `useUi().openOnboarding(): void`
- Preserves: `SettingsDialog({ open, onClose }: { open: boolean; onClose(): void })`

- [x] **Step 1: Add a failing no-nesting test**

Add:

```tsx
it("设置入口先关闭设置再打开入门向导", async () => {
  const user = userEvent.setup();
  render(<GuestHarness />);

  await user.click(screen.getByRole("button", { name: "账户与设置" }));
  await user.click(screen.getByRole("menuitem", { name: /应用设置/ }));
  const settings = screen.getByRole("dialog", { name: "数据与设置" });
  await user.click(within(settings).getByRole("button", { name: /重新运行入门向导/ }));

  expect(screen.getAllByRole("dialog")).toHaveLength(1);
  expect(screen.queryByRole("dialog", { name: "数据与设置" })).not.toBeInTheDocument();
  expect(screen.getByRole("dialog", { name: "把接下来想发生的变化放进日序" })).toBeInTheDocument();
});
```

If the account-menu trigger has a different current accessible name, inspect `AccountControls.tsx` and use that exact name; do not add a test-only label.

- [x] **Step 2: Run the focused test and verify RED**

Run:

```powershell
npm run test --workspace @dayorder/web -- src/App.test.tsx -t "设置入口先关闭设置"
```

Expected: FAIL because Settings has no onboarding row.

- [x] **Step 3: Add the settings row and sequence close/open**

In `SettingsDialog.tsx`, consume `openOnboarding` and derive copy from settings/data:

```tsx
const { openOnboarding, toast } = useUi();
const hasExistingContent = data.settings.onboardingCompleted || data.goals.length > 0 || data.tasks.length > 0;
const onboardingLabel = hasExistingContent ? "重新运行入门向导" : "使用入门向导";

const launchOnboarding = () => {
  onClose();
  openOnboarding();
};
```

Append this row to `.setting-list`:

```tsx
<div className="setting-row">
  <div>
    <strong>入门向导</strong>
    <span>{hasExistingContent ? "完成后会新增目标和今日任务，不会覆盖现有内容" : "用四步整理关注领域、目标和第一个今日行动"}</span>
  </div>
  <button className="button secondary" type="button" onClick={launchOnboarding}>{onboardingLabel}</button>
</div>
```

- [x] **Step 4: Run the onboarding interaction tests and typecheck**

Run:

```powershell
npm run test --workspace @dayorder/web -- src/App.test.tsx -t "入门向导|空数据的今天页|直接创建第一个目标|设置入口"
npm run typecheck --workspace @dayorder/web
```

Expected: all focused tests PASS and TypeScript exits 0.

- [x] **Step 5: Commit the settings-entry slice**

```powershell
git add apps/web/src/components/SettingsDialog.tsx apps/web/src/App.test.tsx
git commit -m "feat(web): expose onboarding from settings"
```

### Task 4: Durable contracts and full verification

**Files:**
- Modify: `UX-CONTRACT.md`
- Modify: `DESIGN.md` only if the final implementation introduces a durable visual rule not already covered by token reuse and the existing empty-state guidance

**Interfaces:**
- Documents: optional onboarding flow, cancellation/reset semantics, starter-card condition, settings handoff, focus behavior

- [x] **Step 1: Update the UX flow ledger and overlay rules**

Add the following row to `UX-CONTRACT.md`'s flow ledger:

```markdown
| Run onboarding | Today starter card / Settings | none until final confirmation | Return to Today context | completion toast | Cancel discards dialog-local drafts | Trigger on cancel; Today content after completion | User decision 2026-09-03 |
```

Add these explicit rules under overlays/feedback:

```markdown
- Onboarding is never opened from persisted settings automatically; `onboardingCompleted` records completion only.
- Every explicit onboarding open starts from step one with fresh dialog-local drafts.
- Settings closes before onboarding opens, so the shared Modal is never nested.
- The Today starter card appears only while both goals and tasks are empty.
```

- [x] **Step 2: Scan plan/spec coverage and forbidden placeholders**

Run:

```powershell
rg -n -e "T[B]D" -e "T[O]DO" -e "implement l[a]ter" -e "fill in d[e]tails" -e "Similar to T[a]sk" docs/superpowers/plans/2026-09-03-nonblocking-onboarding.md
rg -n "自动|起步卡片|设置|取消|重置|onboardingCompleted|嵌套|320" docs/superpowers/specs/2026-09-03-nonblocking-onboarding-design.md docs/superpowers/plans/2026-09-03-nonblocking-onboarding.md
```

Expected: the placeholder scan returns no matches; every spec topic has a corresponding plan match.

- [x] **Step 3: Run the full repository checks**

Run:

```powershell
npm run test:web
npm run typecheck
npm run build:web
npm run test:api
npm run build:api
npx -p @google/design.md designmd lint DESIGN.md
```

Expected: all commands exit 0. If Python is available, also run the premium strict audit; otherwise record the missing runtime honestly:

```powershell
python C:\Users\yeshaopeng\.codex\plugins\cache\openai-curated-remote\frontend-design-premium\1.4.0\skills\frontend-design-premium\scripts\audit_project.py . --mode strict
```

- [x] **Step 4: Verify browser and accessibility behavior**

> Verification note (2026-09-03): the host reported no available browser runtime. Automated onboarding, Modal, keyboard, focus, and responsive-contract tests are recorded as evidence; desktop and narrow-screen visual inspection remains a manual follow-up.

At desktop and 320–580px widths verify: first entry is usable, starter actions remain 44px high, Escape/backdrop/close discard drafts, focus returns to the triggering button, Settings and onboarding never coexist, and reduced-motion does not add delayed state. If the in-app browser is unavailable, record that component interaction tests are the available evidence and do not claim visual browser verification.

- [x] **Step 5: Commit contracts after checks pass**

> Integration note (2026-09-03): the slice checkpoints were consolidated into the final feature commit before the requested local merge.

```powershell
git add UX-CONTRACT.md DESIGN.md
git commit -m "docs: record optional onboarding contract"
```
