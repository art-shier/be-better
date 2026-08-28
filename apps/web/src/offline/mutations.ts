import { cachedEntity } from "./cache";
import {
  entityKey,
  getDayOrderDB,
  mutationKey,
  type CachedEntityType,
  type MutationOperation,
  type OfflineMutation,
  type SyncMetadata,
} from "./db";

interface OptimisticEntity {
  id: string;
  version?: number;
  updatedAt?: string;
}

export interface EnqueueMutationInput {
  mutationId?: string;
  accountId: string;
  deviceId: string;
  entityType: CachedEntityType;
  entityId: string;
  operation: MutationOperation;
  baseVersion?: number;
  payload: unknown;
  optimisticEntity?: OptimisticEntity & Record<string, unknown>;
}

function entityMutationKey(input: Pick<EnqueueMutationInput, "entityType" | "entityId">): string {
  return `${input.entityType}:${input.entityId}`;
}

function compactPendingMutation(existing: OfflineMutation, input: EnqueueMutationInput, timestamp: string): OfflineMutation | undefined {
  if (existing.operation === "create" && input.operation === "delete") return undefined;
  if (existing.operation === "update" && input.operation === "delete") {
    return { ...existing, operation: "delete", payload: {}, optimisticEntity: undefined, updatedAt: timestamp };
  }
  if (existing.operation === "delete" && input.operation !== "delete") {
    return { ...existing, operation: "update", payload: input.payload, optimisticEntity: input.optimisticEntity, updatedAt: timestamp };
  }
  return {
    ...existing,
    operation: existing.operation,
    payload: input.payload,
    optimisticEntity: input.optimisticEntity,
    updatedAt: timestamp,
  };
}

export async function enqueueMutation(input: EnqueueMutationInput): Promise<OfflineMutation> {
  const mutations = await enqueueMutations([input]);
  return mutations[0];
}

export async function enqueueMutations(inputs: EnqueueMutationInput[]): Promise<OfflineMutation[]> {
  if (inputs.length < 1) return [];
  const accountId = inputs[0].accountId;
  const deviceId = inputs[0].deviceId;
  if (inputs.some((input) => input.accountId !== accountId || input.deviceId !== deviceId)) {
    throw new Error("同一原子命令只能包含同一账户和设备的 Mutation");
  }
  const database = await getDayOrderDB();
  const transaction = database.transaction(["entities", "mutations", "syncMeta"], "readwrite");
  const entityStore = transaction.objectStore("entities");
  const mutationStore = transaction.objectStore("mutations");
  const syncStore = transaction.objectStore("syncMeta");
  const currentMeta = await syncStore.get(accountId);
  const pendingByEntity = new Map(
    (await mutationStore.index("by-account-device").getAll([accountId, deviceId]))
      .filter((mutation) => mutation.status === "pending")
      .map((mutation) => [entityMutationKey(mutation), mutation]),
  );
  let sequence = currentMeta?.nextMutationSequence ?? 1;
  const mutations: OfflineMutation[] = [];
  try {
    for (const input of inputs) {
      const timestamp = new Date().toISOString();
      const pendingKey = entityMutationKey(input);
      const existing = pendingByEntity.get(pendingKey);
      if (input.operation === "delete") {
        await entityStore.delete(entityKey(input.accountId, input.entityType, input.entityId));
      } else if (input.optimisticEntity) {
        await entityStore.put(cachedEntity(input.accountId, input.entityType, input.optimisticEntity));
      }
      if (existing) {
        const compacted = compactPendingMutation(existing, input, timestamp);
        if (compacted) {
          await mutationStore.put(compacted);
          pendingByEntity.set(pendingKey, compacted);
          mutations.push(compacted);
        } else {
          await mutationStore.delete(existing.key);
          pendingByEntity.delete(pendingKey);
          mutations.push(existing);
        }
        continue;
      }
      const mutationId = input.mutationId ?? crypto.randomUUID();
      const mutation: OfflineMutation = {
        key: mutationKey(input.accountId, mutationId),
        mutationId,
        accountId: input.accountId,
        deviceId: input.deviceId,
        sequence,
        entityType: input.entityType,
        entityId: input.entityId,
        operation: input.operation,
        baseVersion: input.baseVersion ?? input.optimisticEntity?.version ?? 0,
        payload: input.payload,
        optimisticEntity: input.optimisticEntity,
        status: "pending",
        attempts: 0,
        createdAt: timestamp,
        updatedAt: timestamp,
      };
      await mutationStore.put(mutation);
      pendingByEntity.set(pendingKey, mutation);
      mutations.push(mutation);
      sequence += 1;
    }
    const nextMeta: SyncMetadata = {
      accountId,
      deviceId: currentMeta?.deviceId ?? deviceId,
      cursor: currentMeta?.cursor,
      lastSyncedAt: currentMeta?.lastSyncedAt,
      nextMutationSequence: sequence,
    };
    await syncStore.put(nextMeta);
    await transaction.done;
  } catch (error) {
    try { transaction.abort(); } catch { /* transaction may already be aborted */ }
    try { await transaction.done; } catch { /* preserve the original storage error */ }
    throw error;
  }
  return mutations;
}

export async function listMutations(accountId: string, deviceId: string): Promise<OfflineMutation[]> {
  const values = await (await getDayOrderDB()).getAllFromIndex("mutations", "by-account-device", [accountId, deviceId]);
  return values.sort((left, right) => left.sequence - right.sequence);
}

export async function removeMutation(accountId: string, mutationId: string): Promise<void> {
  await (await getDayOrderDB()).delete("mutations", mutationKey(accountId, mutationId));
}

export async function saveMutationConflict(accountId: string, mutationId: string, errorCode: string, serverData?: unknown): Promise<void> {
  const database = await getDayOrderDB();
  const key = mutationKey(accountId, mutationId);
  const mutation = await database.get("mutations", key);
  if (!mutation) return;
  await database.put("mutations", {
    ...mutation,
    status: "conflict",
    errorCode,
    serverData,
    localCopy: mutation.payload,
    attempts: mutation.attempts + 1,
    updatedAt: new Date().toISOString(),
  });
}

export async function saveMutationRejection(accountId: string, mutationId: string, errorCode: string): Promise<void> {
  const database = await getDayOrderDB();
  const key = mutationKey(accountId, mutationId);
  const mutation = await database.get("mutations", key);
  if (!mutation) return;
  await database.put("mutations", {
    ...mutation,
    status: "rejected",
    errorCode,
    attempts: mutation.attempts + 1,
    updatedAt: new Date().toISOString(),
  });
}
