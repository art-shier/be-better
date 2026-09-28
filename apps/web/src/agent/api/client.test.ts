import { afterEach, describe, expect, it, vi } from "vitest";

import type { ReadonlyRunStart, ReadonlyRunView, ToolResult } from "../generated/protocol";
import { createAgentClient } from "./client";

const runId = "550e8400-e29b-41d4-a716-446655440000";
const deviceId = "550e8400-e29b-41d4-a716-446655440001";
const input: ReadonlyRunStart = {
  intent: "Summarize today",
  executionMode: "foreground",
  scope: { domains: ["calendar"], from: "2026-09-05T00:00:00Z", to: "2026-09-06T00:00:00Z" },
  timezone: "Asia/Shanghai",
  modelProfile: "client/default",
};
const view: ReadonlyRunView = {
  protocolVersion: "2.0",
  runId,
  executionMode: "foreground",
  status: "ready",
  version: 1,
  capabilitySnapshot: {
    runtimeVersion: "2.0.0",
    executionMode: "foreground",
    toolIds: ["dayorder.calendar.read"],
    skills: [{ name: "calendar-overview", version: "1.0.0", digest: "a".repeat(64) }],
    scope: input.scope,
  },
  budget: { maxSteps: 4, maxTokens: 1000, maxDurationMs: 30_000, maxWorkers: 1, maxConcurrency: 1, maxRepeatedToolCalls: 2 },
  modelProfile: "client/default",
  serverNow: "2026-09-05T00:00:00Z",
  deadlineAt: "2026-09-05T00:00:30Z",
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  usageComplete: true,
  resultOrigin: "client_reported",
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

afterEach(() => vi.unstubAllGlobals());

describe("createAgentClient", () => {
  it("requires an explicit base URL", () => {
    expect(() => createAgentClient({ baseURL: "   ", deviceId })).toThrow(/baseURL/i);
  });

  it("uses the reviewed paths, browser fetch settings, strict headers, and distinct mutation IDs", async () => {
    const calendarResult: ToolResult = { ok: true, data: { events: [], window: { start: input.scope.from, end: input.scope.to }, hasMore: false, nextCursor: null } };
    const fetchImpl = vi.fn()
      .mockResolvedValueOnce(json(view, 201))
      .mockResolvedValueOnce(json({ ...view, version: 2 }))
      .mockResolvedValueOnce(json({ ...view, status: "stopped", version: 3 }))
      .mockResolvedValueOnce(json({ ...view, status: "completed", version: 3 }))
      .mockResolvedValueOnce(json(calendarResult));
    const client = createAgentClient({ baseURL: " https://example.test/api/v1/ ", deviceId, fetchImpl });
    const signal = new AbortController().signal;

    await client.create(input, signal);
    await client.get(runId, signal);
    await client.cancel(runId, 2, signal);
    await client.finish(runId, 2, { phase: "completed", summary: "Done", steps: [] }, signal);
    await client.calendar(runId, "call-1", { start: input.scope.from!, end: input.scope.to! }, signal);

    expect(fetchImpl.mock.calls.map(([url]) => url)).toEqual([
      "https://example.test/api/v1/agent/runs",
      `https://example.test/api/v1/agent/runs/${runId}`,
      `https://example.test/api/v1/agent/runs/${runId}/cancel`,
      `https://example.test/api/v1/agent/runs/${runId}/finish`,
      `https://example.test/api/v1/agent/runs/${runId}/tools/calendar-read`,
    ]);
    const requests = fetchImpl.mock.calls.map(([, init]) => init as RequestInit);
    for (const request of requests) {
      expect(request).toMatchObject({ credentials: "include", cache: "no-store", signal });
      const headers = new Headers(request.headers);
      expect(headers.get("X-Device-ID")).toBe(deviceId);
      expect(headers.get("X-Request-ID")).toMatch(/^[0-9a-f-]{36}$/);
    }
    expect(new Headers(requests[0].headers).get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(new Headers(requests[1].headers).get("Idempotency-Key")).toBeNull();
    expect(new Headers(requests[2].headers).get("If-Match")).toBe('"2"');
    expect(new Headers(requests[3].headers).get("If-Match")).toBe('"2"');
    const mutationIds = [0, 2, 3].map((index) => new Headers(requests[index].headers).get("Idempotency-Key"));
    expect(new Set(mutationIds).size).toBe(3);
    expect(JSON.parse(String(requests[2].body))).toEqual({});
    expect(JSON.parse(String(requests[4].body))).toEqual({ callId: "call-1", input: { start: input.scope.from, end: input.scope.to } });
  });

  it("rejects a schema-invalid successful response", async () => {
    const client = createAgentClient({
      baseURL: "/api/v1",
      deviceId,
      fetchImpl: vi.fn().mockResolvedValue(json({ ...view, protocolVersion: "future" })),
    });
    await expect(client.get(runId, new AbortController().signal)).rejects.toThrow(/validation_failed/i);
  });

  it("does not silently retry a failed mutation fetch", async () => {
    const fetchImpl = vi.fn().mockRejectedValue(new TypeError("network failed"));
    const client = createAgentClient({ baseURL: "/api/v1", deviceId, fetchImpl });

    await expect(client.finish(runId, 2, { phase: "completed", summary: "Done", steps: [] }, new AbortController().signal))
      .rejects.toThrow("network failed");
    expect(fetchImpl).toHaveBeenCalledTimes(1);
    expect(new Headers((fetchImpl.mock.calls[0][1] as RequestInit).headers).get("Idempotency-Key"))
      .toMatch(/^[0-9a-f-]{36}$/);
  });

  it("uses secure random bytes for distinct IDs when crypto.randomUUID is unavailable", async () => {
    let seed = 0;
    vi.stubGlobal("crypto", {
      getRandomValues<T extends ArrayBufferView>(array: T): T {
        const bytes = new Uint8Array(array.buffer, array.byteOffset, array.byteLength);
        for (let index = 0; index < bytes.length; index += 1) bytes[index] = (seed + index) & 0xff;
        seed += 17;
        return array;
      },
    });
    const fetchImpl = vi.fn().mockImplementation(async () => json(view, 201));
    const client = createAgentClient({ baseURL: "/api/v1", deviceId, fetchImpl });

    await client.create(input, new AbortController().signal);
    await client.create(input, new AbortController().signal);

    const headers = fetchImpl.mock.calls.map(([, init]) => new Headers((init as RequestInit).headers));
    expect(headers[0].get("X-Request-ID")).not.toBe(headers[1].get("X-Request-ID"));
    expect(headers[0].get("Idempotency-Key")).not.toBe(headers[1].get("Idempotency-Key"));
    for (const value of headers.flatMap((header) => [header.get("X-Request-ID"), header.get("Idempotency-Key")])) {
      expect(value).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    }
  });

  it("rejects before fetch when no secure UUID source is available", async () => {
    vi.stubGlobal("crypto", {});
    const fetchImpl = vi.fn();
    const client = createAgentClient({ baseURL: "/api/v1", deviceId, fetchImpl });

    await expect(client.create(input, new AbortController().signal)).rejects.toThrow(/secure|UUID|crypto/i);
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});
