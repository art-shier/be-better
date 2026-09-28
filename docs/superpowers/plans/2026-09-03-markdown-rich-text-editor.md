# Markdown-first Rich Text Editor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the note body textarea with a Markdown-backed editor that offers rich text, Markdown source, and safe Markdown preview without changing persisted data.

**Architecture:** A focused `NoteContentEditor` owns only presentation mode, Markdown sub-mode, MDXEditor configuration, and synchronization around one canonical Markdown string supplied by `NoteEditor`. `react-markdown` plus `remark-gfm` renders read-only previews without raw-HTML execution; the surrounding note form remains the only place that dispatches a note mutation.

**Tech Stack:** React 19, TypeScript 5.9, `@mdxeditor/editor@4.2.3`, `react-markdown@10.1.0`, `remark-gfm@4.0.1`, Vitest, Testing Library, CSS custom properties.

**Spec:** `docs/superpowers/specs/2026-09-03-markdown-rich-text-editor-design.md`

## Global Constraints

- Markdown remains the only persisted representation in `Note.bodyMarkdown` and `notes.body_markdown`; do not change API, database, offline queue, or sync payloads.
- Top-level modes are `富文本` and `Markdown`; Markdown sub-modes are `源码` and `预览`.
- Rich text exposes only headings, bold, italic, links, ordered/unordered lists, quote, inline code, code block, thematic break, undo, and redo.
- Preview enables GFM and must not enable `rehype-raw`, MDX JSX, expressions, imports, or `dangerouslySetInnerHTML`.
- Switching modes updates only the note form draft; one click on `保存笔记` produces one existing note mutation.
- Existing Markdown loads without migration and invalid rich-text input remains editable/savable in source mode.
- The editor must be IME-safe, keyboard operable, WCAG 2.2 AA, responsive at 320–580px, and use existing CSS tokens.
- Textarea resizing is disabled; provide about 320px desktop and 240px narrow-screen minimum editing height.
- Do not add autosave, image upload, collaboration, comments, custom colors/fonts, or a second storage format.

---

### Task 1: Markdown source and safe preview component

**Files:**
- Modify: `package.json` and `package-lock.json`
- Modify: `apps/web/package.json`
- Create: `apps/web/src/components/NoteContentEditor.tsx`
- Create: `apps/web/src/components/NoteContentEditor.test.tsx`

**Interfaces:**
- Produces: `export interface NoteContentEditorProps { value: string; onChange(value: string): void }`
- Produces: `export function NoteContentEditor(props: NoteContentEditorProps): JSX.Element`
- Internal state: `type EditorMode = "rich" | "markdown"`; `type MarkdownMode = "source" | "preview"`

- [x] **Step 1: Write failing source/preview tests before installing dependencies**

Create `NoteContentEditor.test.tsx` with:

