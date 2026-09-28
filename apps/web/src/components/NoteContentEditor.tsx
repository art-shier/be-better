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
  type MDXEditorMethods,
  type Translation,
} from "@mdxeditor/editor";
import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

export interface NoteContentEditorProps {
  value: string;
  onChange(value: string): void;
}

type EditorMode = "rich" | "markdown";
type MarkdownMode = "source" | "preview";

function moveTabFocus(event: KeyboardEvent<HTMLButtonElement>, tabs: Array<HTMLButtonElement | null>, index: number) {
  if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
  event.preventDefault();
  const offset = event.key === "ArrowRight" ? 1 : -1;
  tabs[(index + offset + tabs.length) % tabs.length]?.focus();
}

const translations: Record<string, string> = {
  "toolbar.undo": "撤销",
  "toolbar.redo": "重做",
  "toolbar.bold": "粗体",
  "toolbar.removeBold": "取消粗体",
  "toolbar.italic": "斜体",
  "toolbar.removeItalic": "取消斜体",
  "toolbar.inlineCode": "行内代码",
  "toolbar.removeInlineCode": "取消行内代码",
  "toolbar.link": "插入链接",
  "toolbar.bulletedList": "无序列表",
  "toolbar.numberedList": "有序列表",
  "toolbar.blockTypes.paragraph": "正文",
  "toolbar.blockTypes.quote": "引用",
  "toolbar.blockTypes.heading": "{{level}} 级标题",
  "toolbar.blockTypeSelect.selectBlockTypeTooltip": "选择段落格式",
  "toolbar.blockTypeSelect.placeholder": "段落格式",
  "toolbar.codeBlock": "插入代码块",
  "toolbar.thematicBreak": "插入分隔线",
  "toolbar.toggleGroup": "格式选项",
  "createLink.url": "链接地址",
  "createLink.urlPlaceholder": "粘贴链接地址",
  "createLink.text": "链接文字",
  "createLink.title": "链接标题",
  "createLink.saveTooltip": "保存链接",
  "createLink.cancelTooltip": "取消链接编辑",
  "dialogControls.save": "保存",
  "dialogControls.cancel": "取消",
  "dialog.close": "关闭",
  "contentArea.editableMarkdown": "富文本正文",
};

const translate: Translation = (key, fallback, interpolations) => {
  if (key === "toolbar.blockTypes.heading") {
    const headingLabels: Record<string, string> = {
      "1": "一级标题",
      "2": "二级标题",
      "3": "三级标题",
    };
    const level = String(interpolations?.level ?? "");
    return headingLabels[level] ?? `${level} 级标题`;
  }
  const template = translations[key] ?? fallback;
  return Object.entries(interpolations ?? {}).reduce((result, [name, replacement]) => result.replaceAll(`{{${name}}}`, String(replacement)), template);
};

function RichTextToolbar() {
  const labelRef = useRef<HTMLSpanElement>(null);

  useLayoutEffect(() => {
    labelRef.current?.parentElement?.setAttribute("aria-label", "正文格式");
  }, []);

  return <><span ref={labelRef} className="sr-only">正文格式</span><UndoRedo /><BlockTypeSelect /><BoldItalicUnderlineToggles options={["Bold", "Italic"]} /><CodeToggle /><CreateLink /><ListsToggle options={["bullet", "number"]} /><InsertCodeBlock /><InsertThematicBreak /></>;
}

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
    toolbarClassName: "rich-editor-toolbar",
    toolbarContents: () => <RichTextToolbar />,
  }),
];

