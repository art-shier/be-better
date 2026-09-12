import { ApiError } from "../../api/http";
import type { AgentClient } from "../api/client";
import { calendarReadSpec } from "../assets/builtin";
import type { AgentError, CalendarReadInput, ToolResult } from "../generated/protocol";
import { validateProtocol } from "../protocol/validate";
import type { ToolBinding } from "./registry";

function failure(code: AgentError["code"], message: string): ToolResult {
  return validateProtocol<ToolResult>("ToolResult", {
    ok: false,
    error: { code, message, retryable: false },
  });
}

function mappedError(error: unknown): ToolResult {
  if (!(error instanceof ApiError)) return failure("tool_failed", "calendar request failed");
  if ([400, 415, 422, 428].includes(error.status)) return failure("validation_failed", "calendar request was rejected");
  if ([401, 403, 404].includes(error.status)) return failure("permission_denied", "calendar access was denied");
  if (error.status === 408) return failure("timeout", "calendar request timed out");
  if (error.status === 409) return failure("version_conflict", "calendar Run state changed");
  return failure("tool_failed", "calendar request failed");
}

export function createCalendarHttpBinding(client: AgentClient): ToolBinding {
  return {
    spec: calendarReadSpec(),
    async invoke(input, context) {
      try {
        return await client.calendar(context.runId, context.callId, input as unknown as CalendarReadInput, context.signal);
      } catch (error) {
        if (context.signal.aborted) throw context.signal.reason ?? error;
        return mappedError(error);
      }
    },
  };
}
