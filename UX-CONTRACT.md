# UX Contract

## Product context

- Audience: 管理个人目标、任务、日程、记录和笔记的中文用户。
- Primary jobs: 快速安排当天行动；保存、检索并关联长期信息。
- Target market(s): 未由仓库资料明确，当前仅承诺中文界面。
- Active locales: `zh-CN` 文案和格式；输入必须兼容中文 IME。
- Language/content register and native-review policy: 简体中文、直接动作词；发布前由产品所有者审阅业务文案。
- Timezone/calendar policy: 沿用浏览器本地时区与现有日期工具。
- Accessibility target: WCAG 2.2 AA。

## Business-context sources

仓库暂无独立 PRD/ADR；以下代码与测试是当前实现证据，产品策略仍以明确的用户决定为准。

| Domain / scope | Authoritative source | Source type | Reviewed date |
|---|---|---|---|
| Permission model | `apps/web/src/domain/types.ts`, `apps/web/src/pages/AgentPage.tsx` | Domain/UI evidence | 2026-09-03 |
| Data lifecycle | `apps/api/internal/service/content.go`, `apps/web/src/store/reducer.ts` | API/domain implementation | 2026-09-03 |
| Deletion / retention | `apps/api/internal/service/content.go` | Soft-delete implementation | 2026-09-03 |
| Billing / payment | Not applicable | — | 2026-09-03 |
| Legal / regulatory copy | Not present | — | 2026-09-03 |
| Market / content conventions | Existing Simplified Chinese UI copy | Product evidence | 2026-09-03 |

## Visual contract

- Project `DESIGN.md`: `DESIGN.md`。
- Token ownership model: existing runtime canonical。
- Runtime design-system/token source: `apps/web/src/styles.css`。
- Mapping/export/adapters: CSS variables → shared/component classes → React components。
- Token drift gate: DESIGN.md lint plus changed-code search for raw visual literals.
- Supported themes: light only; forced-colors remains system-operable。
- Design-context owner/review policy: system-level visual changes update CSS and DESIGN.md together。

## Canonical UI Map

| Capability | Canonical owner | Source of truth | Allowed variants | Verification |
|---|---|---|---|---|
| Select/Listbox | Existing native selects where platform popup ownership is accepted | Existing forms | native | keyboard + browser |
| Date | Existing typed/native date inputs | Existing forms | native | locale + keyboard + browser |
| Form | `.form-field` plus feature validation | This contract | create / edit / inline | interaction tests |
| Scrollbar | Application stylesheet | `DESIGN.md` + `styles.css` | geometry exceptions | computed style |
| Toast | `UiProvider` + `ToastViewport` | This contract | success / warning / info / error | live-region test |
| CRUD | `AppStore` reducer + offline mutation queue + shared Modal | Domain services and this contract | modal / inline | full-flow tests |

## Component behavior

| Component | Default | Hover | Focus | Active | Disabled | Busy | Error |
|---|---|---|---|---|---|---|---|
| Button | semantic style | visible surface change | 3px focus ring | darker/pressed surface | non-interactive | stable geometry | action stays available after recovery |
| Icon button | accessible name | visible surface change | 3px focus ring | pressed surface | non-interactive | stable geometry | adjacent message |
| Input | bordered surface | unchanged geometry | border + focus ring | n/a | muted | submit blocked | inline associated text |
| Search | custom clear when non-empty | unchanged geometry | input focus ring | n/a | muted | stable adornment | search region message |
| Textarea | bordered surface, `resize: none` for new/touched flows | unchanged geometry | border + focus ring | n/a | muted | submit blocked | inline associated text |
| Table/list | stable rows | row/action affordance | contained focus | selected state when relevant | n/a | content retained | inline retry |

## Dataset navigation

- Small bounded lists, including the current tag directory, render all items and scroll inside their dialog body.
- Empty state explains what the resource does and keeps the create action visible.
- Search/filter URL persistence is required only for route-level committed datasets, not modal-local management lists.

## Flow ledger

