import "fake-indexeddb/auto";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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

function GuestHarness({ agentAvailable = false }: { agentAvailable?: boolean }) {
  return <AuthProvider sessionCheckEnabled={false}><AppStoreProvider><UiProvider><Harness agentAvailable={agentAvailable} /></UiProvider></AppStoreProvider></AuthProvider>;
}

function AccountHarness({ agentAvailable = false }: { agentAvailable?: boolean }) {
  return <AuthProvider sessionCheckEnabled={false} initialSession={testSession}><AppStoreProvider identity={{ kind: "user", userId: testUser.id }} syncEnabled={false}><UiProvider><Harness agentAvailable={agentAvailable} /></UiProvider></AppStoreProvider></AuthProvider>;
}

function Harness({ agentAvailable = false }: { agentAvailable?: boolean }) {
  const { data } = useAppStore();
  return <><App agentAvailable={agentAvailable} /><output data-testid="entity-counts">{`${data.events.length}:${data.records.length}`}</output><output data-testid="onboarding-state">{`${data.settings.onboardingCompleted}:${data.goals.length}:${data.tasks.length}`}</output><output data-testid="note-state">{`${data.notes.length}:${data.notes.map((note) => note.bodyMarkdown).join("␞")}`}</output></>;
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

  it("生产界面隐藏所有 Agent 入口", async () => {
    window.location.hash = "agent";

    render(<AccountHarness />);

    expect([...document.querySelectorAll(".nav-button, .bottom-button")].some((item) => item.textContent === "Agent")).toBe(false);
    expect(document.querySelector(".assistant-trigger")).toBeNull();
    await waitFor(() => expect(window.location.hash).toBe("#today"));
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

  it("笔记正文提供富文本和 Markdown 编辑模式", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    await user.click(screen.getByRole("button", { name: "新建笔记" }));

    expect(screen.getByRole("tab", { name: "富文本" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: "Markdown" })).toHaveAttribute("aria-selected", "false");
  });

  it("笔记正文切换和预览只更新草稿，保存时只创建一篇笔记", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);
    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    const before = screen.getByTestId("note-state").textContent!;
    const initialCount = Number(before.split(":", 1)[0]);
    await user.click(screen.getByRole("button", { name: "新建笔记" }));
    await user.type(screen.getByRole("textbox", { name: "标题" }), "Markdown 验收");
    await user.click(screen.getByRole("tab", { name: "Markdown" }));
    await user.type(screen.getByRole("textbox", { name: "Markdown 源码" }), "# 标题{enter}{enter}- [x] 完成");
    await user.click(screen.getByRole("tab", { name: "预览" }));

    expect(screen.getByRole("heading", { level: 1, name: "标题" })).toBeInTheDocument();
    expect(screen.getByTestId("note-state").textContent).toBe(before);

    await user.click(screen.getByRole("button", { name: "创建笔记" }));

    await waitFor(() => expect(screen.getByTestId("note-state")).toHaveTextContent(`${initialCount + 1}:`));
    expect(screen.getByTestId("note-state")).toHaveTextContent("# 标题");
    expect(screen.getAllByText("笔记已创建")).toHaveLength(1);
  });

  it("打开另一篇笔记时重置正文和编辑模式", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);
    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    await user.click(screen.getByRole("button", { name: /生活管理产品的核心闭环/ }));
    await user.click(screen.getByRole("tab", { name: "Markdown" }));
    await user.click(screen.getByRole("button", { name: "取消" }));
    await user.click(screen.getByRole("button", { name: /设计中的设计/ }));

    expect(screen.getByRole("tab", { name: "富文本" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("textbox", { name: "富文本正文" })).toHaveTextContent("重新发现事物之间关系");
  });

  it("可以在笔记页创建未被引用的全局标签", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    await user.click(screen.getByRole("button", { name: "管理标签" }));
    const dialog = screen.getByRole("dialog", { name: "管理标签" });
    await user.type(screen.getByRole("textbox", { name: "新标签名称" }), "灵感");
    await user.click(screen.getByRole("button", { name: "添加标签" }));

    expect(dialog).toHaveTextContent("灵感");
    expect(screen.getByRole("textbox", { name: "新标签名称" })).toHaveValue("");
    expect(screen.getByText("标签“灵感”已创建")).toBeInTheDocument();
  });

  it("可以在标签管理中重命名标签", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    await user.click(screen.getByRole("button", { name: "管理标签" }));
    const dialog = screen.getByRole("dialog", { name: "管理标签" });
    await user.click(within(dialog).getByRole("button", { name: "编辑标签“产品”" }));
    const input = within(dialog).getByRole("textbox", { name: "编辑标签“产品”" });
    await user.clear(input);
    await user.type(input, "产品方向");
    await user.click(within(dialog).getByRole("button", { name: "保存名称" }));

    expect(within(dialog).getByText("产品方向")).toBeInTheDocument();
    expect(within(dialog).queryByText("产品", { exact: true })).not.toBeInTheDocument();
    expect(screen.getByText("标签“产品”已重命名为“产品方向”")).toBeInTheDocument();
  });

  it("删除全局标签前说明笔记和记录的引用影响", async () => {
    const seed = createSeedData();
    const timestamp = "2026-08-27T02:00:00Z";
    seed.tags.push({ id: "tag_shared", name: "共享标签", version: 1, createdAt: timestamp, updatedAt: timestamp });
    seed.notes[0] = { ...seed.notes[0], tags: [...seed.notes[0].tags, "共享标签"] };
    seed.records[0] = { ...seed.records[0], tags: [...seed.records[0].tags, "共享标签"] };
    localStorage.setItem(GUEST_STORAGE_KEY, JSON.stringify(seed));
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    await user.click(screen.getByRole("button", { name: "管理标签" }));
    const deleteTrigger = screen.getByRole("button", { name: "删除标签“共享标签”" });
    await user.click(deleteTrigger);

    let dialog = screen.getByRole("dialog", { name: "删除标签" });
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(dialog).toHaveTextContent("这个标签正在被 1 篇笔记和 1 条记录使用");
    expect(within(dialog).getByRole("button", { name: "取消" })).toHaveFocus();
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    const restoredDeleteTrigger = screen.getByRole("button", { name: "删除标签“共享标签”" });
    await waitFor(() => expect(restoredDeleteTrigger).toHaveFocus());

    await user.click(restoredDeleteTrigger);
    dialog = screen.getByRole("dialog", { name: "删除标签" });
    await user.click(within(dialog).getByRole("button", { name: "确认删除标签" }));

    expect(screen.getByRole("dialog", { name: "管理标签" })).not.toHaveTextContent("共享标签");
    expect(screen.getByText("标签“共享标签”已删除")).toBeInTheDocument();
  });

  it("创建标签时拒绝忽略大小写和空格的重名", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    await user.click(screen.getByRole("button", { name: "管理标签" }));
    const dialog = screen.getByRole("dialog", { name: "管理标签" });
    const input = within(dialog).getByRole("textbox", { name: "新标签名称" });
    await user.type(input, "  产品  ");
    await user.click(within(dialog).getByRole("button", { name: "添加标签" }));

    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveAccessibleDescription("已有同名标签，请换一个名称");
    expect(input).toHaveValue("  产品  ");
    expect(within(dialog).getAllByRole("button", { name: "编辑标签“产品”" })).toHaveLength(1);

    await user.clear(input);
    await user.type(input, "新标签");
    expect(input).not.toHaveAttribute("aria-invalid", "true");
  });

  it("创建标签时提示名称不能为空", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    await user.click(screen.getByRole("button", { name: "管理标签" }));
    const dialog = screen.getByRole("dialog", { name: "管理标签" });
    const input = within(dialog).getByRole("textbox", { name: "新标签名称" });
    await user.type(input, "   ");
    await user.click(within(dialog).getByRole("button", { name: "添加标签" }));

    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveAccessibleDescription("请输入标签名称");
    expect(input).toHaveFocus();
  });

  it("创建标签时拒绝超过八十个字符的名称", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    await user.click(screen.getByRole("button", { name: "管理标签" }));
    const dialog = screen.getByRole("dialog", { name: "管理标签" });
    const input = within(dialog).getByRole("textbox", { name: "新标签名称" });
    await user.type(input, "长".repeat(81));
    await user.click(within(dialog).getByRole("button", { name: "添加标签" }));

    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveAccessibleDescription("标签名称不能超过 80 个字符");
    expect(input).toHaveValue("长".repeat(81));
  });

  it("重命名标签时拒绝与其他标签重名", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    await user.click(screen.getByRole("button", { name: "管理标签" }));
    const dialog = screen.getByRole("dialog", { name: "管理标签" });
    await user.click(within(dialog).getByRole("button", { name: "编辑标签“产品”" }));
    const input = within(dialog).getByRole("textbox", { name: "编辑标签“产品”" });
    await user.clear(input);
    await user.type(input, "  设计  ");
    await user.click(within(dialog).getByRole("button", { name: "保存名称" }));

    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveAccessibleDescription("已有同名标签，请换一个名称");
    expect(input).toHaveValue("  设计  ");
  });

  it("中文输入法确认文字时不会提前提交标签", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    await user.click(screen.getByRole("button", { name: "管理标签" }));
    const dialog = screen.getByRole("dialog", { name: "管理标签" });
    const input = within(dialog).getByRole("textbox", { name: "新标签名称" });
    await user.type(input, "灵感整理");
    const form = input.closest("form");
    expect(form).not.toBeNull();

    fireEvent.compositionStart(input);
    fireEvent.submit(form!);
    expect(screen.queryByText("标签“灵感整理”已创建")).not.toBeInTheDocument();
    expect(input).toHaveValue("灵感整理");

    fireEvent.compositionEnd(input);
    fireEvent.submit(form!);
    expect(screen.getByText("标签“灵感整理”已创建")).toBeInTheDocument();
  });

  it("按 Escape 取消标签改名并恢复焦点", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    await user.click(screen.getByRole("button", { name: "管理标签" }));
    const dialog = screen.getByRole("dialog", { name: "管理标签" });
    await user.click(within(dialog).getByRole("button", { name: "编辑标签“产品”" }));
    const input = within(dialog).getByRole("textbox", { name: "编辑标签“产品”" });
    await user.clear(input);
    await user.type(input, "未保存名称");
    await user.keyboard("{Escape}");

    expect(screen.getByRole("dialog", { name: "管理标签" })).toBeInTheDocument();
    expect(within(dialog).getByText("产品", { exact: true })).toBeInTheDocument();
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "编辑标签“产品”" })).toHaveFocus());
  });

  it("标签库为空时引导创建第一个标签", async () => {
    const seed = createSeedData();
    localStorage.setItem(GUEST_STORAGE_KEY, JSON.stringify({
      ...seed,
      tags: [],
      notes: seed.notes.map((note) => ({ ...note, tags: [] })),
      records: seed.records.map((record) => ({ ...record, tags: [] })),
    }));
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    await user.click(screen.getByRole("button", { name: "管理标签" }));
    const dialog = screen.getByRole("dialog", { name: "管理标签" });

    expect(within(dialog).getByText("还没有标签")).toBeInTheDocument();
    expect(within(dialog).getByText("在上方创建第一个标签，之后可以用于笔记和记录。")).toBeInTheDocument();
  });

  it("关闭标签管理时丢弃未提交的名称", async () => {
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getAllByRole("button", { name: "笔记" })[0]);
    const trigger = screen.getByRole("button", { name: "管理标签" });
    await user.click(trigger);
    await user.type(screen.getByRole("textbox", { name: "新标签名称" }), "未提交");
    await user.click(screen.getByRole("button", { name: "关闭" }));
    await user.click(trigger);

    expect(screen.getByRole("textbox", { name: "新标签名称" })).toHaveValue("");
  });

  it("新用户首次进入时不再被入门向导阻断", () => {
    localStorage.clear();
    render(<GuestHarness />);

    expect(screen.queryByRole("dialog", { name: "把接下来想发生的变化放进日序" })).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /早上好/ })).toBeInTheDocument();
    expect(screen.getByTestId("onboarding-state")).toHaveTextContent("false:0:0");
  });

  it("空数据的今天页提供两条非阻断起步路径", () => {
    localStorage.clear();
    render(<GuestHarness />);

    const starter = screen.getByRole("region", { name: "开始安排你的日序" });
    expect(within(starter).getByRole("button", { name: "创建第一个目标" })).toBeInTheDocument();
    expect(within(starter).getByRole("button", { name: "使用入门向导" })).toBeInTheDocument();
  });

  it("直接创建第一个目标后起步卡片消失", async () => {
    localStorage.clear();
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getByRole("button", { name: "创建第一个目标" }));
    await user.type(screen.getByLabelText("目标名称"), "建立晨间习惯");
    await user.click(screen.getByRole("button", { name: "创建更改" }));

    await waitFor(() => expect(screen.queryByRole("region", { name: "开始安排你的日序" })).not.toBeInTheDocument());
    expect(screen.getByTestId("onboarding-state")).toHaveTextContent("false:1:0");
  });

  it("取消入门向导不会写入数据且重新打开会重置草稿", async () => {
    localStorage.clear();
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getByRole("button", { name: "使用入门向导" }));
    await user.click(screen.getByRole("button", { name: /下一步/ }));
    await user.type(screen.getByLabelText("目标名称"), "未提交目标");
    await user.click(screen.getByRole("button", { name: "关闭" }));

    expect(screen.getByTestId("onboarding-state")).toHaveTextContent("false:0:0");
    await user.click(screen.getByRole("button", { name: "使用入门向导" }));
    expect(screen.getByText("最近最想照顾哪些部分？")).toBeInTheDocument();
    expect(screen.queryByDisplayValue("未提交目标")).not.toBeInTheDocument();
  });

  it("主动完成入门向导仍会创建目标和第一个今日行动", async () => {
    localStorage.clear();
    const user = userEvent.setup();
    render(<GuestHarness />);

    await user.click(screen.getByRole("button", { name: "使用入门向导" }));
    await user.click(screen.getByRole("button", { name: /下一步/ }));
    await user.type(screen.getByLabelText("目标名称"), "完成作品集");
    await user.type(screen.getByLabelText("为什么重要"), "形成可以展示的真实成果");
    await user.click(screen.getByRole("button", { name: /下一步/ }));
    await user.click(screen.getByRole("button", { name: /下一步/ }));
    await user.click(screen.getByRole("button", { name: /开始使用/ }));

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "把接下来想发生的变化放进日序" })).not.toBeInTheDocument());
    expect(screen.getByTestId("onboarding-state")).toHaveTextContent("true:1:1");
  });

  it("设置入口先关闭设置再打开入门向导", async () => {
    const user = userEvent.setup();
    render(<AccountHarness />);

    await user.click(await screen.findByRole("button", { name: "打开账户菜单" }));
    await user.click(screen.getByRole("menuitem", { name: /应用设置/ }));
    const settings = screen.getByRole("dialog", { name: "数据与设置" });
    await user.click(within(settings).getByRole("button", { name: "使用入门向导" }));

    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(screen.queryByRole("dialog", { name: "数据与设置" })).not.toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "把接下来想发生的变化放进日序" })).toBeInTheDocument();
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
    render(<AccountHarness agentAvailable />);

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
    render(<AccountHarness agentAvailable />);

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
    render(<AccountHarness agentAvailable />);

    await user.click(await screen.findByRole("button", { name: "打开 Agent 快捷面板" }));
    await user.click(screen.getByRole("button", { name: "下午怎么安排更合理？" }));

    expect(await screen.findByText(/下午优先完成短任务/)).toBeInTheDocument();
    expect(createAgentRun).toHaveBeenCalledWith(expect.objectContaining({ actionMode: "read", intent: "下午怎么安排更合理？" }), expect.any(Object));
  });

  it("游客点击 Agent 保留当前页面并打开登录门禁", async () => {
    const user = userEvent.setup();
    render(<GuestHarness agentAvailable />);
    await user.click(screen.getAllByRole("button", { name: /^Agent/ })[0]);
    expect(screen.getByRole("dialog", { name: "登录后使用 Agent" })).toBeInTheDocument();
    expect(window.location.hash).toBe("#today");
    expect(screen.getByText(/早上好，把最清醒的时间/)).toBeInTheDocument();
  });
});