export function NoteContentEditor({ value, onChange }: NoteContentEditorProps) {
  const [mode, setMode] = useState<EditorMode>("rich");
  const [markdownMode, setMarkdownMode] = useState<MarkdownMode>("source");
  const [richError, setRichError] = useState("");
  const rootId = useId();
  const richEditorRef = useRef<MDXEditorMethods>(null);
  const previousValue = useRef(value);
  const topTabs = useRef<Array<HTMLButtonElement | null>>([]);
  const markdownTabs = useRef<Array<HTMLButtonElement | null>>([]);

  useEffect(() => {
    if (previousValue.current === value) return;
    previousValue.current = value;
    if (mode === "rich") richEditorRef.current?.setMarkdown(value);
  }, [mode, value]);

  return <div className="note-content-editor">
    <div className="editor-mode-tabs" role="tablist" aria-label="正文编辑模式">
      <button ref={(node) => { topTabs.current[0] = node; }} type="button" role="tab" aria-selected={mode === "rich"} aria-controls={`${rootId}-rich`} tabIndex={mode === "rich" ? 0 : -1} onKeyDown={(event) => moveTabFocus(event, topTabs.current, 0)} onClick={() => setMode("rich")}>富文本</button>
      <button ref={(node) => { topTabs.current[1] = node; }} type="button" role="tab" aria-selected={mode === "markdown"} aria-controls={`${rootId}-markdown`} tabIndex={mode === "markdown" ? 0 : -1} onKeyDown={(event) => moveTabFocus(event, topTabs.current, 1)} onClick={() => setMode("markdown")}>Markdown</button>
    </div>
    {mode === "rich" ? <div id={`${rootId}-rich`} className="rich-editor-panel" role="tabpanel" aria-label="富文本">{richError && <div className="editor-error" role="alert"><span>这段 Markdown 暂时无法在富文本中编辑，源码仍已完整保留。</span><button type="button" onClick={() => { setMode("markdown"); setMarkdownMode("source"); }}>返回源码</button></div>}<div role="region" aria-label="富文本正文"><MDXEditor ref={richEditorRef} className="rich-editor" contentEditableClassName="rich-editor-content" markdown={value} onChange={(markdown) => { setRichError(""); onChange(markdown); }} onError={() => setRichError("parse")} plugins={richTextPlugins} suppressHtmlProcessing translation={translate} /></div></div> : <div id={`${rootId}-markdown`} role="tabpanel" aria-label="Markdown">
      <div className="markdown-mode-tabs" role="tablist" aria-label="Markdown 查看模式">
        <button ref={(node) => { markdownTabs.current[0] = node; }} type="button" role="tab" aria-selected={markdownMode === "source"} aria-controls={`${rootId}-source`} tabIndex={markdownMode === "source" ? 0 : -1} onKeyDown={(event) => moveTabFocus(event, markdownTabs.current, 0)} onClick={() => setMarkdownMode("source")}>源码</button>
        <button ref={(node) => { markdownTabs.current[1] = node; }} type="button" role="tab" aria-selected={markdownMode === "preview"} aria-controls={`${rootId}-preview`} tabIndex={markdownMode === "preview" ? 0 : -1} onKeyDown={(event) => moveTabFocus(event, markdownTabs.current, 1)} onClick={() => setMarkdownMode("preview")}>预览</button>
      </div>
      {markdownMode === "source" ? <div id={`${rootId}-source`} role="tabpanel" aria-label="源码"><label className="sr-only" htmlFor={`${rootId}-source-input`}>Markdown 源码</label><textarea id={`${rootId}-source-input`} className="markdown-source" value={value} onChange={(event) => onChange(event.target.value)} /></div> : <div id={`${rootId}-preview`} className="markdown-preview" role="tabpanel" aria-label="预览">{value.trim() ? <ReactMarkdown remarkPlugins={[remarkGfm]} components={{ a: ({ href, ...props }) => { const external = /^https?:\/\//i.test(href ?? ""); return <a href={href} {...props} {...external ? { target: "_blank", rel: "noreferrer noopener" } : {}} />; } }}>{value}</ReactMarkdown> : <p className="editor-empty">还没有正文，切换到源码或富文本开始输入。</p>}</div>}
    </div>}
  </div>;
}
