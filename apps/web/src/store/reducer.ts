import { addMinutes } from "date-fns";
import { createId } from "../domain/ids";
import { toIso } from "../domain/dates";
import type {
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

export type Action =
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
  | { type: "undo"; auditId: string };

function audit(action: string, entityRefs: string[], actor: AuditEvent["actor"] = "user", undo?: UndoAction, before?: string, after?: string): AuditEvent {
  return { id: createId("audit"), actor, action, entityRefs, before, after, createdAt: new Date().toISOString(), undo };
}

function withAudit(state: AppData, event: AuditEvent, patch: Partial<AppData>): AppData {
  return { ...state, ...patch, audit: [event, ...state.audit].slice(0, 200) };
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
    const task: Task = { id, title: draft.title, status: "todo", priority: "normal", estimateMinutes: draft.estimateMinutes ?? 30, scheduledStart: draft.startAt, scheduledEnd: draft.endAt, goalId: draft.goalId, sourceRecordId, version: 0, createdAt: now, updatedAt: now };
    return { data: { ...data, tasks: [task, ...data.tasks] }, parsedEntityId: id, undo: { type: "delete-task", taskId: id } };
  }
  if (draft.kind === "event") {
    const id = createId("event");
    const startAt = draft.startAt ?? toIso(addMinutes(new Date(), 60));
    const event: CalendarEvent = { id, title: draft.title, startAt, endAt: draft.endAt ?? toIso(addMinutes(new Date(startAt), 45)), reminderMinutes: [10], timezone: Intl.DateTimeFormat().resolvedOptions().timeZone ?? "UTC", kind: "personal", goalId: draft.goalId, version: 0, createdAt: now, updatedAt: now };
    return { data: { ...data, events: [event, ...data.events] }, parsedEntityId: id, undo: { type: "delete-event", eventId: id } };
  }
  if (draft.kind === "note") {
    const id = createId("note");
    const note: Note = { id, title: draft.title.slice(0, 48), bodyMarkdown: draft.rawText, tags: [], category: "其他", linkedEntityIds: [sourceRecordId, ...(draft.goalId ? [draft.goalId] : [])], version: 0, createdAt: now, updatedAt: now };
    return { data: { ...data, notes: [note, ...data.notes] }, parsedEntityId: id, undo: { type: "delete-note", noteId: id } };
  }
  if (draft.kind === "goal") {
    const id = createId("goal");
    const goal: Goal = { id, title: draft.title, why: "由快速记录创建，等待补充为什么重要。", area: "生活", metricType: "project", targetValue: 100, currentValue: 0, unit: "%", startAt: now, status: "active", health: "normal", milestones: [], version: 0, createdAt: now, updatedAt: now };
    return { data: { ...data, goals: [goal, ...data.goals] }, parsedEntityId: id, undo: { type: "delete-goal", goalId: id } };
  }
  return { data };
}

function undoFor(actions: Array<UndoAction | undefined>): UndoAction | undefined {
  const valid = actions.filter((item): item is UndoAction => Boolean(item));
  if (!valid.length) return undefined;
  return valid.length === 1 ? valid[0] : { type: "batch", actions: valid };
}

