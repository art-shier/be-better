import "fake-indexeddb/auto";
import { beforeEach, describe, expect, it } from "vitest";
import { getCachedEntities } from "./cache";
import { closeDayOrderDB, deleteDayOrderDB } from "./db";
import { enqueueMutation, enqueueMutations, listMutations } from "./mutations";

const entity = (id: string, title: string) => ({
  id, title, version: 0, createdAt: "2026-08-28T00:00:00Z", updatedAt: "2026-08-28T00:00:00Z",
});

describe("IndexedDB mutation queue", () => {
  beforeEach(async () => deleteDayOrderDB());

  it("用同一事务保存乐观实体和 Mutation，并保持本地顺序", async () => {
    const accountId = "user-a";
    const deviceId = crypto.randomUUID();
    const first = entity(crypto.randomUUID(), "第一个任务");
    const second = entity(crypto.randomUUID(), "第二个任务");

    await enqueueMutation({ accountId, deviceId, entityType: "task", entityId: first.id, operation: "create", payload: first, optimisticEntity: first });
    await enqueueMutation({ accountId, deviceId, entityType: "task", entityId: second.id, operation: "create", payload: second, optimisticEntity: second });

    expect((await getCachedEntities<{ id: string }>(accountId, "task")).map((item) => item.id)).toEqual(expect.arrayContaining([first.id, second.id]));
    expect((await listMutations(accountId, deviceId)).map((item) => item.sequence)).toEqual([1, 2]);
  });

  it("浏览器数据库重新打开后仍保留队列", async () => {
    const deviceId = crypto.randomUUID();
    const task = entity(crypto.randomUUID(), "离线任务");
    await enqueueMutation({ accountId: "user-a", deviceId, entityType: "task", entityId: task.id, operation: "create", payload: task, optimisticEntity: task });

    closeDayOrderDB();

    expect(await listMutations("user-a", deviceId)).toHaveLength(1);
  });

  it("IndexedDB 写入失败会拒绝调用且不产生半份实体", async () => {
    const id = crypto.randomUUID();
    const invalidPayload = { ...entity(id, "无法克隆"), invalid: () => undefined };

    await expect(enqueueMutation({
      accountId: "user-a", deviceId: crypto.randomUUID(), entityType: "task", entityId: id,
      operation: "create", payload: invalidPayload, optimisticEntity: invalidPayload,
    })).rejects.toBeDefined();

    expect(await getCachedEntities("user-a", "task")).toEqual([]);
  });

  it("复合命令的多个实体和 Mutation 要么全部提交，要么全部回滚", async () => {
    const deviceId = crypto.randomUUID();
    const valid = entity(crypto.randomUUID(), "有效任务");
    const invalid = { ...entity(crypto.randomUUID(), "无效任务"), invalid: () => undefined };

    await expect(enqueueMutations([
      { accountId: "user-a", deviceId, entityType: "task", entityId: valid.id, operation: "create", payload: valid, optimisticEntity: valid },
      { accountId: "user-a", deviceId, entityType: "task", entityId: invalid.id, operation: "create", payload: invalid, optimisticEntity: invalid },
    ])).rejects.toBeDefined();

    expect(await getCachedEntities("user-a", "task")).toEqual([]);
    expect(await listMutations("user-a", deviceId)).toEqual([]);
  });

  it("合并同一实体的连续离线更新并保留最初基础版本", async () => {
    const accountId = "user-a";
    const deviceId = crypto.randomUUID();
    const id = crypto.randomUUID();
    const first = { ...entity(id, "第一次修改"), version: 4 };
    const second = { ...entity(id, "第二次修改"), version: 4 };

    await enqueueMutation({ accountId, deviceId, entityType: "task", entityId: id, operation: "update", baseVersion: 4, payload: { id, title: first.title }, optimisticEntity: first });
    await enqueueMutation({ accountId, deviceId, entityType: "task", entityId: id, operation: "update", baseVersion: 4, payload: { id, title: second.title }, optimisticEntity: second });

    const mutations = await listMutations(accountId, deviceId);
    expect(mutations).toHaveLength(1);
    expect(mutations[0]).toMatchObject({ operation: "update", baseVersion: 4, payload: { id, title: "第二次修改" } });
    expect((await getCachedEntities<typeof second>(accountId, "task"))[0].title).toBe("第二次修改");
  });

  it("离线创建后删除会折叠为无操作", async () => {
    const accountId = "user-a";
    const deviceId = crypto.randomUUID();
    const value = entity(crypto.randomUUID(), "临时任务");

    await enqueueMutation({ accountId, deviceId, entityType: "task", entityId: value.id, operation: "create", payload: value, optimisticEntity: value });
    await enqueueMutation({ accountId, deviceId, entityType: "task", entityId: value.id, operation: "delete", baseVersion: 0, payload: {} });

    expect(await listMutations(accountId, deviceId)).toEqual([]);
    expect(await getCachedEntities(accountId, "task")).toEqual([]);
  });

  it("离线更新后删除复用原 Mutation 顺序与基础版本", async () => {
    const accountId = "user-a";
    const deviceId = crypto.randomUUID();
    const value = { ...entity(crypto.randomUUID(), "待删除任务"), version: 7 };

    const first = await enqueueMutation({ accountId, deviceId, entityType: "task", entityId: value.id, operation: "update", baseVersion: 7, payload: value, optimisticEntity: value });
    await enqueueMutation({ accountId, deviceId, entityType: "task", entityId: value.id, operation: "delete", baseVersion: 7, payload: {} });

    const mutations = await listMutations(accountId, deviceId);
    expect(mutations).toHaveLength(1);
    expect(mutations[0]).toMatchObject({ mutationId: first.mutationId, sequence: first.sequence, operation: "delete", baseVersion: 7, payload: {} });
    expect(await getCachedEntities(accountId, "task")).toEqual([]);
  });
});
