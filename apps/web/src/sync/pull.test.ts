import "fake-indexeddb/auto";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { getCachedEntities, putCachedEntities } from "../offline/cache";
import { deleteDayOrderDB, getSyncMetadata, putSyncMetadata } from "../offline/db";
import { enqueueMutation, listMutations } from "../offline/mutations";
import { pullChanges } from "./pull";

const task = (id: string, title: string, version: number) => ({ id, title, status: "todo", priority: "normal", estimateMinutes: 30, version, createdAt: "2026-08-28T00:00:00Z", updatedAt: "2026-08-28T00:00:00Z" });

describe("incremental pull", () => {
  beforeEach(async () => deleteDayOrderDB());

  it("按版本幂等应用实体和 tombstone，并原子推进游标", async () => {
    const accountId = crypto.randomUUID();
    const deviceId = crypto.randomUUID();
    const removedId = crypto.randomUUID();
    const keptId = crypto.randomUUID();
    await putCachedEntities(accountId, "task", [task(removedId, "待删除", 1), task(keptId, "本地新版本", 3)]);
    await putSyncMetadata({ accountId, deviceId, cursor: "old", nextMutationSequence: 1 });
    const api = vi.fn().mockResolvedValue({
      changes: [
        { sequence: 2, entityType: "task", entityId: removedId, operation: "delete", entityVersion: 2, changedAt: "2026-08-28T01:00:00Z" },
        { sequence: 3, entityType: "task", entityId: keptId, operation: "update", entityVersion: 2, changedAt: "2026-08-28T01:00:00Z", data: task(keptId, "服务端旧版本", 2) },
      ],
      nextCursor: "next",
      hasMore: false,
    });

    await pullChanges(accountId, deviceId, "old", api);

    const tasks = await getCachedEntities<ReturnType<typeof task>>(accountId, "task");
    expect(tasks).toHaveLength(1);
    expect(tasks[0]).toMatchObject({ id: keptId, title: "本地新版本", version: 3 });
    expect((await getSyncMetadata(accountId))?.cursor).toBe("next");
  });

  it("服务端同实体变化不会覆盖未提交本地副本，而是标记冲突", async () => {
    const accountId = crypto.randomUUID();
    const deviceId = crypto.randomUUID();
    const id = crypto.randomUUID();
    const local = task(id, "本地标题", 1);
    await enqueueMutation({ accountId, deviceId, entityType: "task", entityId: id, operation: "update", baseVersion: 1, payload: { id, title: "本地标题" }, optimisticEntity: local });
    const api = vi.fn().mockResolvedValue({ changes: [{ sequence: 2, entityType: "task", entityId: id, operation: "update", entityVersion: 2, changedAt: "2026-08-28T01:00:00Z", data: task(id, "其他设备标题", 2) }], nextCursor: "next", hasMore: false });

    await pullChanges(accountId, deviceId, "old", api);

    expect((await getCachedEntities<ReturnType<typeof task>>(accountId, "task"))[0].title).toBe("本地标题");
    expect((await listMutations(accountId, deviceId))[0]).toMatchObject({ status: "conflict", errorCode: "ENTITY_VERSION_CONFLICT" });
  });
});
