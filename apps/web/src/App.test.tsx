import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";
import { AppStoreProvider, useAppStore } from "./store/AppStore";
import { UiProvider } from "./ui/UiProvider";
import { createSeedData } from "./domain/seed";
import { AuthProvider } from "./auth/AuthProvider";
import { userStorageKeys } from "./store/storage";

const testUser = { id: "user_test", email: "test@example.com", displayName: "测试用户" };
const testSession = { user: testUser, expiresAt: "2026-09-26T08:00:00Z" };

function GuestHarness() {
  return <AuthProvider sessionCheckEnabled={false}><AppStoreProvider><UiProvider><Harness /></UiProvider></AppStoreProvider></AuthProvider>;
}

function AccountHarness() {
  return <AuthProvider sessionCheckEnabled={false} initialSession={testSession}><AppStoreProvider identity={{ kind: "user", userId: testUser.id }} syncEnabled={false}><UiProvider><Harness /></UiProvider></AppStoreProvider></AuthProvider>;
}

function Harness() {
  const { data } = useAppStore();
  return <><App /><output data-testid="entity-counts">{`${data.events.length}:${data.records.length}`}</output><output data-testid="onboarding-state">{`${data.settings.onboardingCompleted}:${data.goals.length}:${data.tasks.length}`}</output></>;
}

describe("关键页面交互", () => {
  beforeEach(() => {
    vi.setSystemTime(new Date("2026-08-27T10:00:00+08:00"));
    window.location.hash = "today";
    localStorage.setItem("dayorder.app.v1", JSON.stringify(createSeedData()));
  });

  it("快速记录默认自动识别并创建带来源的日程", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);
    const initial = screen.getByTestId("entity-counts").textContent;
    const [initialEvents, initialRecords] = initial!.split(":").map(Number);

    await user.click(screen.getAllByRole("button", { name: /快速记录/ })[0]);
    const input = screen.getByRole("textbox", { name: "原始文本" });
    await waitFor(() => expect(input).toHaveFocus());
    expect(screen.getByRole("tab", { name: /自动识别/ })).toHaveAttribute("aria-selected", "true");
    await user.type(input, "周五下午 3 点看牙");
    expect(await screen.findByText("建议整理为：日程")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保留原文并创建" }));

    await waitFor(() => expect(screen.getByTestId("entity-counts")).toHaveTextContent(`${initialEvents + 1}:${initialRecords + 1}`));
  });

  it("快捷键可以直接聚焦全局搜索", async () => {
    render(<GuestHarness />);
    fireEvent.keyDown(document, { key: "k", ctrlKey: true });
    const input = await screen.findByRole("textbox", { name: "搜索内容" });
    await waitFor(() => expect(input).toHaveFocus());
  });

  it("从搜索结果直接打开实体编辑器", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);
    fireEvent.keyDown(document, { key: "k", ctrlKey: true });
    await user.type(await screen.findByRole("textbox", { name: "搜索内容" }), "核心闭环");
    await user.click(screen.getByRole("option", { name: /生活管理产品的核心闭环/ }));
    expect(await screen.findByRole("dialog", { name: "编辑笔记" })).toBeInTheDocument();
  });

  it("新用户可以创建目标并生成第一个今日行动", async () => {
    localStorage.clear();
    const user = userEvent.setup();
    render(<GuestHarness />);

    expect(screen.getByRole("dialog", { name: "把接下来想发生的变化放进日序" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /下一步/ }));
    await user.type(screen.getByLabelText("目标名称"), "完成作品集");
    await user.type(screen.getByLabelText("为什么重要"), "形成可以展示的真实成果");
    await user.click(screen.getByRole("button", { name: /下一步/ }));
    await user.click(screen.getByRole("button", { name: /下一步/ }));
    expect(screen.getByText("推进：完成作品集")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /开始使用/ }));

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "把接下来想发生的变化放进日序" })).not.toBeInTheDocument());
    expect(screen.getByTestId("onboarding-state")).toHaveTextContent("true:1:1");
  });

  it("Agent 为自建目标生成真实任务提案", async () => {
    const seed = createSeedData();
    const goal = { ...seed.goals[0], id: "goal_portfolio", title: "完成个人作品集", why: "用于下一次求职展示" };
    const task = { ...seed.tasks[0], id: "task_portfolio", title: "制作作品集首页", goalId: goal.id, scheduledStart: undefined, scheduledEnd: undefined };
    localStorage.setItem(userStorageKeys(testUser.id).data, JSON.stringify({ ...seed, goals: [goal], tasks: [task], events: [], records: [], notes: [], agentRuns: [], audit: [] }));
    const user = userEvent.setup();
    render(<AccountHarness />);

    await user.click(screen.getAllByRole("button", { name: /^Agent/ })[0]);
    await user.click(screen.getAllByRole("button", { name: "发起委托" })[0]);
    await user.type(screen.getByLabelText("希望得到什么结果"), "安排作品集的下一步");
    await user.click(screen.getByRole("button", { name: /生成执行步骤/ }));

    await waitFor(() => expect(screen.getByText("安排“制作作品集首页”")).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.queryByText(/产品方案核心流程/)).not.toBeInTheDocument();
  });

  it("游客点击 Agent 保留当前页面并打开登录门禁", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);
    await user.click(screen.getAllByRole("button", { name: /^Agent/ })[0]);
    expect(screen.getByRole("dialog", { name: "登录后使用 Agent" })).toBeInTheDocument();
    expect(window.location.hash).toBe("#today");
    expect(screen.getByText(/早上好，把最清醒的时间/)).toBeInTheDocument();
  });
});
