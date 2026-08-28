import { postSyncMutations, type SyncMutation, type SyncMutationResponse } from "../api/sync";
import { cachedEntity } from "../offline/cache";
import { getDayOrderDB, type CachedEntityType, type OfflineMutation } from "../offline/db";
import { listMutations } from "../offline/mutations";
import { localConflictCopy, syncType } from "./conflicts";

export type PushMutationsAPI = (deviceId: string, mutations: SyncMutation[]) => Promise<SyncMutationResponse>;

function outbound(mutation: OfflineMutation): SyncMutation {
  return {
    mutationId: mutation.mutationId,
    sequence: mutation.sequence,
    entityType: syncType(mutation.entityType),
    entityId: mutation.entityId,
    operation: mutation.operation,
    baseVersion: mutation.baseVersion,
    payload: mutation.payload,
  };
}

function resultEntities(mutation: OfflineMutation, data: unknown): Array<{ entityType: CachedEntityType; data: Record<string, unknown> }> {
  if (!data || typeof data !== "object" || Array.isArray(data)) return [];
  const value = data as Record<string, unknown>;
  if (mutation.entityType === "calendar_event" && value.event && typeof value.event === "object") {
    const reminders = Array.isArray(value.reminders) ? value.reminders : [];
    return [
      { entityType: "calendar_event", data: value.event as Record<string, unknown> },
      ...reminders.map((item) => ({ entityType: "calendar_reminder" as const, data: item as Record<string, unknown> })),
    ];
  }
  return [{ entityType: mutation.entityType, data: { ...value, id: typeof value.id === "string" ? value.id : mutation.entityId } }];
}

async function applyPushResponse(accountId: string, mutations: OfflineMutation[], response: SyncMutationResponse): Promise<void> {
  const database = await getDayOrderDB();
  const transaction = database.transaction(["entities", "mutations"], "readwrite");
  const entityStore = transaction.objectStore("entities");
  const mutationStore = transaction.objectStore("mutations");
  const results = new Map(response.results.map((result) => [result.mutationId, result]));
  for (const mutation of mutations) {
    const result = results.get(mutation.mutationId);
    if (!result) continue;
    if (result.status === "applied" || result.status === "duplicate") {
      if (mutation.operation !== "delete") {
        for (const entity of resultEntities(mutation, result.data)) {
          const value = entity.data as { id: string; version?: number; updatedAt?: string };
          await entityStore.put(cachedEntity(accountId, entity.entityType, value));
        }
      }
      await mutationStore.delete(mutation.key);
      continue;
    }
    await mutationStore.put({
      ...mutation,
      status: result.status === "conflict" ? "conflict" : "rejected",
      errorCode: result.error?.code ?? (result.status === "conflict" ? "ENTITY_VERSION_CONFLICT" : "MUTATION_REJECTED"),
      serverData: result.data,
      localCopy: result.status === "conflict" ? localConflictCopy(mutation) : mutation.localCopy,
      attempts: mutation.attempts + 1,
      updatedAt: new Date().toISOString(),
    });
  }
  await transaction.done;
}

export async function pushMutations(accountId: string, deviceId: string, api: PushMutationsAPI = postSyncMutations): Promise<number> {
  const queued = (await listMutations(accountId, deviceId)).filter((mutation) => mutation.status === "pending");
  let applied = 0;
  for (let offset = 0; offset < queued.length; offset += 100) {
    const batch = queued.slice(offset, offset + 100);
    const response = await api(deviceId, batch.map(outbound));
    await applyPushResponse(accountId, batch, response);
    applied += response.results.filter((result) => result.status === "applied" || result.status === "duplicate").length;
  }
  return applied;
}