```tsx
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { NoteContentEditor } from "./NoteContentEditor";

function Harness({ initial = "" }: { initial?: string }) {
  const [value, setValue] = useState(initial);
  return <><NoteContentEditor value={value} onChange={setValue} /><output data-testid="markdown-value">{value}</output></>;
}

describe("NoteContentEditor", () => {
  it("在 Markdown 源码与预览之间同步唯一草稿", async () => {
    const user = userEvent.setup();
    render(<Harness initial="# 原标题" />);

    await user.click(screen.getByRole("tab", { name: "Markdown" }));
    const source = screen.getByRole("textbox", { name: "Markdown 源码" });
    expect(source).toHaveValue("# 原标题");
    await user.clear(source);
    await user.type(source, "## 新标题{enter}{enter}- 第一项");
    await user.click(screen.getByRole("tab", { name: "预览" }));

    const preview = screen.getByRole("tabpanel", { name: "预览" });
    expect(within(preview).getByRole("heading", { level: 2, name: "新标题" })).toBeInTheDocument();
    expect(within(preview).getByRole("listitem")).toHaveTextContent("第一项");
    expect(screen.getByTestId("markdown-value")).toHaveTextContent("## 新标题");
  });

  it("预览支持 GFM 且不执行原始 HTML", async () => {
    const user = userEvent.setup();
    const markdown = "| 项目 | 状态 |\n| --- | --- |\n| 编辑器 | 完成 |\n\n- [x] 预览\n\n<script>window.hacked = true</script>\n<div onclick=\"window.hacked=true\">危险</div>";
    render(<Harness initial={markdown} />);
    await user.click(screen.getByRole("tab", { name: "Markdown" }));
    await user.click(screen.getByRole("tab", { name: "预览" }));

    const preview = screen.getByRole("tabpanel", { name: "预览" });
    expect(within(preview).getByRole("table")).toBeInTheDocument();
    expect(within(preview).getByRole("checkbox")).toBeChecked();
    expect(preview.querySelector("script")).toBeNull();
    expect(preview.querySelector("div[onclick]")).toBeNull();
    expect(preview).toHaveTextContent("<script>window.hacked = true</script>");
  });

  it("空预览提供返回编辑的明确提示", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole("tab", { name: "Markdown" }));
    await user.click(screen.getByRole("tab", { name: "预览" }));
    expect(screen.getByText("还没有正文，切换到源码或富文本开始输入。" )).toBeInTheDocument();
  });

  it("源码输入期间的中文组合事件不会触发模式切换", async () => {
    render(<Harness />);
    fireEvent.click(screen.getByRole("tab", { name: "Markdown" }));
    const source = screen.getByRole("textbox", { name: "Markdown 源码" });
    fireEvent.compositionStart(source);
    fireEvent.change(source, { target: { value: "中文草稿" } });
    fireEvent.keyDown(source, { key: "Enter", isComposing: true });
    fireEvent.compositionEnd(source);
    expect(source).toHaveValue("中文草稿");
    expect(screen.getByRole("tab", { name: "源码" })).toHaveAttribute("aria-selected", "true");
  });
});
```

- [x] **Step 2: Run the component test and verify RED**

Run:

```powershell
npm run test --workspace @dayorder/web -- src/components/NoteContentEditor.test.tsx
```

Expected: FAIL because `./NoteContentEditor` does not exist.

- [x] **Step 3: Install the approved exact runtime versions**

Run:

```powershell
npm install --workspace @dayorder/web --save-exact @mdxeditor/editor@4.2.3 react-markdown@10.1.0 remark-gfm@4.0.1
```

Expected: the three exact versions appear in `apps/web/package.json` and the root `package-lock.json` is updated.

- [x] **Step 4: Implement the tab shell, source editor, and safe preview**

Create `NoteContentEditor.tsx` with native tab semantics and no raw-HTML plugin:

```tsx
import { useId, useRef, useState, type KeyboardEvent } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

export interface NoteContentEditorProps {
  value: string;
  onChange(value: string): void;
}

type EditorMode = "rich" | "markdown";
type MarkdownMode = "source" | "preview";

export function NoteContentEditor({ value, onChange }: NoteContentEditorProps) {
  const [mode, setMode] = useState<EditorMode>("rich");
  const [markdownMode, setMarkdownMode] = useState<MarkdownMode>("source");
  const rootId = useId();
  const topTabs = useRef<Array<HTMLButtonElement | null>>([]);
  const markdownTabs = useRef<Array<HTMLButtonElement | null>>([]);

  const moveTabFocus = (event: KeyboardEvent<HTMLButtonElement>, tabs: Array<HTMLButtonElement | null>, index: number) => {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    const next = event.key === "ArrowRight" ? (index + 1) % tabs.length : (index - 1 + tabs.length) % tabs.length;
    tabs[next]?.focus();
  };

  return (
    <div className="note-content-editor">
      <div className="editor-mode-tabs" role="tablist" aria-label="正文编辑模式">
        {([ ["rich", "富文本"], ["markdown", "Markdown"] ] as const).map(([key, label], index) => (
          <button key={key} ref={(node) => { topTabs.current[index] = node; }} role="tab" type="button" aria-selected={mode === key} aria-controls={`${rootId}-${key}`} tabIndex={mode === key ? 0 : -1} onKeyDown={(event) => moveTabFocus(event, topTabs.current, index)} onClick={() => setMode(key)}>{label}</button>
        ))}
      </div>

      {mode === "rich" ? (
        <div id={`${rootId}-rich`} role="tabpanel" aria-label="富文本">富文本编辑器</div>
      ) : (
        <div id={`${rootId}-markdown`} role="tabpanel" aria-label="Markdown">
          <div className="markdown-mode-tabs" role="tablist" aria-label="Markdown 查看模式">
            {([ ["source", "源码"], ["preview", "预览"] ] as const).map(([key, label], index) => (
              <button key={key} ref={(node) => { markdownTabs.current[index] = node; }} role="tab" type="button" aria-selected={markdownMode === key} tabIndex={markdownMode === key ? 0 : -1} onKeyDown={(event) => moveTabFocus(event, markdownTabs.current, index)} onClick={() => setMarkdownMode(key)}>{label}</button>
            ))}
          </div>
          {markdownMode === "source" ? (
            <div role="tabpanel" aria-label="源码"><label className="editor-source-label" htmlFor={`${rootId}-source`}>Markdown 源码</label><textarea id={`${rootId}-source`} className="markdown-source" value={value} onChange={(event) => onChange(event.target.value)} /></div>
          ) : (
            <div className="markdown-preview" role="tabpanel" aria-label="预览">
              {value.trim() ? <ReactMarkdown remarkPlugins={[remarkGfm]}>{value}</ReactMarkdown> : <p className="editor-empty">还没有正文，切换到源码或富文本开始输入。</p>}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
```

