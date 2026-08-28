import { CalendarDays, Check, CheckSquare2, ChevronRight, Clock3, Flag, Pause, Play, Plus, Sparkles, StickyNote, Target } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { formatTime, greeting, isToday, weekdayLabel } from "../domain/dates";
import { buildTodayPlan } from "../domain/planning";
import type { CalendarEvent, Task } from "../domain/types";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";
import { Modal } from "../components/Modal";

const kindClass: Record<CalendarEvent["kind"], string> = { focus: "focus", fixed: "meeting", personal: "personal", health: "health" };

export function TodayPage() {
  const { data, dispatch } = useAppStore();
  const { openCapture, openReview, editTask, editEvent, toast } = useUi();
  const [sourcesOpen, setSourcesOpen] = useState(false);
  const [planOpen, setPlanOpen] = useState(false);
  const [timerStartedAt, setTimerStartedAt] = useState<number | null>(null);
  const [elapsed, setElapsed] = useState(0);
  const [energyOpen, setEnergyOpen] = useState(false);

  const activeGoals = data.goals.filter((goal) => goal.status === "active");
  const todayEvents = useMemo(() => data.events.filter((event) => isToday(event.startAt)).sort((a, b) => a.startAt.localeCompare(b.startAt)), [data.events]);
  const todayTasks = useMemo(() => data.tasks.filter((task) => task.status !== "archived" && (isToday(task.scheduledStart) || isToday(task.dueAt) || !task.scheduledStart)).slice(0, 8), [data.tasks]);
  const focusTask = todayTasks.find((task) => task.status === "doing") ?? todayTasks.find((task) => task.status === "todo" && task.priority === "important") ?? todayTasks[0];
  const doneCount = todayTasks.filter((task) => task.status === "done").length;
  const planPreview = useMemo(() => buildTodayPlan(data), [data]);
  const leadPlan = planPreview.plans[0];

  useEffect(() => {
    if (!timerStartedAt) return;
    const timer = window.setInterval(() => setElapsed(Math.floor((Date.now() - timerStartedAt) / 1000)), 1000);
    return () => window.clearInterval(timer);
  }, [timerStartedAt]);

  const toggleFocus = () => {
    if (!focusTask) { editTask(); return; }
    if (timerStartedAt) { setTimerStartedAt(null); toast("专注已暂停，进度仍然保留"); }
    else { setTimerStartedAt(Date.now() - elapsed * 1000); if (focusTask.status !== "doing") dispatch({ type: "update-task", task: { ...focusTask, status: "doing" } }); toast("专注计时已开始"); }
  };

  const progressFor = (goalId: string) => {
    const goal = data.goals.find((item) => item.id === goalId);
    return goal ? Math.min(100, Math.round(goal.currentValue / Math.max(1, goal.targetValue) * 100)) : 0;
  };

  const eventPosition = (event: CalendarEvent) => {
    const start = new Date(event.startAt);
    const end = new Date(event.endAt);
    const startMinutes = start.getHours() * 60 + start.getMinutes();
    const endMinutes = end.getHours() * 60 + end.getMinutes();
    const min = 8 * 60;
    const span = 12 * 60;
    return { left: `${Math.max(0, (startMinutes - min) / span * 100)}%`, width: `${Math.max(8, (endMinutes - startMinutes) / span * 100)}%` };
  };

  return (
    <div className="view-page">
      <header className="view-head">
        <div><p className="eyebrow">{new Date().getMonth() + 1} 月 {new Date().getDate()} 日 · {weekdayLabel()}</p><h1>{greeting()}</h1><p>今天有 {todayTasks.filter((task) => task.status !== "done").length} 项待完成，{todayEvents.length ? `下一项日程在 ${formatTime(todayEvents[0].startAt)}` : "没有固定日程"}。</p></div>
        <div className="head-actions energy-anchor">
          <button className="button secondary" type="button" onClick={() => setEnergyOpen((value) => !value)}>精力 {data.settings.energy} / 5</button>
          <button className="button primary" type="button" onClick={openReview}>晚间复盘</button>
          {energyOpen && <div className="energy-popover" role="dialog" aria-label="选择精力"><strong>现在精力如何？</strong><div>{[1,2,3,4,5].map((value) => <button key={value} className={data.settings.energy === value ? "active" : ""} type="button" onClick={() => { dispatch({ type: "set-energy", value }); setEnergyOpen(false); toast(`精力已记录为 ${value} / 5`); }}>{value}</button>)}</div></div>}
        </div>
      </header>

      <section className="day-board">
        <div className="focus-stage">
          <div className="stage-kicker"><span className="live-dot" />{focusTask?.scheduledStart ? `${formatTime(focusTask.scheduledStart)}—${focusTask.scheduledEnd ? formatTime(focusTask.scheduledEnd) : "灵活"}` : "下一项重点"}</div>
          <div className="focus-copy">
            <h2>{focusTask?.title ?? "为今天添加一个重点任务"}</h2>
            <p>{focusTask ? `预计 ${focusTask.estimateMinutes} 分钟。先完成可验证的最小结果，再决定是否继续。` : "重要任务会出现在这里，并与目标和真实可用时间一起安排。"}</p>
            {focusTask && <div className="focus-meta"><span><Target size={17} />{data.goals.find((goal) => goal.id === focusTask.goalId)?.title ?? "独立任务"}</span><span><Clock3 size={17} />{timerStartedAt ? `已专注 ${Math.floor(elapsed / 60)}:${String(elapsed % 60).padStart(2, "0")}` : `${focusTask.estimateMinutes} 分钟`}</span></div>}
          </div>
          <div className="stage-actions"><button className="stage-primary" type="button" onClick={toggleFocus}>{timerStartedAt ? <Pause size={18} /> : <Play size={18} />}{timerStartedAt ? "暂停专注" : "开始专注"}</button><button className="stage-secondary" type="button" onClick={() => openCapture("record")}><Plus size={18} />记下干扰</button></div>
        </div>
        <div className="dayline">
          <div className="dayline-head"><h2>日序时间带</h2><span>08:00—20:00 · {todayEvents.length} 项安排</span></div>
          <div className="ribbon-scroll"><div className="time-ribbon"><div className="ruler-labels">{[8,9,10,12,14,16,18,19,20].map((hour) => <span key={hour}>{String(hour).padStart(2,"0")}</span>)}</div><div className="ruler-line" />{todayEvents.map((event) => <button key={event.id} data-entity-id={event.id} className={`time-event ${kindClass[event.kind]}`} style={eventPosition(event)} type="button" onClick={() => editEvent(event)}><strong>{event.title}</strong><small>{formatTime(event.startAt)}—{formatTime(event.endAt)}</small></button>)}</div></div>
          <div className="ai-strip"><span className="ai-mark"><Sparkles size={17} /></span><div><strong>{data.settings.aiEnabled ? leadPlan ? `建议 ${formatTime(leadPlan.startAt)} 推进“${leadPlan.title}”` : "今天暂时没有可写入的任务安排" : "AI 建议已关闭"}</strong><span>{data.settings.aiEnabled ? leadPlan ? "基于目标、优先级、精力和固定日程；写入前仍需确认" : planPreview.deferred[0]?.reason ?? "添加任务后会在本地计算可用时间" : "规则时间轴和本地功能不受影响"}</span></div>{data.settings.aiEnabled && <div className="ai-buttons"><button type="button" onClick={() => setPlanOpen(true)}>{planPreview.plans.length ? `${planPreview.plans.length} 项建议` : "查看排程"}</button>{leadPlan && <button type="button" onClick={() => setSourcesOpen((value) => !value)}>{sourcesOpen ? "收起依据" : `${planPreview.plans.length} 条依据`}</button>}</div>}</div>
          {sourcesOpen && leadPlan && <div className="source-detail">{planPreview.plans.map((plan) => <span key={plan.id}>{formatTime(plan.startAt)} · {plan.evidence}</span>)}</div>}
        </div>
      </section>

      <div className="today-grid">
        <section className="panel agenda-panel">
          <div className="panel-head"><div className="panel-title"><h2>接下来</h2><span>{doneCount} / {todayTasks.length} 完成</span></div><button className="text-button" type="button" onClick={() => editTask()}>添加任务 <Plus size={16} /></button></div>
          <div className="agenda-list">
            {todayTasks.map((task) => <AgendaRow key={task.id} task={task} goal={data.goals.find((goal) => goal.id === task.goalId)?.title} onToggle={() => dispatch({ type: "toggle-task", id: task.id })} onEdit={() => editTask(task)} />)}
            {!todayTasks.length && <div className="empty-state"><CheckSquare2 size={24} /><strong>今天还没有任务</strong><p>添加一个 30 分钟内可以开始的动作。</p><button className="button primary" type="button" onClick={() => editTask()}>添加任务</button></div>}
          </div>
        </section>
        <aside className="side-stack">
          <section className="panel goal-pulse"><div className="goal-pulse-head"><div><h2>目标脉搏</h2><p>{activeGoals.filter((goal) => goal.health === "normal").length} 项保持节奏，{activeGoals.filter((goal) => goal.health !== "normal").length} 项需关注</p></div><span className="pace-value">{Math.round(activeGoals.reduce((sum, goal) => sum + progressFor(goal.id), 0) / Math.max(activeGoals.length, 1))}%</span></div>{activeGoals.slice(0,3).map((goal) => <button className="goal-row" key={goal.id} type="button" onClick={() => { window.location.hash = "goals"; }}><span><b>{goal.title}</b><small>{goal.currentValue} / {goal.targetValue} {goal.unit}</small></span><span className="progress"><i style={{ width: `${progressFor(goal.id)}%` }} /></span></button>)}<button className="text-button goal-all" type="button" onClick={() => { window.location.hash = "goals"; }}>查看全部目标 <ChevronRight size={15} /></button></section>
          <section className="panel capture-panel"><Flag size={19} /><h2>先记下来，再决定放哪</h2><p>原文会一直保留；解析失败也不会丢失。</p><button className="capture-field" type="button" onClick={() => openCapture()}><StickyNote size={18} />“周五下午三点看牙…”</button></section>
        </aside>
      </div>
      <PlanDialog open={planOpen} onClose={() => setPlanOpen(false)} />
    </div>
  );
}

