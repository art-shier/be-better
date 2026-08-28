import type { AppData, AppSettings, CalendarEvent, DailyReview, Goal, Milestone, Note, RecordEntry, Task } from "../domain/types";
import type { CachedEntityType, MutationOperation } from "../offline/db";
import { enqueueMutations } from "../offline/mutations";
import type { Action } from "./reducer";

export interface PreparedMutation {
  entityType: CachedEntityType;
  entityId: string;
  operation: MutationOperation;
  baseVersion: number;
  payload: Record<string, unknown>;
  optimisticEntity?: Record<string, unknown> & { id: string; version: number; updatedAt: string };
}

interface Snapshot {
  entityType: CachedEntityType;
  entityId: string;
  version: number;
  payload: Record<string, unknown>;
  optimisticEntity: PreparedMutation["optimisticEntity"];
}

const resourceOrder: Record<CachedEntityType, number> = {
  goal: 10,
  goal_milestone: 20,
  record: 30,
  task: 40,
  calendar_event: 50,
  calendar_reminder: 55,
  note: 60,
  daily_review: 70,
  tag: 75,
  user_settings: 80,
};

function dateOnly(value?: string): string | undefined {
  return value?.slice(0, 10);
}

function withoutUndefined(value: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(value).filter(([, item]) => item !== undefined));
}

function metadata<T extends { id: string; version: number; createdAt: string; updatedAt: string; deletedAt?: string }>(value: T, data: Record<string, unknown>) {
  return withoutUndefined({ ...data, id: value.id, version: value.version, createdAt: value.createdAt, updatedAt: value.updatedAt, deletedAt: value.deletedAt }) as Record<string, unknown> & { id: string; version: number; updatedAt: string };
}

function goalSnapshot(goal: Goal): Snapshot {
  const payload = withoutUndefined({
    id: goal.id, title: goal.title, why: goal.why, area: goal.area, metricType: goal.metricType,
    targetValue: goal.targetValue, currentValue: goal.currentValue, unit: goal.unit,
    startDate: dateOnly(goal.startAt), dueDate: dateOnly(goal.dueAt), status: goal.status, health: goal.health,
  });
  return { entityType: "goal", entityId: goal.id, version: goal.version, payload, optimisticEntity: metadata(goal, payload) };
}

function milestoneSnapshot(value: Milestone): Snapshot {
  const payload = withoutUndefined({ id: value.id, goalId: value.goalId, title: value.title, dueAt: value.dueAt, completedAt: value.completedAt, sortOrder: value.sortOrder });
  return { entityType: "goal_milestone", entityId: value.id, version: value.version, payload, optimisticEntity: metadata(value, payload) };
}

function taskSnapshot(value: Task): Snapshot {
  const payload = withoutUndefined({
    id: value.id, title: value.title, status: value.status, priority: value.priority, estimateMinutes: value.estimateMinutes,
    dueAt: value.dueAt, scheduledStart: value.scheduledStart, scheduledEnd: value.scheduledEnd,
    goalId: value.goalId, sourceRecordId: value.sourceRecordId,
  });
  return { entityType: "task", entityId: value.id, version: value.version, payload, optimisticEntity: metadata(value, { ...payload, completedAt: value.completedAt }) };
}

function eventSnapshot(value: CalendarEvent): Snapshot {
  const reminders = value.reminderMinutes.map((offsetMinutes) => ({ offsetMinutes, channel: "in_app" }));
  const payload = withoutUndefined({
    id: value.id, title: value.title, startAt: value.startAt, endAt: value.endAt, timezone: value.timezone,
    location: value.location, kind: value.kind, sourceCalendar: value.sourceCalendar, goalId: value.goalId, reminders,
  });
  return { entityType: "calendar_event", entityId: value.id, version: value.version, payload, optimisticEntity: metadata(value, { ...payload, reminderMinutes: value.reminderMinutes }) };
}

function recordSnapshot(value: RecordEntry): Snapshot {
  const payload = withoutUndefined({ id: value.id, rawText: value.rawText, kind: value.kind, occurredAt: value.occurredAt, mood: value.mood, energy: value.energy, archivedAt: value.archivedAt, tags: value.tags });
  return { entityType: "record", entityId: value.id, version: value.version, payload, optimisticEntity: metadata(value, { ...payload, parsedEntityId: value.parsedEntityId }) };
}

function noteSnapshot(value: Note): Snapshot {
  const payload = withoutUndefined({ id: value.id, title: value.title, bodyMarkdown: value.bodyMarkdown, category: value.category, archivedAt: value.archivedAt, tags: value.tags, linkedEntityIds: value.linkedEntityIds });
  return { entityType: "note", entityId: value.id, version: value.version, payload, optimisticEntity: metadata(value, { ...payload, linkedEntityIds: value.linkedEntityIds }) };
}

