import { describe, expect, it, vi } from "vitest";
import { createSeedData } from "./seed";
import { buildTodayPlan } from "./planning";

describe("buildTodayPlan", () => {
  it("只使用真实任务并避开固定日程与缓冲", () => {
    const now = new Date("2026-08-27T10:00:00+08:00");
    vi.setSystemTime(now);
    const data = createSeedData();
    const result = buildTodayPlan(data, now);

    expect(result.plans.length).toBeGreaterThan(0);
    expect(result.plans.length).toBeLessThanOrEqual(3);
    result.plans.forEach((plan) => {
      expect(data.tasks.some((task) => task.id === plan.taskId && task.title === plan.title)).toBe(true);
      const start = new Date(plan.startAt).getTime();
      const end = new Date(plan.endAt).getTime();
      data.events.filter((event) => new Date(event.startAt).toDateString() === now.toDateString()).forEach((event) => {
        const blockedStart = new Date(event.startAt).getTime() - 15 * 60_000;
        const blockedEnd = new Date(event.endAt).getTime() + 15 * 60_000;
        expect(start < blockedEnd && end > blockedStart).toBe(false);
      });
    });
  });

  it("没有可用连续时间时明确说明未排入原因", () => {
    const now = new Date("2026-08-27T19:50:00+08:00");
    vi.setSystemTime(now);
    const data = createSeedData();
    const result = buildTodayPlan(data, now);
    expect(result.deferred.length).toBeGreaterThan(0);
    expect(result.deferred[0].reason).toContain("没有足够的连续时间");
  });
});
