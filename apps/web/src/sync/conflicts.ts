import type { SyncEntityType } from "../api/sync";
import type { CachedEntityType, OfflineMutation } from "../offline/db";

const syncToCache: Record<SyncEntityType, CachedEntityType> = {
  goal: "goal",
  milestone: "goal_milestone",
  task: "task",
  calendar_event: "calendar_event",
  reminder: "calendar_reminder",
  record: "record",
  note: "note",
  daily_review: "daily_review",
  tag: "tag",
  settings: "user_settings",
};

const cacheToSync: Record<CachedEntityType, SyncEntityType> = {
  goal: "goal",
  goal_milestone: "milestone",
  task: "task",
  calendar_event: "calendar_event",
  calendar_reminder: "reminder",
  record: "record",
  note: "note",
  daily_review: "daily_review",
  tag: "tag",
  user_settings: "settings",
};

export function cachedType(entityType: SyncEntityType): CachedEntityType {
  return syncToCache[entityType];
}

export function syncType(entityType: CachedEntityType): SyncEntityType {
  return cacheToSync[entityType];
}

export function localConflictCopy(mutation: OfflineMutation): unknown {
  return mutation.optimisticEntity ?? mutation.payload;
}

export function conflictCode(operation: "create" | "update" | "delete"): string {
  return operation === "delete" ? "ENTITY_DELETED" : "ENTITY_VERSION_CONFLICT";
}
