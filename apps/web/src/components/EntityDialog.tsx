import { Trash2 } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { addMinutes, format } from "date-fns";
import { createId } from "../domain/ids";
import type { Area, CalendarEvent, Goal, GoalMetricType, GoalStatus, Note, Priority, RecordEntry, RecordKind, Task, TaskStatus } from "../domain/types";
import { useAppStore } from "../store/AppStore";
import { useUi, type EditorTarget } from "../ui/UiProvider";
import { Modal } from "./Modal";

const toLocalInput = (value?: string) => value ? format(new Date(value), "yyyy-MM-dd'T'HH:mm") : "";
const fromLocalInput = (value: string) => value ? new Date(value).toISOString() : undefined;

export function EntityDialog({ target, onClose }: { target: EditorTarget | null; onClose(): void }) {
  if (!target) return null;
  if (target.kind === "goal") return <GoalEditor value={target.value} onClose={onClose} />;
  if (target.kind === "task") return <TaskEditor value={target.value} onClose={onClose} />;
  if (target.kind === "event") return <EventEditor value={target.value} onClose={onClose} />;
  if (target.kind === "record") return <RecordEditor value={target.value} onClose={onClose} />;
  return <NoteEditor value={target.value} onClose={onClose} />;
}

function Footer({ existing, onClose, onDelete, submitLabel = "保存更改" }: { existing: boolean; onClose(): void; onDelete?(): void; submitLabel?: string }) {
  return <><div className="footer-left">{existing && onDelete && <button className="button danger-ghost" type="button" onClick={onDelete}><Trash2 size={16} />删除</button>}</div><button className="button secondary" type="button" onClick={onClose}>取消</button><button className="button primary" type="submit" form="entity-form">{existing ? submitLabel : submitLabel.replace("保存", "创建")}</button></>;
}

