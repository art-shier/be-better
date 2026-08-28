import { describe, expect, it, vi } from "vitest";
import { createResource, deleteResource, listCalendarEvents, listDailyReviews, listGoals, patchResource } from "./resources";

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("resource API client", () => {
  it("资源列表使用不透明游标和限制", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ goals: [], nextCursor: "next", hasMore: true }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listGoals({ cursor: "opaque+/=", limit: 75 })).resolves.toMatchObject({ nextCursor: "next", hasMore: true });
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/goals?cursor=opaque%2B%2F%3D&limit=75");
  });

  it("日程与每日复盘快照接口支持游标分页", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ events: [], nextCursor: "event-next", hasMore: true }))
      .mockResolvedValueOnce(jsonResponse({ reviews: [], nextCursor: "review-next", hasMore: true }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listCalendarEvents({ cursor: "event cursor", limit: 100 })).resolves.toMatchObject({ nextCursor: "event-next", hasMore: true });
    await expect(listDailyReviews({ cursor: "review cursor", limit: 100 })).resolves.toMatchObject({ nextCursor: "review-next", hasMore: true });
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/calendar-events?cursor=event+cursor&limit=100");
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/daily-reviews?cursor=review+cursor&limit=100");
  });

  it("创建资源携带设备和幂等键", async () => {
    const deviceId = crypto.randomUUID();
    const mutationId = crypto.randomUUID();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ id: crypto.randomUUID(), version: 1 }, 201)));

    await createResource("/tasks", { title: "任务" }, { deviceId, mutationId });

    const request = (fetch as ReturnType<typeof vi.fn>).mock.calls[0][1] as RequestInit;
    const headers = new Headers(request.headers);
    expect(headers.get("X-Device-ID")).toBe(deviceId);
    expect(headers.get("Idempotency-Key")).toBe(mutationId);
  });

  it("修改和删除资源携带实体版本", async () => {
    const deviceId = crypto.randomUUID();
    const mutationId = crypto.randomUUID();
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ id: crypto.randomUUID(), version: 4 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await patchResource("/tasks/task-id", { title: "已修改" }, 3, { deviceId, mutationId });
    await deleteResource("/tasks/task-id", 4, { deviceId, mutationId: crypto.randomUUID() });

    const patchHeaders = new Headers((fetchMock.mock.calls[0][1] as RequestInit).headers);
    const deleteHeaders = new Headers((fetchMock.mock.calls[1][1] as RequestInit).headers);
    expect(patchHeaders.get("Content-Type")).toBe("application/merge-patch+json");
    expect(patchHeaders.get("If-Match")).toBe('"3"');
    expect(deleteHeaders.get("If-Match")).toBe('"4"');
  });
});
