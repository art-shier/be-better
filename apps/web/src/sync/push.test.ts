import "fake-indexeddb/auto";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { getCachedEntities } from "../offline/cache";
import { deleteDayOrderDB } from "../offline/db";
import { enqueueMutation, listMutations } from "../offline/mutations";
import { pushMutations } from "./push";

const note = (id: string, bodyMarkdown: string, version: number) => ({ id, title: "笔记", bodyMarkdown, category: "其他", tags: [], linkedEntityIds: [], version, createdAt: "2026-08-28T00:00:00Z", updatedAt: "2026-08-28T00:00:00Z" });

describe("offline mutation push", () => {
  beforeEach(async () => deleteDayOrderDB());

  it("应用成功或 duplicate 的结果并移除队列项", async () => {
    const accountId = crypto.randomUUID();
    const deviceId = crypto.randomUUID();
    const id = crypto.randomUUID();
    const local = note(id, "本地正文", 1);
    const mutation = await enqueueMutation({ accountId, deviceId, entityType: "note", entityId: id, operation: "update", baseVersion: 1, payload: { id, title: local.title, bodyMarkdown: local.bodyMarkdown, category: local.category, tags: [] }, optimisticEntity: local });
    const server = note(id, "服务端已接受", 2);
    const api = vi.fn().mockResolvedValue({ results: [{ mutationId: mutation.mutationId, status: "duplicate", data: server }] });

    await pushMutations(accountId, deviceId, api);

    expect(await listMutations(accountId, deviceId)).toEqual([]);
    expect((await getCachedEntities<ReturnType<typeof note>>(accountId, "note"))[0]).toMatchObject({ bodyMarkdown: "服务端已接受", version: 2 });
  });

  it("Note 正文冲突保留独立本地副本", async () => {
    const accountId = crypto.randomUUID();
    const deviceId = crypto.randomUUID();
    const id = crypto.randomUUID();
    const local = note(id, "本地正文", 1);
    const mutation = await enqueueMutation({ accountId, deviceId, entityType: "note", entityId: id, operation: "update", baseVersion: 1, payload: { id, title: local.title, bodyMarkdown: local.bodyMarkdown, category: local.category, tags: [] }, optimisticEntity: local });
    const server = note(id, "服务端正文", 2);
    const api = vi.fn().mockResolvedValue({ results: [{ mutationId: mutation.mutationId, status: "conflict", error: { code: "ENTITY_VERSION_CONFLICT", message: "冲突" }, data: server }] });

    await pushMutations(accountId, deviceId, api);

    const saved = (await listMutations(accountId, deviceId))[0];
    expect(saved).toMatchObject({ status: "conflict", errorCode: "ENTITY_VERSION_CONFLICT" });
    expect(saved.localCopy).toMatchObject({ bodyMarkdown: "本地正文" });
    expect((await getCachedEntities<ReturnType<typeof note>>(accountId, "note"))[0].bodyMarkdown).toBe("本地正文");
  });
});
