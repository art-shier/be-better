import { ApiError } from "../api/client";
import type { AppData } from "../domain/types";

const configuredBaseUrl = import.meta.env.VITE_API_BASE_URL?.trim();
const API_BASE_URL = (configuredBaseUrl || "/api/v1").replace(/\/$/, "");

export interface AuthUser {
  id: string;
  email: string;
  displayName: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface SessionResponse { user: AuthUser; expiresAt: string }
export interface RegisterResponse extends SessionResponse {
  state: { revision: number; data: AppData; updatedAt: string };
}

async function readResponse(response: Response): Promise<unknown> {
  if (!response.headers.get("content-type")?.includes("application/json")) return undefined;
  try { return await response.json(); } catch { return undefined; }
}

async function request<T>(path: string, init: RequestInit): Promise<T> {
  const response = await fetch(`${API_BASE_URL}${path}`, {
    cache: "no-store",
    credentials: "include",
    ...init,
    headers: { Accept: "application/json", ...init.headers },
  });
  const body = await readResponse(response);
  if (!response.ok) throw new ApiError(response.status, (body ?? {}) as { code?: string; message?: string });
  return body as T;
}

export function getSession(signal?: AbortSignal): Promise<SessionResponse> {
  return request("/auth/session", { method: "GET", signal });
}

export function registerAccount(input: { displayName: string; email: string; password: string; initialData?: AppData }): Promise<RegisterResponse> {
  return request("/auth/register", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(input) });
}

export function loginAccount(input: { email: string; password: string }): Promise<SessionResponse> {
  return request("/auth/login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(input) });
}

export async function logoutAccount(): Promise<void> {
  await request<undefined>("/auth/logout", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
}

export async function updateProfile(displayName: string): Promise<AuthUser> {
  const result = await request<{ user: AuthUser }>("/users/me", { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ displayName }) });
  return result.user;
}

export async function updateEmail(currentPassword: string, email: string): Promise<AuthUser> {
  const result = await request<{ user: AuthUser }>("/users/me/email", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ currentPassword, email }) });
  return result.user;
}

export async function updatePassword(currentPassword: string, password: string): Promise<void> {
  await request<undefined>("/users/me/password", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ currentPassword, password }) });
}
