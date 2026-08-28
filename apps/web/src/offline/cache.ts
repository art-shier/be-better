import { entityKey, getDayOrderDB, type CachedEntity, type CachedEntityType, type SyncMetadata } from "./db";

interface VersionedEntity {
  id: string;
  version?: number;
  createdAt?: string;
  updatedAt?: string;
}

function cachedEntity<T extends VersionedEntity>(accountId: string, entityType: CachedEntityType, data: T): CachedEntity<T> {
  return {
    key: entityKey(accountId, entityType, data.id),
    accountId,
    entityType,
    entityId: data.id,
    version: data.version ?? 0,
    data,
    updatedAt: data.updatedAt ?? new Date().toISOString(),
  };
}

export async function putCachedEntities<T extends VersionedEntity>(accountId: string, entityType: CachedEntityType, values: T[]): Promise<void> {
  const database = await getDayOrderDB();
  const transaction = database.transaction("entities", "readwrite");
  for (const value of values) await transaction.store.put(cachedEntity(accountId, entityType, value));
  await transaction.done;
}

export async function putCachedEntity<T extends VersionedEntity>(accountId: string, entityType: CachedEntityType, value: T): Promise<void> {
  await putCachedEntities(accountId, entityType, [value]);
}

export async function getCachedEntities<T>(accountId: string, entityType: CachedEntityType): Promise<T[]> {
  const records = await (await getDayOrderDB()).getAllFromIndex("entities", "by-account-type", [accountId, entityType]);
  return records.sort((left, right) => left.entityId.localeCompare(right.entityId)).map((record) => record.data as T);
}

export async function getCachedEntity<T>(accountId: string, entityType: CachedEntityType, entityId: string): Promise<T | undefined> {
  const record = await (await getDayOrderDB()).get("entities", entityKey(accountId, entityType, entityId));
  return record?.data as T | undefined;
}

export async function deleteCachedEntity(accountId: string, entityType: CachedEntityType, entityId: string): Promise<void> {
  await (await getDayOrderDB()).delete("entities", entityKey(accountId, entityType, entityId));
}

export async function hasAccountCache(accountId: string): Promise<boolean> {
  return await (await getDayOrderDB()).countFromIndex("entities", "by-account", accountId) > 0;
}

export async function clearAccountCache(accountId: string): Promise<void> {
  const database = await getDayOrderDB();
  const transaction = database.transaction(["entities", "mutations", "syncMeta", "accounts"], "readwrite");
  const entityStore = transaction.objectStore("entities");
  const mutationStore = transaction.objectStore("mutations");
  const entityKeys = await entityStore.index("by-account").getAllKeys(accountId);
  const mutationKeys = await mutationStore.index("by-account").getAllKeys(accountId);
  for (const key of entityKeys) await entityStore.delete(key);
  for (const key of mutationKeys) await mutationStore.delete(key);
  await transaction.objectStore("syncMeta").delete(accountId);
  await transaction.objectStore("accounts").delete(accountId);
  await transaction.done;
}

export { cachedEntity };

export interface CachedEntityBatch {
  entityType: CachedEntityType;
  values: Array<VersionedEntity>;
}

export async function replaceAccountEntities(accountId: string, deviceId: string, cursor: string, batches: CachedEntityBatch[]): Promise<void> {
  const database = await getDayOrderDB();
  const transaction = database.transaction(["entities", "mutations", "syncMeta"], "readwrite");
  const entityStore = transaction.objectStore("entities");
  const mutationStore = transaction.objectStore("mutations");
  const syncStore = transaction.objectStore("syncMeta");
  try {
    const keys = await entityStore.index("by-account").getAllKeys(accountId);
    for (const key of keys) await entityStore.delete(key);
    for (const batch of batches) {
      for (const value of batch.values) await entityStore.put(cachedEntity(accountId, batch.entityType, value));
    }
    const mutations = await mutationStore.index("by-account").getAll(accountId);
    for (const mutation of mutations) {
      if (mutation.operation === "delete") {
        await entityStore.delete(entityKey(accountId, mutation.entityType, mutation.entityId));
      } else if (mutation.optimisticEntity && typeof mutation.optimisticEntity === "object") {
        await entityStore.put(cachedEntity(accountId, mutation.entityType, mutation.optimisticEntity as VersionedEntity));
      }
    }
    const current = await syncStore.get(accountId);
    const metadata: SyncMetadata = {
      accountId,
      deviceId,
      cursor,
      lastSyncedAt: new Date().toISOString(),
      nextMutationSequence: current?.nextMutationSequence ?? 1,
    };
    await syncStore.put(metadata);
    await transaction.done;
  } catch (error) {
    try { transaction.abort(); } catch { /* transaction may already be aborted */ }
    try { await transaction.done; } catch { /* preserve the original storage error */ }
    throw error;
  }
}
