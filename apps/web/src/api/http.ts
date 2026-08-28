const configuredBaseUrl = import.meta.env.VITE_API_BASE_URL?.trim();
export const API_BASE_URL = (configuredBaseUrl || "/api/v1").replace(/\/$/, "");

export type ApiErrorCategory = "auth" | "conflict" | "rate-limit" | "unavailable" | "validation" | "request" | "server";

export interface ApiErrorBody {
  code?: string;
  message?: string;
  fields?: Record<string, string>;
  retryable?: boolean;
  requestId?: string;
  currentRevision?: number;
}

interface ApiErrorEnvelope {
  error?: ApiErrorBody;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly category: ApiErrorCategory;
  readonly fields: Record<string, string>;
  readonly retryable: boolean;
  readonly requestId?: string;
  readonly retryAfterSeconds?: number;
  readonly currentRevision?: number;

  constructor(status: number, payload: ApiErrorBody = {}, retryAfter?: string | null) {
    super(payload.message || `API request failed with status ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = payload.code;
    this.category = classifyStatus(status);
    this.fields = payload.fields ?? {};
    this.retryable = payload.retryable ?? (status === 429 || status === 503 || status >= 500);
    this.requestId = payload.requestId;
    this.currentRevision = payload.currentRevision;
    const parsedRetryAfter = retryAfter ? Number.parseInt(retryAfter, 10) : Number.NaN;
    this.retryAfterSeconds = Number.isFinite(parsedRetryAfter) ? parsedRetryAfter : undefined;
  }
}

export interface ApiRequestInit extends Omit<RequestInit, "body"> {
  body?: BodyInit | null;
  json?: unknown;
  requestId?: string;
}

function classifyStatus(status: number): ApiErrorCategory {
  if (status === 401 || status === 403) return "auth";
  if (status === 409) return "conflict";
  if (status === 429) return "rate-limit";
  if (status === 503) return "unavailable";
  if (status === 400 || status === 415 || status === 422 || status === 428) return "validation";
  if (status >= 500) return "server";
  return "request";
}

function newRequestId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") return crypto.randomUUID();
  return "00000000-0000-4000-8000-000000000000";
}

async function readResponse(response: Response): Promise<unknown> {
  if (response.status === 204 || !response.headers.get("content-type")?.includes("application/json")) return undefined;
  try {
    return await response.json();
  } catch {
    return undefined;
  }
}

function errorBody(value: unknown): ApiErrorBody {
  if (!value || typeof value !== "object") return {};
  const envelope = value as ApiErrorEnvelope & ApiErrorBody;
  return envelope.error && typeof envelope.error === "object" ? envelope.error : envelope;
}

export async function apiRequest<T>(path: string, init: ApiRequestInit = {}): Promise<T> {
  const { json, requestId, headers: inputHeaders, ...requestInit } = init;
  const headers = new Headers(inputHeaders);
  headers.set("Accept", "application/json");
  headers.set("X-Request-ID", requestId ?? newRequestId());
  let body = requestInit.body;
  if (json !== undefined) {
    if (!headers.has("Content-Type")) headers.set("Content-Type", "application/json");
    body = JSON.stringify(json);
  }
  const response = await fetch(`${API_BASE_URL}${path}`, {
    cache: "no-store",
    credentials: "include",
    ...requestInit,
    headers,
    body,
  });
  const value = await readResponse(response);
  if (!response.ok) throw new ApiError(response.status, errorBody(value), response.headers.get("Retry-After"));
  return value as T;
}
