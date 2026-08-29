import { describe, expect, it, vi } from "vitest";
import { ApiError, apiRequest, resolveApiBaseUrl } from "./http";

const jsonResponse = (body: unknown, status = 200, headers: Record<string, string> = {}) => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json", ...headers },
});

describe("resolveApiBaseUrl", () => {
  it("uses the development fallback when no URL is configured", () => {
    expect(resolveApiBaseUrl(undefined, false)).toBe("/api/v1");
  });

  it("uses the production fallback when no URL is configured", () => {
    expect(resolveApiBaseUrl(undefined, true)).toBe("https://better-api.shier.art/api/v1");
  });

  it("uses the production fallback when the configured URL is blank", () => {
    expect(resolveApiBaseUrl("   ", true)).toBe("https://better-api.shier.art/api/v1");
  });

  it("trims and removes one trailing slash from an explicit URL in development", () => {
    expect(resolveApiBaseUrl("  https://staging.example.test/api/v1/  ", false)).toBe("https://staging.example.test/api/v1");
  });

  it("trims and removes one trailing slash from an explicit URL in production", () => {
    expect(resolveApiBaseUrl("  https://staging.example.test/api/v1/  ", true)).toBe("https://staging.example.test/api/v1");
  });
});

describe("apiRequest", () => {
  it("发送 Cookie、请求 ID 和 JSON，并读取成功响应", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ ok: true }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(apiRequest<{ ok: boolean }>("/probe", { method: "POST", json: { value: 1 } })).resolves.toEqual({ ok: true });

    expect(fetchMock).toHaveBeenCalledWith("/api/v1/probe", expect.objectContaining({
      method: "POST",
      credentials: "include",
      cache: "no-store",
      body: JSON.stringify({ value: 1 }),
    }));
    const headers = new Headers((fetchMock.mock.calls[0][1] as RequestInit).headers);
    expect(headers.get("Accept")).toBe("application/json");
    expect(headers.get("Content-Type")).toBe("application/json");
    expect(headers.get("X-Request-ID")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("解析统一错误 envelope 并分类可恢复错误", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ error: {
      code: "LOGIN_RATE_LIMITED",
      message: "请稍后再试",
      fields: { email: "请求过多" },
      retryable: true,
      requestId: "request-123",
    } }, 429, { "Retry-After": "17" })));

    const error = await apiRequest("/probe").catch((reason: unknown) => reason);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({
      status: 429,
      code: "LOGIN_RATE_LIMITED",
      category: "rate-limit",
      retryable: true,
      requestId: "request-123",
      retryAfterSeconds: 17,
      fields: { email: "请求过多" },
    });
  });

  it("对空的 204 响应返回 undefined", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 204 })));
    await expect(apiRequest<void>("/probe", { method: "DELETE" })).resolves.toBeUndefined();
  });
});