function AgendaRow({ task, goal, onToggle, onEdit }: { task: Task; goal?: string; onToggle(): void; onEdit(): void }) {
  const label = task.scheduledStart ? formatTime(task.scheduledStart) : task.dueAt ? formatTime(task.dueAt) : "灵活";
  return <div className={`agenda-row ${task.status === "done" ? "done" : ""}`} data-entity-id={task.id} tabIndex={-1}><time>{task.status === "done" ? "已完成" : label}</time><button className="agenda-check" type="button" onClick={onToggle} aria-label={task.status === "done" ? `恢复${task.title}` : `完成${task.title}`}><span className="agenda-check-box">{task.status === "done" && <Check size={15} />}</span></button><button className="agenda-copy" type="button" onClick={onEdit}><strong>{task.title}</strong><span>{goal ?? "独立任务"} · 预计 {task.estimateMinutes} 分钟</span></button><span className={`priority ${task.priority}`}>{task.priority === "important" ? "重要" : "普通"}</span></div>;
}

function PlanDialog({ open, onClose }: { open: boolean; onClose(): void }) {
  const { data, dispatch } = useAppStore();
  const { toast } = useUi();
  const { plans, deferred } = useMemo(() => buildTodayPlan(data), [data]);
  const planKey = plans.map((plan) => `${plan.id}:${plan.startAt}`).join("|");
  const [checked, setChecked] = useState<string[]>([]);

  useEffect(() => {
    if (open) setChecked(plans.map((plan) => plan.id));
  }, [open, planKey]);

  const apply = () => {
    plans.filter((plan) => checked.includes(plan.id)).forEach((plan, index) => {
      const task = data.tasks.find((item) => item.id === plan.taskId);
      if (task) dispatch({ type: "update-task", task: { ...task, status: index === 0 && task.status === "todo" ? "doing" : task.status, scheduledStart: plan.startAt, scheduledEnd: plan.endAt } });
    });
    onClose(); toast(`已确认 ${checked.length} 项计划，操作可在审计记录中撤销`);
  };
  return <Modal open={open} title="建议的今日计划" description="固定日程和缓冲不会移动；取消勾选即可跳过。" onClose={onClose} footer={<><button className="button secondary" type="button" onClick={onClose}>暂不调整</button><button className="button primary" type="button" disabled={!checked.length} onClick={apply}>确认写入 {checked.length} 项</button></>}><div className="plan-list">{plans.map((plan) => <label className="plan-row" key={plan.id}><input type="checkbox" checked={checked.includes(plan.id)} onChange={(event) => setChecked((items) => event.target.checked ? [...items, plan.id] : items.filter((item) => item !== plan.id))} /><time>{formatTime(plan.startAt)}</time><span><strong>{plan.title}</strong><small>{plan.detail}</small><small className="plan-evidence">依据：{plan.evidence} · 置信度 {plan.confidence}%</small></span></label>)}{!plans.length && <div className="empty-state compact"><CalendarDays size={22} /><strong>今天没有可排入的任务</strong><p>固定日程与缓冲保持不变；可以先添加任务或调整预计时长。</p></div>}</div>{deferred.length > 0 && <div className="plan-exclusions"><strong>这次没有排入</strong>{deferred.map((task) => <span key={task.taskId}>{task.title}：{task.reason}</span>)}</div>}</Modal>;
}
