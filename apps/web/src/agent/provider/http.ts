import { ApiError, errorBody, readResponse } from "../../api/http";
import { createAgentUUID } from "../api/uuid";
import type { ModelTurnRequest } from "../generated/protocol";
import { validateProtocol } from "../protocol/validate";
import type { ProviderGateway } from "./provider";
import { parseProviderSSE } from "./sse";

export interface HttpProviderConfig {
  baseURL: string;
  deviceId: string;
  fetchImpl?: typeof fetch;
}

function explicitBaseURL(value: string): string {
  const normalized = value.trim().replace(/\/+$/, "");
  if (normalized === "") throw new Error("provider baseURL is required");
  return normalized;
}

export function createHttpProvider(config: HttpProviderConfig): ProviderGateway {
  const baseURL = explicitBaseURL(config.baseURL);
  const fetchImpl = config.fetchImpl ?? fetch;
  return {
    async *stream(source, signal) {
      const request = structuredClone(validateProtocol<ModelTurnRequest>("ModelTurnRequest", structuredClone(source)));
      const headers = new Headers({
        Accept: "text/event-stream",
        "Content-Type": "application/json",
        "X-Device-ID": config.deviceId,
        "X-Request-ID": createAgentUUID(),
      });
      const response = await fetchImpl(
        `${baseURL}/agent/runs/${encodeURIComponent(request.runId)}/turns/${encodeURIComponent(request.turnId)}/stream`,
        {
          method: "POST",
          credentials: "include",
          cache: "no-store",
          signal,
          headers,
          body: JSON.stringify(request),
        },
      );
      if (!response.ok) {
        const value = await readResponse(response);
        throw new ApiError(response.status, errorBody(value), response.headers.get("Retry-After"));
      }
      const contentType = response.headers.get("Content-Type") ?? "";
      if (!/^text\/event-stream(?:\s*;|$)/i.test(contentType)) {
        throw new Error("provider response must use text/event-stream");
      }
      if (!response.body) throw new Error("provider response has no SSE body");
      yield* parseProviderSSE(response.body, { runId: request.runId, turnId: request.turnId }, signal);
    },
  };
}