function unlinkNotes(notes: Note[], entityId: string): { notes: Note[]; linked: Note[] } {
  const linked = notes.filter((note) => note.id !== entityId && note.linkedEntityIds.includes(entityId));
  if (linked.length === 0) return { notes, linked };
  const updatedAt = new Date().toISOString();
  return {
    linked,
    notes: notes.map((note) => note.id !== entityId && note.linkedEntityIds.includes(entityId)
      ? { ...note, linkedEntityIds: note.linkedEntityIds.filter((id) => id !== entityId), updatedAt }
      : note),
  };
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
      if (!before) return state;
      const noteChanges = unlinkNotes(state.notes, action.id);
      const undo = undoFor([{ type: "restore-task", task: before }, ...noteChanges.linked.map((note): UndoAction => ({ type: "restore-note", note }))]);
      return withAudit(state, audit("删除任务", [action.id, ...noteChanges.linked.map((note) => note.id)], "user", undo), { tasks: state.tasks.filter((item) => item.id !== action.id), notes: noteChanges.notes });
    }
    case "add-event": return withAudit(state, audit("创建日程", [action.event.id], "user", { type: "delete-event", eventId: action.event.id }), { events: [action.event, ...state.events] });
    case "update-event": {
      const before = state.events.find((item) => item.id === action.event.id);
      return withAudit(state, audit("更新日程", [action.event.id], "user", before ? { type: "restore-event", event: before } : undefined), { events: state.events.map((item) => item.id === action.event.id ? action.event : item) });
    }
    case "delete-event": {
      const before = state.events.find((item) => item.id === action.id);
      if (!before) return state;
      const noteChanges = unlinkNotes(state.notes, action.id);
      const undo = undoFor([{ type: "restore-event", event: before }, ...noteChanges.linked.map((note): UndoAction => ({ type: "restore-note", note }))]);
      return withAudit(state, audit("删除日程", [action.id, ...noteChanges.linked.map((note) => note.id)], "user", undo), { events: state.events.filter((item) => item.id !== action.id), notes: noteChanges.notes });
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
      const noteChanges = unlinkNotes(state.notes, action.id);
      const undo = undoFor([{ type: "restore-record", record: before }, ...noteChanges.linked.map((note): UndoAction => ({ type: "restore-note", note }))]);
      return withAudit(state, audit("删除记录", [action.id, ...noteChanges.linked.map((note) => note.id)], "user", undo), { records: state.records.filter((item) => item.id !== action.id), notes: noteChanges.notes });
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
      if (!before) return state;
      const remaining = state.notes.filter((item) => item.id !== action.id);
      const noteChanges = unlinkNotes(remaining, action.id);
      const undo = undoFor([{ type: "restore-note", note: before }, ...noteChanges.linked.map((note): UndoAction => ({ type: "restore-note", note }))]);
      return withAudit(state, audit("删除笔记", [action.id, ...noteChanges.linked.map((note) => note.id)], "user", undo), { notes: noteChanges.notes });
    }
    case "save-capture": {
      const { draft } = action;
      const recordId = createId("record");
      const created = createEntityFromDraft(state, draft, recordId);
      const now = new Date().toISOString();
      const record: RecordEntry = { id: recordId, rawText: draft.rawText, kind: draft.kind === "record" ? draft.recordKind ?? "idea" : "inbox", occurredAt: draft.occurredAt, mood: draft.mood, energy: draft.energy, tags: draft.kind === "record" ? ["快速记录"] : ["已整理", draft.kind], parsedEntityId: created.parsedEntityId, version: 0, createdAt: now, updatedAt: now };
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
      const now = new Date().toISOString();
      const firstTask: Task | undefined = firstGoal ? { id: createId("task"), title: `推进：${firstGoal.title}`, status: "todo", priority: "important", estimateMinutes: 45, scheduledStart: start.toISOString(), scheduledEnd: addMinutes(start, 45).toISOString(), goalId: firstGoal.id, version: 0, createdAt: now, updatedAt: now } : undefined;
      const next = { ...state, goals: [...action.goals, ...state.goals], tasks: firstTask ? [firstTask, ...state.tasks] : state.tasks, settings: { ...state.settings, onboardingCompleted: true, focusAreas: action.focusAreas, dataMode: action.dataMode, localOnly: action.dataMode === "local" } };
      return withAudit(next, audit("完成首次设置并生成今日行动", [...action.goals.map((goal) => goal.id), ...(firstTask ? [firstTask.id] : [])]), {});
    }
    case "set-energy": return { ...state, settings: { ...state.settings, energy: action.value } };
    case "set-ai": return { ...state, settings: { ...state.settings, aiEnabled: action.value } };
    case "set-data-mode": return { ...state, settings: { ...state.settings, dataMode: action.value, localOnly: action.value === "local" } };
    case "set-reminders": return { ...state, settings: { ...state.settings, remindersEnabled: action.value } };
    case "set-permission": return { ...state, settings: { ...state.settings, permissions: { ...state.settings.permissions, [action.key]: action.value } } };
    case "undo": {
      const entry = state.audit.find((item) => item.id === action.auditId);
      if (!entry?.undo) return state;
      const restored = applyUndo(state, entry.undo);
      return { ...restored, audit: [audit(`撤销：${entry.action}`, entry.entityRefs), ...restored.audit.filter((item) => item.id !== entry.id)] };
    }
  }
}
