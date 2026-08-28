import { apiRequest, type ApiErrorBody } from "./http";

export interface UserDevice {
  id: string;
  deviceName: string;
  platform: string;
  lastSeenAt: string;
  lastSyncCursor: number;
  createdAt: string;
  revokedAt?: string;
}

export interface DeviceRegistration { device: UserDevice }

export function registerDevice(deviceId: string, input: { deviceName: string; platform: string }): Promise<DeviceRegistration> {
  return apiRequest(`/users/me/devices/${deviceId}`, { method: "PUT", json: input });
}

export async function listDevices(): Promise<UserDevice[]> {
  return (await apiRequest<{ devices: UserDevice[] }>("/users/me/devices")).devices;
}

export interface SyncBootstrap { cursor: string }
export type SyncEntityType = "goal" | "milestone" | "task" | "calendar_event" | "reminder" | "record" | "note" | "daily_review" | "tag" | "settings";

export interface SyncChange<T = unknown> {
  sequence: number;
  entityType: SyncEntityType;
  entityId: string;
  operation: "create" | "update" | "delete";
  entityVersion: number;
  changedAt: string;
  data?: T;
}

export interface SyncChangesPage {
  changes: SyncChange[];
  nextCursor: string;
  hasMore: boolean;
}

export function getSyncBootstrap(deviceId: string): Promise<SyncBootstrap> {
  return apiRequest("/sync/bootstrap", { headers: { "X-Device-ID": deviceId } });
}

export function getSyncChanges(deviceId: string, cursor: string, limit = 500): Promise<SyncChangesPage> {
  const query = new URLSearchParams({ cursor, limit: String(limit) });
  return apiRequest(`/sync/changes?${query}`, { headers: { "X-Device-ID": deviceId } });
}

export interface SyncMutation {
  mutationId: string;
  sequence: number;
  entityType: SyncEntityType;
  entityId: string;
  operation: "create" | "update" | "delete";
  baseVersion: number;
  payload: unknown;
}

export type SyncMutationStatus = "applied" | "conflict" | "rejected" | "duplicate";
export interface SyncMutationResult {
  mutationId: string;
  status: SyncMutationStatus;
  data?: unknown;
  error?: ApiErrorBody;
}

export interface SyncMutationResponse { results: SyncMutationResult[] }

export function postSyncMutations(deviceId: string, mutations: SyncMutation[]): Promise<SyncMutationResponse> {
  if (mutations.length < 1 || mutations.length > 100) throw new RangeError("每批同步 Mutation 必须在 1 到 100 项之间");
  return apiRequest("/sync/mutations", { method: "POST", headers: { "X-Device-ID": deviceId }, json: { mutations } });
}
