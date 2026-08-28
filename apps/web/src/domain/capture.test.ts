import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { parseCapture } from "./capture";
import { createSeedData } from "./seed";

describe("parseCapture", () => {
  beforeEach(() => vi.setSystemTime(new Date("2026-08-27T10:00:00+08:00")));
  afterEach(() => vi.useRealTimers());

  it("识别带星期和时段的日程", () => {
    const draft = parseCapture("周五下午 3 点看牙", createSeedData().goals);
    expect(draft.kind).toBe("event");
    expect(new Date(draft.startAt!).getDay()).toBe(5);
    expect(new Date(draft.startAt!).getHours()).toBe(15);
    expect(draft.confidence).toBeGreaterThan(0.9);
  });

  it("不会把明早跑 5 公里的距离误判为 5 点", () => {
    const draft = parseCapture("明早跑 5 公里", createSeedData().goals);
    expect(draft.kind).toBe("task");
    expect(new Date(draft.startAt!).getHours()).toBe(7);
    expect(new Date(draft.startAt!).getMinutes()).toBe(30);
    expect(draft.estimateMinutes).toBe(35);
  });

  it("把状态分数归一化为五分制记录", () => {
    const draft = parseCapture("今天状态 7 分，下午有点困", createSeedData().goals, "record");
    expect(draft.kind).toBe("record");
    expect(draft.recordKind).toBe("status");
    expect(draft.energy).toBe(4);
  });
});
