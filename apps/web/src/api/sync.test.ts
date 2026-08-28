import { describe, expect, it, vi } from "vitest";
import { getSyncBootstrap, getSyncChanges, postSyncMutations, registerDevice } from "./sync";

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("sync API client", () => {
  it("先注册客户端生成的设备 ID", async () => {
    const deviceId = crypto.randomUUID();
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ device: { id: deviceId } }, 201));
    vi.stubGlobal("fetch", fetchMock);

    await registerDevice(deviceId, { deviceName: "Chrome", platform: "web" });

    expect(fetchMock.mock.calls[0][0]).toBe(`/api/v1/users/me/devices/${deviceId}`);
    expect(new Headers((fetchMock.mock.calls[0][1] as RequestInit).headers).get("X-Device-ID")).toBeNull();
  });

  it("Bootstrap 和 Changes 都绑定已注册设备", async () => {
    const deviceId = crypto.randomUUID();
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ cursor: "high-water" }))
      .mockResolvedValueOnce(jsonResponse({ changes: [], nextCursor: "next", hasMore: false }));
    vi.stubGlobal("fetch", fetchMock);

    await getSyncBootstrap(deviceId);
    await getSyncChanges(deviceId, "high-water", 250);

    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/sync/changes?cursor=high-water&limit=250");
    expect(new Headers((fetchMock.mock.calls[0][1] as RequestInit).headers).get("X-Device-ID")).toBe(deviceId);
  });

  it("批量发送本地有序 Mutation", async () => {
    const deviceId = crypto.randomUUID();
    const mutationId = crypto.randomUUID();
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ results: [{ mutationId, status: "applied", data: { id: crypto.randomUUID(), version: 1 } }] }));
    vi.stubGlobal("fetch", fetchMock);

    await postSyncMutations(deviceId, [{
      mutationId, sequence: 1, entityType: "task", entityId: crypto.randomUUID(), operation: "create", baseVersion: 0, payload: { title: "离线任务" },
    }]);

    const request = fetchMock.mock.calls[0][1] as RequestInit;
    expect(JSON.parse(String(request.body)).mutations[0]).toMatchObject({ mutationId, sequence: 1, operation: "create" });
    expect(new Headers(request.headers).get("X-Device-ID")).toBe(deviceId);
  });
});
