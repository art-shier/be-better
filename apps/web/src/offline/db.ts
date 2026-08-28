import { deleteDB, openDB, type DBSchema, type IDBPDatabase } from "idb";

export const DAYORDER_DB_NAME = "dayorder.offline.v1";
export const DAYORDER_DB_VERSION = 1;

export type CachedEntityType =
  | "goal"
  | "goal_milestone"
  | "task"
  | "calendar_event"
  | "calendar_reminder"
  | "record"
  | "note"
  | "daily_review"
  | "tag"
  | "user_settings";

export interface CachedEntity<T = unknown> {
  key: string;
  accountId: string;
  entityType: CachedEntityType;
  entityId: string;
  version: number;
  data: T;
  updatedAt: string;
}

export type MutationOperation = "create" | "update" | "delete";
export type MutationStatus = "pending" | "conflict" | "rejected";

export interface OfflineMutation<T = unknown> {
  key: string;
  mutationId: string;
  accountId: string;
  deviceId: string;
  sequence: number;
  entityType: CachedEntityType;
  entityId: string;
  operation: MutationOperation;
  baseVersion: number;
  payload: T;
  optimisticEntity?: unknown;
  status: MutationStatus;
  attempts: number;
  createdAt: string;
  updatedAt: string;
  errorCode?: string;
  serverData?: unknown;
  localCopy?: unknown;
}

export interface SyncMetadata {
  accountId: string;
  deviceId?: string;
  cursor?: string;
  lastSyncedAt?: string;
  nextMutationSequence: number;
}

export interface CachedAccount {
  id: string;
  email: string;
  displayName: string;
  lastUsedAt: string;
}

interface DayOrderDBSchema extends DBSchema {
  entities: {
    key: string;
    value: CachedEntity;
    indexes: {
      "by-account": string;
      "by-account-type": [string, CachedEntityType];
    };
  };
  mutations: {
    key: string;
    value: OfflineMutation;
    indexes: {
      "by-account": string;
      "by-account-device": [string, string];
    };
  };
  syncMeta: {
    key: string;
    value: SyncMetadata;
  };
  accounts: {
    key: string;
    value: CachedAccount;
  };
}

export type DayOrderDB = IDBPDatabase<DayOrderDBSchema>;

let databasePromise: Promise<DayOrderDB> | undefined;
let databaseHandle: DayOrderDB | undefined;

export function entityKey(accountId: string, entityType: CachedEntityType, entityId: string): string {
  return `${accountId}:${entityType}:${entityId}`;
}

export function mutationKey(accountId: string, mutationId: string): string {
  return `${accountId}:${mutationId}`;
}

export function getDayOrderDB(): Promise<DayOrderDB> {
  if (!databasePromise) {
    databasePromise = openDB<DayOrderDBSchema>(DAYORDER_DB_NAME, DAYORDER_DB_VERSION, {
      upgrade(database) {
        const entities = database.createObjectStore("entities", { keyPath: "key" });
        entities.createIndex("by-account", "accountId");
        entities.createIndex("by-account-type", ["accountId", "entityType"]);

        const mutations = database.createObjectStore("mutations", { keyPath: "key" });
        mutations.createIndex("by-account", "accountId");
        mutations.createIndex("by-account-device", ["accountId", "deviceId"]);

        database.createObjectStore("syncMeta", { keyPath: "accountId" });
        database.createObjectStore("accounts", { keyPath: "id" });
      },
      blocking() {
        databaseHandle?.close();
        databaseHandle = undefined;
        databasePromise = undefined;
      },
    }).then((database) => {
      databaseHandle = database;
      return database;
    });
  }
  return databasePromise;
}

export function closeDayOrderDB(): void {
  databaseHandle?.close();
  databaseHandle = undefined;
  databasePromise = undefined;
}

export async function deleteDayOrderDB(): Promise<void> {
  closeDayOrderDB();
  await deleteDB(DAYORDER_DB_NAME);
}

export async function getSyncMetadata(accountId: string): Promise<SyncMetadata | undefined> {
  return (await getDayOrderDB()).get("syncMeta", accountId);
}

export async function putSyncMetadata(metadata: SyncMetadata): Promise<void> {
  await (await getDayOrderDB()).put("syncMeta", metadata);
}

export async function saveCachedAccount(account: CachedAccount): Promise<void> {
  await (await getDayOrderDB()).put("accounts", account);
}

export async function getCachedAccount(accountId: string): Promise<CachedAccount | undefined> {
  return (await getDayOrderDB()).get("accounts", accountId);
}
