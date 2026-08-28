import "fake-indexeddb/auto";
import { beforeEach, describe, expect, it } from "vitest";
import { putCachedEntities } from "../offline/cache";
import { deleteDayOrderDB } from "../offline/db";
import { loadCachedAppData } from "./selectors";

const metadata = { version: 2, createdAt: "2026-08-28T00:00:00Z", updatedAt: "2026-08-28T01:00:00Z" };

describe("cached AppData projection", () => {
  beforeEach(async () => deleteDayOrderDB());

  it("把关系资源组合成页面使用的目标、日程、标签和设置", async () => {
    const accountId = crypto.randomUUID();
    const goalId = crypto.randomUUID();
    const eventId = crypto.randomUUID();
    await putCachedEntities(accountId, "goal", [{ id: goalId, title: "目标", why: "原因", area: "工作", metricType: "milestone", targetValue: 1, currentValue: 0, unit: "项", startDate: "2026-08-28", status: "active", health: "normal", ...metadata }]);
    await putCachedEntities(accountId, "goal_milestone", [{ id: crypto.randomUUID(), goalId, title: "第一步", sortOrder: 1, ...metadata }]);
    await putCachedEntities(accountId, "calendar_event", [{ id: eventId, title: "日程", startAt: "2026-08-28T02:00:00Z", endAt: "2026-08-28T03:00:00Z", timezone: "Asia/Shanghai", kind: "focus", ...metadata }]);
    await putCachedEntities(accountId, "calendar_reminder", [{ id: crypto.randomUUID(), eventId, offsetMinutes: 10, channel: "in_app", scheduledAt: "2026-08-28T01:50:00Z", status: "pending", attempts: 0, ...metadata }]);
    await putCachedEntities(accountId, "record", [{ id: crypto.randomUUID(), rawText: "记录", kind: "idea", occurredAt: "2026-08-28T01:00:00Z", tags: [{ id: crypto.randomUUID(), name: "想法", ...metadata }], ...metadata }]);
    await putCachedEntities(accountId, "user_settings", [{ id: accountId, schemaVersion: 1, settings: { energy: 4, remindersEnabled: true }, ...metadata }]);

    const data = await loadCachedAppData(accountId);

    expect(data.goals[0].milestones[0]).toMatchObject({ goalId, title: "第一步" });
    expect(data.events[0]).toMatchObject({ reminderMinutes: [10], timezone: "Asia/Shanghai" });
    expect(data.records[0].tags).toEqual(["想法"]);
    expect(data.settings).toMatchObject({ version: 2, energy: 4, remindersEnabled: true });
  });
});
