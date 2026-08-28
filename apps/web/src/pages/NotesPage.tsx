import { FileText, Plus, Search, Tags } from "lucide-react";
import { useMemo, useState } from "react";
import { formatDate } from "../domain/dates";
import type { Note } from "../domain/types";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";

type Category = Note["category"] | "全部";

export function NotesPage() {
  const { data } = useAppStore();
  const { editNote } = useUi();
  const [category, setCategory] = useState<Category>("全部");
  const [query, setQuery] = useState("");
  const categories: Category[] = ["全部", "产品思考", "阅读笔记", "健康训练", "生活方法", "其他"];
  const notes = useMemo(() => data.notes.filter((note) => !note.archivedAt && (category === "全部" || note.category === category) && (!query.trim() || `${note.title} ${note.bodyMarkdown} ${note.tags.join(" ")}`.toLowerCase().includes(query.trim().toLowerCase()))).sort((a,b) => b.updatedAt.localeCompare(a.updatedAt)), [category, data.notes, query]);

  return <div className="view-page"><header className="view-head"><div><p className="eyebrow">长期知识</p><h1>把碎片整理成以后还能找到的东西。</h1><p>笔记保留标题、正文、标签和与目标、日程之间的关系。</p></div><button className="button primary" type="button" onClick={() => editNote()}><Plus size={17} />新建笔记</button></header>
    <div className="note-toolbar"><div className="note-search"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索笔记内容" aria-label="搜索笔记" /></div><div className="filter-tabs" role="tablist" aria-label="笔记分类">{categories.map((item) => <button key={item} className={category === item ? "active" : ""} type="button" role="tab" aria-selected={category === item} onClick={() => setCategory(item)}>{item}<span>{item === "全部" ? data.notes.filter((note) => !note.archivedAt).length : data.notes.filter((note) => !note.archivedAt && note.category === item).length}</span></button>)}</div></div>
    <div className="note-grid">{notes.map((note, index) => <button className={`panel note-card ${index % 3 === 0 ? "wide" : ""}`} key={note.id} data-entity-id={note.id} type="button" onClick={() => editNote(note)}><span className="note-label">{note.category}</span><h2>{note.title}</h2><p>{note.bodyMarkdown.replace(/[#*_>`]/g, "").slice(0,180)}</p><div className="note-tags"><Tags size={13} />{note.tags.slice(0,3).map((tag) => <span key={tag}>{tag}</span>)}</div><div className="note-meta"><span>{formatDate(note.updatedAt)}</span><span>{note.bodyMarkdown.length.toLocaleString()} 字 · 关联 {note.linkedEntityIds.length} 项</span></div></button>)}{!notes.length && <div className="empty-state page-empty"><FileText size={28} /><strong>没有匹配的笔记</strong><p>调整分类或搜索词，也可以新建一篇笔记。</p><button className="button primary" type="button" onClick={() => editNote()}>新建笔记</button></div>}</div>
  </div>;
}
