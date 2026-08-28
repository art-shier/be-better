import type { AppData } from "../domain/types";

const configuredBaseUrl = import.meta.env.VITE_API_BASE_URL?.trim();
const API_BASE_URL = (configuredBaseUrl || "/api/v1").replace(/\/$/, "");

export interface RemoteState {
  revision: number;
  data: AppData;
  updatedAt: string;
}

interface ErrorPayload {
  code?: string;
  message?: string;
  currentRevision?: number;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly currentRevision?: number;

  constructor(status: number, payload: ErrorPayload = {}) {
    super(payload.message || `API request failed with status ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = payload.code;
    this.currentRevision = payload.currentRevision;
  }
}

async function readResponse(response: Response): Promise<unknown> {
  const contentType = response.headers.get("content-type") ?? "";
  if (!contentType.includes("application/json")) return undefined;
  try {
    return await response.json();
  } catch {
    return undefined;
  }
}

function isRemoteState(value: unknown): value is RemoteState {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Partial<RemoteState>;
  return Number.isInteger(candidate.revision)
    && (candidate.revision ?? 0) > 0
    && typeof candidate.updatedAt === "string"
    && Boolean(candidate.data)
    && typeof candidate.data === "object"
    && candidate.data?.version === 1;
}

async function requestState(path: string, init: RequestInit): Promise<RemoteState> {
  const response = await fetch(`${API_BASE_URL}${path}`, {
    ...init,
    credentials: "include",
    headers: {
      Accept: "application/json",
      ...init.headers,
    },
  });
  const body = await readResponse(response);
  if (!response.ok) throw new ApiError(response.status, (body ?? {}) as ErrorPayload);
  if (!isRemoteState(body)) throw new ApiError(502, { code: "INVALID_RESPONSE", message: "服务端返回了无法识别的数据" });
  return body;
}

export function getRemoteState(signal?: AbortSignal): Promise<RemoteState> {
  return requestState("/state", { method: "GET", cache: "no-store", signal });
}

export function putRemoteState(data: AppData, expectedRevision: number, signal?: AbortSignal): Promise<RemoteState> {
  return requestState("/state", {
    method: "PUT",
    cache: "no-store",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ expectedRevision, data }),
    signal,
  });
}

export { API_BASE_URL };
