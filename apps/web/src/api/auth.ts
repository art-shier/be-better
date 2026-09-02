import { apiRequest } from "./http";

export type AccountStatus = "pending_verification" | "active" | "disabled" | "deletion_pending";

export interface AuthUser {
  id: string;
  email: string;
  displayName: string;
  status?: AccountStatus;
  emailVerifiedAt?: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface SessionResponse {
  user: AuthUser;
  expiresAt: string;
}

export interface RegisterResponse extends SessionResponse {
  verificationRequired: false;
}

export function getSession(signal?: AbortSignal): Promise<SessionResponse> {
  return apiRequest("/auth/session", { method: "GET", signal });
}

export function registerAccount(input: { displayName: string; email: string; password: string }): Promise<RegisterResponse> {
  return apiRequest("/auth/register", { method: "POST", json: input });
}

export function verifyEmail(token: string): Promise<SessionResponse> {
  return apiRequest("/auth/verify-email", { method: "POST", json: { token } });
}

export async function resendVerification(email: string): Promise<void> {
  await apiRequest("/auth/resend-verification", { method: "POST", json: { email } });
}

export async function requestPasswordReset(email: string): Promise<void> {
  await apiRequest("/auth/password-reset/request", { method: "POST", json: { email } });
}

export async function completePasswordReset(token: string, password: string): Promise<void> {
  await apiRequest("/auth/password-reset/complete", { method: "POST", json: { token, password } });
}

export function loginAccount(input: { email: string; password: string }): Promise<SessionResponse> {
  return apiRequest("/auth/login", { method: "POST", json: input });
}

export async function logoutAccount(): Promise<void> {
  await apiRequest("/auth/logout", { method: "POST", json: {} });
}

export async function updateProfile(displayName: string): Promise<AuthUser> {
  const result = await apiRequest<{ user: AuthUser }>("/users/me", { method: "PATCH", json: { displayName } });
  return result.user;
}

export async function updateEmail(currentPassword: string, email: string): Promise<AuthUser> {
  const result = await apiRequest<{ user: AuthUser }>("/users/me/email", { method: "PUT", json: { currentPassword, email } });
  return result.user;
}

export async function updatePassword(currentPassword: string, password: string): Promise<void> {
  await apiRequest("/users/me/password", { method: "PUT", json: { currentPassword, password } });
}
