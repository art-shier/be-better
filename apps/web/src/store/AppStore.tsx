import { createContext, useCallback, useContext, useEffect, useMemo, useReducer, useRef, useState, type Dispatch, type ReactNode } from "react";
import { addMinutes } from "date-fns";
import { ApiError, getRemoteState, putRemoteState, type RemoteState } from "../api/client";
import { createId } from "../domain/ids";
import { atOffset, toIso } from "../domain/dates";
import { createEmptyData, createSeedData } from "../domain/seed";
import { fingerprintData, guestStorageKeys, migrateLegacyStorage, userStorageKeys, type StorageKeys } from "./storage";
import type {
  AgentChange,
  AgentRun,
  AppData,
  AuditEvent,
  CalendarEvent,
  CaptureDraft,
  DataMode,
  DailyReview,
  Goal,
  Area,
  Note,
  RecordEntry,
  Task,
  UndoAction,
} from "../domain/types";

const SYNC_DEBOUNCE_MS = 500;

export interface StoreIdentity {
  kind: "guest" | "user";
  userId?: string;
}

const guestIdentity: StoreIdentity = { kind: "guest" };

export type SyncStatus = "connecting" | "synced" | "offline" | "conflict" | "local-only";

interface SyncMetadata {
  revision: number;
  fingerprint: string;
  updatedAt: string;
}

type Action =
  | { type: "replace"; data: AppData }
  | { type: "toggle-task"; id: string }
  | { type: "add-task"; task: Task }
  | { type: "update-task"; task: Task }
  | { type: "delete-task"; id: string }
  | { type: "add-event"; event: CalendarEvent }
  | { type: "update-event"; event: CalendarEvent }
  | { type: "delete-event"; id: string }
  | { type: "add-goal"; goal: Goal }
  | { type: "update-goal"; goal: Goal }
  | { type: "delete-goal"; id: string }
  | { type: "add-record"; record: RecordEntry }
  | { type: "update-record"; record: RecordEntry }
  | { type: "delete-record"; id: string }
  | { type: "archive-record"; id: string }
  | { type: "add-note"; note: Note }
  | { type: "update-note"; note: Note }
  | { type: "delete-note"; id: string }
  | { type: "save-capture"; draft: CaptureDraft }
  | { type: "accept-record"; recordId: string; draft: CaptureDraft }
  | { type: "save-review"; review: DailyReview }
  | { type: "complete-onboarding"; goals: Goal[]; focusAreas: Area[]; dataMode: DataMode }
  | { type: "set-energy"; value: number }
  | { type: "set-ai"; value: boolean }
  | { type: "set-data-mode"; value: DataMode }
  | { type: "set-reminders"; value: boolean }
  | { type: "set-permission"; key: keyof AppData["settings"]["permissions"]; value: boolean }
  | { type: "edit-agent-change"; runId: string; changeId: string; after: string }
  | { type: "start-agent"; run: AgentRun }
  | { type: "advance-agent"; id: string }
  | { type: "stop-agent"; id: string }
  | { type: "approve-agent"; id: string; changeIds: string[] }
  | { type: "reject-agent"; id: string }
  | { type: "undo"; auditId: string };

function audit(action: string, entityRefs: string[], actor: AuditEvent["actor"] = "user", undo?: UndoAction, before?: string, after?: string): AuditEvent {
  return { id: createId("audit"), actor, action, entityRefs, before, after, createdAt: new Date().toISOString(), undo };
}

function withAudit(state: AppData, event: AuditEvent, patch: Partial<AppData>): AppData {
  return { ...state, ...patch, audit: [event, ...state.audit].slice(0, 200) };
}

function updateRun(state: AppData, id: string, updater: (run: AgentRun) => AgentRun): AppData {
  return { ...state, agentRuns: state.agentRuns.map((run) => (run.id === id ? updater(run) : run)) };
}

function applyUndo(state: AppData, undo: UndoAction): AppData {
  switch (undo.type) {
    case "batch": return undo.actions.reduce((current, item) => applyUndo(current, item), state);
    case "restore-task": return { ...state, tasks: state.tasks.some((item) => item.id === undo.task.id) ? state.tasks.map((item) => item.id === undo.task.id ? undo.task : item) : [undo.task, ...state.tasks] };
    case "delete-task": return { ...state, tasks: state.tasks.filter((item) => item.id !== undo.taskId) };
    case "restore-event": return { ...state, events: state.events.some((item) => item.id === undo.event.id) ? state.events.map((item) => item.id === undo.event.id ? undo.event : item) : [undo.event, ...state.events] };
    case "delete-event": return { ...state, events: state.events.filter((item) => item.id !== undo.eventId) };
    case "restore-record": return { ...state, records: state.records.some((item) => item.id === undo.record.id) ? state.records.map((item) => item.id === undo.record.id ? undo.record : item) : [undo.record, ...state.records] };
    case "delete-record": return { ...state, records: state.records.filter((item) => item.id !== undo.recordId) };
    case "restore-note": return { ...state, notes: state.notes.some((item) => item.id === undo.note.id) ? state.notes.map((item) => item.id === undo.note.id ? undo.note : item) : [undo.note, ...state.notes] };
    case "delete-note": return { ...state, notes: state.notes.filter((item) => item.id !== undo.noteId) };
    case "restore-goal": return { ...state, goals: state.goals.some((item) => item.id === undo.goal.id) ? state.goals.map((item) => item.id === undo.goal.id ? undo.goal : item) : [undo.goal, ...state.goals] };
    case "delete-goal": return { ...state, goals: state.goals.filter((item) => item.id !== undo.goalId) };
  }
}

