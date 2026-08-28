import type { AppData } from "../domain/types";
import { getSyncMetadata, putSyncMetadata } from "../offline/db";
import { listMutations } from "../offline/mutations";
import { prepareInitialMutations, persistPreparedMutations } from "../store/commands";
import { clearGuestStorage } from "../store/storage";
import { runSyncCycle } from "../sync/engine";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

export type GuestMigrationSync = (accountId: string) => Promise<unknown>;

export function normalizeGuestDataForMigration(data: AppData): AppData {
  const identifiers = new Map<string, string>();
  const register = (id: string) => {
    const normalized = uuidPattern.test(id) ? id : crypto.randomUUID();
    identifiers.set(id, normalized);
    return normalized;
  };
  const resolve = (id?: string) => id ? identifiers.get(id) ?? register(id) : undefined;

  for (const goal of data.goals) {
    register(goal.id);
    for (const milestone of goal.milestones) register(milestone.id);
  }
  for (const value of [...data.records, ...data.tasks, ...data.events, ...data.notes, ...data.reviews]) register(value.id);

  const goals = data.goals.map((goal) => {
    const goalId = resolve(goal.id)!;
    return {
      ...goal,
      id: goalId,
      version: 0,
      deletedAt: undefined,
      milestones: goal.milestones.map((milestone) => ({
        ...milestone,
        id: resolve(milestone.id)!,
        goalId,
        version: 0,
        deletedAt: undefined,
      })),
    };
  });
  return {
    ...data,
    goals,
    records: data.records.map((value) => ({ ...value, id: resolve(value.id)!, version: 0, deletedAt: undefined })),
    tasks: data.tasks.map((value) => ({
      ...value,
      id: resolve(value.id)!,
      goalId: resolve(value.goalId),
      sourceRecordId: resolve(value.sourceRecordId),
      version: 0,
      deletedAt: undefined,
    })),
    events: data.events.map((value) => ({
      ...value,
      id: resolve(value.id)!,
      goalId: resolve(value.goalId),
      reminders: undefined,
      version: 0,
      deletedAt: undefined,
    })),
    notes: data.notes.map((value) => ({
      ...value,
      id: resolve(value.id)!,
      linkedEntityIds: value.linkedEntityIds.map((id) => resolve(id)!),
      version: 0,
      deletedAt: undefined,
    })),
    reviews: data.reviews.map((value) => ({ ...value, id: resolve(value.id)!, version: 0, deletedAt: undefined })),
    settings: { ...data.settings, version: 1, updatedAt: new Date().toISOString() },
  };
}

export async function migrateGuestData(accountId: string, data: AppData, sync: GuestMigrationSync = runSyncCycle): Promise<void> {
  const metadata = await getSyncMetadata(accountId);
  const deviceId = metadata?.deviceId ?? crypto.randomUUID();
  if (!metadata) {
    await putSyncMetadata({ accountId, deviceId, nextMutationSequence: 1 });
  }
  const existing = await listMutations(accountId, deviceId);
  if (existing.length === 0) {
    const normalized = normalizeGuestDataForMigration(data);
    await persistPreparedMutations(accountId, deviceId, prepareInitialMutations(accountId, normalized));
  }
  await sync(accountId);
  const remaining = await listMutations(accountId, deviceId);
  if (remaining.length > 0) {
    throw new Error(`游客数据迁移未全部完成，仍有 ${remaining.length} 项需要处理`);
  }
  clearGuestStorage();
}