Before declaring GREEN, replace the temporary `富文本编辑器` text in Task 2; it is an intentional red-to-green seam for this task, not a shipped fallback.

- [x] **Step 5: Run the focused source/preview tests**

Run:

```powershell
npm run test --workspace @dayorder/web -- src/components/NoteContentEditor.test.tsx
```

Expected: source, preview, GFM, raw-HTML safety, empty state, and IME tests PASS.

- [x] **Step 6: Commit the source/preview slice**

```powershell
git add package.json package-lock.json apps/web/package.json apps/web/src/components/NoteContentEditor.tsx apps/web/src/components/NoteContentEditor.test.tsx
git commit -m "feat(web): add markdown source and safe preview"
```

### Task 2: Markdown-first rich text editing and synchronization

**Files:**
- Modify: `apps/web/src/components/NoteContentEditor.test.tsx`
- Modify: `apps/web/src/components/NoteContentEditor.tsx`

**Interfaces:**
- Consumes: `MDXEditorMethods.setMarkdown(markdown: string): void`
- Consumes: `MDXEditorProps.onChange(markdown: string): void`
- Preserves: `NoteContentEditorProps`

- [x] **Step 1: Add failing rich-text and mode-sync tests**

Append tests that assert existing Markdown is visible, rich input changes canonical Markdown, and round trips do not lose a source draft:

```tsx
it("默认以富文本显示已有 Markdown", () => {
  render(<Harness initial="# 已有标题\n\n正文" />);
  expect(screen.getByRole("tab", { name: "富文本" })).toHaveAttribute("aria-selected", "true");
  expect(screen.getByRole("textbox", { name: "富文本正文" })).toHaveTextContent("已有标题");
  expect(screen.getByRole("toolbar", { name: "正文格式" })).toBeInTheDocument();
});

it("从源码切回富文本时保留最新 Markdown", async () => {
  const user = userEvent.setup();
  render(<Harness initial="原文" />);
  await user.click(screen.getByRole("tab", { name: "Markdown" }));
  const source = screen.getByRole("textbox", { name: "Markdown 源码" });
  await user.clear(source);
  await user.type(source, "## 切换后的标题");
  await user.click(screen.getByRole("tab", { name: "富文本" }));
  expect(screen.getByRole("textbox", { name: "富文本正文" })).toHaveTextContent("切换后的标题");
  expect(screen.getByTestId("markdown-value")).toHaveTextContent("## 切换后的标题");
});

it("富文本输入与格式化导出 Markdown", async () => {
  const user = userEvent.setup();
  render(<Harness />);
  const editor = screen.getByRole("textbox", { name: "富文本正文" });
  await user.click(editor);
  await user.type(editor, "重要内容");
  await user.keyboard("{Control>}a{/Control}");
  await user.click(screen.getByRole("button", { name: "粗体" }));
  await waitFor(() => expect(screen.getByTestId("markdown-value")).toHaveTextContent("**重要内容**"));
  await user.click(screen.getByRole("button", { name: "引用" }));
  await waitFor(() => expect(screen.getByTestId("markdown-value")).toHaveTextContent(">"));
});
```

