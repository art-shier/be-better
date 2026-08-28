import { getSyncChanges, type SyncChange, type SyncChangesPage } from "../api/sync";
import { cachedEntity } from "../offline/cache";
import { entityKey, getDayOrderDB, type OfflineMutation, type SyncMetadata } from "../offline/db";
import { cachedType, conflictCode, localConflictCopy } from "./conflicts";

export type PullChangesAPI = (deviceId: string, cursor: string, limit?: number) => Promise<SyncChangesPage>;

function normalizedData(change: SyncChange): Record<string, unknown> {
  if (!change.data || typeof change.data !== "object" || Array.isArray(change.data)) throw new Error("同步变化缺少实体数据");
  const value = change.data as Record<string, unknown>;
  return {
    ...value,
    id: typeof value.id === "string" ? value.id : change.entityId,
    version: typeof value.version === "number" ? value.version : change.entityVersion,
    updatedAt: typeof value.updatedAt === "string" ? value.updatedAt : change.changedAt,
  };
}

export async function applySyncChangesPage(accountId: string, deviceId: string, page: SyncChangesPage): Promise<void> {
  const database = await getDayOrderDB();
  const transaction = database.transaction(["entities", "mutations", "syncMeta"], "readwrite");
  const entityStore = transaction.objectStore("entities");
  const mutationStore = transaction.objectStore("mutations");
  const syncStore = transaction.objectStore("syncMeta");
  const mutations = await mutationStore.index("by-account").getAll(accountId);

  for (const change of page.changes) {
    const entityType = cachedType(change.entityType);
    const pending = mutations.filter((mutation) => mutation.entityType === entityType && mutation.entityId === change.entityId && mutation.status !== "rejected");
    if (pending.length > 0) {
      for (const mutation of pending) {
        const updated: OfflineMutation = {
          ...mutation,
          status: "conflict",
          errorCode: conflictCode(change.operation),
          serverData: change.data,
          localCopy: localConflictCopy(mutation),
          attempts: mutation.attempts + 1,
          updatedAt: new Date().toISOString(),
        };
        await mutationStore.put(updated);
        const index = mutations.findIndex((item) => item.key === mutation.key);
        if (index >= 0) mutations[index] = updated;
      }
      continue;
    }

    const key = entityKey(accountId, entityType, change.entityId);
    if (change.operation === "delete") {
      await entityStore.delete(key);
      continue;
    }
    const current = await entityStore.get(key);
    if (current && current.version >= change.entityVersion) continue;
    await entityStore.put(cachedEntity(accountId, entityType, normalizedData(change) as { id: string; version: number; updatedAt: string }));
  }

  const currentMeta = await syncStore.get(accountId);
  const metadata: SyncMetadata = {
    accountId,
    deviceId: currentMeta?.deviceId ?? deviceId,
    cursor: page.nextCursor,
    lastSyncedAt: new Date().toISOString(),
    nextMutationSequence: currentMeta?.nextMutationSequence ?? 1,
  };
  await syncStore.put(metadata);
  await transaction.done;
}

export async function pullChanges(accountId: string, deviceId: string, cursor: string, api: PullChangesAPI = getSyncChanges): Promise<string> {
  let nextCursor = cursor;
  let hasMore = true;
  while (hasMore) {
    const page = await api(deviceId, nextCursor, 500);
    await applySyncChangesPage(accountId, deviceId, page);
    nextCursor = page.nextCursor;
    hasMore = page.hasMore;
  }
  return nextCursor;
}
