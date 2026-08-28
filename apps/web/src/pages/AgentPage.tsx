import { Bot, Check, ChevronRight, CircleStop, Clock3, FileClock, Flag, Lock, Play, RotateCcw, ShieldCheck, Sparkles, Target } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import {
  acceptAgentChange,
  createAgentRun,
  getAgentRun,
  listAgentRuns,
  rejectAgentChange,
  stopAgentRun,
  type AgentScope,
  type ServerAgentChange,
  type ServerAgentRun,
  type ServerAgentRunStatus,
} from "../api/agent";
import { auditEntityVersion, listAuditEvents, undoAuditEvent, type ServerAuditEvent } from "../api/audit";
import { Modal } from "../components/Modal";
import { formatDateTime } from "../domain/dates";
import type { ActionMode, AppData } from "../domain/types";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";

const statusLabel: Record<ServerAgentRunStatus, string> = {
  ready: "准备中", reading: "正在读取", analyzing: "正在分析", waiting: "等待确认",
  applying: "正在写入", completed: "已完成", failed: "失败", stopped: "已停止",
};
const activeStatuses = new Set<ServerAgentRunStatus>(["ready", "reading", "analyzing", "applying"]);
const stoppableStatuses = new Set<ServerAgentRunStatus>([...activeStatuses, "waiting"]);
const auditActionLabel: Record<string, string> = {
  "agent.run.create": "发起 Agent 委托",
  "agent.run.start": "Agent 开始分析",
  "agent.run.analyze": "Agent 完成分析",
  "agent.run.stop": "停止 Agent 运行",
  "agent.run.fail": "Agent 分析失败",
  "agent.change.apply": "应用 Agent 变更",
  "agent.change.reject": "拒绝 Agent 变更",
  "audit.undo": "撤销历史变更",
};

type Permissions = AppData["settings"]["permissions"];

function scopeFor(permissions: Permissions): AgentScope {
  const domains: AgentScope["domains"] = [];
  if (permissions.goals) domains.push("goals", "tasks");
  if (permissions.calendar) domains.push("calendar");
  if (permissions.records) domains.push("records");
  if (permissions.privateNotes) domains.push("notes");
  const scope: AgentScope = { domains, entityIds: [] };
  if (permissions.calendar || permissions.records) {
    const from = new Date();
    const to = new Date();
    from.setDate(from.getDate() - (permissions.records ? 14 : 0));
    to.setDate(to.getDate() + (permissions.calendar ? 30 : 0));
    scope.from = from.toISOString();
    scope.to = to.toISOString();
  }
  return scope;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "请求未完成，请稍后重试";
}

function validDate(value: string): boolean {
  return /^\d{4}-\d{2}-\d{2}T/.test(value) && Number.isFinite(new Date(value).getTime());
}

const previewLabels: Record<string, string> = {
  title: "标题", status: "状态", priority: "优先级", estimateMinutes: "预计分钟",
  scheduledStart: "开始", scheduledEnd: "结束", startAt: "开始", endAt: "结束",
  goalId: "目标", sourceRecordId: "来源记录", archivedAt: "归档时间", linkedEntityIds: "关联对象",
};

function previewText(value: unknown): string {
  if (value === undefined || value === null) return "未设置";
  if (typeof value === "string") return validDate(value) ? formatDateTime(value) : value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  if (Array.isArray(value)) return value.map(previewText).join("、") || "空列表";
  if (typeof value === "object") {
    return Object.entries(value as Record<string, unknown>)
      .map(([key, item]) => `${previewLabels[key] ?? key}：${previewText(item)}`)
      .join(" · ") || "无内容";
  }
  return String(value);
}