function GoalEditor({ value, onClose }: { value?: Goal; onClose(): void }) {
  const { dispatch } = useAppStore();
  const { toast } = useUi();
  const [title, setTitle] = useState(value?.title ?? "");
  const [why, setWhy] = useState(value?.why ?? "");
  const [area, setArea] = useState<Area>(value?.area ?? "生活");
  const [metricType, setMetricType] = useState<GoalMetricType>(value?.metricType ?? "project");
  const [currentValue, setCurrentValue] = useState(value?.currentValue ?? 0);
  const [targetValue, setTargetValue] = useState(value?.targetValue ?? 100);
  const [unit, setUnit] = useState(value?.unit ?? "%");
  const [dueAt, setDueAt] = useState(toLocalInput(value?.dueAt));
  const [status, setStatus] = useState<GoalStatus>(value?.status ?? "active");
  const [milestoneText, setMilestoneText] = useState(value?.milestones.map((item) => item.title).join("\n") ?? "");

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!title.trim()) return;
    const now = new Date().toISOString();
    const milestones = milestoneText.split(/\r?\n/).map((item) => item.trim()).filter(Boolean).map((milestoneTitle, index) => { const existing = value?.milestones.find((item) => item.title === milestoneTitle) ?? value?.milestones[index]; return { id: existing?.id ?? createId("milestone"), title: milestoneTitle, dueAt: existing?.dueAt, completedAt: existing?.title === milestoneTitle ? existing.completedAt : undefined, sortOrder: index }; });
    const milestoneMetric = metricType === "milestone" && milestones.length > 0;
    const goal: Goal = { id: value?.id ?? createId("goal"), title: title.trim(), why: why.trim(), area, metricType, currentValue: milestoneMetric ? milestones.filter((item) => item.completedAt).length : Number(currentValue), targetValue: milestoneMetric ? milestones.length : Math.max(1, Number(targetValue)), unit: milestoneMetric ? "项" : unit.trim() || "%", startAt: value?.startAt ?? now, dueAt: fromLocalInput(dueAt), status, health: value?.health ?? "normal", milestones: metricType === "milestone" ? milestones : value?.milestones ?? [], createdAt: value?.createdAt ?? now, updatedAt: now };
    dispatch({ type: value ? "update-goal" : "add-goal", goal });
    onClose(); toast(value ? "目标已更新" : "目标已创建");
  };
  const remove = () => { if (value && window.confirm("删除这个目标？关联任务会保留但不再关联。")) { dispatch({ type: "delete-goal", id: value.id }); onClose(); toast("目标已删除"); } };

  return <Modal open title={value ? "编辑目标" : "新建目标"} description="目标要说明为什么重要，以及如何判断正在推进。" onClose={onClose} size="large" footer={<Footer existing={Boolean(value)} onClose={onClose} onDelete={remove} />}><form id="entity-form" className="entity-form" onSubmit={submit}><label className="form-field span-2"><span>目标名称</span><input required value={title} onChange={(event) => setTitle(event.target.value)} placeholder="例如：稳定跑完 10 公里" /></label><label className="form-field span-2"><span>为什么重要</span><textarea value={why} onChange={(event) => setWhy(event.target.value)} placeholder="这个变化会带来什么？" /></label><label className="form-field"><span>领域</span><select value={area} onChange={(event) => setArea(event.target.value as Area)}>{["工作","健康","成长","关系","财务","生活"].map((item) => <option key={item}>{item}</option>)}</select></label><label className="form-field"><span>衡量方式</span><select value={metricType} onChange={(event) => setMetricType(event.target.value as GoalMetricType)}><option value="project">项目完成</option><option value="milestone">里程碑</option><option value="numeric">数值累计</option><option value="habit">周期习惯</option></select></label>{metricType === "milestone" ? <label className="form-field span-2"><span>里程碑（每行一项）</span><textarea value={milestoneText} onChange={(event) => setMilestoneText(event.target.value)} placeholder={"完成第一阶段\n完成验证\n输出最终结果"} /></label> : <><label className="form-field"><span>当前值</span><input type="number" min="0" value={currentValue} onChange={(event) => setCurrentValue(Number(event.target.value))} /></label><label className="form-field"><span>目标值</span><div className="input-pair"><input type="number" min="1" value={targetValue} onChange={(event) => setTargetValue(Number(event.target.value))} /><input aria-label="单位" value={unit} onChange={(event) => setUnit(event.target.value)} /></div></label></>}<label className="form-field"><span>期望日期</span><input type="datetime-local" value={dueAt} onChange={(event) => setDueAt(event.target.value)} /></label><label className="form-field"><span>状态</span><select value={status} onChange={(event) => setStatus(event.target.value as GoalStatus)}><option value="active">进行中</option><option value="paused">已暂停</option><option value="completed">已完成</option><option value="abandoned">已放弃</option></select></label></form></Modal>;
}

