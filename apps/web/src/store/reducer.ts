import { addMinutes } from "date-fns";
import { createId } from "../domain/ids";
import { toIso } from "../domain/dates";
import type {
  AppData,
  CalendarEvent,
  CaptureDraft,
  DataMode,
  DailyReview,
  Goal,
  Area,
  Note,
  RecordEntry,
  Task,
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
  | { type: "set-permission"; key: keyof AppData["settings"]["permissions"]; value: boolean };

function createEntityFromDraft(data: AppData, draft: CaptureDraft, sourceRecordId: string): { data: AppData; parsedEntityId?: string } {
  const now = new Date().toISOString();
  if (draft.kind === "task") {
    const id = createId("task");
    const task: Task = { id, title: draft.title, status: "todo", priority: "normal", estimateMinutes: draft.estimateMinutes ?? 30, scheduledStart: draft.startAt, scheduledEnd: draft.endAt, goalId: draft.goalId, sourceRecordId, version: 0, createdAt: now, updatedAt: now };
    return { data: { ...data, tasks: [task, ...data.tasks] }, parsedEntityId: id };
  }
  if (draft.kind === "event") {
    const id = createId("event");
    const startAt = draft.startAt ?? toIso(addMinutes(new Date(), 60));
    const event: CalendarEvent = { id, title: draft.title, startAt, endAt: draft.endAt ?? toIso(addMinutes(new Date(startAt), 45)), reminderMinutes: [10], timezone: Intl.DateTimeFormat().resolvedOptions().timeZone ?? "UTC", kind: "personal", goalId: draft.goalId, version: 0, createdAt: now, updatedAt: now };
    return { data: { ...data, events: [event, ...data.events] }, parsedEntityId: id };
  }
  if (draft.kind === "note") {
    const id = createId("note");
    const note: Note = { id, title: draft.title.slice(0, 48), bodyMarkdown: draft.rawText, tags: [], category: "其他", linkedEntityIds: [sourceRecordId, ...(draft.goalId ? [draft.goalId] : [])], version: 0, createdAt: now, updatedAt: now };
    return { data: { ...data, notes: [note, ...data.notes] }, parsedEntityId: id };
  }
  if (draft.kind === "goal") {
    const id = createId("goal");
    const goal: Goal = { id, title: draft.title, why: "由快速记录创建，等待补充为什么重要。", area: "生活", metricType: "project", targetValue: 100, currentValue: 0, unit: "%", startAt: now, status: "active", health: "normal", milestones: [], version: 0, createdAt: now, updatedAt: now };
    return { data: { ...data, goals: [goal, ...data.goals] }, parsedEntityId: id };
  }
  return { data };
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
      return { ...state, tasks: state.tasks.map((item) => item.id === task.id ? updated : item) };
    }
    case "add-task": return { ...state, tasks: [action.task, ...state.tasks] };
    case "update-task": return { ...state, tasks: state.tasks.map((item) => item.id === action.task.id ? action.task : item) };
    case "delete-task": {
      const before = state.tasks.find((item) => item.id === action.id);
      if (!before) return state;
      const noteChanges = unlinkNotes(state.notes, action.id);
      return { ...state, tasks: state.tasks.filter((item) => item.id !== action.id), notes: noteChanges.notes };
    }
    case "add-event": return { ...state, events: [action.event, ...state.events] };
    case "update-event": return { ...state, events: state.events.map((item) => item.id === action.event.id ? action.event : item) };
    case "delete-event": {
      const before = state.events.find((item) => item.id === action.id);
      if (!before) return state;
      const noteChanges = unlinkNotes(state.notes, action.id);
      return { ...state, events: state.events.filter((item) => item.id !== action.id), notes: noteChanges.notes };
    }
    case "add-goal": return { ...state, goals: [action.goal, ...state.goals] };
    case "update-goal": return { ...state, goals: state.goals.map((item) => item.id === action.goal.id ? action.goal : item) };
    case "delete-goal": {
      const before = state.goals.find((item) => item.id === action.id);
      if (!before) return state;
      return { ...state, goals: state.goals.filter((item) => item.id !== action.id), tasks: state.tasks.map((item) => item.goalId === action.id ? { ...item, goalId: undefined } : item), events: state.events.map((item) => item.goalId === action.id ? { ...item, goalId: undefined } : item), notes: state.notes.map((item) => item.linkedEntityIds.includes(action.id) ? { ...item, linkedEntityIds: item.linkedEntityIds.filter((id) => id !== action.id), updatedAt: new Date().toISOString() } : item) };
    }
    case "add-record": return { ...state, records: [action.record, ...state.records] };
    case "update-record": {
      const before = state.records.find((item) => item.id === action.record.id);
      if (!before) return state;
      return { ...state, records: state.records.map((item) => item.id === action.record.id ? action.record : item) };
    }
    case "delete-record": {
      const before = state.records.find((item) => item.id === action.id);
      if (!before) return state;
      const noteChanges = unlinkNotes(state.notes, action.id);
      return { ...state, records: state.records.filter((item) => item.id !== action.id), notes: noteChanges.notes };
    }
    case "archive-record": {
      const before = state.records.find((item) => item.id === action.id);
      if (!before) return state;
      const updated = { ...before, archivedAt: new Date().toISOString() };
      return { ...state, records: state.records.map((item) => item.id === action.id ? updated : item) };
    }
    case "add-note": return { ...state, notes: [action.note, ...state.notes] };
    case "update-note": return { ...state, notes: state.notes.map((item) => item.id === action.note.id ? action.note : item) };
    case "delete-note": {
      const before = state.notes.find((item) => item.id === action.id);
      if (!before) return state;
      const remaining = state.notes.filter((item) => item.id !== action.id);
      const noteChanges = unlinkNotes(remaining, action.id);
      return { ...state, notes: noteChanges.notes };
    }
    case "save-capture": {
      const { draft } = action;
      const recordId = createId("record");
      const created = createEntityFromDraft(state, draft, recordId);
      const now = new Date().toISOString();
      const record: RecordEntry = { id: recordId, rawText: draft.rawText, kind: draft.kind === "record" ? draft.recordKind ?? "idea" : "inbox", occurredAt: draft.occurredAt, mood: draft.mood, energy: draft.energy, tags: draft.kind === "record" ? ["快速记录"] : ["已整理", draft.kind], parsedEntityId: created.parsedEntityId, version: 0, createdAt: now, updatedAt: now };
      return { ...created.data, records: [record, ...created.data.records] };
    }
    case "accept-record": {
      const before = state.records.find((item) => item.id === action.recordId);
      if (!before || before.parsedEntityId) return state;
      const created = createEntityFromDraft(state, action.draft, before.id);
      if (!created.parsedEntityId) return state;
      const updated: RecordEntry = { ...before, parsedEntityId: created.parsedEntityId, tags: [...new Set([...before.tags.filter((tag) => tag !== "待确认"), "已整理", action.draft.kind])] };
      return { ...created.data, records: created.data.records.map((item) => item.id === before.id ? updated : item) };
    }
    case "save-review": {
      const exists = state.reviews.some((item) => item.date === action.review.date);
      return { ...state, reviews: exists ? state.reviews.map((item) => item.date === action.review.date ? action.review : item) : [action.review, ...state.reviews] };
    }
    case "complete-onboarding": {
      const firstGoal = action.goals[0];
      const start = addMinutes(new Date(), 30);
      start.setMinutes(start.getMinutes() <= 30 ? 30 : 0, 0, 0);
      if (start.getMinutes() === 0) start.setHours(start.getHours() + 1);
      const now = new Date().toISOString();
      const firstTask: Task | undefined = firstGoal ? { id: createId("task"), title: `推进：${firstGoal.title}`, status: "todo", priority: "important", estimateMinutes: 45, scheduledStart: start.toISOString(), scheduledEnd: addMinutes(start, 45).toISOString(), goalId: firstGoal.id, version: 0, createdAt: now, updatedAt: now } : undefined;
      return { ...state, goals: [...action.goals, ...state.goals], tasks: firstTask ? [firstTask, ...state.tasks] : state.tasks, settings: { ...state.settings, onboardingCompleted: true, focusAreas: action.focusAreas, dataMode: action.dataMode, localOnly: action.dataMode === "local" } };
    }
    case "set-energy": return { ...state, settings: { ...state.settings, energy: action.value } };
    case "set-ai": return { ...state, settings: { ...state.settings, aiEnabled: action.value } };
    case "set-data-mode": return { ...state, settings: { ...state.settings, dataMode: action.value, localOnly: action.value === "local" } };
    case "set-reminders": return { ...state, settings: { ...state.settings, remindersEnabled: action.value } };
    case "set-permission": return { ...state, settings: { ...state.settings, permissions: { ...state.settings.permissions, [action.key]: action.value } } };
  }
}
