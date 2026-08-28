import { Bot, Check, ChevronRight, CircleStop, Clock3, FileClock, Flag, Lock, Play, RotateCcw, ShieldCheck, Sparkles, Target } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { addMinutes, format } from "date-fns";
import { createId } from "../domain/ids";
import { formatDateTime } from "../domain/dates";
import type { ActionMode, AgentChange, AgentRun, AgentStep } from "../domain/types";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";
import { Modal } from "../components/Modal";

const statusLabel: Record<AgentRun["status"], string> = { ready: "准备中", reading: "正在读取", analyzing: "正在分析", waiting: "等待确认", applying: "正在写入", completed: "已完成", failed: "失败", stopped: "已停止" };

export function AgentPage() {
  const { data, dispatch } = useAppStore();
  const { openSettings, toast } = useUi();
  const [delegateOpen, setDelegateOpen] = useState(false);
  const [intent, setIntent] = useState("");
  const [mode, setMode] = useState<ActionMode>("confirm");
  const [selectedRunId, setSelectedRunId] = useState(data.agentRuns[0]?.id ?? "");
  const run = data.agentRuns.find((item) => item.id === selectedRunId) ?? data.agentRuns[0];
  const [selectedChanges, setSelectedChanges] = useState<string[]>(run?.changes.filter((change) => change.status === "pending").map((change) => change.id) ?? []);
  const [sourcesOpen, setSourcesOpen] = useState(false);

  useEffect(() => {
    if (!run || (run.status !== "reading" && run.status !== "analyzing")) return;
    const timer = window.setTimeout(() => dispatch({ type: "advance-agent", id: run.id }), run.status === "reading" ? 650 : 850);
    return () => window.clearTimeout(timer);
  }, [dispatch, run]);

  useEffect(() => {
    if (run) setSelectedChanges(run.changes.filter((change) => change.status === "pending").map((change) => change.id));
  }, [run?.id, run?.status]);

  const pendingCount = data.agentRuns.reduce((sum, item) => sum + (item.status === "waiting" ? item.changes.filter((change) => change.status === "pending").length : 0), 0);
  const startRun = () => {
    if (!intent.trim()) return;
    if (!data.settings.aiEnabled) { toast("请先在设置中开启 AI 建议"); return; }
    const scope = [data.settings.permissions.goals ? "目标与任务" : "", data.settings.permissions.calendar ? "未来 30 天日程" : "", data.settings.permissions.records ? "最近 14 天记录" : "", data.settings.permissions.privateNotes ? "私人笔记" : ""].filter(Boolean);
    if (!scope.length) { toast("至少授权一个数据范围后才能运行"); return; }
    const id = createId("run");
    const activeGoal = data.settings.permissions.goals ? data.goals.find((goal) => goal.status === "active" && goal.health !== "normal") ?? data.goals.find((goal) => goal.status === "active") : undefined;
    const openTask = data.settings.permissions.goals ? data.tasks.find((task) => (task.status === "todo" || task.status === "doing") && (!activeGoal || task.goalId === activeGoal.id)) ?? data.tasks.find((task) => task.status === "todo" || task.status === "doing") : undefined;
    const taskDuration = openTask?.estimateMinutes ?? 45;
    const suggestedStart = new Date(); suggestedStart.setDate(suggestedStart.getDate() + 1); suggestedStart.setHours(data.settings.energy >= 4 ? 9 : 10, 0, 0, 0);
    const suggestedEnd = addMinutes(suggestedStart, taskDuration);
    const suggestedWindow = `明天 ${format(suggestedStart, "HH:mm")}—${format(suggestedEnd, "HH:mm")} · ${taskDuration} 分钟`;
    const taskSources = [activeGoal ? { id: activeGoal.id, kind: "goal" as const, label: activeGoal.title } : null, openTask ? { id: openTask.id, kind: "task" as const, label: openTask.title } : null].filter((item): item is NonNullable<typeof item> => Boolean(item));
    const changes: AgentChange[] = mode === "read" || !data.settings.permissions.goals ? [] : openTask ? [{ id: createId("change"), type: "reschedule-task", entityId: openTask.id, title: `安排“${openTask.title}”`, before: openTask.scheduledStart ? formatDateTime(openTask.scheduledStart) : "尚未安排", after: suggestedWindow, reason: activeGoal ? `这项任务直接推进“${activeGoal.title}”，并匹配当前精力 ${data.settings.energy} / 5。` : `这是当前优先级较高的未完成任务，并匹配当前精力 ${data.settings.energy} / 5。`, status: "pending", sourceRefs: taskSources }] : activeGoal ? [{ id: createId("change"), type: "create-task", entityId: activeGoal.id, title: `创建“推进：${activeGoal.title}”任务`, after: suggestedWindow, reason: `当前没有可执行任务，先为“${activeGoal.title}”创建一个 ${taskDuration} 分钟的起步动作。`, status: "pending", sourceRefs: taskSources }] : [];
    const effectiveMode: ActionMode = mode === "confirm" && changes.length ? "confirm" : "read";
    const steps: AgentStep[] = [
      { id: createId("step"), title: "读取授权数据", detail: scope.join("、"), status: "running", meta: "正在读取" },
      { id: createId("step"), title: "检查时间与目标冲突", detail: "使用确定性规则计算可用时间", status: "pending" },
      { id: createId("step"), title: effectiveMode === "read" ? "生成只读结果" : "生成待确认变更", detail: effectiveMode === "read" ? "返回带来源的分析" : "任何管理动作都需要确认", status: "pending" },
      ...(effectiveMode === "confirm" ? [{ id: createId("step"), title: "写入并核验", detail: "只在确认后执行", status: "pending" as const }] : []),
    ];
    const sourceRefs = [data.settings.permissions.goals && data.goals[0] ? { id: data.goals[0].id, kind: "goal" as const, label: data.goals[0].title } : null, data.settings.permissions.calendar && data.events[0] ? { id: data.events[0].id, kind: "event" as const, label: data.events[0].title } : null, data.settings.permissions.records && data.records[0] ? { id: data.records[0].id, kind: "record" as const, label: data.records[0].rawText.slice(0, 32) } : null, data.settings.permissions.privateNotes && data.notes[0] ? { id: data.notes[0].id, kind: "note" as const, label: data.notes[0].title } : null].filter((item): item is NonNullable<typeof item> => Boolean(item));
    const next: AgentRun = { id, intent: intent.trim(), status: "reading", actionMode: effectiveMode, scope, sourceRefs, steps, changes, startedAt: new Date().toISOString() };
    dispatch({ type: "start-agent", run: next });
    setSelectedRunId(id); setDelegateOpen(false); setIntent(""); toast("Agent 已开始检查，可随时停止");
  };

  const approve = () => {
    if (!run || !selectedChanges.length) return;
    dispatch({ type: "approve-agent", id: run.id, changeIds: selectedChanges });
    toast(`${selectedChanges.length} 项变更已执行并核验，可在审计记录中撤销`);
  };

  const recentAudit = useMemo(() => data.audit.slice(0, 8), [data.audit]);

  return <div className="view-page"><header className="view-head"><div><p className="eyebrow">可委托、可监督</p><h1>把整理交出去，把决定留给自己。</h1><p>查看读取范围、运行步骤、待确认变更和操作历史。</p></div><button className="button primary" type="button" onClick={() => data.settings.aiEnabled ? setDelegateOpen(true) : openSettings()}><Sparkles size={17} />{data.settings.aiEnabled ? "发起委托" : "开启 AI"}</button></header>
    <section className="agent-hero"><div><div className="agent-status"><span className="live-dot" />本地 Agent {data.settings.aiEnabled ? "已就绪" : "已暂停"}</div><h2>{pendingCount ? `有 ${pendingCount} 项变更正在等待你的确认` : run?.summary ?? "没有待处理变更"}</h2><p>Agent 只在授权范围内读取；所有写入都在确认后执行。</p></div>{pendingCount > 0 ? <button className="agent-hero-button" type="button" onClick={() => document.querySelector(".approval-box")?.scrollIntoView({ behavior: "smooth", block: "center" })}>检查 {pendingCount} 项变更</button> : <button className="agent-hero-button" type="button" onClick={() => data.settings.aiEnabled ? setDelegateOpen(true) : openSettings()}>{data.settings.aiEnabled ? "开始新委托" : "前往设置开启"}</button>}</section>
    <div className="agent-layout"><section className="panel run-panel">{run ? <><div className="run-header"><div><small>当前委托 · {formatDateTime(run.startedAt)}</small><h2>{run.intent}</h2></div><div className="run-header-actions">{(["reading","analyzing","waiting"] as AgentRun["status"][]).includes(run.status) && <button className="text-button danger-text" type="button" onClick={() => { dispatch({ type: "stop-agent", id: run.id }); toast("Agent 已停止，没有执行新的写入"); }}><CircleStop size={15} />停止</button>}<span className={`run-status ${run.status}`}>{statusLabel[run.status]}</span></div></div><div className="run-steps">{run.steps.map((step) => <div className={`run-step ${step.status}`} key={step.id}><span className="step-node">{step.status === "done" ? <Check size={15} /> : step.status === "running" ? <Sparkles size={15} /> : <Lock size={14} />}</span><div><strong>{step.title}</strong><span>{step.detail}</span></div><small>{step.meta}</small></div>)}</div>{run.status === "waiting" && <div className="approval-box"><div className="approval-head"><div><h3>待确认变更</h3><p>可调整建议值；取消勾选即可跳过该项。</p></div><span>低风险 · 可撤销</span></div>{run.changes.filter((change) => change.status === "pending").map((change) => <div className="change-row" key={change.id}><label className="change-toggle"><input aria-label={`选择${change.title}`} type="checkbox" checked={selectedChanges.includes(change.id)} onChange={(event) => setSelectedChanges((items) => event.target.checked ? [...items,change.id] : items.filter((id) => id !== change.id))} /></label><span><strong>{change.title}</strong>{change.before && <span className="diff"><del>{change.before}</del></span>}<label className="change-after"><span>调整后</span><input value={change.after} onChange={(event) => dispatch({ type: "edit-agent-change", runId: run.id, changeId: change.id, after: event.target.value })} /></label><small>{change.reason}</small></span></div>)}{sourcesOpen && <div className="agent-sources">{run.changes.flatMap((change) => change.sourceRefs).filter((ref,index,all) => all.findIndex((item) => item.id === ref.id) === index).map((ref,index) => <span key={ref.id}><b>{index + 1}</b>{ref.label}</span>)}</div>}<div className="approval-actions"><button className="text-button" type="button" onClick={() => setSourcesOpen((value) => !value)}>{sourcesOpen ? "收起依据" : "查看引用依据"}</button><div><button className="button secondary" type="button" onClick={() => { dispatch({ type: "reject-agent", id: run.id }); toast("全部变更已拒绝，没有修改数据"); }}>全部拒绝</button><button className="button primary" type="button" disabled={!selectedChanges.length} onClick={approve}>确认并执行 {selectedChanges.length} 项</button></div></div></div>}{(run.status === "completed" || run.status === "stopped") && <div className="run-result"><ShieldCheck size={21} /><div><strong>{run.summary ?? "运行已经结束"}</strong><p>{run.status === "completed" ? "结果与审计记录均已保存在本地。" : "可以调整范围后重新发起。"}</p>{run.sourceRefs?.length ? <div className="result-sources">来源：{run.sourceRefs.map((ref) => ref.label).join("、")}</div> : null}<button className="text-button retry-run" type="button" onClick={() => { setIntent(run.intent); setMode(run.actionMode); setDelegateOpen(true); }}>调整范围后再运行</button></div></div>}</> : <div className="empty-state page-empty"><Bot size={28} /><strong>还没有 Agent 委托</strong><p>从一个结果明确的任务开始。</p><button className="button primary" type="button" onClick={() => setDelegateOpen(true)}>发起委托</button></div>}</section>
      <aside className="agent-side"><section className="panel permission-panel"><div className="panel-title"><Lock size={17} /><h2>数据权限</h2></div>{(["goals","calendar","records","privateNotes"] as const).map((key) => { const meta = { goals:["目标与任务","允许读取全部"], calendar:["日程","只读未来 30 天"], records:["记录","只读最近 14 天"], privateNotes:["私人笔记","本次任务不允许"] }[key]; const value = data.settings.permissions[key]; return <div className="permission-row" key={key}><div><strong>{meta[0]}</strong><span>{meta[1]}</span></div><button className={`switch ${value ? "on" : ""}`} type="button" role="switch" aria-checked={value} onClick={() => { dispatch({ type: "set-permission", key, value: !value }); toast(`${meta[0]}权限已${value ? "关闭" : "开启"}`); }}><span /></button></div>; })}<div className="write-policy"><ShieldCheck size={15} /><span><strong>写入策略：始终确认</strong><small>Agent 只能生成变更草稿。</small></span></div></section>
        <section className="panel run-history"><div className="panel-title"><Clock3 size={17} /><h2>运行历史</h2></div>{data.agentRuns.slice(0,5).map((item) => <button key={item.id} className={item.id === run?.id ? "active" : ""} type="button" onClick={() => setSelectedRunId(item.id)}><span><strong>{item.intent}</strong><small>{formatDateTime(item.startedAt)} · {statusLabel[item.status]}</small></span><ChevronRight size={15} /></button>)}</section>
        <section className="panel audit-panel"><div className="panel-title"><FileClock size={17} /><h2>操作记录</h2></div>{recentAudit.map((item) => <div className="audit-item" key={item.id}><span><strong>{item.action}</strong><small>{formatDateTime(item.createdAt)} · {item.actor === "agent" ? "Agent" : "由你"}</small></span>{item.undo && <button type="button" onClick={() => { dispatch({ type: "undo", auditId: item.id }); toast(`已撤销：${item.action}`); }}><RotateCcw size={14} />撤销</button>}</div>)}</section></aside>
    </div>
    <DelegateDialog open={delegateOpen} intent={intent} setIntent={setIntent} mode={mode} setMode={setMode} onClose={() => setDelegateOpen(false)} onStart={startRun} permissions={data.settings.permissions} />
  </div>;
}