function TaskEditor({ value, onClose }: { value?: Task; onClose(): void }) {
  const { data, dispatch } = useAppStore();
  const { toast } = useUi();
  const [title, setTitle] = useState(value?.title ?? "");
  const [priority, setPriority] = useState<Priority>(value?.priority ?? "normal");
  const [estimate, setEstimate] = useState(value?.estimateMinutes ?? 30);
  const [goalId, setGoalId] = useState(value?.goalId ?? "");
  const [scheduledStart, setScheduledStart] = useState(toLocalInput(value?.scheduledStart));
  const [dueAt, setDueAt] = useState(toLocalInput(value?.dueAt));
  const [status, setStatus] = useState<TaskStatus>(value?.status ?? "todo");

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!title.trim()) return;
    const start = fromLocalInput(scheduledStart);
    const task: Task = { id: value?.id ?? createId("task"), title: title.trim(), status, priority, estimateMinutes: Math.max(5, Number(estimate)), dueAt: fromLocalInput(dueAt), scheduledStart: start, scheduledEnd: start ? addMinutes(new Date(start), Math.max(5, Number(estimate))).toISOString() : undefined, goalId: goalId || undefined, createdAt: value?.createdAt ?? new Date().toISOString(), completedAt: status === "done" ? value?.completedAt ?? new Date().toISOString() : undefined, sourceRecordId: value?.sourceRecordId };
    dispatch({ type: value ? "update-task" : "add-task", task }); onClose(); toast(value ? "任务已更新" : "任务已创建");
  };
  const remove = () => { if (value && window.confirm("删除这个任务？")) { dispatch({ type: "delete-task", id: value.id }); onClose(); toast("任务已删除"); } };

  return <Modal open title={value ? "编辑任务" : "新建任务"} onClose={onClose} footer={<Footer existing={Boolean(value)} onClose={onClose} onDelete={remove} />}><form id="entity-form" className="entity-form" onSubmit={submit}><label className="form-field span-2"><span>任务名称</span><input required value={title} onChange={(event) => setTitle(event.target.value)} /></label><label className="form-field"><span>状态</span><select value={status} onChange={(event) => setStatus(event.target.value as TaskStatus)}><option value="todo">待开始</option><option value="doing">进行中</option><option value="done">已完成</option><option value="archived">已归档</option></select></label><label className="form-field"><span>优先级</span><select value={priority} onChange={(event) => setPriority(event.target.value as Priority)}><option value="normal">普通</option><option value="important">重要</option></select></label><label className="form-field"><span>预计分钟</span><input type="number" min="5" step="5" value={estimate} onChange={(event) => setEstimate(Number(event.target.value))} /></label><label className="form-field"><span>截止时间</span><input type="datetime-local" value={dueAt} onChange={(event) => setDueAt(event.target.value)} /></label><label className="form-field"><span>关联目标</span><select value={goalId} onChange={(event) => setGoalId(event.target.value)}><option value="">不关联</option>{data.goals.filter((goal) => goal.status === "active").map((goal) => <option key={goal.id} value={goal.id}>{goal.title}</option>)}</select></label><label className="form-field"><span>安排时间</span><input type="datetime-local" value={scheduledStart} onChange={(event) => setScheduledStart(event.target.value)} /></label></form></Modal>;
}

