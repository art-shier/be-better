import "fake-indexeddb/auto";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";
import { AppStoreProvider, useAppStore } from "./store/AppStore";
import { UiProvider } from "./ui/UiProvider";
import { createSeedData } from "./domain/seed";
import { AuthProvider } from "./auth/AuthProvider";
import type { AppData } from "./domain/types";
import { replaceAccountEntities, type CachedEntityBatch } from "./offline/cache";
import { deleteDayOrderDB } from "./offline/db";
import { prepareInitialMutations } from "./store/commands";
import { acceptAgentChange, createAgentRun, getAgentRun, listAgentRuns, rejectAgentChange, stopAgentRun, type ServerAgentRun } from "./api/agent";
import { listAuditEvents, undoAuditEvent } from "./api/audit";
import { GUEST_STORAGE_KEY } from "./store/storage";

vi.mock("./api/agent", async (importOriginal) => ({
  ...await importOriginal<typeof import("./api/agent")>(),
  acceptAgentChange: vi.fn(), createAgentRun: vi.fn(), getAgentRun: vi.fn(), listAgentRuns: vi.fn(), rejectAgentChange: vi.fn(), stopAgentRun: vi.fn(),
}));
vi.mock("./api/audit", async (importOriginal) => ({
  ...await importOriginal<typeof import("./api/audit")>(),
  listAuditEvents: vi.fn(), undoAuditEvent: vi.fn(),
}));

const testUser = { id: "user_test", email: "test@example.com", displayName: "测试用户" };
const testSession = { user: testUser, expiresAt: "2026-09-26T08:00:00Z" };

function serverRun(overrides: Partial<ServerAgentRun> = {}): ServerAgentRun {
  return {
    id: crypto.randomUUID(), intent: "安排作品集的下一步", status: "waiting", actionMode: "confirm",
    scope: { domains: ["goals", "tasks"], entityIds: [] }, version: 2,
    createdAt: "2026-08-27T02:00:00Z", updatedAt: "2026-08-27T02:01:00Z", startedAt: "2026-08-27T02:00:10Z",
    steps: [], changes: [], sourceRefs: [], ...overrides,
  };
}

