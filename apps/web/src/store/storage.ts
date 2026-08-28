import type { AppData } from "../domain/types";

export const LEGACY_STORAGE_KEY = "dayorder.app.v1";
export const LEGACY_SYNC_KEY = "dayorder.sync.v1";
export const LEGACY_CONFLICT_KEY = "dayorder.conflict.v1";
export const GUEST_STORAGE_KEY = "dayorder.guest.app.v1";
export const LAST_ACCOUNT_KEY = "dayorder.last-account.v1";

export interface LastAccount {
  id: string;
  email: string;
  displayName: string;
}

export interface StorageKeys {
  data: string;
  sync: string;
  conflict: string;
}

export function userStorageKeys(userId: string): StorageKeys {
  return {
    data: `dayorder.user.${userId}.app.v1`,
    sync: `dayorder.user.${userId}.sync.v1`,
    conflict: `dayorder.user.${userId}.conflict.v1`,
  };
}

export const guestStorageKeys: StorageKeys = {
  data: GUEST_STORAGE_KEY,
  sync: "dayorder.guest.sync.v1",
  conflict: "dayorder.guest.conflict.v1",
};

export function migrateLegacyStorage(): void {
  try {
    if (!localStorage.getItem(GUEST_STORAGE_KEY)) {
      const legacy = localStorage.getItem(LEGACY_STORAGE_KEY);
      if (legacy) localStorage.setItem(GUEST_STORAGE_KEY, legacy);
    }
    localStorage.removeItem(LEGACY_STORAGE_KEY);
    localStorage.removeItem(LEGACY_SYNC_KEY);
    localStorage.removeItem(LEGACY_CONFLICT_KEY);
  } catch {
    // Storage errors must not make the local application unusable.
  }
}

export function readLastAccount(): LastAccount | null {
  try {
    const parsed = JSON.parse(localStorage.getItem(LAST_ACCOUNT_KEY) ?? "null") as Partial<LastAccount> | null;
    if (!parsed || typeof parsed.id !== "string" || typeof parsed.email !== "string" || typeof parsed.displayName !== "string") return null;
    return parsed as LastAccount;
  } catch {
    return null;
  }
}

export function saveLastAccount(account: LastAccount): void {
  try { localStorage.setItem(LAST_ACCOUNT_KEY, JSON.stringify(account)); } catch { /* best effort */ }
}

export function clearLastAccount(): void {
  try { localStorage.removeItem(LAST_ACCOUNT_KEY); } catch { /* best effort */ }
}

export function hasUserCache(userId: string): boolean {
  try { return Boolean(localStorage.getItem(userStorageKeys(userId).data)); } catch { return false; }
}

export function saveUserState(userId: string, data: AppData, revision: number, updatedAt: string): void {
  const keys = userStorageKeys(userId);
  localStorage.setItem(keys.data, JSON.stringify(data));
  localStorage.setItem(keys.sync, JSON.stringify({ revision, fingerprint: fingerprintData(data), updatedAt }));
}

export function clearGuestStorage(): void {
  try {
    localStorage.removeItem(guestStorageKeys.data);
    localStorage.removeItem(guestStorageKeys.sync);
    localStorage.removeItem(guestStorageKeys.conflict);
  } catch { /* best effort */ }
}

export function clearUserStorage(userId: string): void {
  const keys = userStorageKeys(userId);
  try {
    localStorage.removeItem(keys.data);
    localStorage.removeItem(keys.sync);
    localStorage.removeItem(keys.conflict);
  } catch { /* best effort */ }
}

export function fingerprintData(data: AppData): string {
  const source = JSON.stringify(data);
  let hash = 2166136261;
  for (let index = 0; index < source.length; index += 1) {
    hash ^= source.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return `${source.length}:${(hash >>> 0).toString(36)}`;
}
