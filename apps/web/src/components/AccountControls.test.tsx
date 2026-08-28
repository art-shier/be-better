import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { AuthProvider, useAuth } from "../auth/AuthProvider";
import { createSeedData } from "../domain/seed";
import { AppStoreProvider } from "../store/AppStore";
import { userStorageKeys } from "../store/storage";
import { UiProvider } from "../ui/UiProvider";
import { AccountControls } from "./AccountControls";

const user = { id: "user_account_controls", email: "account@example.com", displayName: "账户用户" };

function Harness() {
  const auth = useAuth();
  return <>
    <button type="button" onClick={auth.markServiceOffline}>模拟服务不可达</button>
    <output data-testid="mode">{auth.mode}</output>
    <AccountControls syncStatus="offline" lastSyncedAt={null} />
  </>;
}

describe("AccountControls", () => {
  it("Go 服务不可达时禁用账户修改和退出", async () => {
    const actions = userEvent.setup();
    localStorage.setItem(userStorageKeys(user.id).data, JSON.stringify(createSeedData()));
    render(
      <AuthProvider initialSession={{ user, expiresAt: "2026-09-26T08:00:00Z" }}>
        <AppStoreProvider identity={{ kind: "user", userId: user.id }} syncEnabled={false}>
          <UiProvider><Harness /></UiProvider>
        </AppStoreProvider>
      </AuthProvider>,
    );

    await actions.click(screen.getByRole("button", { name: "模拟服务不可达" }));
    expect(screen.getByTestId("mode")).toHaveTextContent("authenticated");
    await actions.click(screen.getByRole("button", { name: "打开账户菜单" }));

    expect(screen.getByRole("menuitem", { name: "联网后退出" })).toBeDisabled();
    await actions.click(screen.getByRole("menuitem", { name: "账户资料" }));
    expect(screen.getByLabelText("称呼")).toBeDisabled();
  });
});
