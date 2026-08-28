import "fake-indexeddb/auto";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/http";
import { getCachedEntities } from "../offline/cache";
import { deleteDayOrderDB, getSyncMetadata, putSyncMetadata } from "../offline/db";
import { enqueueMutation, listMutations } from "../offline/mutations";
import { runSyncCycle } from "./engine";

const task = (id: string, title: string, version: number) => ({ id, title, status: "todo", priority: "normal", estimateMinutes: 30, version, createdAt: "2026-08-28T00:00:00Z", updatedAt: "2026-08-28T00:00:00Z" });

describe("sync engine", () => {
  beforeEach(async () => deleteDayOrderDB());

  it("首次同步注册设备，以 Bootstrap 高水位建立快照后再推拉", async () => {
    const accountId = crypto.randomUUID();
    const register = vi.fn().mockResolvedValue(undefined);
    const bootstrap = vi.fn().mockResolvedValue({ cursor: "high-water" });
    const snapshot = vi.fn().mockResolvedValue([{ entityType: "task" as const, values: [task(crypto.randomUUID(), "服务端任务", 1)] }]);
    const push = vi.fn().mockResolvedValue(0);
    const pull = vi.fn().mockResolvedValue("after-pull");

    const result = await runSyncCycle(accountId, { register, bootstrap, snapshot, push, pull, deviceName: "Test Browser" });

    expect(register).toHaveBeenCalledOnce();
    expect(snapshot).toHaveBeenCalledWith(accountId);
    expect(pull).toHaveBeenCalledWith(accountId, result.deviceId, "high-water");
    expect((await getSyncMetadata(accountId))?.cursor).toBe("after-pull");
    expect(await getCachedEntities(accountId, "task")).toHaveLength(1);
  });

  it("SYNC_RESET_REQUIRED 全量重建时保留待提交 Mutation 和乐观实体", async () => {
    const accountId = crypto.randomUUID();
    const deviceId = crypto.randomUUID();
    const id = crypto.randomUUID();
    const local = task(id, "本地未提交", 1);
    await putSyncMetadata({ accountId, deviceId, cursor: "expired", nextMutationSequence: 1 });
    await enqueueMutation({ accountId, deviceId, entityType: "task", entityId: id, operation: "update", baseVersion: 1, payload: { id, title: local.title }, optimisticEntity: local });
    const pull = vi.fn()
      .mockRejectedValueOnce(new ApiError(409, { code: "SYNC_RESET_REQUIRED", message: "重建" }))
      .mockResolvedValueOnce("after-reset");
    const serverTask = task(id, "服务端版本", 2);

    await runSyncCycle(accountId, {
      register: vi.fn().mockResolvedValue(undefined),
      bootstrap: vi.fn().mockResolvedValue({ cursor: "new-high-water" }),
      snapshot: vi.fn().mockResolvedValue([{ entityType: "task", values: [serverTask] }]),
      push: vi.fn().mockResolvedValue(0),
      pull,
      deviceName: "Test Browser",
    });

    expect(await listMutations(accountId, deviceId)).toHaveLength(1);
    expect((await getCachedEntities<ReturnType<typeof task>>(accountId, "task"))[0].title).toBe("本地未提交");
    expect((await getSyncMetadata(accountId))?.cursor).toBe("after-reset");
  });
});
