import type { AppData } from "../domain/types";

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
  try { localStorage.removeItem(GUEST_STORAGE_KEY); } catch { /* best effort */ }
}