Use the actual accessible textbox exposed by MDXEditor. If its Lexical surface lacks the product name, add `aria-label="富文本正文"` through the supported `contentEditableClassName`/root customization or a labelled containing region; do not use a test-only query.

- [x] **Step 2: Run rich-text tests and verify RED**

Run:

```powershell
npm run test --workspace @dayorder/web -- src/components/NoteContentEditor.test.tsx -t "富文本|源码切回"
```

Expected: FAIL because the rich pane is still placeholder text.

- [x] **Step 3: Configure MDXEditor with only lossless Markdown features**

Replace the placeholder with MDXEditor. Import its stylesheet and configure only these plugins/components:

```tsx
import "@mdxeditor/editor/style.css";
import {
  BlockTypeSelect,
  BoldItalicUnderlineToggles,
  CodeToggle,
  CreateLink,
  InsertCodeBlock,
  InsertThematicBreak,
  ListsToggle,
  MDXEditor,
  type MDXEditorMethods,
  UndoRedo,
  codeBlockPlugin,
  codeMirrorPlugin,
  headingsPlugin,
  linkDialogPlugin,
  linkPlugin,
  listsPlugin,
  markdownShortcutPlugin,
  quotePlugin,
  thematicBreakPlugin,
  toolbarPlugin,
} from "@mdxeditor/editor";

const richTextPlugins = [
  headingsPlugin({ allowedHeadingLevels: [1, 2, 3] }),
  listsPlugin(),
  quotePlugin(),
  thematicBreakPlugin(),
  linkPlugin(),
  linkDialogPlugin(),
  codeBlockPlugin({ defaultCodeBlockLanguage: "" }),
  codeMirrorPlugin({ codeBlockLanguages: { "": "纯文本", js: "JavaScript", ts: "TypeScript", css: "CSS", html: "HTML", markdown: "Markdown" } }),
  markdownShortcutPlugin(),
  toolbarPlugin({
    toolbarContents: () => <><UndoRedo /><BlockTypeSelect /><BoldItalicUnderlineToggles options={["Bold", "Italic"]} /><CodeToggle /><CreateLink /><ListsToggle options={["bullet", "number"]} /><InsertCodeBlock /><InsertThematicBreak /></>,
  }),
];

const richEditorRef = useRef<MDXEditorMethods>(null);
const [richError, setRichError] = useState("");

const selectMode = (next: EditorMode) => {
  setMode(next);
  if (next === "rich") window.requestAnimationFrame(() => richEditorRef.current?.setMarkdown(value));
};

<div id={`${rootId}-rich`} className="rich-editor-panel" role="tabpanel" aria-label="富文本">
  {richError && <div className="editor-error" role="alert"><span>这段 Markdown 暂时无法在富文本中编辑，源码仍已完整保留。</span><button type="button" onClick={() => { setMode("markdown"); setMarkdownMode("source"); }}>返回源码</button></div>}
  <div role="region" aria-label="富文本正文">
    <MDXEditor
      ref={richEditorRef}
      markdown={value}
      onChange={(markdown) => { setRichError(""); onChange(markdown); }}
      onError={() => setRichError("parse")}
      plugins={richTextPlugins}
      contentEditableClassName="rich-editor-content"
    />
  </div>
</div>
```

Inspect `node_modules/@mdxeditor/editor/dist/types` after installation and align exact component prop names with v4.2.3 before coding. Keep the plugin list fixed to the approved Markdown subset; do not add JSX/directives/image/table rich-edit plugins.

- [x] **Step 4: Localize editor controls and preserve keyboard operation**

Use MDXEditor's supported `translation` callback to map visible/accessible toolbar strings needed by the configured controls to `撤销`, `重做`, `正文`, `一级标题`, `二级标题`, `三级标题`, `粗体`, `斜体`, `链接`, `无序列表`, `有序列表`, `引用`, `行内代码`, `代码块`, and `分隔线`. If v4.2.3 lacks a direct quote toolbar control, include `引用` through `BlockTypeSelect` and test that menu path; do not emulate rich-text commands with `document.execCommand`.

Add an effect that synchronizes a parent-supplied replacement while avoiding cursor churn:

