import { describe, expect, it } from "vitest";
import { createEmptyData } from "./seed";
import type { CalendarReminder, Milestone } from "./types";

describe("versioned resource types", () => {
  it("本地空数据的设置从未同步版本开始", () => {
    expect(createEmptyData().settings.version).toBe(0);
  });

  it("里程碑和提醒显式保存父实体 ID", () => {
    const milestone: Milestone = {
      id: crypto.randomUUID(), goalId: crypto.randomUUID(), title: "里程碑", sortOrder: 1,
      version: 0, createdAt: "2026-08-28T00:00:00Z", updatedAt: "2026-08-28T00:00:00Z",
    };
    const reminder: CalendarReminder = {
      id: crypto.randomUUID(), eventId: crypto.randomUUID(), offsetMinutes: 10, channel: "in_app",
      scheduledAt: "2026-08-28T00:00:00Z", status: "pending", attempts: 0, version: 0,
      createdAt: "2026-08-28T00:00:00Z", updatedAt: "2026-08-28T00:00:00Z",
    };
    expect(milestone.goalId).toBeTruthy();
    expect(reminder.eventId).toBeTruthy();
  });
});
