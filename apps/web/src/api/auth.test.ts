import { describe, expect, it, vi } from "vitest";
import { completePasswordReset, registerAccount, requestPasswordReset, resendVerification, verifyEmail } from "./auth";

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("account API", () => {
  it("注册只创建待验证账号，不上传整份 AppData", async () => {
    const response = { user: { id: crypto.randomUUID(), email: "new@example.com", displayName: "新用户", status: "pending_verification" }, verificationRequired: true };
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(response, 201));
    vi.stubGlobal("fetch", fetchMock);

    await expect(registerAccount({ displayName: "新用户", email: "new@example.com", password: "safe-password-123" })).resolves.toEqual(response);
    const request = fetchMock.mock.calls[0][1] as RequestInit;
    expect(JSON.parse(String(request.body))).toEqual({ displayName: "新用户", email: "new@example.com", password: "safe-password-123" });
  });

  it("邮箱验证成功后取得正式 Session", async () => {
    const response = { user: { id: crypto.randomUUID(), email: "new@example.com", displayName: "新用户", status: "active" }, expiresAt: "2026-09-27T00:00:00Z" };
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(response));
    vi.stubGlobal("fetch", fetchMock);

    await expect(verifyEmail("verify-token")).resolves.toEqual(response);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/auth/verify-email");
  });

  it("支持重发验证邮件和密码重置完整流程", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ accepted: true }, 202))
      .mockResolvedValueOnce(jsonResponse({ accepted: true }, 202))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await resendVerification("new@example.com");
    await requestPasswordReset("new@example.com");
    await completePasswordReset("reset-token", "new-safe-password");

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/auth/resend-verification",
      "/api/v1/auth/password-reset/request",
      "/api/v1/auth/password-reset/complete",
    ]);
  });
});
