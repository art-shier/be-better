import { ApiError, errorBody, readResponse } from "../../api/http";
import type {
  CalendarReadInput,
  CalendarReadRequest,
  ReadonlyRunFinish,
  ReadonlyRunStart,
  ReadonlyRunView,
  ToolResult,
} from "../generated/protocol";
import { validateProtocol } from "../protocol/validate";
import { createAgentUUID } from "./uuid";

export interface AgentClientConfig {
  baseURL: string;
  deviceId: string;
  fetchImpl?: typeof fetch;
}

export interface AgentClient {
  create(input: ReadonlyRunStart, signal: AbortSignal): Promise<ReadonlyRunView>;
  get(runId: string, signal: AbortSignal): Promise<ReadonlyRunView>;
  cancel(runId: string, version: number, signal: AbortSignal): Promise<ReadonlyRunView>;
  finish(runId: string, version: number, result: ReadonlyRunFinish, signal: AbortSignal): Promise<ReadonlyRunView>;
  calendar(runId: string, callId: string, input: CalendarReadInput, signal: AbortSignal): Promise<ToolResult>;
}

function explicitBaseURL(value: string): string {
  const normalized = value.trim().replace(/\/+$/, "");
  if (normalized === "") throw new Error("agent baseURL is required");
  return normalized;
}

function runPath(runId?: string): string {
  return `/agent/runs${runId === undefined ? "" : `/${encodeURIComponent(runId)}`}`;
}

export function createAgentClient(config: AgentClientConfig): AgentClient {
  const baseURL = explicitBaseURL(config.baseURL);
  const fetchImpl = config.fetchImpl ?? fetch;

  const request = async <T>(
    path: string,
    signal: AbortSignal,
    definition: "ReadonlyRunView" | "ToolResult",
    options: { method?: "GET" | "POST"; json?: unknown; version?: number; mutation?: boolean } = {},
  ): Promise<T> => {
    const headers = new Headers({ Accept: "application/json" });
    headers.set("X-Request-ID", createAgentUUID());
    headers.set("X-Device-ID", config.deviceId);
    if (options.method === "POST") headers.set("Content-Type", "application/json");
    if (options.mutation) headers.set("Idempotency-Key", createAgentUUID());
    if (options.version !== undefined) headers.set("If-Match", `"${options.version}"`);

    const response = await fetchImpl(`${baseURL}${path}`, {
      method: options.method ?? "GET",
      credentials: "include",
      cache: "no-store",
      signal,
      headers,
      ...(options.method === "POST" ? { body: JSON.stringify(options.json ?? {}) } : {}),
    });
    const value = await readResponse(response);
    if (!response.ok) throw new ApiError(response.status, errorBody(value), response.headers.get("Retry-After"));
    return validateProtocol<T>(definition, value);
  };

  return {
    create(input, signal) {
      const body = structuredClone(validateProtocol<ReadonlyRunStart>("ReadonlyRunStart", structuredClone(input)));
      return request(runPath(), signal, "ReadonlyRunView", { method: "POST", json: body, mutation: true });
    },
    get(runId, signal) {
      return request(runPath(runId), signal, "ReadonlyRunView");
    },
    cancel(runId, version, signal) {
      return request(`${runPath(runId)}/cancel`, signal, "ReadonlyRunView", {
        method: "POST", json: {}, version, mutation: true,
      });
    },
    finish(runId, version, result, signal) {
      const body = structuredClone(validateProtocol<ReadonlyRunFinish>("ReadonlyRunFinish", structuredClone(result)));
      return request(`${runPath(runId)}/finish`, signal, "ReadonlyRunView", {
        method: "POST", json: body, version, mutation: true,
      });
    },
    calendar(runId, callId, input, signal) {
      const body = validateProtocol<CalendarReadRequest>("CalendarReadRequest", {
        callId,
        input: structuredClone(input),
      });
      return request(`${runPath(runId)}/tools/calendar-read`, signal, "ToolResult", {
        method: "POST", json: body,
      });
    },
  };
}
