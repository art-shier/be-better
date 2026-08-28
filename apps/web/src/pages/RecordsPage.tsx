import { Archive, CalendarDays, Check, CheckSquare2, Lightbulb, ListFilter, Plus, Smile, Sparkles, StickyNote } from "lucide-react";
import { useMemo, useState } from "react";
import { parseCapture } from "../domain/capture";
import { formatTime } from "../domain/dates";
import type { RecordEntry, RecordKind } from "../domain/types";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";

type Filter = "all" | RecordKind;
const kindLabel: Record<RecordKind, string> = { status: "状态", idea: "想法", completion: "完成", inbox: "收件箱" };
const iconFor = (kind: RecordKind) => kind === "status" ? Smile : kind === "idea" ? Lightbulb : kind === "completion" ? Check : StickyNote;

export function RecordsPage() {
  const { data, dispatch } = useAppStore();
  const { openCapture, editRecord, toast } = useUi();
  const [filter, setFilter] = useState<Filter>("all");
  const records = useMemo(() => data.records.filter((record) => !record.archivedAt && (filter === "all" || record.kind === filter)).sort((a,b) => b.occurredAt.localeCompare(a.occurredAt)), [data.records, filter]);
  const inbox = data.records.filter((record) => record.kind === "inbox" && !record.archivedAt && !record.parsedEntityId);
  const completedTasks = data.tasks.filter((task) => task.status === "done" && task.completedAt);
  const morningCompleted = completedTasks.filter((task) => new Date(task.completedAt!).getHours() < 12).length;
  const energyRecords = data.records.filter((record) => !record.archivedAt && typeof record.energy === "number");
  const averageEnergy = energyRecords.length ? (energyRecords.reduce((sum, record) => sum + (record.energy ?? 0), 0) / energyRecords.length).toFixed(1) : null;

  const acceptDraft = (record: RecordEntry) => {
    const draft = parseCapture(record.rawText, data.goals);
    dispatch({ type: "accept-record", recordId: record.id, draft });
    toast(`已创建${draft.kind === "event" ? "日程" : draft.kind === "task" ? "任务" : draft.kind === "note" ? "笔记" : "目标"}，原文仍保留在时间流中`);
  };

  const toTask = (record: RecordEntry) => {
    dispatch({ type: "accept-record", recordId: record.id, draft: { ...parseCapture(record.rawText, data.goals, "task"), kind: "task" } });
    toast("已整理为任务，并保留来源链接");
  };

  const toNote = (record: RecordEntry) => {
    dispatch({ type: "accept-record", recordId: record.id, draft: { ...parseCapture(record.rawText, data.goals, "note"), kind: "note" } });
    toast("已整理为笔记，并保留来源链接");
  };

  const toEvent = (record: RecordEntry) => {
    dispatch({ type: "accept-record", recordId: record.id, draft: { ...parseCapture(record.rawText, data.goals, "event"), kind: "event" } });
    toast("已整理为日程，并保留来源链接");
  };

  return <div className="view-page"><header className="view-head"><div><p className="eyebrow">发生过的真实片段</p><h1>记录不要求立刻变得有用。</h1><p>先保留原文和发生时间，之后再整理成任务、日程或笔记。</p></div><button className="button primary" type="button" onClick={() => openCapture("record")}><Plus size={17} />写一条记录</button></header>
      <div className="record-layout"><section className="panel record-panel"><div className="panel-head"><div className="panel-title"><h2>时间流</h2><span>{records.length} 条</span></div><div className="record-filters"><ListFilter size={15} />{(["all","status","idea","completion","inbox"] as Filter[]).map((value) => <button key={value} className={filter === value ? "active" : ""} type="button" onClick={() => setFilter(value)}>{value === "all" ? "全部" : kindLabel[value]}</button>)}</div></div><div className="record-feed">{records.map((record) => { const Icon = iconFor(record.kind); return <article className="record-item" key={record.id} data-entity-id={record.id} tabIndex={-1}><time>{formatTime(record.occurredAt)}</time><span className={`record-type ${record.kind}`}><Icon size={17} /></span><div className="record-copy"><button className="record-copy-main" type="button" onClick={() => editRecord(record)}><p>{record.rawText}</p></button><div className="tags">{record.tags.map((tag) => <span key={tag}>{tag}</span>)}</div>{record.parsedEntityId ? <div className="record-linked"><Check size={13} />已整理并保留来源链接</div> : record.kind !== "inbox" && <div className="record-actions"><button type="button" onClick={() => toTask(record)}><CheckSquare2 size={14} />转为任务</button><button type="button" onClick={() => toEvent(record)}><CalendarDays size={14} />转为日程</button><button type="button" onClick={() => toNote(record)}><StickyNote size={14} />整理为笔记</button><button type="button" onClick={() => { dispatch({ type: "archive-record", id: record.id }); toast("记录已归档"); }}><Archive size={14} />归档</button></div>}</div></article>; })}{!records.length && <div className="empty-state"><StickyNote size={24} /><strong>这个筛选下还没有记录</strong><p>原始输入会按发生时间出现在这里。</p></div>}</div></section>
      <aside className="side-stack"><section className="panel inbox-panel"><div className="panel-title"><h2>收件箱解析</h2><span>{inbox.length} 条待确认</span></div><p>系统只生成草稿，你确认后才会写入其他模块。</p>{inbox.slice(0,4).map((record) => { const draft = parseCapture(record.rawText, data.goals); return <div className="parse-card" key={record.id}><strong>“{record.rawText}”</strong><span>{formatTime(record.occurredAt)} · 原文已保留</span><div className="parse-result">{draft.kind === "event" ? <CalendarDays size={15} /> : <CheckSquare2 size={15} />}识别为{draft.kind === "event" ? "日程" : "任务"} · {draft.explanation}</div><div className="parse-actions"><button className="button secondary small" type="button" onClick={() => { dispatch({ type: "archive-record", id: record.id }); toast("已跳过，原始记录已归档"); }}>跳过</button><button className="button primary small" type="button" onClick={() => acceptDraft(record)}>接受建议</button></div></div>; })}{!inbox.length && <div className="empty-state compact"><Check size={22} /><strong>收件箱已清空</strong><p>所有解析草稿都已经处理。</p></div>}</section>
        <section className="panel insight-panel"><div className="panel-title"><Sparkles size={17} /><h2>{data.settings.aiEnabled ? "AI 观察" : "记录概览"}</h2></div>{completedTasks.length > 0 && <div className="insight"><strong>事实：已完成 {completedTasks.length} 项任务</strong><p>其中 {morningCompleted} 项在中午前完成；样本继续增加后再判断高效时段。</p></div>}{averageEnergy && <div className="insight"><strong>事实：最近状态平均精力 {averageEnergy} / 5</strong><p>{Number(averageEnergy) < 3 ? "建议先安排短任务；这是基于当前状态记录的推断。" : "当前记录没有显示明显低精力；这是基于有限样本的推断。"}</p></div>}{inbox.length > 0 && <div className="insight"><strong>事实：还有 {inbox.length} 条收件箱记录</strong><p>建议先确认带时间的内容，避免遗漏日程。</p></div>}{!completedTasks.length && !averageEnergy && !inbox.length && <div className="empty-state compact"><Sparkles size={22} /><strong>还没有足够的记录</strong><p>完成任务或记录状态后，这里会区分事实与推断。</p></div>}<small>{data.settings.aiEnabled ? "所有观察都来自当前本地数据，不会自动写入。" : "AI 已关闭；上面只显示本地确定性统计。"}</small></section></aside>
    </div>
  </div>;
}
