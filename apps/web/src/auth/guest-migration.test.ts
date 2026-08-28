import "fake-indexeddb/auto";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createSeedData } from "../domain/seed";
import { deleteDayOrderDB, getSyncMetadata } from "../offline/db";
import { listMutations, removeMutation } from "../offline/mutations";
import { GUEST_STORAGE_KEY } from "../store/storage";
import { migrateGuestData, normalizeGuestDataForMigration } from "./guest-migration";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

describe("guest account migration", () => {
  beforeEach(async () => {
    localStorage.clear();
    await deleteDayOrderDB();
  });

  it("把演示/旧游客 ID 转成 UUID，并保持目标关系", () => {
    const migrated = normalizeGuestDataForMigration(createSeedData());

    expect(migrated.goals.every((goal) => uuidPattern.test(goal.id) && goal.version === 0)).toBe(true);
    expect(migrated.goals.flatMap((goal) => goal.milestones).every((item) => uuidPattern.test(item.id) && migrated.goals.some((goal) => goal.id === item.goalId))).toBe(true);
    expect(migrated.tasks.filter((task) => task.goalId).every((task) => migrated.goals.some((goal) => goal.id === task.goalId))).toBe(true);
  });

  it("只有全部迁移 Mutation 成功后才清理游客副本", async () => {
    const data = createSeedData();
    const accountId = crypto.randomUUID();
    localStorage.setItem(GUEST_STORAGE_KEY, JSON.stringify(data));
    const sync = vi.fn(async () => {
      const deviceId = (await getSyncMetadata(accountId))?.deviceId ?? "";
			const mutations = await listMutations(accountId, deviceId);
      for (const mutation of mutations) await removeMutation(accountId, mutation.mutationId);
    });

    await migrateGuestData(accountId, data, sync);

    expect(sync).toHaveBeenCalledWith(accountId);
    expect(localStorage.getItem(GUEST_STORAGE_KEY)).toBeNull();
  });

  it("迁移存在拒绝或冲突时保留游客副本", async () => {
    const data = createSeedData();
    const accountId = crypto.randomUUID();
    localStorage.setItem(GUEST_STORAGE_KEY, JSON.stringify(data));

    await expect(migrateGuestData(accountId, data, vi.fn().mockResolvedValue(undefined))).rejects.toThrow("未全部完成");

    expect(localStorage.getItem(GUEST_STORAGE_KEY)).not.toBeNull();
  });
});
