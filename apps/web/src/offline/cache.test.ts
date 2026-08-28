import "fake-indexeddb/auto";
import { beforeEach, describe, expect, it } from "vitest";
import { clearAccountCache, getCachedEntities, hasAccountCache, putCachedEntities } from "./cache";
import { deleteDayOrderDB } from "./db";

const goal = (id: string, title: string, version = 1) => ({
  id, title, version, createdAt: "2026-08-28T00:00:00Z", updatedAt: "2026-08-28T00:00:00Z",
});

describe("IndexedDB entity cache", () => {
  beforeEach(async () => deleteDayOrderDB());

  it("按账户和实体类型隔离相同 ID", async () => {
    const id = crypto.randomUUID();
    await putCachedEntities("user-a", "goal", [goal(id, "A 的目标")]);
    await putCachedEntities("user-b", "goal", [goal(id, "B 的目标")]);

    expect((await getCachedEntities<typeof goal extends (...args: never[]) => infer R ? R : never>("user-a", "goal"))[0].title).toBe("A 的目标");
    expect((await getCachedEntities<{ id: string; title: string }>("user-b", "goal"))[0].title).toBe("B 的目标");
  });

  it("登出清理指定账户，不影响游客空间和其他账户", async () => {
    await putCachedEntities("guest", "goal", [goal(crypto.randomUUID(), "游客目标")]);
    await putCachedEntities("user-a", "goal", [goal(crypto.randomUUID(), "待清理")]);
    await putCachedEntities("user-b", "goal", [goal(crypto.randomUUID(), "保留")]);

    await clearAccountCache("user-a");

    expect(await hasAccountCache("user-a")).toBe(false);
    expect(await hasAccountCache("guest")).toBe(true);
    expect(await hasAccountCache("user-b")).toBe(true);
  });
});