function reviewSnapshot(value: DailyReview): Snapshot {
  const payload = withoutUndefined({ id: value.id, reviewDate: value.date, wins: value.wins, blockers: value.blockers, mood: value.mood, energy: value.energy, tomorrowFocus: value.tomorrowFocus, aiSummary: value.aiSummary });
  return { entityType: "daily_review", entityId: value.id, version: value.version, payload, optimisticEntity: metadata(value, payload) };
}

function settingsPayload(settings: AppSettings): Record<string, unknown> {
  const { schemaVersion: _schemaVersion, version: _version, updatedAt: _updatedAt, ...payload } = settings;
  return payload;
}

function collect(accountId: string, data: AppData): Map<string, Snapshot> {
  const snapshots: Snapshot[] = [
    ...data.goals.map(goalSnapshot),
    ...data.goals.flatMap((goal) => goal.milestones.map(milestoneSnapshot)),
    ...data.records.map(recordSnapshot),
    ...data.tasks.map(taskSnapshot),
    ...data.events.map(eventSnapshot),
    ...data.notes.map(noteSnapshot),
    ...data.reviews.map(reviewSnapshot),
  ];
  const settings = settingsPayload(data.settings);
  snapshots.push({
    entityType: "user_settings",
    entityId: accountId,
    version: data.settings.version,
    payload: settings,
    optimisticEntity: {
      id: accountId,
      schemaVersion: data.settings.schemaVersion,
      version: data.settings.version,
      settings,
      createdAt: data.settings.updatedAt,
      updatedAt: data.settings.updatedAt,
    },
  });
  return new Map(snapshots.map((snapshot) => [`${snapshot.entityType}:${snapshot.entityId}`, snapshot]));
}

function payloadChanged(before: Snapshot, after: Snapshot): boolean {
  return JSON.stringify(before.payload) !== JSON.stringify(after.payload);
}

export function prepareMutations(accountId: string, before: AppData, after: AppData, action: Action): PreparedMutation[] {
  const previous = collect(accountId, before);
  const next = collect(accountId, after);
  const mutations: PreparedMutation[] = [];
  const keys = new Set([...previous.keys(), ...next.keys()]);
  for (const key of keys) {
    const oldValue = previous.get(key);
    const newValue = next.get(key);
    if (!oldValue && newValue) {
      mutations.push({ ...newValue, operation: "create", baseVersion: 0 });
      continue;
    }
    if (oldValue && !newValue) {
      if (action.type === "delete-goal" && (oldValue.entityType === "goal_milestone" || oldValue.entityType === "task")) continue;
      mutations.push({ entityType: oldValue.entityType, entityId: oldValue.entityId, operation: "delete", baseVersion: oldValue.version, payload: {} });
      continue;
    }
    if (oldValue && newValue && payloadChanged(oldValue, newValue)) {
      if (action.type === "delete-goal" && oldValue.entityType === "task") continue;
      mutations.push({ ...newValue, operation: oldValue.version === 0 ? "create" : "update", baseVersion: oldValue.version });
    }
  }
  return mutations.sort((left, right) => resourceOrder[left.entityType] - resourceOrder[right.entityType]);
}

export function prepareInitialMutations(accountId: string, data: AppData): PreparedMutation[] {
	const mutations = [...collect(accountId, data).values()].map<PreparedMutation>((snapshot) => {
		if (snapshot.entityType === "user_settings") {
			return {
				entityType: snapshot.entityType,
				entityId: snapshot.entityId,
				operation: "update",
				baseVersion: 1,
				payload: snapshot.payload,
				optimisticEntity: snapshot.optimisticEntity ? { ...snapshot.optimisticEntity, version: 1 } : undefined,
			};
		}
		return {
			entityType: snapshot.entityType,
			entityId: snapshot.entityId,
			operation: "create",
			baseVersion: 0,
			payload: snapshot.payload,
			optimisticEntity: snapshot.optimisticEntity ? { ...snapshot.optimisticEntity, version: 0 } : undefined,
		};
	});
	return mutations.sort((left, right) => resourceOrder[left.entityType] - resourceOrder[right.entityType]);
}

export async function persistPreparedMutations(accountId: string, deviceId: string, mutations: PreparedMutation[]): Promise<void> {
  await enqueueMutations(mutations.map((mutation) => ({
    accountId,
    deviceId,
    entityType: mutation.entityType,
    entityId: mutation.entityId,
    operation: mutation.operation,
    baseVersion: mutation.baseVersion,
    payload: mutation.payload,
    optimisticEntity: mutation.optimisticEntity,
  })));
}