function changeTitle(change: ServerAgentChange, run: ServerAgentRun): string {
  const after = change.previewAfter && typeof change.previewAfter === "object" && !Array.isArray(change.previewAfter)
    ? change.previewAfter as Record<string, unknown>
    : {};
  const source = run.sourceRefs.find((item) => item.entityId === change.targetId)?.labelSnapshot;
  if (change.changeType === "create-task") return `创建任务“${typeof after.title === "string" ? after.title : "未命名任务"}”`;
  if (change.changeType === "create-event") return `创建日程“${typeof after.title === "string" ? after.title : "未命名日程"}”`;
  if (change.changeType === "reschedule-task") return `调整“${source ?? "任务"}”的安排`;
  if (change.changeType === "archive-record") return `归档“${source ?? "记录"}”`;
  if (change.changeType === "link-note") return `更新“${source ?? "笔记"}”的关联`;
  return "应用受控变更";
}

export function AgentPage() {
  const { data, dispatch, createServerMutationContext, syncNow } = useAppStore();
  const { openSettings, toast } = useUi();
  const [runs, setRuns] = useState<ServerAgentRun[]>([]);
  const [audits, setAudits] = useState<ServerAuditEvent[]>([]);
  const [selectedRunId, setSelectedRunId] = useState("");
  const [selectedChanges, setSelectedChanges] = useState<string[]>([]);
  const [delegateOpen, setDelegateOpen] = useState(false);
  const [intent, setIntent] = useState("");
  const [mode, setMode] = useState<ActionMode>("confirm");
  const [sourcesOpen, setSourcesOpen] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const run = runs.find((item) => item.id === selectedRunId) ?? runs[0];

  const replaceRun = useCallback((next: ServerAgentRun) => {
    setRuns((items) => items.some((item) => item.id === next.id)
      ? items.map((item) => item.id === next.id ? next : item)
      : [next, ...items]);
  }, []);

  const refreshAudits = useCallback(async () => {
    const page = await listAuditEvents({ limit: 8 });
    setAudits(page.events);
  }, []);

  const loadOverview = useCallback(async () => {
    setLoadError(null);
    const [runPage, auditPage] = await Promise.all([listAgentRuns({ limit: 20 }), listAuditEvents({ limit: 8 })]);
    setRuns(runPage.runs);
    setAudits(auditPage.events);
    setSelectedRunId((current) => current && runPage.runs.some((item) => item.id === current) ? current : runPage.runs[0]?.id ?? "");
  }, []);

  useEffect(() => {
    let mounted = true;
    void loadOverview().catch((error) => { if (mounted) setLoadError(errorMessage(error)); }).finally(() => { if (mounted) setLoading(false); });
    return () => { mounted = false; };
  }, [loadOverview]);

  useEffect(() => {
    if (!run || !activeStatuses.has(run.status)) return;
    let mounted = true;
    const refresh = async () => {
      try {
        const next = await getAgentRun(run.id);
        if (mounted) replaceRun(next);
      } catch {
        // A later poll or explicit user action can recover transient failures.
      }
    };
    void refresh();
    const timer = window.setInterval(refresh, 1_500);
    return () => { mounted = false; window.clearInterval(timer); };
  }, [replaceRun, run?.id, run?.status]);

  useEffect(() => {
    if (!run) { setSelectedChanges([]); return; }
    setSelectedChanges(run.changes.filter((change) => change.status === "pending").map((change) => change.id));
  }, [run?.id, run?.updatedAt]);

  const pendingCount = useMemo(() => runs.reduce((sum, item) => sum + item.changes.filter((change) => change.status === "pending").length, 0), [runs]);

  const startRun = async () => {
    if (!intent.trim() || busy) return;
    if (!data.settings.aiEnabled) { toast("请先在设置中开启 AI 建议"); return; }
    const scope = scopeFor(data.settings.permissions);
    if (!scope.domains.length) { toast("至少授权一个数据范围后才能运行"); return; }
    setBusy("create");
    try {
      const context = await createServerMutationContext();
      const created = await createAgentRun({ intent: intent.trim(), actionMode: mode, scope }, context);
      replaceRun(created);
      setSelectedRunId(created.id);
      setDelegateOpen(false);
      setIntent("");
      toast("Agent 已进入后台队列，可随时查看或停止");
      try { replaceRun(await getAgentRun(created.id)); } catch { /* polling continues */ }
      await refreshAudits().catch(() => undefined);
    } catch (error) {
      toast(errorMessage(error));
    } finally {
      setBusy(null);
    }
  };

  const stopRun = async () => {
    if (!run || busy) return;
    setBusy(run.id);
    try {
      const stopped = await stopAgentRun(run.id, run.version, await createServerMutationContext());
      replaceRun(stopped);
      await refreshAudits().catch(() => undefined);
      toast("Agent 已停止，没有执行新的写入");
    } catch (error) {
      toast(errorMessage(error));
      try { replaceRun(await getAgentRun(run.id)); } catch { /* keep current state */ }
    } finally {
      setBusy(null);
    }
  };

  const resolveChanges = async (acceptedIds: string[]) => {
    if (!run || busy) return;
    const pending = run.changes.filter((change) => change.status === "pending");
    if (!pending.length) return;
    const accepted = new Set(acceptedIds);
    setBusy(run.id);
    try {
      for (const change of pending) {
        const context = await createServerMutationContext();
        if (accepted.has(change.id)) await acceptAgentChange(change.id, change.version, context);
        else await rejectAgentChange(change.id, change.version, context);
      }
      replaceRun(await getAgentRun(run.id));
      await Promise.all([syncNow(), refreshAudits()]);
      toast(accepted.size ? `已执行 ${accepted.size} 项变更，未选择项已拒绝` : "全部变更已拒绝，没有修改数据");
    } catch (error) {
      toast(`部分操作可能已经提交：${errorMessage(error)}`);
      try { replaceRun(await getAgentRun(run.id)); } catch { /* keep last confirmed response */ }
      await Promise.all([syncNow(), refreshAudits().catch(() => undefined)]);
    } finally {
      setBusy(null);
    }
  };

  const undo = async (event: ServerAuditEvent) => {
    const version = auditEntityVersion(event);
    if (!version || busy) return;
    setBusy(event.id);
    try {
      await undoAuditEvent(event.id, version, await createServerMutationContext());
      await Promise.all([syncNow(), refreshAudits()]);
      toast(`已撤销：${auditActionLabel[event.action] ?? event.action}`);
    } catch (error) {
      toast(errorMessage(error));
      await refreshAudits().catch(() => undefined);
    } finally {
      setBusy(null);
    }
  };

  return <div className="view-page">
    <header className="view-head"><div><p className="eyebrow">可委托、可监督</p><h1>把整理交出去，把决定留给自己。</h1><p>查看读取范围、运行步骤、待确认变更和服务端审计历史。</p></div><button className="button primary" type="button" onClick={() => data.settings.aiEnabled ? setDelegateOpen(true) : openSettings()}><Sparkles size={17} />{data.settings.aiEnabled ? "发起委托" : "开启 AI"}</button></header>
    <section className="agent-hero"><div><div className="agent-status"><span className="live-dot" />服务端 Agent {data.settings.aiEnabled ? "已就绪" : "已暂停"}</div><h2>{pendingCount ? `有 ${pendingCount} 项变更正在等待你的确认` : run?.summary ?? "没有待处理变更"}</h2><p>Agent 只在本次授权范围内读取；所有写入都在确认后执行并记录审计。</p></div>{pendingCount > 0 ? <button className="agent-hero-button" type="button" onClick={() => document.querySelector(".approval-box")?.scrollIntoView({ behavior: "smooth", block: "center" })}>检查 {pendingCount} 项变更</button> : <button className="agent-hero-button" type="button" onClick={() => data.settings.aiEnabled ? setDelegateOpen(true) : openSettings()}>{data.settings.aiEnabled ? "开始新委托" : "前往设置开启"}</button>}</section>
    <div className="agent-layout"><section className="panel run-panel">
      {loading ? <div className="empty-state page-empty"><Sparkles size={28} /><strong>正在读取 Agent 运行</strong></div>
        : loadError ? <div className="empty-state page-empty"><Bot size={28} /><strong>暂时无法读取 Agent</strong><p>{loadError}</p><button className="button secondary" type="button" onClick={() => { setLoading(true); void loadOverview().catch((error) => setLoadError(errorMessage(error))).finally(() => setLoading(false)); }}>重新加载</button></div>
        : run ? <><div className="run-header"><div><small>当前委托 · {formatDateTime(run.startedAt ?? run.createdAt)}</small><h2>{run.intent}</h2></div><div className="run-header-actions">{stoppableStatuses.has(run.status) && <button className="text-button danger-text" type="button" disabled={busy === run.id} onClick={() => void stopRun()}><CircleStop size={15} />停止</button>}<span className={`run-status ${run.status}`}>{statusLabel[run.status]}</span></div></div>
          <div className="run-steps">{run.steps.length ? run.steps.map((step) => <div className={`run-step ${step.status}`} key={step.id}><span className="step-node">{step.status === "done" ? <Check size={15} /> : step.status === "running" ? <Sparkles size={15} /> : <Lock size={14} />}</span><div><strong>{step.title}</strong><span>{step.detail}</span></div><small>{typeof step.metadata?.phase === "string" ? step.metadata.phase : undefined}</small></div>) : <div className="run-step pending"><span className="step-node"><Clock3 size={14} /></span><div><strong>等待 Worker 领取</strong><span>运行已经持久化，服务重启后仍会继续处理。</span></div></div>}</div>
          {run.status === "waiting" && <div className="approval-box"><div className="approval-head"><div><h3>待确认变更</h3><p>勾选的项目会执行；未勾选的项目会明确拒绝。</p></div><span>版本保护 · 可审计</span></div>{run.changes.filter((change) => change.status === "pending").map((change) => <div className="change-row" key={change.id}><label className="change-toggle"><input aria-label={`选择${changeTitle(change, run)}`} type="checkbox" checked={selectedChanges.includes(change.id)} onChange={(event) => setSelectedChanges((items) => event.target.checked ? [...items, change.id] : items.filter((id) => id !== change.id))} /></label><span><strong>{changeTitle(change, run)}</strong>{change.previewBefore !== undefined && <span className="diff"><del>{previewText(change.previewBefore)}</del></span>}<span className="change-preview"><b>建议值</b>{previewText(change.previewAfter)}</span><small>{change.reason}</small></span></div>)}
            {sourcesOpen && <div className="agent-sources">{run.sourceRefs.map((ref, index) => <span key={ref.id}><b>{index + 1}</b>{ref.labelSnapshot}</span>)}</div>}
            <div className="approval-actions"><button className="text-button" type="button" onClick={() => setSourcesOpen((value) => !value)}>{sourcesOpen ? "收起依据" : `查看 ${run.sourceRefs.length} 条引用依据`}</button><div><button className="button secondary" type="button" disabled={busy === run.id} onClick={() => void resolveChanges([])}>全部拒绝</button><button className="button primary" type="button" disabled={!selectedChanges.length || busy === run.id} onClick={() => void resolveChanges(selectedChanges)}>确认并执行 {selectedChanges.length} 项</button></div></div></div>}
          {(run.status === "completed" || run.status === "stopped" || run.status === "failed") && <div className="run-result"><ShieldCheck size={21} /><div><strong>{run.summary ?? run.errorMessage ?? "运行已经结束"}</strong><p>{run.status === "completed" ? "结果、变更与审计记录均已保存在服务端。" : run.status === "failed" ? "没有绕过确认门禁写入业务数据。" : "可以调整范围后重新发起。"}</p>{run.sourceRefs.length ? <div className="result-sources">来源：{run.sourceRefs.map((ref) => ref.labelSnapshot).join("、")}</div> : null}<button className="text-button retry-run" type="button" onClick={() => { setIntent(run.intent); setMode(run.actionMode); setDelegateOpen(true); }}>调整范围后再运行</button></div></div>}</>
        : <div className="empty-state page-empty"><Bot size={28} /><strong>还没有 Agent 委托</strong><p>从一个结果明确的任务开始。</p><button className="button primary" type="button" onClick={() => setDelegateOpen(true)}>发起委托</button></div>}
    </section>
      <aside className="agent-side"><section className="panel permission-panel"><div className="panel-title"><Lock size={17} /><h2>数据权限</h2></div>{(["goals", "calendar", "records", "privateNotes"] as const).map((key) => { const meta = { goals: ["目标与任务", "允许读取全部"], calendar: ["日程", "只读未来 30 天"], records: ["记录", "只读最近 14 天"], privateNotes: ["私人笔记", "按本次授权读取"] }[key]; const value = data.settings.permissions[key]; return <div className="permission-row" key={key}><div><strong>{meta[0]}</strong><span>{meta[1]}</span></div><button className={`switch ${value ? "on" : ""}`} type="button" role="switch" aria-checked={value} onClick={() => { dispatch({ type: "set-permission", key, value: !value }); toast(`${meta[0]}权限已${value ? "关闭" : "开启"}，仅影响之后发起的运行`); }}><span /></button></div>; })}<div className="write-policy"><ShieldCheck size={15} /><span><strong>写入策略：始终确认</strong><small>Agent 只能生成受控变更草稿。</small></span></div></section>
        <section className="panel run-history"><div className="panel-title"><Clock3 size={17} /><h2>运行历史</h2></div>{runs.slice(0, 5).map((item) => <button key={item.id} className={item.id === run?.id ? "active" : ""} type="button" onClick={() => setSelectedRunId(item.id)}><span><strong>{item.intent}</strong><small>{formatDateTime(item.startedAt ?? item.createdAt)} · {statusLabel[item.status]}</small></span><ChevronRight size={15} /></button>)}</section>
        <section className="panel audit-panel"><div className="panel-title"><FileClock size={17} /><h2>操作记录</h2></div>{audits.map((item) => { const version = auditEntityVersion(item); return <div className="audit-item" key={item.id}><span><strong>{auditActionLabel[item.action] ?? item.action}</strong><small>{formatDateTime(item.createdAt)} · {item.actorType === "agent" ? "Agent" : item.actorType === "system" ? "系统" : "由你"}</small></span>{item.undoable && version && <button type="button" disabled={busy === item.id} onClick={() => void undo(item)}><RotateCcw size={14} />撤销</button>}</div>; })}</section></aside>
    </div>
    <DelegateDialog open={delegateOpen} intent={intent} setIntent={setIntent} mode={mode} setMode={setMode} onClose={() => setDelegateOpen(false)} onStart={() => void startRun()} permissions={data.settings.permissions} busy={busy === "create"} />
  </div>;
}

