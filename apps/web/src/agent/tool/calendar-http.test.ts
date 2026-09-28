import { describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/http";
import type { AgentClient } from "../api/client";
import type { ToolResult } from "../generated/protocol";
import { createCalendarHttpBinding } from "./calendar-http";

const input = { start: "2026-09-05T00:00:00Z", end: "2026-09-06T00:00:00Z" };

function clientWithCalendar(calendar: AgentClient["calendar"]): AgentClient {
  return { create: vi.fn(), get: vi.fn(), cancel: vi.fn(), finish: vi.fn(), calendar };
}

describe("createCalendarHttpBinding", () => {
  it("forwards the Runtime run/call identity and exact signal", async () => {
    const expected: ToolResult = { ok: true, data: { events: [], window: input, hasMore: false, nextCursor: null } };
    const calendar = vi.fn().mockResolvedValue(expected);
    const signal = new AbortController().signal;
    const result = await createCalendarHttpBinding(clientWithCalendar(calendar)).invoke(input, { runId: "run-1", callId: "call-1", signal });

    expect(result).toEqual(expected);
    expect(calendar).toHaveBeenCalledWith("run-1", "call-1", input, signal);
  });

  it.each([
    [400, "validation_failed"], [415, "validation_failed"], [422, "validation_failed"], [428, "validation_failed"],
    [401, "permission_denied"], [403, "permission_denied"], [404, "permission_denied"],
    [408, "timeout"], [409, "version_conflict"], [429, "tool_failed"], [503, "tool_failed"], [418, "tool_failed"],
  ])("maps HTTP %i to a controlled %s result without exposing server text", async (status, code) => {
    const calendar = vi.fn().mockRejectedValue(new ApiError(status, { message: "sensitive server detail" }));
    const result = await createCalendarHttpBinding(clientWithCalendar(calendar)).invoke(input, {
      runId: "run-1", callId: "call-1", signal: new AbortController().signal,
    });
    expect(result).toMatchObject({ ok: false, error: { code, retryable: false } });
    expect(result.error?.message).not.toContain("sensitive");
  });

  it("turns a non-cancellation network failure into tool_failed", async () => {
    const binding = createCalendarHttpBinding(clientWithCalendar(vi.fn().mockRejectedValue(new TypeError("network address"))));
    await expect(binding.invoke(input, { runId: "r", callId: "c", signal: new AbortController().signal })).resolves.toMatchObject({
      ok: false, error: { code: "tool_failed", retryable: false },
    });
  });

  it("propagates an active caller abort instead of manufacturing tool_failed", async () => {
    const controller = new AbortController();
    const reason = new DOMException("stopped", "AbortError");
    controller.abort(reason);
    const binding = createCalendarHttpBinding(clientWithCalendar(vi.fn().mockRejectedValue(reason)));
    await expect(binding.invoke(input, { runId: "r", callId: "c", signal: controller.signal })).rejects.toBe(reason);
  });
});
