import { render, screen, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { describe, expect, it, vi } from "vitest";
import { parseCapture } from "../domain/capture";
import { createSeedData } from "../domain/seed";
import type { AgentRun } from "../domain/types";
import { AppStoreProvider, STORAGE_KEY, appReducer, useAppStore } from "./AppStore";
import { userStorageKeys } from "./storage";

const accountIdentity = { kind: "user" as const, userId: "user_test" };
const accountKeys = userStorageKeys(accountIdentity.userId);

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("appReducer", () => {
  it("撤销快速记录时同时移除原文和派生任务", () => {
    const seed = createSeedData();
    const draft = parseCapture("明早跑 5 公里", seed.goals);
    const saved = appReducer(seed, { type: "save-capture", draft });
    expect(saved.records).toHaveLength(seed.records.length + 1);
    expect(saved.tasks).toHaveLength(seed.tasks.length + 1);

    const restored = appReducer(saved, { type: "undo", auditId: saved.audit[0].id });
    expect(restored.records).toHaveLength(seed.records.length);
    expect(restored.tasks).toHaveLength(seed.tasks.length);
  });

  it("接受收件箱建议时复用原记录且重复提交无效", () => {
    const seed = createSeedData();
    const source = seed.records.find((record) => record.kind === "inbox")!;
    const draft = parseCapture(source.rawText, seed.goals);
    const accepted = appReducer(seed, { type: "accept-record", recordId: source.id, draft });
    const acceptedAgain = appReducer(accepted, { type: "accept-record", recordId: source.id, draft });

    expect(accepted.records).toHaveLength(seed.records.length);
    expect(accepted.records.find((record) => record.id === source.id)?.parsedEntityId).toBeTruthy();
    expect(acceptedAgain).toBe(accepted);

    const restored = appReducer(accepted, { type: "undo", auditId: accepted.audit[0].id });
    expect(restored.records.find((record) => record.id === source.id)?.parsedEntityId).toBeUndefined();
  });

  it("Agent 审批只能执行一次", () => {
    const seed = createSeedData();
    const run: AgentRun = {
      id: "run_test",
      intent: "安排产品方案",
      status: "waiting",
      actionMode: "confirm",
      scope: ["目标与任务"],
      steps: [],
      changes: [{ id: "change_test", type: "create-task", entityId: "goal_product", title: "创建“产品方案核心流程”专注任务", after: "明天 08:30—09:20 · 50 分钟", reason: "测试", sourceRefs: [{ id: "goal_product", kind: "goal", label: "完成个人产品方案" }], status: "pending" }],
      startedAt: new Date().toISOString(),
    };
    const state = { ...seed, agentRuns: [run, ...seed.agentRuns] };
    const approved = appReducer(state, { type: "approve-agent", id: run.id, changeIds: ["change_test"] });
    const approvedAgain = appReducer(approved, { type: "approve-agent", id: run.id, changeIds: ["change_test"] });

    expect(approved.agentRuns[0].status).toBe("completed");
    expect(approvedAgain).toBe(approved);
    const task = approved.tasks.find((item) => item.title.includes("产品方案核心流程"))!;
    expect(new Date(task.scheduledStart!).getHours()).toBe(8);
    expect(task.estimateMinutes).toBe(50);
  });

  it("Agent 使用当前来源 ID 创建任务，不写入演示目标", () => {
    const seed = createSeedData();
    const customGoal = { ...seed.goals[0], id: "goal_custom", title: "完成个人作品集" };
    const run: AgentRun = {
      id: "run_custom",
      intent: "为作品集安排起步动作",
      status: "waiting",
      actionMode: "confirm",
      scope: ["目标与任务"],
      steps: [],
      changes: [{ id: "change_custom", type: "create-task", entityId: customGoal.id, title: "创建“推进：完成个人作品集”任务", after: "明天 10:00—10:45 · 45 分钟", reason: "测试真实来源", sourceRefs: [{ id: customGoal.id, kind: "goal", label: customGoal.title }], status: "pending" }],
      startedAt: new Date().toISOString(),
    };
    const state = { ...seed, goals: [customGoal], tasks: [], agentRuns: [run] };
    const approved = appReducer(state, { type: "approve-agent", id: run.id, changeIds: ["change_custom"] });
    expect(approved.tasks[0].title).toBe("推进：完成个人作品集");
    expect(approved.tasks[0].goalId).toBe(customGoal.id);
    expect(approved.tasks[0].goalId).not.toBe("goal_product");
  });

  it("撤回运行所需权限会立即停止 Agent", () => {
    const seed = createSeedData();
    const run: AgentRun = { id: "run_reading", intent: "读取日程", status: "reading", actionMode: "read", scope: ["未来 30 天日程"], steps: [{ id: "step", title: "读取", detail: "日程", status: "running" }], changes: [], startedAt: new Date().toISOString() };
    const stopped = appReducer({ ...seed, agentRuns: [run] }, { type: "set-permission", key: "calendar", value: false });
    expect(stopped.agentRuns[0].status).toBe("stopped");
    expect(stopped.agentRuns[0].summary).toContain("权限已撤回");
    expect(stopped.settings.permissions.calendar).toBe(false);
  });

  it("删除目标会解除关联且撤销后完整恢复", () => {
    const seed = createSeedData();
    const goal = seed.goals.find((item) => seed.tasks.some((task) => task.goalId === item.id))!;
    const linkedTask = seed.tasks.find((task) => task.goalId === goal.id)!;
    const deleted = appReducer(seed, { type: "delete-goal", id: goal.id });
    expect(deleted.tasks.find((task) => task.id === linkedTask.id)?.goalId).toBeUndefined();
    const restored = appReducer(deleted, { type: "undo", auditId: deleted.audit[0].id });
    expect(restored.goals.some((item) => item.id === goal.id)).toBe(true);
    expect(restored.tasks.find((task) => task.id === linkedTask.id)?.goalId).toBe(goal.id);
  });
});

describe("AppStoreProvider", () => {
  it("兼容旧备份并补齐提醒设置", () => {
    const data = createSeedData();
    const legacy = { ...data, settings: { ...data.settings } } as typeof data & { settings: Omit<typeof data.settings, "remindersEnabled"> };
    delete (legacy.settings as Partial<typeof data.settings>).remindersEnabled;
    localStorage.setItem(STORAGE_KEY, JSON.stringify(legacy));

    function Probe() {
      const { data: current } = useAppStore();
      return <output>{String(current.settings.remindersEnabled)}</output>;
    }

    render(<AppStoreProvider><Probe /></AppStoreProvider>);
    expect(screen.getByText("false")).toBeInTheDocument();
  });

  it("游客数据只保存在本机且不请求状态接口", async () => {
    const local = createSeedData();
    localStorage.setItem(STORAGE_KEY, JSON.stringify(local));
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    function Probe() {
      const store = useAppStore();
      return <output>{store.syncStatus}</output>;
    }

    render(<AppStoreProvider syncEnabled><Probe /></AppStoreProvider>);
    expect(screen.getByText("local-only")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("服务不可用时仍保留浏览器数据", async () => {
    const local = createSeedData();
    localStorage.setItem(accountKeys.data, JSON.stringify(local));
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("connection refused")));

    function Probe() {
      const store = useAppStore();
      return <output>{store.syncStatus}</output>;
    }

    render(<AppStoreProvider identity={accountIdentity} syncEnabled><Probe /></AppStoreProvider>);
    await waitFor(() => expect(screen.getByText("offline")).toBeInTheDocument());
    expect(JSON.parse(localStorage.getItem(accountKeys.data) ?? "null").goals).toHaveLength(local.goals.length);
  });

  it("远端 hydration 不会触发重复 PUT", async () => {
    const remoteData = { ...createSeedData(), goals: [{ ...createSeedData().goals[0], title: "服务端目标" }] };
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(jsonResponse({ revision: 4, data: remoteData, updatedAt: "2026-08-27T08:00:00Z" })));
    vi.stubGlobal("fetch", fetchMock);

    function Probe() {
      const store = useAppStore();
      return <><output>{store.syncStatus}</output><span>{store.data.goals[0]?.title}</span></>;
    }

    render(<StrictMode><AppStoreProvider identity={accountIdentity} syncEnabled><Probe /></AppStoreProvider></StrictMode>);
    await waitFor(() => expect(screen.getByText("服务端目标")).toBeInTheDocument());
    await new Promise((resolve) => window.setTimeout(resolve, 600));
    expect(fetchMock.mock.calls.every(([, init]) => (init as RequestInit).method === "GET")).toBe(true);
  });

  it("本地与服务端同时变化时保留冲突备份", async () => {
    const base = createSeedData();
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse({ revision: 1, data: base, updatedAt: "2026-08-27T08:00:00Z" }));
    vi.stubGlobal("fetch", fetchMock);

    function Probe() {
      const store = useAppStore();
      return <><output>{store.syncStatus}</output><span>{store.data.goals[0]?.title}</span></>;
    }

    const first = render(<AppStoreProvider identity={accountIdentity} syncEnabled><Probe /></AppStoreProvider>);
    await waitFor(() => expect(screen.getByText("synced")).toBeInTheDocument());
    first.unmount();

    const localChanged = { ...base, goals: [{ ...base.goals[0], title: "离线修改" }, ...base.goals.slice(1)] };
    const remoteChanged = { ...base, goals: [{ ...base.goals[0], title: "服务端修改" }, ...base.goals.slice(1)] };
    localStorage.setItem(accountKeys.data, JSON.stringify(localChanged));
    fetchMock.mockResolvedValueOnce(jsonResponse({ revision: 2, data: remoteChanged, updatedAt: "2026-08-27T09:00:00Z" }));

    render(<AppStoreProvider identity={accountIdentity} syncEnabled><Probe /></AppStoreProvider>);
    await waitFor(() => expect(screen.getByText("conflict")).toBeInTheDocument());
    expect(screen.getByText("服务端修改")).toBeInTheDocument();
    expect(JSON.parse(localStorage.getItem(accountKeys.conflict) ?? "null").data.goals[0].title).toBe("离线修改");
  });
});