```tsx
const previousValue = useRef(value);
useEffect(() => {
  if (previousValue.current === value) return;
  previousValue.current = value;
  if (mode === "rich") richEditorRef.current?.setMarkdown(value);
}, [mode, value]);
```

- [x] **Step 5: Run component tests and typecheck**

Run:

```powershell
npm run test --workspace @dayorder/web -- src/components/NoteContentEditor.test.tsx
npm run typecheck --workspace @dayorder/web
```

Expected: all component tests PASS and TypeScript confirms the v4.2.3 API usage.

- [x] **Step 6: Commit the rich-text slice**

```powershell
git add apps/web/src/components/NoteContentEditor.tsx apps/web/src/components/NoteContentEditor.test.tsx
git commit -m "feat(web): add markdown-backed rich text editing"
```

### Task 3: Integrate with the note form and prove one-save semantics

**Files:**
- Modify: `apps/web/src/App.test.tsx`
- Modify: `apps/web/src/components/EntityDialog.tsx:1-220`

**Interfaces:**
- Consumes: `<NoteContentEditor value={body} onChange={setBody} />`
- Preserves: `Note.bodyMarkdown: string`
- Preserves: existing `add-note` / `update-note` dispatch on form submit only

- [x] **Step 1: Expose note body/version state in the test harness**

Extend `Harness` in `App.test.tsx` with a deterministic output:

```tsx
<output data-testid="note-state">{JSON.stringify(data.notes.map((note) => ({ id: note.id, bodyMarkdown: note.bodyMarkdown, version: note.version })))}</output>
```

- [x] **Step 2: Add failing note-dialog integration tests**

Add:

```tsx
it("笔记正文可在源码编辑、预览并只在保存时提交", async () => {
  const user = userEvent.setup();
  render(<GuestHarness />);
  await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
  await user.click(screen.getByRole("button", { name: "新建笔记" }));
  await user.type(screen.getByLabelText("标题"), "编辑器验收");

  const before = screen.getByTestId("note-state").textContent;
  await user.click(screen.getByRole("tab", { name: "Markdown" }));
  await user.type(screen.getByRole("textbox", { name: "Markdown 源码" }), "# 标题{enter}{enter}- [x] 完成");
  await user.click(screen.getByRole("tab", { name: "预览" }));
  expect(screen.getByRole("heading", { level: 1, name: "标题" })).toBeInTheDocument();
  expect(screen.getByTestId("note-state")).toHaveTextContent(before!);

  await user.click(screen.getByRole("button", { name: "创建笔记" }));
  await waitFor(() => expect(screen.getByTestId("note-state")).toHaveTextContent("# 标题"));
  expect(screen.getAllByText("笔记已创建")).toHaveLength(1);
});

it("重新选择另一篇笔记时重置正文与编辑模式", async () => {
  const user = userEvent.setup();
  render(<GuestHarness />);
  await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
  const editButtons = screen.getAllByRole("button", { name: /编辑笔记/ });
  await user.click(editButtons[0]);
  await user.click(screen.getByRole("tab", { name: "Markdown" }));
  await user.click(screen.getByRole("button", { name: "取消" }));
  await user.click(editButtons[1]);
  expect(screen.getByRole("tab", { name: "富文本" })).toHaveAttribute("aria-selected", "true");
  expect(screen.getByRole("region", { name: "富文本正文" })).toHaveTextContent(createSeedData().notes[1].title);
});
```

Use the actual note-card edit accessible names from `NotesPage.tsx`; the assertion must select two different notes rather than reusing detached elements after a dialog close.

- [x] **Step 3: Run integration tests and verify RED**

Run:

```powershell
npm run test --workspace @dayorder/web -- src/App.test.tsx -t "笔记正文可在源码|重新选择另一篇笔记"
```

Expected: FAIL because `EntityDialog` still renders the plain textarea and no mode tabs.

- [x] **Step 4: Replace only the note body field**

In `EntityDialog.tsx`, import the component:

```tsx
import { NoteContentEditor } from "./NoteContentEditor";
```

Replace the body `<label>` with a group that keeps a visible label without nesting interactive descendants inside a label:

```tsx
<div className="form-field span-2 note-body-field">
  <span id="note-body-label">正文</span>
  <NoteContentEditor key={value?.id ?? "new-note"} value={body} onChange={setBody} />
</div>
```

