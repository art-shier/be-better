import { CalendarDays, CheckSquare2, FileText, Flag, Search, StickyNote } from "lucide-react";
import { useMemo, useState } from "react";
import { formatDateTime } from "../domain/dates";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";
import { Modal } from "./Modal";

interface SearchItem {
  id: string;
  kind: "goal" | "task" | "event" | "record" | "note";
  title: string;
  meta: string;
  searchText: string;
}

const kindMeta = {
  goal: { label: "目标", view: "goals", icon: Flag },
  task: { label: "任务", view: "today", icon: CheckSquare2 },
  event: { label: "日程", view: "calendar", icon: CalendarDays },
  record: { label: "记录", view: "records", icon: StickyNote },
  note: { label: "笔记", view: "notes", icon: FileText },
} as const;

export function SearchDialog({ open, onClose }: { open: boolean; onClose(): void }) {
  const { data } = useAppStore();
  const { editGoal, editTask, editEvent, editRecord, editNote } = useUi();
  const [query, setQuery] = useState("");

  const items = useMemo<SearchItem[]>(() => [
    ...data.goals.map((goal) => ({ id: goal.id, kind: "goal" as const, title: goal.title, meta: `${goal.area} · ${goal.status === "active" ? "进行中" : goal.status}`, searchText: `${goal.title} ${goal.why} ${goal.area} ${goal.milestones.map((item) => item.title).join(" ")}` })),
    ...data.tasks.filter((task) => task.status !== "archived").map((task) => ({ id: task.id, kind: "task" as const, title: task.title, meta: `${task.status === "done" ? "已完成" : "待完成"} · ${task.estimateMinutes} 分钟`, searchText: `${task.title} ${data.goals.find((goal) => goal.id === task.goalId)?.title ?? ""}` })),
    ...data.events.map((event) => ({ id: event.id, kind: "event" as const, title: event.title, meta: formatDateTime(event.startAt), searchText: `${event.title} ${event.location ?? ""} ${data.goals.find((goal) => goal.id === event.goalId)?.title ?? ""}` })),
    ...data.records.filter((record) => !record.archivedAt).map((record) => ({ id: record.id, kind: "record" as const, title: record.rawText, meta: formatDateTime(record.occurredAt), searchText: `${record.rawText} ${record.tags.join(" ")}` })),
    ...data.notes.filter((note) => !note.archivedAt).map((note) => ({ id: note.id, kind: "note" as const, title: note.title, meta: `${note.category} · ${formatDateTime(note.updatedAt)}`, searchText: `${note.title} ${note.bodyMarkdown} ${note.tags.join(" ")} ${note.category}` })),
  ], [data]);

  const filtered = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return (normalized ? items.filter((item) => `${item.searchText} ${item.meta}`.toLowerCase().includes(normalized)) : items).slice(0, 12);
  }, [items, query]);

  const choose = (item: SearchItem) => {
    onClose();
    window.location.hash = kindMeta[item.kind].view;
    if (item.kind === "goal") editGoal(data.goals.find((goal) => goal.id === item.id));
    if (item.kind === "task") editTask(data.tasks.find((task) => task.id === item.id));
    if (item.kind === "event") editEvent(data.events.find((event) => event.id === item.id));
    if (item.kind === "record") editRecord(data.records.find((record) => record.id === item.id));
    if (item.kind === "note") editNote(data.notes.find((note) => note.id === item.id));
  };

  return (
    <Modal open={open} title="查找任何内容" description="搜索目标、任务、日程、记录和笔记。" onClose={onClose} size="small">
      <div className="search-wrap"><Search size={18} /><input data-autofocus value={query} onChange={(event) => setQuery(event.target.value)} placeholder="输入关键词" aria-label="搜索内容" /></div>
      <div className="search-results" role="listbox" aria-label="搜索结果">
        {filtered.map((item) => {
          const Icon = kindMeta[item.kind].icon;
          return <button key={`${item.kind}-${item.id}`} className="search-result" type="button" role="option" onClick={() => choose(item)}><span className="result-icon"><Icon size={17} /></span><span><strong>{item.title}</strong><small>{kindMeta[item.kind].label} · {item.meta}</small></span></button>;
        })}
        {!filtered.length && <div className="empty-state compact"><Search size={22} /><strong>没有匹配内容</strong><p>试试目标名称、记录原文或标签。</p></div>}
      </div>
    </Modal>
  );
}
