import "fake-indexeddb/auto";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/http";
import { parseCapture } from "../domain/capture";
import { createSeedData } from "../domain/seed";
import type { AppData } from "../domain/types";
import { getCachedEntities, replaceAccountEntities, type CachedEntityBatch } from "../offline/cache";
import { deleteDayOrderDB } from "../offline/db";
import { listMutations } from "../offline/mutations";
import { AppStoreProvider, STORAGE_KEY, appReducer, useAppStore } from "./AppStore";
import { prepareInitialMutations } from "./commands";

const accountIdentity = { kind: "user" as const, userId: "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11" };

function SyncProbe() {
  return <output>{useAppStore().syncStatus}</output>;
}

async function seedAccount(accountId: string, data: AppData): Promise<string> {
  const deviceId = crypto.randomUUID();
  const versions = new Map<string, number>([
    ...data.goals.map((value) => [`goal:${value.id}`, value.version] as const),
    ...data.goals.flatMap((goal) => goal.milestones.map((value) => [`goal_milestone:${value.id}`, value.version] as const)),
    ...data.tasks.map((value) => [`task:${value.id}`, value.version] as const),
    ...data.events.map((value) => [`calendar_event:${value.id}`, value.version] as const),
    ...data.records.map((value) => [`record:${value.id}`, value.version] as const),
    ...data.notes.map((value) => [`note:${value.id}`, value.version] as const),
    ...data.reviews.map((value) => [`daily_review:${value.id}`, value.version] as const),
    [`user_settings:${accountId}`, data.settings.version] as const,
  ]);
  const grouped = new Map<CachedEntityBatch["entityType"], CachedEntityBatch["values"]>();
  for (const mutation of prepareInitialMutations(accountId, data)) {
    if (!mutation.optimisticEntity) continue;
    const values = grouped.get(mutation.entityType) ?? [];
    values.push({ ...mutation.optimisticEntity, version: versions.get(`${mutation.entityType}:${mutation.entityId}`) ?? mutation.optimisticEntity.version });
    grouped.set(mutation.entityType, values);
  }
  await replaceAccountEntities(accountId, deviceId, "seed-cursor", [...grouped].map(([entityType, values]) => ({ entityType, values })));
  return deviceId;
}

describe("appReducer", () => {
  it("快速记录同时创建原文和关联的派生任务", () => {
    const seed = createSeedData();
    const draft = parseCapture("明早跑 5 公里", seed.goals);
    const saved = appReducer(seed, { type: "save-capture", draft });
    expect(saved.records).toHaveLength(seed.records.length + 1);
    expect(saved.tasks).toHaveLength(seed.tasks.length + 1);
    expect(saved.records[0].parsedEntityId).toBe(saved.tasks[0].id);
    expect(saved.tasks[0].sourceRecordId).toBe(saved.records[0].id);
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
  });

  it("删除目标会解除弱关联且重复删除无效", () => {
    const seed = createSeedData();
    const goal = seed.goals.find((item) => seed.tasks.some((task) => task.goalId === item.id))!;
    const linkedTask = seed.tasks.find((task) => task.goalId === goal.id)!;
    const deleted = appReducer(seed, { type: "delete-goal", id: goal.id });
    expect(deleted.goals.some((item) => item.id === goal.id)).toBe(false);
    expect(deleted.tasks.find((task) => task.id === linkedTask.id)?.goalId).toBeUndefined();
    expect(appReducer(deleted, { type: "delete-goal", id: goal.id })).toBe(deleted);
  });

  it("删除记录会清理笔记弱关联且重复删除无效", () => {
    const seed = createSeedData();
    const record = seed.records[0];
    const note = { ...seed.notes[0], linkedEntityIds: [record.id] };
    const state = { ...seed, notes: [note, ...seed.notes.slice(1)] };

    const deleted = appReducer(state, { type: "delete-record", id: record.id });
    expect(deleted.records.some((item) => item.id === record.id)).toBe(false);
    expect(deleted.notes[0].linkedEntityIds).toEqual([]);
    expect(appReducer(deleted, { type: "delete-record", id: record.id })).toBe(deleted);
  });
});

