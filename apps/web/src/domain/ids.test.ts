import { describe, expect, it } from "vitest";
import { createId } from "./ids";

describe("createId", () => {
  it("生成不带实体前缀的随机 UUID", () => {
    const first = createId("task");
    const second = createId("goal");
    expect(first).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i);
    expect(second).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i);
    expect(first).not.toBe(second);
  });
});