function createEntityFromDraft(data: AppData, draft: CaptureDraft, sourceRecordId: string): { data: AppData; parsedEntityId?: string; undo?: UndoAction } {
  const now = new Date().toISOString();
  if (draft.kind === "task") {
    const id = createId("task");
    const task: Task = { id, title: draft.title, status: "todo", priority: "normal", estimateMinutes: draft.estimateMinutes ?? 30, scheduledStart: draft.startAt, scheduledEnd: draft.endAt, goalId: draft.goalId, sourceRecordId, createdAt: now };
    return { data: { ...data, tasks: [task, ...data.tasks] }, parsedEntityId: id, undo: { type: "delete-task", taskId: id } };
  }
  if (draft.kind === "event") {
    const id = createId("event");
    const startAt = draft.startAt ?? toIso(addMinutes(new Date(), 60));
    const event: CalendarEvent = { id, title: draft.title, startAt, endAt: draft.endAt ?? toIso(addMinutes(new Date(startAt), 45)), reminderMinutes: [10], kind: "personal", goalId: draft.goalId, createdAt: now };
    return { data: { ...data, events: [event, ...data.events] }, parsedEntityId: id, undo: { type: "delete-event", eventId: id } };
  }
  if (draft.kind === "note") {
    const id = createId("note");
    const note: Note = { id, title: draft.title.slice(0, 48), bodyMarkdown: draft.rawText, tags: [], category: "其他", linkedEntityIds: [sourceRecordId, ...(draft.goalId ? [draft.goalId] : [])], createdAt: now, updatedAt: now };
    return { data: { ...data, notes: [note, ...data.notes] }, parsedEntityId: id, undo: { type: "delete-note", noteId: id } };
  }
  if (draft.kind === "goal") {
    const id = createId("goal");
    const goal: Goal = { id, title: draft.title, why: "由快速记录创建，等待补充为什么重要。", area: "生活", metricType: "project", targetValue: 100, currentValue: 0, unit: "%", startAt: now, status: "active", health: "normal", milestones: [], createdAt: now, updatedAt: now };
    return { data: { ...data, goals: [goal, ...data.goals] }, parsedEntityId: id, undo: { type: "delete-goal", goalId: id } };
  }
  return { data };
}

function undoFor(actions: Array<UndoAction | undefined>): UndoAction | undefined {
  const valid = actions.filter((item): item is UndoAction => Boolean(item));
  if (!valid.length) return undefined;
  return valid.length === 1 ? valid[0] : { type: "batch", actions: valid };
}

function changeWindow(after: string, fallbackMinutes: number): { start: Date; end: Date } {
  const matches = [...after.matchAll(/(\d{1,2}):(\d{2})/g)];
  const dayOffset = after.includes("明天") ? 1 : 0;
  const first = matches[0];
  const start = first ? atOffset(dayOffset, Number(first[1]), Number(first[2])) : atOffset(dayOffset, 9);
  const second = matches[1];
  const end = second ? atOffset(dayOffset, Number(second[1]), Number(second[2])) : addMinutes(start, fallbackMinutes);
  return { start, end: end > start ? end : addMinutes(start, fallbackMinutes) };
}