describe("AppStoreProvider", () => {
  beforeEach(async () => {
    localStorage.clear();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    await deleteDayOrderDB();
  });

  it("从当前游客分区恢复本地数据", () => {
    const data = createSeedData();
    localStorage.setItem(STORAGE_KEY, JSON.stringify(data));

    function Probe() {
      const { data: current } = useAppStore();
      return <output>{current.goals[0]?.title}</output>;
    }

    render(<AppStoreProvider><Probe /></AppStoreProvider>);
    expect(screen.getByText(data.goals[0].title)).toBeInTheDocument();
  });

  it("游客数据只保存在本机且不请求状态接口", async () => {
    const local = createSeedData();
    localStorage.setItem(STORAGE_KEY, JSON.stringify(local));
    const fetchMock = vi.fn();
    const runSync = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    function Probe() {
      const store = useAppStore();
      return <output>{store.syncStatus}</output>;
    }

    render(<AppStoreProvider syncEnabled dependencies={{ runSync }}><Probe /></AppStoreProvider>);
    expect(screen.getByText("local-only")).toBeInTheDocument();
    expect(runSync).not.toHaveBeenCalled();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("登录账户先从 IndexedDB 恢复界面", async () => {
    const cached = createSeedData();
    cached.goals[0] = { ...cached.goals[0], title: "离线缓存目标" };
    await seedAccount(accountIdentity.userId, cached);

    function Probe() {
      const store = useAppStore();
      return <><output>{store.syncStatus}</output><span>{store.data.goals.find((goal) => goal.id === cached.goals[0].id)?.title}</span></>;
    }

    render(<AppStoreProvider identity={accountIdentity} syncEnabled={false}><Probe /></AppStoreProvider>);
    expect(await screen.findByText("离线缓存目标")).toBeInTheDocument();
    expect(screen.getByText("local-only")).toBeInTheDocument();
  });

  it("直接服务端命令复用已注册设备并生成独立幂等键", async () => {
    const cached = createSeedData();
    const deviceId = await seedAccount(accountIdentity.userId, cached);

    function Probe() {
      const store = useAppStore();
      return <button type="button" onClick={async () => {
        const first = await store.createServerMutationContext();
        const second = await store.createServerMutationContext();
        document.body.dataset.serverMutation = `${first.deviceId}:${first.mutationId}:${second.mutationId}`;
      }}>准备服务端命令</button>;
    }

    render(<AppStoreProvider identity={accountIdentity} syncEnabled={false}><Probe /></AppStoreProvider>);
    await screen.findByRole("button", { name: "准备服务端命令" });
    fireEvent.click(screen.getByRole("button", { name: "准备服务端命令" }));

    await waitFor(() => expect(document.body.dataset.serverMutation).toContain(deviceId));
    const [, firstMutationId, secondMutationId] = document.body.dataset.serverMutation!.split(":");
    expect(firstMutationId).not.toBe(secondMutationId);
  });

  it("dispatch 同时更新内存并原子持久化实体与 Mutation", async () => {
    const cached = createSeedData();
    const task = cached.tasks[0];
    const deviceId = await seedAccount(accountIdentity.userId, cached);

    function Probe() {
      const store = useAppStore();
      const current = store.data.tasks.find((item) => item.id === task.id);
      return <><span>{current?.title}</span><button type="button" onClick={() => current && store.dispatch({ type: "update-task", task: { ...current, title: "离线新标题", updatedAt: new Date().toISOString() } })}>修改任务</button></>;
    }

    render(<AppStoreProvider identity={accountIdentity} syncEnabled={false}><Probe /></AppStoreProvider>);
    await screen.findByText(task.title);
    fireEvent.click(screen.getByRole("button", { name: "修改任务" }));
    expect(screen.getByText("离线新标题")).toBeInTheDocument();
    await waitFor(async () => expect(await listMutations(accountIdentity.userId, deviceId)).toHaveLength(1));
    expect((await listMutations(accountIdentity.userId, deviceId))[0]).toMatchObject({ entityType: "task", entityId: task.id, operation: "update", baseVersion: task.version });
    expect((await getCachedEntities<typeof task>(accountIdentity.userId, "task")).find((item) => item.id === task.id)?.title).toBe("离线新标题");
  });

  it("联网同步后从 IndexedDB 重新投影，并响应 focus 与 online", async () => {
    const cached = createSeedData();
    const task = cached.tasks[0];
    const deviceId = await seedAccount(accountIdentity.userId, cached);
    const runSync = vi.fn(async () => {
      const current = (await getCachedEntities<typeof task>(accountIdentity.userId, "task")).find((item) => item.id === task.id)!;
      const updated = { ...current, title: "服务端刷新任务", version: current.version + 1 };
      await replaceAccountEntities(accountIdentity.userId, deviceId, `cursor-${runSync.mock.calls.length}`, [{ entityType: "task", values: [updated] }]);
      return { deviceId, cursor: `cursor-${runSync.mock.calls.length}`, conflicts: 0 };
    });
    const onServiceOnline = vi.fn();

    function Probe() {
      const store = useAppStore();
      return <><output>{store.syncStatus}</output><span>{store.data.tasks[0]?.title}</span></>;
    }

    render(<AppStoreProvider identity={accountIdentity} syncEnabled dependencies={{ runSync, syncIntervalMs: 60_000 }} onServiceOnline={onServiceOnline}><Probe /></AppStoreProvider>);
    expect(await screen.findByText("服务端刷新任务")).toBeInTheDocument();
    expect(screen.getByText("synced")).toBeInTheDocument();
    expect(runSync).toHaveBeenCalledTimes(1);
    expect(onServiceOnline).toHaveBeenCalled();

    fireEvent.focus(window);
    await waitFor(() => expect(runSync).toHaveBeenCalledTimes(2));
    fireEvent(window, new Event("online"));
    await waitFor(() => expect(runSync).toHaveBeenCalledTimes(3));
  });

  it("401 通知认证层，网络错误通知离线层，且都保留 IndexedDB 数据", async () => {
    const cached = createSeedData();
    await seedAccount(accountIdentity.userId, cached);
    const onUnauthorized = vi.fn();
    const unauthorized = render(
      <AppStoreProvider identity={accountIdentity} syncEnabled dependencies={{ runSync: vi.fn().mockRejectedValue(new ApiError(401, { code: "UNAUTHORIZED" })) }} onUnauthorized={onUnauthorized}>
        <SyncProbe />
      </AppStoreProvider>,
    );
    await waitFor(() => expect(onUnauthorized).toHaveBeenCalledOnce());
    expect(screen.getByText("offline")).toBeInTheDocument();
    unauthorized.unmount();

    const onServiceOffline = vi.fn();
    render(
      <AppStoreProvider identity={accountIdentity} syncEnabled dependencies={{ runSync: vi.fn().mockRejectedValue(new TypeError("connection refused")) }} onServiceOffline={onServiceOffline}>
        <SyncProbe />
      </AppStoreProvider>,
    );
    await waitFor(() => expect(onServiceOffline).toHaveBeenCalledOnce());
    expect(screen.getByText("offline")).toBeInTheDocument();
    expect(await getCachedEntities(accountIdentity.userId, "goal")).not.toHaveLength(0);
  });
});
