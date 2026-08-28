import type { AppData } from "../domain/types";

export const LEGACY_STORAGE_KEY = "dayorder.app.v1";
export const LEGACY_SYNC_KEY = "dayorder.sync.v1";
export const LEGACY_CONFLICT_KEY = "dayorder.conflict.v1";
export const GUEST_STORAGE_KEY = "dayorder.guest.app.v1";
export const LAST_ACCOUNT_KEY = "dayorder.last-account.v1";
export const PENDING_REGISTRATION_KEY = "dayorder.pending-registration.v1";

export interface LastAccount {
  id: string;
  email: string;
  displayName: string;
}

export interface PendingRegistration {
	user: LastAccount;
	migrate: boolean;
}

export interface StorageKeys {
  data: string;
  sync: string;
  conflict: string;
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

export function readPendingRegistration(): PendingRegistration | null {
	try {
		const parsed = JSON.parse(localStorage.getItem(PENDING_REGISTRATION_KEY) ?? "null") as Partial<PendingRegistration> | null;
		if (!parsed || typeof parsed.migrate !== "boolean" || !parsed.user || typeof parsed.user.id !== "string" || typeof parsed.user.email !== "string" || typeof parsed.user.displayName !== "string") return null;
		return parsed as PendingRegistration;
	} catch {
		return null;
	}
}

export function savePendingRegistration(registration: PendingRegistration): void {
	try { localStorage.setItem(PENDING_REGISTRATION_KEY, JSON.stringify(registration)); } catch { /* best effort */ }
}

export function clearPendingRegistration(): void {
	try { localStorage.removeItem(PENDING_REGISTRATION_KEY); } catch { /* best effort */ }
}

export function readGuestState(): AppData | null {
	try {
		const parsed = JSON.parse(localStorage.getItem(GUEST_STORAGE_KEY) ?? "null") as Partial<AppData> | null;
		if (!parsed || !Array.isArray(parsed.goals) || !Array.isArray(parsed.tasks) || !Array.isArray(parsed.events) || !parsed.settings) return null;
		return parsed as AppData;
	} catch {
		return null;
	}
}

export function clearGuestStorage(): void {
  try {
    localStorage.removeItem(guestStorageKeys.data);
    localStorage.removeItem(guestStorageKeys.sync);
    localStorage.removeItem(guestStorageKeys.conflict);
  } catch { /* best effort */ }
}
