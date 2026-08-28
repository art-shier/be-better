import "fake-indexeddb/auto";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AuthDialog } from "../components/AuthDialog";
import { createSeedData } from "../domain/seed";
import type { AppData } from "../domain/types";
import { getCachedEntities, hasAccountCache, putCachedEntity } from "../offline/cache";
import { deleteDayOrderDB } from "../offline/db";
import { AppStoreProvider } from "../store/AppStore";
import { GUEST_STORAGE_KEY, LAST_ACCOUNT_KEY } from "../store/storage";
import { UiProvider } from "../ui/UiProvider";
import { AuthProvider, useAuth } from "./AuthProvider";

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

function Probe() {
  const auth = useAuth();
  return <>
    <output data-testid="auth-mode">{auth.mode}</output>
    <button type="button" onClick={() => auth.openAuth("account")}>打开账户</button>
    {auth.pendingVerification && <button type="button" onClick={() => void auth.verifyEmail("verify-token")}>验证邮箱</button>}
    {auth.user && <button type="button" onClick={() => void auth.logout()}>退出账户</button>}
  </>;
}

function GuestAuthHarness({ sessionCheckEnabled = false, guestMigrator }: { sessionCheckEnabled?: boolean; guestMigrator?: (accountId: string, data: AppData) => Promise<void> }) {
  return <AuthProvider sessionCheckEnabled={sessionCheckEnabled} guestMigrator={guestMigrator}>
    <AppStoreProvider><UiProvider><Probe /><AuthDialog /></UiProvider></AppStoreProvider>
  </AuthProvider>;
}

describe("账户状态与游客迁移", () => {
  beforeEach(async () => {
    localStorage.clear();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    await deleteDayOrderDB();
  });

  it("注册后等待邮箱验证，验证成功才建立会话并启动游客迁移", async () => {
    const seed = createSeedData();
    localStorage.setItem(GUEST_STORAGE_KEY, JSON.stringify(seed));
    const user = { id: crypto.randomUUID(), email: "new@example.com", displayName: "新用户", status: "pending_verification" };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ user, verificationRequired: true }, 201))
      .mockResolvedValueOnce(jsonResponse({ user: { ...user, status: "active" }, expiresAt: "2026-09-26T08:00:00Z" }));
    vi.stubGlobal("fetch", fetchMock);
    const guestMigrator = vi.fn().mockResolvedValue(undefined);
    const actions = userEvent.setup();
    render(<GuestAuthHarness guestMigrator={guestMigrator} />);

    await actions.click(screen.getByRole("button", { name: "打开账户" }));
    await actions.click(screen.getByRole("tab", { name: "注册" }));
    await actions.type(screen.getByLabelText("称呼"), "新用户");
    await actions.type(screen.getByLabelText("邮箱"), "new@example.com");
    await actions.type(screen.getByLabelText("密码"), "safe-password-123");
    await actions.click(screen.getByRole("button", { name: "创建账户" }));

    await waitFor(() => expect(screen.getByTestId("auth-mode")).toHaveTextContent("verification-pending"));
    expect(localStorage.getItem(GUEST_STORAGE_KEY)).not.toBeNull();
    const request = fetchMock.mock.calls[0][1] as RequestInit;
    expect(JSON.parse(String(request.body))).toEqual({ displayName: "新用户", email: "new@example.com", password: "safe-password-123" });

    await actions.click(screen.getByRole("button", { name: "验证邮箱" }));
    await waitFor(() => expect(screen.getByTestId("auth-mode")).toHaveTextContent("authenticated"));
    expect(guestMigrator).toHaveBeenCalledWith(user.id, expect.objectContaining({ goals: seed.goals }));
  });

  it("注册失败保留完整游客数据", async () => {
    const seed = createSeedData();
    localStorage.setItem(GUEST_STORAGE_KEY, JSON.stringify(seed));
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ error: { code: "INTERNAL_ERROR", message: "账户未创建，本机数据没有变化" } }, 500)));
    const actions = userEvent.setup();
    render(<GuestAuthHarness />);
    await actions.click(screen.getByRole("button", { name: "打开账户" }));
    await actions.click(screen.getByRole("tab", { name: "注册" }));
    await actions.type(screen.getByLabelText("称呼"), "新用户");
    await actions.type(screen.getByLabelText("邮箱"), "new@example.com");
    await actions.type(screen.getByLabelText("密码"), "safe-password-123");
    await actions.click(screen.getByRole("button", { name: "创建账户" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("本机数据没有变化");
    expect(JSON.parse(localStorage.getItem(GUEST_STORAGE_KEY) ?? "null").tasks).toHaveLength(seed.tasks.length);
  });

  it("登录已有账户不会合并或删除游客数据", async () => {
    const seed = createSeedData();
    localStorage.setItem(GUEST_STORAGE_KEY, JSON.stringify(seed));
    const user = { id: crypto.randomUUID(), email: "existing@example.com", displayName: "已有用户" };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ user, expiresAt: "2026-09-26T08:00:00Z" })));
    const actions = userEvent.setup();
    render(<GuestAuthHarness />);
    await actions.click(screen.getByRole("button", { name: "打开账户" }));
    await actions.type(screen.getByLabelText("邮箱"), "existing@example.com");
    await actions.type(screen.getByLabelText("密码"), "safe-password-123");
    await actions.click(screen.getByRole("button", { name: "登录账户" }));
    await waitFor(() => expect(screen.getByTestId("auth-mode")).toHaveTextContent("authenticated"));
    expect(localStorage.getItem(GUEST_STORAGE_KEY)).not.toBeNull();
    expect(await hasAccountCache(user.id)).toBe(false);
  });

  it("在线校验为 401 时保留账户缓存并进入过期状态", async () => {
    const seed = createSeedData();
    const user = { id: crypto.randomUUID(), email: "cached@example.com", displayName: "缓存用户" };
    localStorage.setItem(LAST_ACCOUNT_KEY, JSON.stringify(user));
    await putCachedEntity(user.id, "goal", seed.goals[0]);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ error: { code: "AUTH_REQUIRED", message: "expired" } }, 401)));
    render(<GuestAuthHarness sessionCheckEnabled />);
    await waitFor(() => expect(screen.getByTestId("auth-mode")).toHaveTextContent("expired"));
    expect(await hasAccountCache(user.id)).toBe(true);
    expect(screen.getByRole("dialog", { name: "重新验证账户" })).toBeInTheDocument();
  });

  it("退出登录只清理当前账户的 IndexedDB 缓存", async () => {
    const seed = createSeedData();
    const current = { id: crypto.randomUUID(), email: "current@example.com", displayName: "当前用户" };
    const otherId = crypto.randomUUID();
    await putCachedEntity(current.id, "goal", seed.goals[0]);
    await putCachedEntity(otherId, "goal", seed.goals[1]);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 204 })));
    const actions = userEvent.setup();

    render(<AuthProvider initialSession={{ user: current, expiresAt: "2026-09-26T08:00:00Z" }}><Probe /></AuthProvider>);
    await actions.click(screen.getByRole("button", { name: "退出账户" }));

    await waitFor(() => expect(screen.getByTestId("auth-mode")).toHaveTextContent("guest"));
    expect(await getCachedEntities(current.id, "goal")).toEqual([]);
    expect(await getCachedEntities(otherId, "goal")).toHaveLength(1);
  });
});
