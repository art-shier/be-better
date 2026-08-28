import { describe, expect, it, vi } from "vitest";
import { createSeedData } from "../domain/seed";
import { ApiError, getRemoteState, putRemoteState } from "./client";

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("state API client", () => {
  it("读取服务端状态且禁用 HTTP 缓存", async () => {
    const data = createSeedData();
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ revision: 3, data, updatedAt: "2026-08-27T08:00:00Z" }));
    vi.stubGlobal("fetch", fetchMock);

    const state = await getRemoteState();
    expect(state.revision).toBe(3);
    expect(state.data.goals[0].id).toBe(data.goals[0].id);
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/state", expect.objectContaining({ method: "GET", cache: "no-store" }));
  });

  it("写入时携带期望 revision", async () => {
    const data = createSeedData();
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ revision: 8, data, updatedAt: "2026-08-27T08:00:00Z" }));
    vi.stubGlobal("fetch", fetchMock);

    await putRemoteState(data, 7);
    const request = fetchMock.mock.calls[0][1] as RequestInit;
    expect(JSON.parse(String(request.body))).toEqual({ expectedRevision: 7, data });
  });

  it("保留 404 与 revision conflict 的错误信息", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ code: "STATE_NOT_FOUND", message: "missing" }, 404))
      .mockResolvedValueOnce(jsonResponse({ code: "REVISION_CONFLICT", message: "changed", currentRevision: 9 }, 409));
    vi.stubGlobal("fetch", fetchMock);

    await expect(getRemoteState()).rejects.toMatchObject({ status: 404, code: "STATE_NOT_FOUND" } satisfies Partial<ApiError>);
    await expect(putRemoteState(createSeedData(), 8)).rejects.toMatchObject({ status: 409, currentRevision: 9 } satisfies Partial<ApiError>);
  });
});