function applyAgentChange(data: AppData, change: AgentChange): AppData {
  if (change.type === "create-task") {
    const quotedTitle = change.title.match(/[“「\"]([^”」\"]+)[”」\"]/)?.[1];
    const taskTitle = quotedTitle ?? (change.title.replace(/^创建/, "").replace(/(?:专注)?任务$/, "").trim() || "Agent 建议任务");
    const goalId = data.goals.some((goal) => goal.id === change.entityId) ? change.entityId : change.sourceRefs.find((ref) => ref.kind === "goal" && data.goals.some((goal) => goal.id === ref.id))?.id;
    const existing = data.tasks.find((task) => task.title === taskTitle || task.title.includes(taskTitle) || taskTitle.includes(task.title));
    if (existing) {
      const before = existing;
      const window = changeWindow(change.after, 70);
      const updated: Task = { ...existing, status: "doing", estimateMinutes: Math.max(5, Math.round((window.end.getTime() - window.start.getTime()) / 60_000)), scheduledStart: toIso(window.start), scheduledEnd: toIso(window.end), goalId: goalId ?? existing.goalId };
      return withAudit(data, audit(`Agent 更新“${existing.title}”`, [existing.id], "agent", { type: "restore-task", task: before }, JSON.stringify(before), JSON.stringify(updated)), { tasks: data.tasks.map((task) => task.id === updated.id ? updated : task) });
    }
    const window = changeWindow(change.after, 70);
    const task: Task = { id: createId("task"), title: taskTitle, status: "todo", priority: "important", estimateMinutes: Math.max(5, Math.round((window.end.getTime() - window.start.getTime()) / 60_000)), scheduledStart: toIso(window.start), scheduledEnd: toIso(window.end), goalId, createdAt: new Date().toISOString() };
    return withAudit(data, audit(`Agent 创建“${task.title}”`, [task.id], "agent", { type: "delete-task", taskId: task.id }, undefined, JSON.stringify(task)), { tasks: [task, ...data.tasks] });
  }

  if (change.type === "reschedule-task") {
    const taskTarget = data.tasks.find((task) => task.id === change.entityId || change.sourceRefs.some((ref) => ref.kind === "task" && ref.id === task.id));
    if (taskTarget) {
      const window = changeWindow(change.after, taskTarget.estimateMinutes);
      const updated: Task = { ...taskTarget, estimateMinutes: Math.max(5, Math.round((window.end.getTime() - window.start.getTime()) / 60_000)), scheduledStart: toIso(window.start), scheduledEnd: toIso(window.end) };
      return withAudit(data, audit(`Agent 调整“${taskTarget.title}”时间`, [taskTarget.id], "agent", { type: "restore-task", task: taskTarget }, JSON.stringify(taskTarget), JSON.stringify(updated)), { tasks: data.tasks.map((task) => task.id === updated.id ? updated : task) });
    }
    const eventTarget = data.events.find((event) => event.id === change.entityId || change.sourceRefs.some((ref) => ref.kind === "event" && ref.id === event.id));
    if (!eventTarget) return data;
    const duration = new Date(eventTarget.endAt).getTime() - new Date(eventTarget.startAt).getTime();
    const start = changeWindow(change.after, Math.round(duration / 60_000)).start;
    const updated = { ...eventTarget, startAt: toIso(start), endAt: toIso(new Date(start.getTime() + duration)) };
    return withAudit(data, audit(`Agent 调整“${eventTarget.title}”时间`, [eventTarget.id], "agent", { type: "restore-event", event: eventTarget }, JSON.stringify(eventTarget), JSON.stringify(updated)), { events: data.events.map((event) => event.id === eventTarget.id ? updated : event) });
  }

  if (change.type === "archive-record" && change.entityId) {
    const target = data.records.find((record) => record.id === change.entityId);
    if (!target) return data;
    const updated = { ...target, archivedAt: new Date().toISOString() };
    return withAudit(data, audit("Agent 归档记录", [target.id], "agent", { type: "restore-record", record: target }), { records: data.records.map((record) => record.id === target.id ? updated : record) });
  }

  return data;
}

export function appReducer(state: AppData, action: Action): AppData {
  switch (action.type) {
    case "replace": return action.data;
    case "toggle-task": {
      const task = state.tasks.find((item) => item.id === action.id);
      if (!task) return state;
      const done = task.status === "done";
      const updated: Task = { ...task, status: done ? "todo" : "done", completedAt: done ? undefined : new Date().toISOString() };
      return withAudit(state, audit(done ? "恢复任务" : "完成任务", [task.id], "user", { type: "restore-task", task }), { tasks: state.tasks.map((item) => item.id === task.id ? updated : item) });
    }
    case "add-task": return withAudit(state, audit("创建任务", [action.task.id], "user", { type: "delete-task", taskId: action.task.id }), { tasks: [action.task, ...state.tasks] });
    case "update-task": {
      const before = state.tasks.find((item) => item.id === action.task.id);
      return withAudit(state, audit("更新任务", [action.task.id], "user", before ? { type: "restore-task", task: before } : undefined), { tasks: state.tasks.map((item) => item.id === action.task.id ? action.task : item) });
    }
    case "delete-task": {
      const before = state.tasks.find((item) => item.id === action.id);
      return withAudit(state, audit("删除任务", [action.id], "user", before ? { type: "restore-task", task: before } : undefined), { tasks: state.tasks.filter((item) => item.id !== action.id) });
    }
    case "add-event": return withAudit(state, audit("创建日程", [action.event.id], "user", { type: "delete-event", eventId: action.event.id }), { events: [action.event, ...state.events] });
    case "update-event": {
      const before = state.events.find((item) => item.id === action.event.id);
      return withAudit(state, audit("更新日程", [action.event.id], "user", before ? { type: "restore-event", event: before } : undefined), { events: state.events.map((item) => item.id === action.event.id ? action.event : item) });
    }
    case "delete-event": {
      const before = state.events.find((item) => item.id === action.id);
      return withAudit(state, audit("删除日程", [action.id], "user", before ? { type: "restore-event", event: before } : undefined), { events: state.events.filter((item) => item.id !== action.id) });
    }
    case "add-goal": return withAudit(state, audit("创建目标", [action.goal.id], "user", { type: "delete-goal", goalId: action.goal.id }), { goals: [action.goal, ...state.goals] });
    case "update-goal": {
      const before = state.goals.find((item) => item.id === action.goal.id);
      return withAudit(state, audit("更新目标", [action.goal.id], "user", before ? { type: "restore-goal", goal: before } : undefined), { goals: state.goals.map((item) => item.id === action.goal.id ? action.goal : item) });
    }
    case "delete-goal": {
      const before = state.goals.find((item) => item.id === action.id);
      if (!before) return state;
      const linkedTasks = state.tasks.filter((item) => item.goalId === action.id);
      const linkedEvents = state.events.filter((item) => item.goalId === action.id);
      const linkedNotes = state.notes.filter((item) => item.linkedEntityIds.includes(action.id));
      const undo = undoFor([{ type: "restore-goal", goal: before }, ...linkedTasks.map((task): UndoAction => ({ type: "restore-task", task })), ...linkedEvents.map((event): UndoAction => ({ type: "restore-event", event })), ...linkedNotes.map((note): UndoAction => ({ type: "restore-note", note }))]);
      return withAudit(state, audit("删除目标", [action.id, ...linkedTasks.map((item) => item.id), ...linkedEvents.map((item) => item.id), ...linkedNotes.map((item) => item.id)], "user", undo), { goals: state.goals.filter((item) => item.id !== action.id), tasks: state.tasks.map((item) => item.goalId === action.id ? { ...item, goalId: undefined } : item), events: state.events.map((item) => item.goalId === action.id ? { ...item, goalId: undefined } : item), notes: state.notes.map((item) => item.linkedEntityIds.includes(action.id) ? { ...item, linkedEntityIds: item.linkedEntityIds.filter((id) => id !== action.id), updatedAt: new Date().toISOString() } : item) });
    }
    case "add-record": return withAudit(state, audit("创建记录", [action.record.id], "user", { type: "delete-record", recordId: action.record.id }), { records: [action.record, ...state.records] });
    case "update-record": {
      const before = state.records.find((item) => item.id === action.record.id);
      if (!before) return state;
      return withAudit(state, audit("更新记录", [action.record.id], "user", { type: "restore-record", record: before }), { records: state.records.map((item) => item.id === action.record.id ? action.record : item) });
    }
    case "delete-record": {
      const before = state.records.find((item) => item.id === action.id);
      if (!before) return state;
      return withAudit(state, audit("删除记录", [action.id], "user", { type: "restore-record", record: before }), { records: state.records.filter((item) => item.id !== action.id) });
    }
    case "archive-record": {
      const before = state.records.find((item) => item.id === action.id);
      if (!before) return state;
      const updated = { ...before, archivedAt: new Date().toISOString() };
      return withAudit(state, audit("归档记录", [action.id], "user", { type: "restore-record", record: before }), { records: state.records.map((item) => item.id === action.id ? updated : item) });
    }
    case "add-note": return withAudit(state, audit("创建笔记", [action.note.id], "user", { type: "delete-note", noteId: action.note.id }), { notes: [action.note, ...state.notes] });
    case "update-note": {
      const before = state.notes.find((item) => item.id === action.note.id);
      return withAudit(state, audit("更新笔记", [action.note.id], "user", before ? { type: "restore-note", note: before } : undefined), { notes: state.notes.map((item) => item.id === action.note.id ? action.note : item) });
    }
    case "delete-note": {
      const before = state.notes.find((item) => item.id === action.id);
      return withAudit(state, audit("删除笔记", [action.id], "user", before ? { type: "restore-note", note: before } : undefined), { notes: state.notes.filter((item) => item.id !== action.id) });
    }
    case "save-capture": {
      const { draft } = action;
      const recordId = createId("record");
      const created = createEntityFromDraft(state, draft, recordId);
      const record: RecordEntry = { id: recordId, rawText: draft.rawText, kind: draft.kind === "record" ? draft.recordKind ?? "idea" : "inbox", occurredAt: draft.occurredAt, mood: draft.mood, energy: draft.energy, tags: draft.kind === "record" ? ["快速记录"] : ["已整理", draft.kind], parsedEntityId: created.parsedEntityId };
      const undo = undoFor([{ type: "delete-record", recordId }, created.undo]);
      return withAudit(created.data, audit(`快速记录并创建${draft.kind}`, [recordId, ...(created.parsedEntityId ? [created.parsedEntityId] : [])], "user", undo), { records: [record, ...created.data.records] });
    }
    case "accept-record": {
      const before = state.records.find((item) => item.id === action.recordId);
      if (!before || before.parsedEntityId) return state;
      const created = createEntityFromDraft(state, action.draft, before.id);
      if (!created.parsedEntityId) return state;
      const updated: RecordEntry = { ...before, parsedEntityId: created.parsedEntityId, tags: [...new Set([...before.tags.filter((tag) => tag !== "待确认"), "已整理", action.draft.kind])] };
      const undo = undoFor([{ type: "restore-record", record: before }, created.undo]);
      return withAudit(created.data, audit(`整理记录为${action.draft.kind}`, [before.id, created.parsedEntityId], "user", undo), { records: created.data.records.map((item) => item.id === before.id ? updated : item) });
    }
    case "save-review": {
      const exists = state.reviews.some((item) => item.date === action.review.date);
      return withAudit(state, audit(exists ? "更新晚间复盘" : "完成晚间复盘", [action.review.id]), { reviews: exists ? state.reviews.map((item) => item.date === action.review.date ? action.review : item) : [action.review, ...state.reviews] });
    }
    case "complete-onboarding": {
      const firstGoal = action.goals[0];
      const start = addMinutes(new Date(), 30);
      start.setMinutes(start.getMinutes() <= 30 ? 30 : 0, 0, 0);
      if (start.getMinutes() === 0) start.setHours(start.getHours() + 1);
      const firstTask: Task | undefined = firstGoal ? { id: createId("task"), title: `推进：${firstGoal.title}`, status: "todo", priority: "important", estimateMinutes: 45, scheduledStart: start.toISOString(), scheduledEnd: addMinutes(start, 45).toISOString(), goalId: firstGoal.id, createdAt: new Date().toISOString() } : undefined;
      const next = { ...state, goals: [...action.goals, ...state.goals], tasks: firstTask ? [firstTask, ...state.tasks] : state.tasks, settings: { ...state.settings, onboardingCompleted: true, focusAreas: action.focusAreas, dataMode: action.dataMode, localOnly: action.dataMode === "local" } };
      return withAudit(next, audit("完成首次设置并生成今日行动", [...action.goals.map((goal) => goal.id), ...(firstTask ? [firstTask.id] : [])]), {});
    }
    case "set-energy": return { ...state, settings: { ...state.settings, energy: action.value } };
    case "set-ai": return { ...state, settings: { ...state.settings, aiEnabled: action.value } };
    case "set-data-mode": return { ...state, settings: { ...state.settings, dataMode: action.value, localOnly: action.value === "local" } };
    case "set-reminders": return { ...state, settings: { ...state.settings, remindersEnabled: action.value } };
    case "set-permission": {
      const settings = { ...state.settings, permissions: { ...state.settings.permissions, [action.key]: action.value } };
      if (action.value) return { ...state, settings };
      const scopePattern = { goals: /目标|任务/, calendar: /日程/, records: /记录/, privateNotes: /笔记/ }[action.key];
      const affected: string[] = [];
      const agentRuns = state.agentRuns.map((run) => {
        const active = ["ready", "reading", "analyzing", "waiting", "applying"].includes(run.status);
        if (!active || !run.scope.some((item) => scopePattern.test(item))) return run;
        affected.push(run.id);
        return { ...run, status: "stopped" as const, finishedAt: new Date().toISOString(), summary: "读取权限已撤回，运行立即停止；没有执行新的写入。", steps: run.steps.map((step) => step.status === "running" ? { ...step, status: "pending" as const } : step) };
      });
      if (!affected.length) return { ...state, settings };
      return withAudit(state, audit("撤回权限并停止 Agent", affected), { settings, agentRuns });
    }
    case "edit-agent-change": return updateRun(state, action.runId, (run) => run.status !== "waiting" ? run : { ...run, changes: run.changes.map((change) => change.id === action.changeId && change.status === "pending" ? { ...change, after: action.after } : change) });
    case "start-agent": return withAudit({ ...state, agentRuns: [action.run, ...state.agentRuns] }, audit("发起 Agent 委托", [action.run.id]), {});
    case "advance-agent": return updateRun(state, action.id, (run) => {
      if (run.status === "reading") return { ...run, status: "analyzing", steps: run.steps.map((step, index) => index === 0 ? { ...step, status: "done" } : index === 1 ? { ...step, status: "running" } : step) };
      if (run.status === "analyzing") {
        const readOnly = run.actionMode === "read";
        return { ...run, status: readOnly ? "completed" : "waiting", finishedAt: readOnly ? new Date().toISOString() : undefined, summary: readOnly ? "只读分析完成，没有修改数据。" : undefined, steps: run.steps.map((step, index) => index < 3 ? { ...step, status: "done" } : step) };
      }
      return run;
    });
    case "stop-agent": return updateRun(state, action.id, (run) => ({ ...run, status: "stopped", finishedAt: new Date().toISOString(), summary: "运行已停止，没有执行新的写入。", steps: run.steps.map((step) => step.status === "running" ? { ...step, status: "pending" } : step) }));
    case "approve-agent": {
      const run = state.agentRuns.find((item) => item.id === action.id);
      if (!run || run.status !== "waiting") return state;
      let next = state;
      const acceptedIds = run.changes.filter((change) => change.status === "pending" && action.changeIds.includes(change.id)).map((change) => change.id);
      run.changes.filter((change) => acceptedIds.includes(change.id)).forEach((change) => { next = applyAgentChange(next, change); });
      const updatedRun: AgentRun = { ...run, status: "completed", finishedAt: new Date().toISOString(), summary: `已执行并核验 ${acceptedIds.length} 项变更。`, changes: run.changes.map((change) => ({ ...change, status: acceptedIds.includes(change.id) ? "accepted" : "rejected" })), steps: run.steps.map((step) => ({ ...step, status: "done" })) };
      return withAudit({ ...next, agentRuns: next.agentRuns.map((item) => item.id === run.id ? updatedRun : item) }, audit("确认 Agent 变更", acceptedIds, "user"), {});
    }
    case "reject-agent": {
      const run = state.agentRuns.find((item) => item.id === action.id);
      if (!run || run.status !== "waiting") return state;
      return withAudit(updateRun(state, action.id, (item) => ({ ...item, status: "completed", finishedAt: new Date().toISOString(), summary: "变更已全部拒绝，没有修改数据。", changes: item.changes.map((change) => ({ ...change, status: "rejected" })) })), audit("拒绝 Agent 变更", [action.id]), {});
    }
    case "undo": {
      const entry = state.audit.find((item) => item.id === action.auditId);
      if (!entry?.undo) return state;
      const restored = applyUndo(state, entry.undo);
      return { ...restored, audit: [audit(`撤销：${entry.action}`, entry.entityRefs), ...restored.audit.filter((item) => item.id !== entry.id)] };
    }
  }
}

function loadInitial(storageKey: string): AppData {
  try {
    const raw = localStorage.getItem(storageKey);
    if (!raw) return createEmptyData();
    const parsed = JSON.parse(raw) as AppData;
    if (parsed.version !== 1 || !Array.isArray(parsed.goals) || !Array.isArray(parsed.tasks)) return createEmptyData();
    return normalizeData(parsed);
  } catch {
    return createEmptyData();
  }
}

function normalizeData(data: AppData): AppData {
  const focusAreas = data.settings.focusAreas ?? [...new Set(data.goals.map((goal) => goal.area))];
  return { ...data, reviews: data.reviews ?? [], agentRuns: data.agentRuns ?? [], audit: data.audit ?? [], settings: { ...data.settings, remindersEnabled: data.settings.remindersEnabled ?? false, onboardingCompleted: data.settings.onboardingCompleted ?? true, focusAreas, dataMode: data.settings.dataMode ?? (data.settings.localOnly ? "local" : "selected") } };
}

function isAppData(value: unknown): value is AppData {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Partial<AppData>;
  return candidate.version === 1
    && Array.isArray(candidate.goals)
    && Array.isArray(candidate.tasks)
    && Array.isArray(candidate.events)
    && Array.isArray(candidate.records)
    && Array.isArray(candidate.notes)
    && Boolean(candidate.settings);
}

function loadSyncMetadata(syncKey: string): SyncMetadata | null {
  try {
    const raw = localStorage.getItem(syncKey);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<SyncMetadata>;
    if (!Number.isInteger(parsed.revision) || (parsed.revision ?? 0) < 1 || typeof parsed.fingerprint !== "string" || typeof parsed.updatedAt !== "string") return null;
    return parsed as SyncMetadata;
  } catch {
    return null;
  }
}

function saveSyncMetadata(syncKey: string, metadata: SyncMetadata): void {
  try {
    localStorage.setItem(syncKey, JSON.stringify(metadata));
  } catch {
    // A full browser storage quota must not break the in-memory application.
  }
}

function saveConflictBackup(conflictKey: string, data: AppData, remoteRevision?: number): void {
  try {
    localStorage.setItem(conflictKey, JSON.stringify({ capturedAt: new Date().toISOString(), remoteRevision, data }));
  } catch {
    // The main local copy is already persisted in the active identity partition.
  }
}

interface StoreContextValue {
  data: AppData;
  dispatch: Dispatch<Action>;
  syncStatus: SyncStatus;
  lastSyncedAt: string | null;
  reset(): void;
  importData(value: string): { ok: boolean; message: string };
  exportData(): string;
}

const StoreContext = createContext<StoreContextValue | null>(null);

export function AppStoreProvider({ children, identity = guestIdentity, syncEnabled = import.meta.env.MODE !== "test", onUnauthorized, onServiceOffline, onServiceOnline }: { children: ReactNode; identity?: StoreIdentity; syncEnabled?: boolean; onUnauthorized?(): void; onServiceOffline?(): void; onServiceOnline?(): void }) {
  migrateLegacyStorage();
  const keys: StorageKeys = identity.kind === "user" && identity.userId ? userStorageKeys(identity.userId) : guestStorageKeys;
  const remoteSyncEnabled = syncEnabled && identity.kind === "user" && Boolean(identity.userId);
  const [data, dispatch] = useReducer(appReducer, keys.data, loadInitial);
  const [syncStatus, setSyncStatus] = useState<SyncStatus>(remoteSyncEnabled ? "connecting" : "local-only");
  const [lastSyncedAt, setLastSyncedAt] = useState<string | null>(() => loadSyncMetadata(keys.sync)?.updatedAt ?? null);
  const dataRef = useRef(data);
  const revisionRef = useRef<number | null>(null);
  const syncReadyRef = useRef(false);
  const connectInFlightRef = useRef(false);
  const connectionSequenceRef = useRef(0);
  const pushInFlightRef = useRef(false);
  const queuedDataRef = useRef<AppData | null>(null);
  const skipPushFingerprintRef = useRef<string | null>(null);
  const pushTimerRef = useRef<number | null>(null);
  const flushQueueRef = useRef<() => void>(() => undefined);
  dataRef.current = data;

  const rememberRemote = useCallback((remote: RemoteState, snapshot: AppData) => {
    onServiceOnline?.();
    revisionRef.current = remote.revision;
    saveSyncMetadata(keys.sync, { revision: remote.revision, fingerprint: fingerprintData(snapshot), updatedAt: remote.updatedAt });
    setLastSyncedAt(remote.updatedAt);
  }, [keys.sync, onServiceOnline]);

  const applyRemote = useCallback((remote: RemoteState, status: SyncStatus = "synced") => {
    if (!isAppData(remote.data)) throw new Error("服务端状态结构不完整");
    const normalized = normalizeData(remote.data);
    queuedDataRef.current = null;
    skipPushFingerprintRef.current = fingerprintData(normalized);
    dataRef.current = normalized;
    rememberRemote(remote, normalized);
    syncReadyRef.current = true;
    setSyncStatus(status);
    dispatch({ type: "replace", data: normalized });
  }, [rememberRemote]);

  const resolveConflict = useCallback(async (localData: AppData, currentRevision?: number, signal?: AbortSignal) => {
    saveConflictBackup(keys.conflict, localData, currentRevision);
    syncReadyRef.current = false;
    const remote = await getRemoteState(signal);
    applyRemote(remote, "conflict");
  }, [applyRemote, keys.conflict]);

  flushQueueRef.current = () => {
    if (!remoteSyncEnabled || !syncReadyRef.current || pushInFlightRef.current || revisionRef.current === null) return;
    const snapshot = queuedDataRef.current;
    if (!snapshot) return;
    queuedDataRef.current = null;
    pushInFlightRef.current = true;
    setSyncStatus("connecting");

    void putRemoteState(snapshot, revisionRef.current)
      .then((remote) => {
        rememberRemote(remote, snapshot);
        setSyncStatus("synced");
        if (fingerprintData(dataRef.current) !== fingerprintData(snapshot)) queuedDataRef.current = dataRef.current;
      })
      .catch(async (error: unknown) => {
        if (error instanceof ApiError && error.status === 401) {
          onUnauthorized?.();
          queuedDataRef.current = dataRef.current;
          syncReadyRef.current = false;
          setSyncStatus("offline");
          return;
        }
        if (error instanceof ApiError && error.status === 409) {
          try {
            await resolveConflict(dataRef.current, error.currentRevision);
            return;
          } catch {
            // The retry GET failed; retain the current local state for the next reconnect.
          }
        }
        queuedDataRef.current = dataRef.current;
        syncReadyRef.current = false;
        onServiceOffline?.();
        setSyncStatus("offline");
      })
      .finally(() => {
        pushInFlightRef.current = false;
        if (queuedDataRef.current && syncReadyRef.current) {
          if (pushTimerRef.current !== null) window.clearTimeout(pushTimerRef.current);
          pushTimerRef.current = window.setTimeout(() => flushQueueRef.current(), SYNC_DEBOUNCE_MS);
        }
      });
  };

  useEffect(() => {
    try {
      localStorage.setItem(keys.data, JSON.stringify(data));
    } catch {
      // Keep the running session usable even if browser storage is full.
    }
    if (!remoteSyncEnabled) {
      setSyncStatus("local-only");
      return;
    }
    if (!syncReadyRef.current) return;

    const fingerprint = fingerprintData(data);
    if (skipPushFingerprintRef.current === fingerprint) {
      skipPushFingerprintRef.current = null;
      return;
    }
    queuedDataRef.current = data;
    if (pushTimerRef.current !== null) window.clearTimeout(pushTimerRef.current);
    pushTimerRef.current = window.setTimeout(() => flushQueueRef.current(), SYNC_DEBOUNCE_MS);
    return () => {
      if (pushTimerRef.current !== null) window.clearTimeout(pushTimerRef.current);
    };
  }, [data, keys.data, remoteSyncEnabled]);

  useEffect(() => {
    if (!remoteSyncEnabled) return;
    let disposed = false;
    let controller: AbortController | null = null;

    const connect = async () => {
      if (disposed || connectInFlightRef.current || pushInFlightRef.current) return;
      const connectionSequence = ++connectionSequenceRef.current;
      connectInFlightRef.current = true;
      syncReadyRef.current = false;
      setSyncStatus("connecting");
      controller?.abort();
      controller = new AbortController();
      const localAtStart = dataRef.current;
      const startFingerprint = fingerprintData(localAtStart);

      try {
        let remote: RemoteState;
        try {
          remote = await getRemoteState(controller.signal);
        } catch (error) {
          if (!(error instanceof ApiError) || error.status !== 404) throw error;
          const snapshot = dataRef.current;
          remote = await putRemoteState(snapshot, 0, controller.signal);
          if (disposed) return;
          rememberRemote(remote, snapshot);
          syncReadyRef.current = true;
          setSyncStatus("synced");
          if (fingerprintData(dataRef.current) !== fingerprintData(snapshot)) {
            queuedDataRef.current = dataRef.current;
            pushTimerRef.current = window.setTimeout(() => flushQueueRef.current(), SYNC_DEBOUNCE_MS);
          }
          return;
        }

        if (disposed) return;
        const metadata = loadSyncMetadata(keys.sync);
        const current = dataRef.current;
        const currentFingerprint = fingerprintData(current);
        const hasPendingLocalChanges = Boolean(metadata && currentFingerprint !== metadata.fingerprint);

        if (metadata && metadata.revision === remote.revision && hasPendingLocalChanges) {
          try {
            const saved = await putRemoteState(current, remote.revision, controller.signal);
            if (disposed) return;
            rememberRemote(saved, current);
            syncReadyRef.current = true;
            setSyncStatus("synced");
            if (fingerprintData(dataRef.current) !== fingerprintData(current)) {
              queuedDataRef.current = dataRef.current;
              pushTimerRef.current = window.setTimeout(() => flushQueueRef.current(), SYNC_DEBOUNCE_MS);
            }
          } catch (error) {
            if (!(error instanceof ApiError) || error.status !== 409) throw error;
            await resolveConflict(current, error.currentRevision, controller.signal);
          }
          return;
        }

        const remoteChangedAlongsideLocal = Boolean(metadata && metadata.revision !== remote.revision && hasPendingLocalChanges);
        const editedDuringFirstConnection = !metadata && currentFingerprint !== startFingerprint;
        if (remoteChangedAlongsideLocal || editedDuringFirstConnection) {
          saveConflictBackup(keys.conflict, current, remote.revision);
          applyRemote(remote, "conflict");
          return;
        }
        applyRemote(remote);
      } catch (error) {
        if (!disposed && !(error instanceof DOMException && error.name === "AbortError")) {
          if (error instanceof ApiError && error.status === 401) onUnauthorized?.();
          else onServiceOffline?.();
          setSyncStatus("offline");
        }
      } finally {
        if (connectionSequenceRef.current === connectionSequence) connectInFlightRef.current = false;
      }
    };

    const reconnect = () => { void connect(); };
    window.addEventListener("online", reconnect);
    void connect();
    return () => {
      disposed = true;
      controller?.abort();
      connectionSequenceRef.current += 1;
      connectInFlightRef.current = false;
      if (pushTimerRef.current !== null) window.clearTimeout(pushTimerRef.current);
      window.removeEventListener("online", reconnect);
    };
  }, [applyRemote, keys.conflict, keys.sync, onServiceOffline, onUnauthorized, rememberRemote, remoteSyncEnabled, resolveConflict]);

  const reset = useCallback(() => dispatch({ type: "replace", data: createSeedData() }), []);
  const importData = useCallback((value: string) => {
    try {
      const parsed = JSON.parse(value) as AppData;
      if (parsed.version !== 1 || !Array.isArray(parsed.goals) || !Array.isArray(parsed.tasks) || !Array.isArray(parsed.events)) throw new Error("文件结构不符合日序数据格式");
      dispatch({ type: "replace", data: normalizeData(parsed) });
      return { ok: true, message: "数据已导入" };
    } catch (error) {
      return { ok: false, message: error instanceof Error ? error.message : "无法读取导入文件" };
    }
  }, []);
  const exportData = useCallback(() => JSON.stringify(data, null, 2), [data]);

  const value = useMemo(() => ({ data, dispatch, syncStatus, lastSyncedAt, reset, importData, exportData }), [data, exportData, importData, lastSyncedAt, reset, syncStatus]);
  return <StoreContext.Provider value={value}>{children}</StoreContext.Provider>;
}

export function useAppStore(): StoreContextValue {
  const context = useContext(StoreContext);
  if (!context) throw new Error("useAppStore must be used inside AppStoreProvider");
  return context;
}

export const STORAGE_KEY = guestStorageKeys.data;
export const SYNC_META_KEY = guestStorageKeys.sync;
export const CONFLICT_KEY = guestStorageKeys.conflict;
