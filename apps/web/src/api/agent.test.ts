import { describe, expect, it, vi } from "vitest";
import { acceptAgentChange, createAgentRun, getAgentRun, listAgentRuns, rejectAgentChange, stopAgentRun } from "./agent";

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("agent API client", () => {
  it("创建运行携带结构化范围、设备和幂等键", async () => {
    const deviceId = crypto.randomUUID();
    const mutationId = crypto.randomUUID();
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ id: crypto.randomUUID(), status: "ready", version: 1 }, 201));
    vi.stubGlobal("fetch", fetchMock);

    await createAgentRun({
      intent: "安排本周重点",
      actionMode: "confirm",
      scope: { domains: ["goals", "tasks"], entityIds: [] },
    }, { deviceId, mutationId });

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/agent-runs");
    const request = fetchMock.mock.calls[0][1] as RequestInit;
    const headers = new Headers(request.headers);
    expect(headers.get("X-Device-ID")).toBe(deviceId);
    expect(headers.get("Idempotency-Key")).toBe(mutationId);
    expect(JSON.parse(String(request.body))).toMatchObject({ scope: { domains: ["goals", "tasks"] } });
  });

  it("运行列表和详情使用游标协议", async () => {
    const runId = crypto.randomUUID();
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ runs: [], nextCursor: "next", hasMore: true }))
      .mockResolvedValueOnce(jsonResponse({ id: runId, status: "completed", version: 2 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listAgentRuns({ cursor: "opaque+/=", limit: 25 })).resolves.toMatchObject({ nextCursor: "next", hasMore: true });
    await getAgentRun(runId);

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/agent-runs?cursor=opaque%2B%2F%3D&limit=25");
    expect(fetchMock.mock.calls[1][0]).toBe(`/api/v1/agent-runs/${runId}`);
  });

  it("停止、接受和拒绝都携带实体版本与独立幂等键", async () => {
    const deviceId = crypto.randomUUID();
    const runId = crypto.randomUUID();
    const acceptedId = crypto.randomUUID();
    const rejectedId = crypto.randomUUID();
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ id: runId, status: "stopped", version: 3 }))
      .mockResolvedValueOnce(jsonResponse({ change: { id: acceptedId, status: "applied", version: 2 } }))
      .mockResolvedValueOnce(jsonResponse({ change: { id: rejectedId, status: "rejected", version: 2 } }));
    vi.stubGlobal("fetch", fetchMock);

    await stopAgentRun(runId, 2, { deviceId, mutationId: crypto.randomUUID() });
    await acceptAgentChange(acceptedId, 1, { deviceId, mutationId: crypto.randomUUID() });
    await rejectAgentChange(rejectedId, 1, { deviceId, mutationId: crypto.randomUUID() });

    for (const call of fetchMock.mock.calls) {
      const headers = new Headers((call[1] as RequestInit).headers);
      expect(headers.get("X-Device-ID")).toBe(deviceId);
      expect(headers.get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    }
    expect(fetchMock.mock.calls[0][0]).toBe(`/api/v1/agent-runs/${runId}/stop`);
    expect(new Headers((fetchMock.mock.calls[0][1] as RequestInit).headers).get("If-Match")).toBe('"2"');
    expect(fetchMock.mock.calls[1][0]).toBe(`/api/v1/agent-changes/${acceptedId}/accept`);
    expect(new Headers((fetchMock.mock.calls[1][1] as RequestInit).headers).get("If-Match")).toBe('"1"');
    expect(fetchMock.mock.calls[2][0]).toBe(`/api/v1/agent-changes/${rejectedId}/reject`);
    expect(new Headers((fetchMock.mock.calls[2][1] as RequestInit).headers).get("If-Match")).toBe('"1"');
  });
});