function EventEditor({ value, onClose }: { value?: CalendarEvent; onClose(): void }) {
  const { data, dispatch } = useAppStore();
  const { toast } = useUi();
  const initialStart = value?.startAt ?? addMinutes(new Date(), 60).toISOString();
  const [title, setTitle] = useState(value?.title ?? "");
  const [startAt, setStartAt] = useState(toLocalInput(initialStart));
  const [endAt, setEndAt] = useState(toLocalInput(value?.endAt ?? addMinutes(new Date(initialStart), 45).toISOString()));
  const [location, setLocation] = useState(value?.location ?? "");
  const [kind, setKind] = useState<CalendarEvent["kind"]>(value?.kind ?? "fixed");
  const [goalId, setGoalId] = useState(value?.goalId ?? "");
  const [reminder, setReminder] = useState(value?.reminderMinutes[0] ?? 10);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!title.trim() || !startAt || !endAt) return;
    const startDate = new Date(startAt);
    const endDate = new Date(endAt);
    if (endDate <= startDate) { toast("结束时间必须晚于开始时间"); return; }
    const calendarEvent: CalendarEvent = { id: value?.id ?? createId("event"), title: title.trim(), startAt: startDate.toISOString(), endAt: endDate.toISOString(), location: location.trim() || undefined, reminderMinutes: reminder >= 0 ? [reminder] : [], sourceCalendar: value?.sourceCalendar, kind, goalId: goalId || undefined, createdAt: value?.createdAt ?? new Date().toISOString() };
    dispatch({ type: value ? "update-event" : "add-event", event: calendarEvent }); onClose(); toast(value ? "日程已更新" : "日程已创建");
  };
  const remove = () => { if (value && window.confirm("删除这个日程？")) { dispatch({ type: "delete-event", id: value.id }); onClose(); toast("日程已删除"); } };

  return <Modal open title={value ? "编辑日程" : "添加日程"} onClose={onClose} footer={<Footer existing={Boolean(value)} onClose={onClose} onDelete={remove} />}><form id="entity-form" className="entity-form" onSubmit={submit}><label className="form-field span-2"><span>日程名称</span><input required value={title} onChange={(event) => setTitle(event.target.value)} /></label><label className="form-field"><span>开始</span><input required type="datetime-local" value={startAt} onChange={(event) => setStartAt(event.target.value)} /></label><label className="form-field"><span>结束</span><input required type="datetime-local" value={endAt} min={startAt} onChange={(event) => setEndAt(event.target.value)} /></label><label className="form-field"><span>类型</span><select value={kind} onChange={(event) => setKind(event.target.value as CalendarEvent["kind"])}><option value="fixed">固定日程</option><option value="focus">专注块</option><option value="health">健康</option><option value="personal">个人</option></select></label><label className="form-field"><span>提醒</span><select value={reminder} onChange={(event) => setReminder(Number(event.target.value))}><option value={-1}>不提醒</option><option value={0}>开始时</option><option value={5}>提前 5 分钟</option><option value={10}>提前 10 分钟</option><option value={30}>提前 30 分钟</option><option value={60}>提前 1 小时</option></select></label><label className="form-field"><span>地点</span><input value={location} onChange={(event) => setLocation(event.target.value)} /></label><label className="form-field"><span>关联目标</span><select value={goalId} onChange={(event) => setGoalId(event.target.value)}><option value="">不关联</option>{data.goals.filter((goal) => goal.status === "active").map((goal) => <option key={goal.id} value={goal.id}>{goal.title}</option>)}</select></label></form></Modal>;
}

function RecordEditor({ value, onClose }: { value?: RecordEntry; onClose(): void }) {
  const { dispatch } = useAppStore();
  const { toast } = useUi();
  const [rawText, setRawText] = useState(value?.rawText ?? "");
  const [kind, setKind] = useState<RecordKind>(value?.kind ?? "idea");
  const [occurredAt, setOccurredAt] = useState(toLocalInput(value?.occurredAt ?? new Date().toISOString()));
  const [tags, setTags] = useState(value?.tags.join("，") ?? "");
  const [mood, setMood] = useState(value?.mood ?? 0);
  const [energy, setEnergy] = useState(value?.energy ?? 0);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!rawText.trim() || !occurredAt) return;
    const record: RecordEntry = { id: value?.id ?? createId("record"), rawText: rawText.trim(), kind, occurredAt: new Date(occurredAt).toISOString(), tags: tags.split(/[，,]/).map((item) => item.trim()).filter(Boolean), mood: mood || undefined, energy: energy || undefined, parsedEntityId: value?.parsedEntityId, archivedAt: value?.archivedAt };
    dispatch({ type: value ? "update-record" : "add-record", record });
    onClose(); toast(value ? "记录已更新" : "记录已创建");
  };
  const remove = () => { if (value && window.confirm("删除这条记录？已整理出的对象不会被删除。")) { dispatch({ type: "delete-record", id: value.id }); onClose(); toast("记录已删除"); } };

  return <Modal open title={value ? "编辑记录" : "新建记录"} description="原文和发生时间会保留在时间流中。" onClose={onClose} footer={<Footer existing={Boolean(value)} onClose={onClose} onDelete={remove} submitLabel="保存记录" />}><form id="entity-form" className="entity-form" onSubmit={submit}><label className="form-field span-2"><span>原始内容</span><textarea required value={rawText} onChange={(event) => setRawText(event.target.value)} /></label><label className="form-field"><span>类型</span><select value={kind} onChange={(event) => setKind(event.target.value as RecordKind)}><option value="status">状态</option><option value="idea">想法</option><option value="completion">完成</option><option value="inbox">收件箱</option></select></label><label className="form-field"><span>发生时间</span><input required type="datetime-local" value={occurredAt} onChange={(event) => setOccurredAt(event.target.value)} /></label><label className="form-field span-2"><span>标签</span><input value={tags} onChange={(event) => setTags(event.target.value)} placeholder="用逗号分隔" /></label><label className="form-field"><span>心情（可选）</span><select value={mood} onChange={(event) => setMood(Number(event.target.value))}><option value={0}>未记录</option>{[1,2,3,4,5].map((item) => <option key={item} value={item}>{item} / 5</option>)}</select></label><label className="form-field"><span>精力（可选）</span><select value={energy} onChange={(event) => setEnergy(Number(event.target.value))}><option value={0}>未记录</option>{[1,2,3,4,5].map((item) => <option key={item} value={item}>{item} / 5</option>)}</select></label></form></Modal>;
}

