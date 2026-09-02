import { describe, expect, it, vi } from "vitest";
import { completePasswordReset, registerAccount, requestPasswordReset, resendVerification, verifyEmail } from "./auth";

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("account API", () => {
  it("注册直接取得正式 Session，且不上传整份 AppData", async () => {
    const response = {
      user: { id: crypto.randomUUID(), email: "new@example.com", displayName: "新用户", status: "active" },
      expiresAt: "2026-09-27T00:00:00Z",
      verificationRequired: false,
    };
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(response, 201));
    vi.stubGlobal("fetch", fetchMock);

    await expect(registerAccount({ displayName: "新用户", email: "new@example.com", password: "safe-password-123" })).resolves.toEqual(response);
    const request = fetchMock.mock.calls[0][1] as RequestInit;
    expect(JSON.parse(String(request.body))).toEqual({ displayName: "新用户", email: "new@example.com", password: "safe-password-123" });
  });

  it("邮箱验证接口未接入时透传明确错误", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ error: {
      code: "EMAIL_VERIFICATION_NOT_AVAILABLE", message: "邮箱验证功能暂未接入",
    } }, 503));
    vi.stubGlobal("fetch", fetchMock);

    await expect(verifyEmail("verify-token")).rejects.toThrow("邮箱验证功能暂未接入");
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/auth/verify-email");
  });

  it("重发验证邮件和密码重置接口未接入时透传明确错误", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ error: { code: "EMAIL_VERIFICATION_NOT_AVAILABLE", message: "邮箱验证功能暂未接入" } }, 503))
      .mockResolvedValueOnce(jsonResponse({ error: { code: "PASSWORD_RESET_NOT_AVAILABLE", message: "忘记密码功能暂未接入" } }, 503))
      .mockResolvedValueOnce(jsonResponse({ error: { code: "PASSWORD_RESET_NOT_AVAILABLE", message: "忘记密码功能暂未接入" } }, 503));
    vi.stubGlobal("fetch", fetchMock);

    await expect(resendVerification("new@example.com")).rejects.toThrow("邮箱验证功能暂未接入");
    await expect(requestPasswordReset("new@example.com")).rejects.toThrow("忘记密码功能暂未接入");
    await expect(completePasswordReset("reset-token", "new-safe-password")).rejects.toThrow("忘记密码功能暂未接入");

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/auth/resend-verification",
      "/api/v1/auth/password-reset/request",
      "/api/v1/auth/password-reset/complete",
    ]);
  });
});
