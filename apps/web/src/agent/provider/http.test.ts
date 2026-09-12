import { afterEach, describe, expect, it, vi } from "vitest";

import type { ModelTurnRequest } from "../generated/protocol";
import { ApiError } from "../../api/http";
import { createHttpProvider } from "./http";

const request: ModelTurnRequest = {
  protocolVersion: "2.0",
  runId: "run-1",
  turnId: "turn-1",
  modelProfile: "client/default",
  messages: [{ role: "user", content: [{ type: "text", text: "hello" }] }],
  tools: [],
};

function terminalSSE(): Response {
  const envelope = JSON.stringify({
    protocolVersion: "2.0",
    runId: request.runId,
    turnId: request.turnId,
    sequence: 1,
    event: { type: "completed", stopReason: "end_turn", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } },
  });
  return new Response(`data: ${envelope}\n\n`, { headers: { "Content-Type": "text/event-stream; charset=utf-8" } });
}

afterEach(() => vi.unstubAllGlobals());

describe("createHttpProvider", () => {
  it("POSTs a validated model turn to the run-scoped SSE endpoint", async () => {
    const fetchImpl = vi.fn().mockResolvedValue(terminalSSE());
    const provider = createHttpProvider({ baseURL: "https://example.test/api/v1/", deviceId: "device-1", fetchImpl });
    const signal = new AbortController().signal;

    const events = [];
    for await (const event of provider.stream(request, signal)) events.push(event);
    expect(events).toHaveLength(1);
    expect(fetchImpl).toHaveBeenCalledWith(
      "https://example.test/api/v1/agent/runs/run-1/turns/turn-1/stream",
      expect.objectContaining({ method: "POST", credentials: "include", cache: "no-store", signal, body: JSON.stringify(request) }),
    );
    const headers = new Headers((fetchImpl.mock.calls[0][1] as RequestInit).headers);
    expect(headers.get("Accept")).toBe("text/event-stream");
    expect(headers.get("Content-Type")).toBe("application/json");
    expect(headers.get("X-Device-ID")).toBe("device-1");
    expect(headers.get("X-Request-ID")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("parses a JSON prepare error without trying to consume it as SSE", async () => {
    const fetchImpl = vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: "NO_SESSION", message: "raw server detail" } }), {
      status: 401,
      headers: { "Content-Type": "application/json" },
    }));
    const provider = createHttpProvider({ baseURL: "/api/v1", deviceId: "device-1", fetchImpl });

    const error = await (async () => {
      for await (const _event of provider.stream(request, new AbortController().signal)) { /* consume */ }
    })().catch((reason: unknown) => reason);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 401, code: "NO_SESSION" });
  });

  it("rejects a successful response with the wrong media type or no body", async () => {
    const wrongType = createHttpProvider({
      baseURL: "/api/v1",
      deviceId: "device-1",
      fetchImpl: vi.fn().mockResolvedValue(new Response("{}", { headers: { "Content-Type": "application/json" } })),
    });
    await expect((async () => {
      for await (const _event of wrongType.stream(request, new AbortController().signal)) { /* consume */ }
    })()).rejects.toThrow(/text\/event-stream/i);
  });

  it("uses secure random bytes for distinct request IDs when crypto.randomUUID is unavailable", async () => {
    let seed = 0;
    vi.stubGlobal("crypto", {
      getRandomValues<T extends ArrayBufferView>(array: T): T {
        const bytes = new Uint8Array(array.buffer, array.byteOffset, array.byteLength);
        for (let index = 0; index < bytes.length; index += 1) bytes[index] = (seed + index) & 0xff;
        seed += 19;
        return array;
      },
    });
    const fetchImpl = vi.fn().mockImplementation(async () => terminalSSE());
    const provider = createHttpProvider({ baseURL: "/api/v1", deviceId: "device-1", fetchImpl });

    for (let iteration = 0; iteration < 2; iteration += 1) {
      for await (const _event of provider.stream(request, new AbortController().signal)) { /* consume */ }
    }

    const ids = fetchImpl.mock.calls.map(([, init]) => new Headers((init as RequestInit).headers).get("X-Request-ID"));
    expect(ids[0]).not.toBe(ids[1]);
    expect(ids[0]).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  });

  it("rejects before fetch when no secure request ID source is available", async () => {
    vi.stubGlobal("crypto", {});
    const fetchImpl = vi.fn();
    const provider = createHttpProvider({ baseURL: "/api/v1", deviceId: "device-1", fetchImpl });

    await expect((async () => {
      for await (const _event of provider.stream(request, new AbortController().signal)) { /* consume */ }
    })()).rejects.toThrow(/secure|UUID|crypto/i);
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});
