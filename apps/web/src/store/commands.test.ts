import { describe, expect, it } from "vitest";
import { createSeedData } from "../domain/seed";
import { appReducer, type Action } from "./AppStore";
import { prepareInitialMutations, prepareMutations } from "./commands";

describe("store commands", () => {
  it("全局重命名标签只提交一个标签 Mutation，并立即更新所有引用名称", () => {
    const timestamp = "2026-09-03T08:00:00.000Z";
    const tag = { id: crypto.randomUUID(), name: "产品", version: 2, createdAt: timestamp, updatedAt: timestamp };
    const before = { ...createSeedData(), tags: [tag] };
    const action = { type: "update-tag", tag: { ...tag, name: "产品设计", updatedAt: "2026-09-03T09:00:00.000Z" } } as never;

    const after = appReducer(before, action);
    const mutations = prepareMutations("user-a", before, after, action);

    expect(after.tags).toEqual([expect.objectContaining({ id: tag.id, name: "产品设计" })]);
    expect(after.notes.find((note) => note.id === "note_loop")?.tags).toContain("产品设计");
    expect(after.notes.find((note) => note.id === "note_loop")?.tags).not.toContain("产品");
    expect(mutations).toHaveLength(1);
    expect(mutations[0]).toMatchObject({ entityType: "tag", entityId: tag.id, operation: "update", baseVersion: 2, payload: { name: "产品设计" } });
  });

  it("可以创建暂未被内容引用的全局标签", () => {
    const before = createSeedData();
    const timestamp = "2026-09-03T08:00:00.000Z";
    const tag = { id: crypto.randomUUID(), name: "灵感", version: 0, createdAt: timestamp, updatedAt: timestamp };
    const action = { type: "add-tag", tag } as never;

    const after = appReducer(before, action);
    const mutations = prepareMutations("user-a", before, after, action);

    expect(after.tags).toContainEqual(tag);
    expect(mutations).toHaveLength(1);
    expect(mutations[0]).toMatchObject({ entityType: "tag", entityId: tag.id, operation: "create", baseVersion: 0, payload: { name: "灵感" } });
  });

  it("全局删除标签会移除全部引用且只提交一个标签 Mutation", () => {
    const before = createSeedData();
    const tag = before.tags.find((item) => item.name === "产品")!;
    const action = { type: "delete-tag", id: tag.id } as never;

    const after = appReducer(before, action);
    const mutations = prepareMutations("user-a", before, after, action);

    expect(after.tags.some((item) => item.id === tag.id)).toBe(false);
    expect(after.notes.some((note) => note.tags.includes("产品"))).toBe(false);
    expect(after.records.some((record) => record.tags.includes("产品"))).toBe(false);
    expect(mutations).toHaveLength(1);
    expect(mutations[0]).toMatchObject({ entityType: "tag", entityId: tag.id, operation: "delete", baseVersion: 1 });
  });

  it("把单任务乐观更新转换为带基础版本的资源 Mutation", () => {
    const before = createSeedData();
    const task = before.tasks[0];
    const action: Action = { type: "update-task", task: { ...task, title: "新的标题", updatedAt: new Date().toISOString() } };
    const after = appReducer(before, action);

    const mutations = prepareMutations("user-a", before, after, action);

    expect(mutations).toHaveLength(1);
    expect(mutations[0]).toMatchObject({ entityType: "task", entityId: task.id, operation: "update", baseVersion: task.version });
    expect(mutations[0].payload).toMatchObject({ id: task.id, title: "新的标题" });
    expect(mutations[0].payload).not.toHaveProperty("version");
  });

  it("快速记录按依赖顺序产生记录和目标创建", () => {
    const before = createSeedData();
    const action: Action = { type: "save-capture", draft: {
      rawText: "建立可持续写作习惯", kind: "goal", title: "每周写作", occurredAt: new Date().toISOString(), confidence: 0.9, explanation: "目标",
    } };
    const after = appReducer(before, action);

    const mutations = prepareMutations("user-a", before, after, action);

    const creates = mutations.filter((item) => item.operation === "create");
    expect(creates.map((item) => item.entityType)).toEqual(["goal", "tag", "tag", "record"]);
    expect(creates.filter((item) => item.entityType === "tag").map((item) => item.payload.name)).toEqual(["已整理", "goal"]);
  });

  it("删除目标不重复提交服务端已经级联处理的任务变化", () => {
    const before = createSeedData();
    const goal = before.goals.find((item) => before.tasks.some((task) => task.goalId === item.id))!;
    const action: Action = { type: "delete-goal", id: goal.id };
    const after = appReducer(before, action);

    const mutations = prepareMutations("user-a", before, after, action);

    expect(mutations.some((item) => item.entityType === "goal" && item.operation === "delete")).toBe(true);
    expect(mutations.some((item) => item.entityType === "task")).toBe(false);
  });

  it("游客迁移把业务实体作为创建提交，并把既有账户设置作为版本 1 更新", () => {
    const data = createSeedData();
    const accountId = crypto.randomUUID();

    const mutations = prepareInitialMutations(accountId, data);

    expect(mutations.filter((item) => item.entityType !== "user_settings").every((item) => item.operation === "create" && item.baseVersion === 0)).toBe(true);
    expect(mutations.at(-1)).toMatchObject({ entityType: "user_settings", entityId: accountId, operation: "update", baseVersion: 1 });
  });

  it("笔记 Mutation 把跨实体关联作为关系字段提交", () => {
    const before = createSeedData();
    const note = before.notes[0];
    const linkedEntityIds = [before.goals[0].id, before.records[0].id];
    const action: Action = { type: "update-note", note: { ...note, linkedEntityIds, updatedAt: new Date().toISOString() } };

    const mutation = prepareMutations("user-a", before, appReducer(before, action), action)[0];

    expect(mutation).toMatchObject({ entityType: "note", operation: "update" });
    expect(mutation.payload.linkedEntityIds).toEqual(linkedEntityIds);
  });
});