Keep the submit code exactly on the form boundary:

```tsx
const note: Note = {
  // existing identity, metadata, category, tag, and link fields
  bodyMarkdown: body,
};
dispatch({ type: value ? "update-note" : "add-note", note });
```

Do not dispatch from `NoteContentEditor` or mode-change handlers.

- [x] **Step 5: Run component, integration, and store regression tests**

Run:

```powershell
npm run test --workspace @dayorder/web -- src/components/NoteContentEditor.test.tsx src/App.test.tsx src/store/AppStore.test.tsx src/store/commands.test.ts
npm run typecheck --workspace @dayorder/web
```

Expected: all tests PASS, and no store/API type changes are required.

- [x] **Step 6: Commit form integration**

```powershell
git add apps/web/src/components/EntityDialog.tsx apps/web/src/App.test.tsx
git commit -m "feat(web): integrate note content editor"
```

### Task 4: Responsive editor styling and durable contracts

**Files:**
- Modify: `apps/web/src/styles.css:493-537,715-750`
- Modify: `DESIGN.md`
- Modify: `UX-CONTRACT.md`

**Interfaces:**
- Documents: runtime CSS remains the token source of truth
- Styles: `.note-content-editor`, `.editor-mode-tabs`, `.markdown-mode-tabs`, `.rich-editor-panel`, `.rich-editor-content`, `.markdown-source`, `.markdown-preview`, `.editor-error`

- [x] **Step 1: Add token-based editor styling**

Replace the obsolete `.form-field .note-editor` rule and add:

```css
.note-content-editor { overflow: hidden; border: 1px solid var(--line); border-radius: var(--r-panel); background: var(--surface); }
.editor-mode-tabs, .markdown-mode-tabs { display: flex; align-items: center; gap: 2px; border-bottom: 1px solid var(--line); background: var(--surface-2); padding: 5px; }
.editor-mode-tabs button, .markdown-mode-tabs button { min-height: 36px; border: 0; border-radius: var(--r-control); background: transparent; color: var(--ink-2); cursor: pointer; padding: 0 12px; font-size: 10px; font-weight: 700; }
.editor-mode-tabs button[aria-selected="true"], .markdown-mode-tabs button[aria-selected="true"] { background: var(--surface); color: var(--primary-deep); box-shadow: inset 0 0 0 1px var(--line); }
.editor-mode-tabs button:focus-visible, .markdown-mode-tabs button:focus-visible, .editor-error button:focus-visible { outline: 0; box-shadow: 0 0 0 3px rgba(82,117,138,.15); }
.rich-editor-panel, .markdown-source, .markdown-preview { min-height: 320px; }
.rich-editor-panel [role="toolbar"] { overflow-x: auto; border-bottom: 1px solid var(--line); scrollbar-gutter: stable; }
.rich-editor-content { min-height: 270px; color: var(--ink); padding: 18px; font-size: 12px; line-height: 1.75; }
.editor-source-label { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0,0,0,0); white-space: nowrap; }
.markdown-source { display: block; width: 100%; resize: none; border: 0; border-radius: 0; outline: 0; background: var(--surface); color: var(--ink); padding: 18px; font: 11px/1.75 "SFMono-Regular", Consolas, monospace; }
.markdown-source:focus { box-shadow: inset 0 0 0 2px var(--primary); }
.markdown-preview { overflow-wrap: anywhere; padding: 18px; color: var(--ink); font-size: 12px; line-height: 1.75; }
.markdown-preview pre, .markdown-preview table { display: block; max-width: 100%; overflow-x: auto; }
.markdown-preview code { font-family: "SFMono-Regular", Consolas, monospace; }
.markdown-preview blockquote { margin-left: 0; border-left: 3px solid var(--primary); color: var(--ink-2); padding-left: 14px; }
.markdown-preview img { max-width: 100%; height: auto; }
.editor-empty { color: var(--ink-3); }
.editor-error { display: flex; align-items: center; justify-content: space-between; gap: 12px; border-bottom: 1px solid var(--danger); background: var(--surface-2); color: var(--danger); padding: 10px 12px; }
.editor-error button { border: 0; background: transparent; color: inherit; cursor: pointer; font-weight: 700; }

@media (max-width: 580px) {
  .editor-mode-tabs, .markdown-mode-tabs { overflow-x: auto; }
  .rich-editor-panel, .markdown-source, .markdown-preview { min-height: 240px; }
  .rich-editor-content, .markdown-source, .markdown-preview { padding: 14px; }
}
```

