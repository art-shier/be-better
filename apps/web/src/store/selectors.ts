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
import type { AppData, AppSettings, CalendarEvent, CalendarReminder, DailyReview, Goal, Milestone, Note, RecordEntry, Tag, Task } from "../domain/types";
import { createEmptyData } from "../domain/seed";
import { getCachedEntities } from "../offline/cache";

type CachedSettings = ServerUserSettings & { id: string; createdAt?: string };
type CachedEvent = ServerCalendarEvent & { reminderMinutes?: number[]; reminders?: Array<{ offsetMinutes: number; channel: string }> };
type CachedRecord = ServerRecord & { parsedEntityId?: string };
type CachedNote = ServerNote & { linkedEntityIds?: string[] };

function tagNames(values: Array<ServerTag | string> | undefined, tagsById: Map<string, Tag>, tagsByName: Map<string, Tag>): string[] {
  return (values ?? []).flatMap((value) => {
    const tag = typeof value === "string" ? tagsByName.get(value.toLocaleLowerCase()) : tagsById.get(value.id);
    return tag ? [tag.name] : [];
  });
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

function record(value: CachedRecord, tagsById: Map<string, Tag>, tagsByName: Map<string, Tag>): RecordEntry {
  return { ...value, kind: value.kind as RecordEntry["kind"], tags: tagNames(value.tags, tagsById, tagsByName) };
}

function note(value: CachedNote, tagsById: Map<string, Tag>, tagsByName: Map<string, Tag>): Note {
  return { ...value, category: value.category as Note["category"], tags: tagNames(value.tags, tagsById, tagsByName), linkedEntityIds: value.linkedEntityIds ?? [] };
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
  const [goals, milestones, tasks, events, reminders, records, notes, reviews, tags, userSettings] = await Promise.all([
    getCachedEntities<ServerGoal>(accountId, "goal"),
    getCachedEntities<ServerMilestone>(accountId, "goal_milestone"),
    getCachedEntities<ServerTask>(accountId, "task"),
    getCachedEntities<CachedEvent>(accountId, "calendar_event"),
    getCachedEntities<ServerReminder>(accountId, "calendar_reminder"),
    getCachedEntities<CachedRecord>(accountId, "record"),
    getCachedEntities<CachedNote>(accountId, "note"),
    getCachedEntities<ServerDailyReview>(accountId, "daily_review"),
    getCachedEntities<ServerTag>(accountId, "tag"),
    getCachedEntities<CachedSettings>(accountId, "user_settings"),
  ]);
  const tagsById = new Map(tags.map((tag) => [tag.id, tag]));
  const tagsByName = new Map(tags.map((tag) => [tag.name.toLocaleLowerCase(), tag]));
  return {
    version: 1,
    goals: goals.map((value) => goal(value, milestones)),
    tasks: tasks.map(task),
    events: events.map((value) => event(value, reminders)),
    records: records.map((value) => record(value, tagsById, tagsByName)),
    notes: notes.map((value) => note(value, tagsById, tagsByName)),
    tags,
    reviews: reviews.map(review),
    settings: settings(userSettings[0]),
  };
}