function DelegateDialog({ open, intent, setIntent, mode, setMode, onClose, onStart, permissions }: { open: boolean; intent: string; setIntent(value:string):void; mode: ActionMode; setMode(value:ActionMode):void; onClose():void; onStart():void; permissions: { goals:boolean; calendar:boolean; records:boolean; privateNotes:boolean } }) {
  return <Modal open={open} title="发起 Agent 委托" description="先检查它准备读取什么，再开始运行。" onClose={onClose} footer={<><button className="button secondary" type="button" onClick={onClose}>取消</button><button className="button primary" type="button" onClick={onStart} disabled={!intent.trim()}><Play size={16} />生成执行步骤</button></>}><label className="form-field"><span>希望得到什么结果</span><textarea value={intent} onChange={(event) => setIntent(event.target.value)} placeholder="例如：整理本周未完成事项，优先保证产品方案和跑步目标" /></label><fieldset className="mode-options"><legend>运行模式</legend><label className={mode === "confirm" ? "active" : ""}><input type="radio" name="mode" checked={mode === "confirm"} onChange={() => setMode("confirm")} /><span><Flag size={17} /><strong>生成管理建议</strong><small>变更逐项确认后执行</small></span></label><label className={mode === "read" ? "active" : ""}><input type="radio" name="mode" checked={mode === "read"} onChange={() => setMode("read")} /><span><Target size={17} /><strong>只读分析</strong><small>只返回结论和引用来源</small></span></label></fieldset><div className="scope-preview"><Lock size={15} /><div><strong>本次读取范围</strong><span>{[permissions.goals&&"目标与任务",permissions.calendar&&"未来 30 天日程",permissions.records&&"最近 14 天记录",permissions.privateNotes&&"私人笔记"].filter(Boolean).join("、") || "尚未授权任何数据"}</span></div></div></Modal>;
}