Inspect MDXEditor's emitted class specificity in the production build. Scope any override under `.note-content-editor` and continue to use project variables; do not copy package colors into the app.

- [x] **Step 2: Update durable design and UX contracts**

Add to `DESIGN.md` Components:

```markdown
### Note content editor

Markdown is the visual and data backbone. Rich text, source, and preview share one bordered work surface; mode controls use the quiet segmented-control grammar, code uses the mono stack, and long code/table content scrolls inside itself. The editor introduces no new palette or radius token.
```

Add to `UX-CONTRACT.md`:

```markdown
| Edit note body | Rich text / Markdown source | dialog-local canonical Markdown draft | Stay in note dialog until Save | existing note toast after Save | invalid rich rendering keeps source intact and offers Return to source | current editor surface | User decision 2026-09-03 |
```

Also record: `bodyMarkdown` is the only persisted representation; preview uses GFM without raw HTML; mode switches never dispatch; top and Markdown sub-mode tabs follow the horizontal-arrow tab model; source and rich input are Chinese-IME safe.

- [x] **Step 3: Run security and anti-pattern searches**

Run:

```powershell
rg -n "dangerouslySetInnerHTML|rehype-raw|jsxPlugin|directivesPlugin|imagePlugin|MDXProvider|window\.(alert|confirm|prompt)" apps/web/src/components/NoteContentEditor.tsx apps/web/src/components/EntityDialog.tsx
rg -n "#[0-9a-fA-F]{3,8}|rgb\(|hsl\(|oklch\(" apps/web/src/components/NoteContentEditor.tsx
```

Expected: no forbidden editor feature/security matches and no raw visual literals in the component. Existing unrelated `window.confirm` calls in `EntityDialog.tsx` are pre-existing debt and must not be counted as introduced by this feature.

- [x] **Step 4: Run full verification**

Run:

```powershell
npm run test:web
npm run typecheck
npm run build:web
npm run test:api
npm run build:api
npx -p @google/design.md designmd lint DESIGN.md
```

Expected: every command exits 0. Also scan plan/spec consistency:

```powershell
rg -n -e "T[B]D" -e "T[O]DO" -e "implement l[a]ter" -e "fill in d[e]tails" -e "Similar to T[a]sk" docs/superpowers/plans/2026-09-03-markdown-rich-text-editor.md
rg -n "Markdown|富文本|源码|预览|GFM|HTML|IME|320|240|bodyMarkdown" docs/superpowers/specs/2026-09-03-markdown-rich-text-editor-design.md docs/superpowers/plans/2026-09-03-markdown-rich-text-editor.md
```

Expected: placeholder scan returns no matches and all spec themes are covered.

If Python is available, run the premium strict audit:

```powershell
python C:\Users\yeshaopeng\.codex\plugins\cache\openai-curated-remote\frontend-design-premium\1.4.0\skills\frontend-design-premium\scripts\audit_project.py . --mode strict
```

- [x] **Step 5: Verify the rendered editor**

> Verification note (2026-09-03): the host reported no available browser runtime. Component/integration tests and the production build are recorded as evidence; desktop and 320–580px visual inspection remains a manual follow-up.

In a real browser at desktop and 320–580px widths, verify: existing notes load in rich text; source edits round-trip to preview and rich text; toolbar is reachable by keyboard; focus remains in the Modal; GFM tables/tasks render; scripts and raw HTML do not execute; code/table overflow stays local; Escape closes the note dialog and restores trigger focus; reduced-motion causes no delayed mode change. If the browser runtime is unavailable, record that limitation and rely only on interaction tests/build evidence.

- [x] **Step 6: Commit styles and contracts after checks pass**

> Integration note (2026-09-03): the slice checkpoints were consolidated into the final feature commit before the requested local merge.

```powershell
git add apps/web/src/styles.css DESIGN.md UX-CONTRACT.md
git commit -m "docs(web): codify markdown-first editor behavior"
```
