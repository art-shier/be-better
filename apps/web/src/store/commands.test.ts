import { describe, expect, it } from "vitest";
import { createSeedData } from "../domain/seed";
import { appReducer, type Action } from "./AppStore";
import { prepareInitialMutations, prepareMutations } from "./commands";

describe("store commands", () => {
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

    expect(mutations.filter((item) => item.operation === "create").map((item) => item.entityType)).toEqual(["goal", "record"]);
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