async function seedAccount(accountId: string, data: AppData): Promise<void> {
  const grouped = new Map<CachedEntityBatch["entityType"], CachedEntityBatch["values"]>();
  for (const mutation of prepareInitialMutations(accountId, data)) {
    if (!mutation.optimisticEntity) continue;
    const values = grouped.get(mutation.entityType) ?? [];
    values.push(mutation.optimisticEntity);
    grouped.set(mutation.entityType, values);
  }
  await replaceAccountEntities(accountId, crypto.randomUUID(), "test-cursor", [...grouped].map(([entityType, values]) => ({ entityType, values })));
}

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
  beforeEach(async () => {
    vi.mocked(listAgentRuns).mockReset().mockResolvedValue({ runs: [], hasMore: false });
    vi.mocked(listAuditEvents).mockReset().mockResolvedValue({ events: [], hasMore: false });
    vi.mocked(createAgentRun).mockReset();
    vi.mocked(getAgentRun).mockReset();
    vi.mocked(acceptAgentChange).mockReset();
    vi.mocked(rejectAgentChange).mockReset();
    vi.mocked(stopAgentRun).mockReset();
    vi.mocked(undoAuditEvent).mockReset();
    vi.setSystemTime(new Date("2026-08-27T10:00:00+08:00"));
    window.location.hash = "today";
    localStorage.setItem(GUEST_STORAGE_KEY, JSON.stringify(createSeedData()));
    await deleteDayOrderDB();
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
    await seedAccount(testUser.id, { ...seed, goals: [goal], tasks: [task], events: [], records: [], notes: [] });
    const run = serverRun({
      sourceRefs: [{ id: crypto.randomUUID(), runId: "run_portfolio", entityType: "task", entityId: task.id, entityVersion: task.version, labelSnapshot: task.title, createdAt: "2026-08-27T02:01:00Z" }],
      changes: [{
        id: crypto.randomUUID(), runId: "run_portfolio", changeType: "reschedule-task", targetType: "task", targetId: task.id,
        baseVersion: task.version, patch: [], previewBefore: { scheduledStart: null, scheduledEnd: null },
        previewAfter: { scheduledStart: "2026-08-28T01:00:00Z", scheduledEnd: "2026-08-28T01:45:00Z" },
        reason: "当前优先级最高", status: "pending", version: 1, createdAt: "2026-08-27T02:01:00Z", updatedAt: "2026-08-27T02:01:00Z",
      }],
    });
    vi.mocked(createAgentRun).mockResolvedValue(run);
    vi.mocked(getAgentRun).mockResolvedValue(run);
    const user = userEvent.setup();
    render(<AccountHarness />);

    await user.click((await screen.findAllByRole("button", { name: /^Agent/ }))[0]);
    await user.click(screen.getAllByRole("button", { name: "发起委托" })[0]);
    await user.type(screen.getByLabelText("希望得到什么结果"), "安排作品集的下一步");
    await user.click(screen.getByRole("button", { name: /生成执行步骤/ }));

    await waitFor(() => expect(screen.getByText("调整“制作作品集首页”的安排")).toBeInTheDocument());
    expect(createAgentRun).toHaveBeenCalledWith(expect.objectContaining({
      intent: "安排作品集的下一步",
      actionMode: "confirm",
      scope: expect.objectContaining({ domains: expect.arrayContaining(["goals", "tasks"]) }),
    }), expect.objectContaining({ deviceId: expect.any(String), mutationId: expect.any(String) }));
    expect(screen.queryByText(/产品方案核心流程/)).not.toBeInTheDocument();
  });

  it("Agent 只接受勾选变更并明确拒绝未勾选项", async () => {
    const seed = createSeedData();
    await seedAccount(testUser.id, seed);
    const firstId = crypto.randomUUID();
    const secondId = crypto.randomUUID();
    const pending = serverRun({
      changes: [firstId, secondId].map((id, index) => ({
        id, runId: "run_resolve", changeType: "create-task", targetType: "task", patch: [],
        previewAfter: { title: index === 0 ? "优先任务" : "次要任务", estimateMinutes: 30 }, reason: "测试",
        status: "pending" as const, version: 1, createdAt: "2026-08-27T02:01:00Z", updatedAt: "2026-08-27T02:01:00Z",
      })),
    });
    const completed = serverRun({ ...pending, status: "completed", version: 3, summary: "已处理全部变更。", changes: pending.changes.map((change, index) => ({ ...change, status: index === 0 ? "applied" : "rejected", version: 2 })) });
    vi.mocked(createAgentRun).mockResolvedValue(pending);
    vi.mocked(getAgentRun).mockResolvedValueOnce(pending).mockResolvedValueOnce(completed);
    vi.mocked(acceptAgentChange).mockResolvedValue({ change: { ...pending.changes[0], status: "applied", version: 2 }, run: pending });
    vi.mocked(rejectAgentChange).mockResolvedValue({ change: { ...pending.changes[1], status: "rejected", version: 2 }, run: completed });
    const user = userEvent.setup();
    render(<AccountHarness />);

    await user.click((await screen.findAllByRole("button", { name: /^Agent/ }))[0]);
    await user.click(screen.getAllByRole("button", { name: "发起委托" })[0]);
    await user.type(screen.getByLabelText("希望得到什么结果"), "生成两个任务建议");
    await user.click(screen.getByRole("button", { name: /生成执行步骤/ }));
    await screen.findByText("创建任务“优先任务”");
    await user.click(screen.getByRole("checkbox", { name: "选择创建任务“次要任务”" }));
    await user.click(screen.getByRole("button", { name: "确认并执行 1 项" }));

    await waitFor(() => expect(acceptAgentChange).toHaveBeenCalledWith(firstId, 1, expect.objectContaining({ deviceId: expect.any(String), mutationId: expect.any(String) })));
    expect(rejectAgentChange).toHaveBeenCalledWith(secondId, 1, expect.objectContaining({ deviceId: expect.any(String), mutationId: expect.any(String) }));
    expect((await screen.findAllByText("已处理全部变更。")).length).toBeGreaterThan(0);
  });

  it("快捷面板通过服务端只读 Run 返回回答", async () => {
    const seed = createSeedData();
    await seedAccount(testUser.id, seed);
    vi.mocked(createAgentRun).mockResolvedValue(serverRun({
      actionMode: "read", status: "completed", summary: "下午优先完成短任务。",
      sourceRefs: [{ id: crypto.randomUUID(), runId: "run_read", entityType: "task", entityId: seed.tasks[0].id, entityVersion: seed.tasks[0].version, labelSnapshot: seed.tasks[0].title, createdAt: "2026-08-27T02:01:00Z" }],
    }));
    const user = userEvent.setup();
    render(<AccountHarness />);

    await user.click(await screen.findByRole("button", { name: "打开 Agent 快捷面板" }));
    await user.click(screen.getByRole("button", { name: "下午怎么安排更合理？" }));

    expect(await screen.findByText(/下午优先完成短任务/)).toBeInTheDocument();
    expect(createAgentRun).toHaveBeenCalledWith(expect.objectContaining({ actionMode: "read", intent: "下午怎么安排更合理？" }), expect.any(Object));
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
