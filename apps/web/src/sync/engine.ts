import { ApiError } from "../api/http";
import {
  getCalendarEvent,
  getUserSettings,
  listCalendarEvents,
  listDailyReviews,
  listGoals,
  listMilestones,
  listNotes,
  listRecords,
  listTags,
  listTasks,
  type CursorPage,
} from "../api/resources";
import { getSyncBootstrap, registerDevice } from "../api/sync";
import { replaceAccountEntities, type CachedEntityBatch } from "../offline/cache";
import { getSyncMetadata, putSyncMetadata } from "../offline/db";
import { listMutations } from "../offline/mutations";
import { pullChanges } from "./pull";
import { pushMutations } from "./push";

export interface SyncCycleDependencies {
  register(deviceId: string, input: { deviceName: string; platform: string }): Promise<unknown>;
  bootstrap(deviceId: string): Promise<{ cursor: string }>;
  snapshot(accountId: string): Promise<CachedEntityBatch[]>;
  push(accountId: string, deviceId: string): Promise<number>;
  pull(accountId: string, deviceId: string, cursor: string): Promise<string>;
  deviceName: string;
}

export interface SyncCycleResult {
  deviceId: string;
  cursor: string;
  conflicts: number;
}

async function allCursorPages<T>(loader: (cursor?: string) => Promise<CursorPage<T>>): Promise<T[]> {
  const values: T[] = [];
  let cursor: string | undefined;
  do {
    const page = await loader(cursor);
    values.push(...page.items);
    cursor = page.hasMore ? page.nextCursor : undefined;
  } while (cursor);
  return values;
}

export async function fetchResourceSnapshot(accountId: string): Promise<CachedEntityBatch[]> {
  const [goals, tasks, records, notes, events, reviews, tags, settings] = await Promise.all([
    allCursorPages((cursor) => listGoals({ cursor, limit: 100 })),
    allCursorPages((cursor) => listTasks({ cursor, limit: 100 })),
    allCursorPages((cursor) => listRecords({ cursor, limit: 100 })),
    allCursorPages((cursor) => listNotes({ cursor, limit: 100 })),
		allCursorPages((cursor) => listCalendarEvents({ cursor, limit: 100 })),
		allCursorPages((cursor) => listDailyReviews({ cursor, limit: 100 })),
    listTags(),
    getUserSettings(),
  ]);
  const [milestoneGroups, eventDetails] = await Promise.all([
    Promise.all(goals.map((goal) => listMilestones(goal.id))),
    Promise.all(events.map((event) => getCalendarEvent(event.id))),
  ]);
  return [
    { entityType: "goal", values: goals },
    { entityType: "goal_milestone", values: milestoneGroups.flat() },
    { entityType: "task", values: tasks },
    { entityType: "calendar_event", values: eventDetails.map((result) => result.event) },
    { entityType: "calendar_reminder", values: eventDetails.flatMap((result) => result.reminders) },
    { entityType: "record", values: records },
    { entityType: "note", values: notes },
    { entityType: "daily_review", values: reviews },
    { entityType: "tag", values: tags },
    { entityType: "user_settings", values: [{ id: accountId, createdAt: settings.updatedAt, ...settings }] },
  ];
}

function defaultDeviceName(): string {
  const browserNavigator = navigator as Navigator & { userAgentData?: { platform?: string } };
  const platform = browserNavigator.userAgentData?.platform || navigator.platform || "Browser";
  return `DayOrder Web · ${platform}`.slice(0, 120);
}

const defaultDependencies: SyncCycleDependencies = {
  register: registerDevice,
  bootstrap: getSyncBootstrap,
  snapshot: fetchResourceSnapshot,
  push: pushMutations,
  pull: pullChanges,
  deviceName: defaultDeviceName(),
};

async function ensureDevice(accountId: string, dependencies: SyncCycleDependencies): Promise<{ deviceId: string; cursor?: string }> {
  const metadata = await getSyncMetadata(accountId);
  let deviceId = metadata?.deviceId ?? crypto.randomUUID();
  try {
    await dependencies.register(deviceId, { deviceName: dependencies.deviceName, platform: "web" });
  } catch (error) {
    if (!(error instanceof ApiError) || error.status !== 409) throw error;
    deviceId = crypto.randomUUID();
    await dependencies.register(deviceId, { deviceName: dependencies.deviceName, platform: "web" });
  }
  await putSyncMetadata({
    accountId,
    deviceId,
    cursor: metadata?.cursor,
    lastSyncedAt: metadata?.lastSyncedAt,
    nextMutationSequence: metadata?.nextMutationSequence ?? 1,
  });
  return { deviceId, cursor: metadata?.cursor };
}

async function rebuild(accountId: string, deviceId: string, dependencies: SyncCycleDependencies): Promise<string> {
  const bootstrap = await dependencies.bootstrap(deviceId);
  const batches = await dependencies.snapshot(accountId);
  await replaceAccountEntities(accountId, deviceId, bootstrap.cursor, batches);
  return dependencies.pull(accountId, deviceId, bootstrap.cursor);
}

export async function runSyncCycle(accountId: string, overrides: Partial<SyncCycleDependencies> = {}): Promise<SyncCycleResult> {
  const dependencies = { ...defaultDependencies, ...overrides };
  const { deviceId, cursor: initialCursor } = await ensureDevice(accountId, dependencies);
  let cursor = initialCursor;
  if (!cursor) {
    cursor = await rebuild(accountId, deviceId, dependencies);
    await dependencies.push(accountId, deviceId);
  } else {
    await dependencies.push(accountId, deviceId);
    try {
      cursor = await dependencies.pull(accountId, deviceId, cursor);
    } catch (error) {
      if (!(error instanceof ApiError) || error.code !== "SYNC_RESET_REQUIRED") throw error;
      cursor = await rebuild(accountId, deviceId, dependencies);
    }
  }
  const metadata = await getSyncMetadata(accountId);
  if (metadata?.cursor !== cursor) await putSyncMetadata({ ...metadata, accountId, deviceId, cursor, nextMutationSequence: metadata?.nextMutationSequence ?? 1, lastSyncedAt: new Date().toISOString() });
  const conflicts = (await listMutations(accountId, deviceId)).filter((mutation) => mutation.status === "conflict").length;
  return { deviceId, cursor, conflicts };
}
