import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AuthDialog } from "../components/AuthDialog";
import { createSeedData } from "../domain/seed";
import { AppStoreProvider } from "../store/AppStore";
import { GUEST_STORAGE_KEY, LAST_ACCOUNT_KEY, LEGACY_STORAGE_KEY, userStorageKeys } from "../store/storage";
import { UiProvider } from "../ui/UiProvider";
import { AuthProvider, useAuth } from "./AuthProvider";

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

function Probe() {
  const auth = useAuth();
  return <><output data-testid="auth-mode">{auth.mode}</output><button type="button" onClick={() => auth.openAuth("account")}>打开账户</button></>;
}

function GuestAuthHarness({ sessionCheckEnabled = false }: { sessionCheckEnabled?: boolean }) {
  return <AuthProvider sessionCheckEnabled={sessionCheckEnabled}><AppStoreProvider><UiProvider><Probe /><AuthDialog /></UiProvider></AppStoreProvider></AuthProvider>;
}

describe("账户状态与游客迁移", () => {
  it("首次启动把旧全局数据移动到游客分区", () => {
    const seed = createSeedData();
    localStorage.setItem(LEGACY_STORAGE_KEY, JSON.stringify(seed));
    render(<GuestAuthHarness />);
    expect(localStorage.getItem(LEGACY_STORAGE_KEY)).toBeNull();
    expect(JSON.parse(localStorage.getItem(GUEST_STORAGE_KEY) ?? "null").goals).toHaveLength(seed.goals.length);
  });

  it("注册成功后写入用户分区并清除游客数据", async () => {
    const seed = createSeedData();
    localStorage.setItem(GUEST_STORAGE_KEY, JSON.stringify(seed));
    const state = { revision: 1, data: seed, updatedAt: "2026-08-27T08:00:00Z" };
    const user = { id: "user_new", email: "new@example.com", displayName: "新用户" };
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ user, expiresAt: "2026-09-26T08:00:00Z", state }, 201));
    vi.stubGlobal("fetch", fetchMock);
    const actions = userEvent.setup();
    render(<GuestAuthHarness />);
    await actions.click(screen.getByRole("button", { name: "打开账户" }));
    await actions.click(screen.getByRole("tab", { name: "注册" }));
    await actions.type(screen.getByLabelText("称呼"), "新用户");
    await actions.type(screen.getByLabelText("邮箱"), "new@example.com");
    await actions.type(screen.getByLabelText("密码"), "safe-password-123");
    await actions.click(screen.getByRole("button", { name: "创建账户" }));

    await waitFor(() => expect(screen.getByTestId("auth-mode")).toHaveTextContent("authenticated"));
    expect(localStorage.getItem(GUEST_STORAGE_KEY)).toBeNull();
    expect(JSON.parse(localStorage.getItem(userStorageKeys(user.id).data) ?? "null").goals).toHaveLength(seed.goals.length);
    const request = fetchMock.mock.calls[0][1] as RequestInit;
    expect(JSON.parse(String(request.body)).initialData.goals).toHaveLength(seed.goals.length);
  });

  it("注册失败保留完整游客数据", async () => {
    const seed = createSeedData();
    localStorage.setItem(GUEST_STORAGE_KEY, JSON.stringify(seed));
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ code: "INTERNAL_ERROR", message: "账户未创建，本机数据没有变化" }, 500)));
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
    const user = { id: "user_existing", email: "existing@example.com", displayName: "已有用户" };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ user, expiresAt: "2026-09-26T08:00:00Z" })));
    const actions = userEvent.setup();
    render(<GuestAuthHarness />);
    await actions.click(screen.getByRole("button", { name: "打开账户" }));
    await actions.type(screen.getByLabelText("邮箱"), "existing@example.com");
    await actions.type(screen.getByLabelText("密码"), "safe-password-123");
    await actions.click(screen.getByRole("button", { name: "登录账户" }));
    await waitFor(() => expect(screen.getByTestId("auth-mode")).toHaveTextContent("authenticated"));
    expect(localStorage.getItem(GUEST_STORAGE_KEY)).not.toBeNull();
    expect(localStorage.getItem(userStorageKeys(user.id).data)).toBeNull();
  });

  it("在线校验为 401 时保留账户缓存并进入过期状态", async () => {
    const seed = createSeedData();
    const user = { id: "user_cached", email: "cached@example.com", displayName: "缓存用户" };
    localStorage.setItem(LAST_ACCOUNT_KEY, JSON.stringify(user));
    localStorage.setItem(userStorageKeys(user.id).data, JSON.stringify(seed));
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ code: "AUTH_REQUIRED", message: "expired" }, 401)));
    render(<GuestAuthHarness sessionCheckEnabled />);
    await waitFor(() => expect(screen.getByTestId("auth-mode")).toHaveTextContent("expired"));
    expect(localStorage.getItem(userStorageKeys(user.id).data)).not.toBeNull();
    expect(screen.getByRole("dialog", { name: "重新验证账户" })).toBeInTheDocument();
  });
});
