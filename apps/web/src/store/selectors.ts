import type {
  ServerCalendarEvent,
  ServerDailyReview,
  ServerGoal,
  ServerMilestone,
  ServerNote,
  ServerRecord,
  ServerReminder,
  ServerTag,
  ServerTask,
  ServerUserSettings,
} from "../api/resources";
import type { AppData, AppSettings, CalendarEvent, CalendarReminder, DailyReview, Goal, Milestone, Note, RecordEntry, Task } from "../domain/types";
import { createEmptyData } from "../domain/seed";
import { getCachedEntities } from "../offline/cache";

type CachedSettings = ServerUserSettings & { id: string; createdAt?: string };
type CachedEvent = ServerCalendarEvent & { reminderMinutes?: number[]; reminders?: Array<{ offsetMinutes: number; channel: string }> };
type CachedRecord = ServerRecord & { parsedEntityId?: string };
type CachedNote = ServerNote & { linkedEntityIds?: string[] };

function tagNames(tags?: Array<ServerTag | string>): string[] {
  return (tags ?? []).map((tag) => typeof tag === "string" ? tag : tag.name);
}

function milestone(value: ServerMilestone): Milestone {
  return { ...value };
}

function goal(value: ServerGoal, milestones: ServerMilestone[]): Goal {
  return {
    id: value.id,
    title: value.title,
    why: value.why,
    area: value.area as Goal["area"],
    metricType: value.metricType as Goal["metricType"],
    targetValue: value.targetValue,
    currentValue: value.currentValue,
    unit: value.unit,
    startAt: value.startDate,
    dueAt: value.dueDate,
    status: value.status as Goal["status"],
    health: value.health as Goal["health"],
    milestones: milestones.filter((item) => item.goalId === value.id).sort((left, right) => left.sortOrder - right.sortOrder).map(milestone),
    version: value.version,
    createdAt: value.createdAt,
    updatedAt: value.updatedAt,
    deletedAt: value.deletedAt,
  };
}

function task(value: ServerTask): Task {
  return {
    ...value,
    status: value.status as Task["status"],
    priority: value.priority as Task["priority"],
  };
}

function reminder(value: ServerReminder): CalendarReminder {
  return {
    ...value,
    channel: value.channel as CalendarReminder["channel"],
    status: value.status as CalendarReminder["status"],
  };
}

function event(value: CachedEvent, reminders: ServerReminder[]): CalendarEvent {
  const related = reminders.filter((item) => item.eventId === value.id).map(reminder);
  const embeddedOffsets = value.reminderMinutes ?? value.reminders?.map((item) => item.offsetMinutes) ?? [];
  return {
    ...value,
    kind: value.kind as CalendarEvent["kind"],
    reminderMinutes: related.length ? related.map((item) => item.offsetMinutes) : embeddedOffsets,
    reminders: related,
  };
}

function record(value: CachedRecord): RecordEntry {
  return { ...value, kind: value.kind as RecordEntry["kind"], tags: tagNames(value.tags) };
}

function note(value: CachedNote): Note {
  return { ...value, category: value.category as Note["category"], tags: tagNames(value.tags), linkedEntityIds: value.linkedEntityIds ?? [] };
}

function review(value: ServerDailyReview): DailyReview {
  const { reviewDate, ...rest } = value;
  return { ...rest, date: reviewDate };
}

function settings(value: CachedSettings | undefined): AppSettings {
  const defaults = createEmptyData().settings;
  if (!value) return defaults;
  return {
    ...defaults,
    ...value.settings,
    schemaVersion: value.schemaVersion,
    version: value.version,
    updatedAt: value.updatedAt,
    permissions: { ...defaults.permissions, ...((value.settings.permissions as Partial<AppSettings["permissions"]> | undefined) ?? {}) },
  } as AppSettings;
}

export async function loadCachedAppData(accountId: string): Promise<AppData> {
  const [goals, milestones, tasks, events, reminders, records, notes, reviews, userSettings] = await Promise.all([
    getCachedEntities<ServerGoal>(accountId, "goal"),
    getCachedEntities<ServerMilestone>(accountId, "goal_milestone"),
    getCachedEntities<ServerTask>(accountId, "task"),
    getCachedEntities<CachedEvent>(accountId, "calendar_event"),
    getCachedEntities<ServerReminder>(accountId, "calendar_reminder"),
    getCachedEntities<CachedRecord>(accountId, "record"),
    getCachedEntities<CachedNote>(accountId, "note"),
    getCachedEntities<ServerDailyReview>(accountId, "daily_review"),
    getCachedEntities<CachedSettings>(accountId, "user_settings"),
  ]);
  return {
    version: 1,
    goals: goals.map((value) => goal(value, milestones)),
    tasks: tasks.map(task),
    events: events.map((value) => event(value, reminders)),
    records: records.map(record),
    notes: notes.map(note),
    reviews: reviews.map(review),
    agentRuns: [],
    audit: [],
    settings: settings(userSettings[0]),
  };
}