function DelegateDialog({ open, intent, setIntent, mode, setMode, onClose, onStart, permissions, busy }: { open: boolean; intent: string; setIntent(value: string): void; mode: ActionMode; setMode(value: ActionMode): void; onClose(): void; onStart(): void; permissions: Permissions; busy: boolean }) {
  return <Modal open={open} title="发起 Agent 委托" description="先检查它准备读取什么，再开始运行。" onClose={onClose} footer={<><button className="button secondary" type="button" onClick={onClose}>取消</button><button className="button primary" type="button" onClick={onStart} disabled={!intent.trim() || busy}><Play size={16} />{busy ? "正在提交" : "生成执行步骤"}</button></>}><label className="form-field"><span>希望得到什么结果</span><textarea value={intent} onChange={(event) => setIntent(event.target.value)} placeholder="例如：整理本周未完成事项，优先保证产品方案和跑步目标" /></label><fieldset className="mode-options"><legend>运行模式</legend><label className={mode === "confirm" ? "active" : ""}><input type="radio" name="mode" checked={mode === "confirm"} onChange={() => setMode("confirm")} /><span><Flag size={17} /><strong>生成管理建议</strong><small>变更逐项确认后执行</small></span></label><label className={mode === "read" ? "active" : ""}><input type="radio" name="mode" checked={mode === "read"} onChange={() => setMode("read")} /><span><Target size={17} /><strong>只读分析</strong><small>只返回结论和引用来源</small></span></label></fieldset><div className="scope-preview"><Lock size={15} /><div><strong>本次读取范围</strong><span>{[permissions.goals && "目标与任务", permissions.calendar && "未来 30 天日程", permissions.records && "最近 14 天记录", permissions.privateNotes && "私人笔记"].filter(Boolean).join("、") || "尚未授权任何数据"}</span></div></div></Modal>;
}