| Operation | Trigger | Pending | Success destination | Success feedback | Failure recovery | Focus outcome | Source ref |
|---|---|---|---|---|---|---|---|
| Create tag | 添加标签 | local commit without loader | Stay in tag manager | “标签…已创建” toast | Keep value and show inline error | Return to new-tag input | User decision 2026-09-03 |
| Edit tag | 编辑 → 保存名称 | local commit without loader | Stay on edited row | “标签…已重命名” toast | Keep edit mode and inline error | Edited row action | User decision 2026-09-03 |
| Delete tag | 删除标签 | Confirm in same Modal state | Return to tag list | “标签…已删除” toast | Keep confirmation state | Next row, create input, or dialog heading | User decision 2026-09-03 |
| Search notes | 搜索笔记 | immediate local filter | Same route | Result list | Clear/edit query | Search input | Existing NotesPage |
| Edit note body | Rich text / Markdown source | dialog-local canonical Markdown draft | Stay in note dialog until Save | existing note toast after Save | invalid rich rendering keeps source intact and offers Return to source | current editor surface | User decision 2026-09-03 |
| Cancel/back | 取消 / 关闭 | none | Prior context | none | n/a | Original trigger | Shared Modal |

## Navigation and responsive behavior

- Route document title policy: retain the existing application policy; route-title work is outside the global-tag slice.
- Breadcrumb/tab/route-state policy: tag management is contextual to notes and does not create a route.
- Sidebar/drawer/bottom-sheet transformation: shared Modal becomes bottom-aligned with full-width actions at ≤580px.
- Truncation/full-value access: tag names wrap in the manager; content tags follow existing compact display.
- Focus restoration and sticky-obstruction policy: Radix dialog restores trigger focus; internal edits focus their input.
- Note editor top-level and Markdown sub-mode tabs use the horizontal Arrow-key tab model; focus moves independently and Enter/Space activates the focused tab.
- Editor toolbar, source, preview, code blocks, and tables own their local overflow; they must not create page-level horizontal scrolling.

## Overlays and feedback

- Dialog primitive: `apps/web/src/components/Modal.tsx` backed by Radix Dialog.
- Destructive confirmation levels: tag delete requires app-owned confirmation because it removes references globally; the confirmation names note and record impact counts. Cancel receives initial focus.
- Toast placement/duration/deduplication: shared bottom-right `ToastViewport`; current duration 3400ms. Critical correction remains inline.
- Unsaved-changes behavior: inline tag edit cancels with Escape and preserves the last saved value; closing the manager discards an unsubmitted draft.
- Layer/z-index contract: keep existing app layers; no nested dialogs.

## Async and resilience

- Mutation default: optimistic local-first with queued synchronization, matching `AppStore`.
- Idempotency and duplicate-submit policy: disable/no-op invalid submissions; one reducer action per user commit.
- Offline/read-stale/write behavior: guest changes persist locally; authenticated changes enter the existing offline mutation queue.
- Version conflict and multi-tab behavior: existing sync engine remains authoritative; no screen-local retry protocol.
- Stale-request cancellation/invalidation and pending-state ownership: no remote request is started by the component.
- Dialog/form preservation and retry after mutation failure: reducer commits locally; synchronization feedback follows the existing global sync status.
- `Note.bodyMarkdown` / `notes.body_markdown` is the only persisted note-body representation. Rich text, source, and preview are views of the dialog-local canonical Markdown draft; mode switches never dispatch a Store mutation.

## Validation

- Schema/validation layer: feature-local pure name validation shared by create and rename.
- Trigger timing: submit, then revalidate the field on edit.
- Error summary/inline policy: empty, case-insensitive duplicate, and >80-character names show associated inline text.
- Sensitive-value handling: not applicable.
- Forms use `noValidate`; invalid input exposes `aria-invalid` and `aria-describedby`. Enter submits only when `event.isComposing` is false; Escape cancels inline rename.
- Markdown preview enables GFM but never raw HTML, MDX JSX, expressions, or imports. Rich-text parse failure preserves the exact source and exposes “返回源码”.
- Markdown source and rich-text input must preserve Chinese IME composition; composition Enter does not switch mode, submit, or truncate text.

## Verification

- Required static commands: DESIGN.md lint, strict premium audit, TypeScript typecheck, frontend tests/build, Go tests/build.
- Browser/device/locale/theme matrix: desktop and ≤580px; pointer and keyboard; Chinese IME-safe Enter; reduced motion.
- Accessibility checks: dialog naming/focus/Escape/restoration, associated errors, semantic buttons, minimum target size.
- Canonical sibling flow used for comparison: SettingsDialog and EntityDialog using shared Modal/Toast/Store.
- CRUD full-flow evidence: `apps/web/src/App.test.tsx`, reducer/store tests, API service/router tests.
- Failure-path evidence: validation interaction tests and existing sync engine tests.