function NoteEditor({ value, onClose }: { value?: Note; onClose(): void }) {
  const { data, dispatch } = useAppStore();
  const { toast } = useUi();
  const [title, setTitle] = useState(value?.title ?? "");
  const [body, setBody] = useState(value?.bodyMarkdown ?? "");
  const [category, setCategory] = useState<Note["category"]>(value?.category ?? "其他");
  const [tags, setTags] = useState(value?.tags.join("，") ?? "");
  const [linkedGoalId, setLinkedGoalId] = useState(value?.linkedEntityIds.find((id) => data.goals.some((goal) => goal.id === id)) ?? "");

  useEffect(() => { setTitle(value?.title ?? ""); setBody(value?.bodyMarkdown ?? ""); setCategory(value?.category ?? "其他"); setTags(value?.tags.join("，") ?? ""); }, [value]);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!title.trim()) return;
    const now = new Date().toISOString();
    const otherLinks = (value?.linkedEntityIds ?? []).filter((id) => !data.goals.some((goal) => goal.id === id));
    const note: Note = { id: value?.id ?? createId("note"), title: title.trim(), bodyMarkdown: body, category, tags: tags.split(/[，,]/).map((item) => item.trim()).filter(Boolean), linkedEntityIds: [...otherLinks, ...(linkedGoalId ? [linkedGoalId] : [])], createdAt: value?.createdAt ?? now, updatedAt: now };
    dispatch({ type: value ? "update-note" : "add-note", note }); onClose(); toast(value ? "笔记已保存" : "笔记已创建");
  };
  const remove = () => { if (value && window.confirm("删除这篇笔记？")) { dispatch({ type: "delete-note", id: value.id }); onClose(); toast("笔记已删除"); } };

  return <Modal open title={value ? "编辑笔记" : "新建笔记"} onClose={onClose} size="large" footer={<Footer existing={Boolean(value)} onClose={onClose} onDelete={remove} submitLabel="保存笔记" />}><form id="entity-form" className="entity-form" onSubmit={submit}><label className="form-field span-2"><span>标题</span><input required value={title} onChange={(event) => setTitle(event.target.value)} /></label><label className="form-field"><span>分类</span><select value={category} onChange={(event) => setCategory(event.target.value as Note["category"])}>{["产品思考","阅读笔记","健康训练","生活方法","其他"].map((item) => <option key={item}>{item}</option>)}</select></label><label className="form-field"><span>标签</span><input value={tags} onChange={(event) => setTags(event.target.value)} placeholder="用逗号分隔" /></label><label className="form-field span-2"><span>关联目标</span><select value={linkedGoalId} onChange={(event) => setLinkedGoalId(event.target.value)}><option value="">不关联目标</option>{data.goals.map((goal) => <option key={goal.id} value={goal.id}>{goal.title}</option>)}</select></label><label className="form-field span-2"><span>正文（Markdown）</span><textarea className="note-editor" value={body} onChange={(event) => setBody(event.target.value)} /></label></form></Modal>;
}
