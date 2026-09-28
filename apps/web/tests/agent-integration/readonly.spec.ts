import { expect, test } from "@playwright/test";

import type {
  CalendarReadData,
  ReadonlyRunView,
  ToolResult,
} from "../../src/agent/generated/protocol";
import {
  browserFetch,
  calendarSnapshot,
  createRun,
  calendarResultRefs,
  firstCalendarSnapshotDifference,
  firstTraceDifference,
  loginAs,
  openHarness,
  readRunState,
  waitForRunState,
  type HTTPErrorEnvelope,
  type RuntimeTrace,
} from "./support";
import { validateToolResult } from "./node-protocol-validation";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isCalendarReadData(value: unknown): value is CalendarReadData {
  if (
    !isRecord(value) ||
    !Array.isArray(value.events) ||
    typeof value.hasMore !== "boolean"
  )
    return false;
  if (
    !isRecord(value.window) ||
    typeof value.window.start !== "string" ||
    typeof value.window.end !== "string"
  )
    return false;
  if (value.nextCursor !== null && typeof value.nextCursor !== "string")
    return false;
  return value.events.every(
    (event) =>
      isRecord(event) &&
      typeof event.id === "string" &&
      typeof event.title === "string" &&
      typeof event.startAt === "string" &&
      typeof event.endAt === "string" &&
      typeof event.timezone === "string" &&
      typeof event.kind === "string" &&
      typeof event.version === "number",
  );
}

function isCalendarSuccess(
  value: ToolResult,
): value is ToolResult & { ok: true; data: CalendarReadData } {
  return value.ok === true && isCalendarReadData(value.data);
}

function isHTTPErrorEnvelope(value: unknown): value is HTTPErrorEnvelope {
  return (
    isRecord(value) &&
    isRecord(value.error) &&
    typeof value.error.code === "string"
  );
}

test("foreground and page-independent background runs preserve one ordered readonly contract", async ({
  browser,
  page,
  baseURL,
}) => {
  const config = await openHarness(page);
  const calendarBefore = await calendarSnapshot(page, config);
  const foreground = await page.evaluate(async () => {
    return (
      window as Window & {
        agentTest: {
          runForeground(): Promise<{
            run: ReadonlyRunView;
            trace: RuntimeTrace;
          }>;
        };
      }
    ).agentTest.runForeground();
  });
  expect(foreground.run.status).toBe("completed");
  expect(
    foreground.trace.effects
      .filter((effect) => effect.type === "execute_tool")
      .map((effect) => effect.toolCall?.name),
  ).toEqual(["skill_list", "skill_load", "dayorder.calendar.read"]);
  expect(foreground.run.usage.totalTokens).toBe(60);

  const foregroundDiagnostic = await readRunState(page, foreground.run.runId);
  expect(foregroundDiagnostic.outboxCount).toBe(0);
  expect(foregroundDiagnostic.sourceRefs).toEqual(
    calendarResultRefs(foreground.trace),
  );
  expect(foregroundDiagnostic.trace.status).toBe("missing");

  const responsePromise = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/agent/runs"),
  );
  await page.evaluate(() => {
    void (
      window as Window & {
        agentTest: { runBackground(): Promise<ReadonlyRunView> };
      }
    ).agentTest.runBackground();
  });
  const createdResponse = await responsePromise;
  expect(createdResponse.status()).toBe(201);
  const created = (await createdResponse.json()) as ReadonlyRunView;
  await page.close();

  const independentContext = await browser.newContext({
    baseURL: String(baseURL),
  });
  const independentPage = await independentContext.newPage();
  await openHarness(independentPage);
  const backgroundDiagnostic = await waitForRunState(
    independentPage,
    created.runId,
    (state) =>
      state.status === "completed" &&
      state.outboxStatus === "processed" &&
      state.trace.status === "ready",
  );
  expect(backgroundDiagnostic.outboxCount).toBe(1);
  expect(backgroundDiagnostic.deliveries).toBe(1);
  expect(backgroundDiagnostic.trace.value).toBeDefined();
  expect(backgroundDiagnostic.sourceRefs).toEqual(
    calendarResultRefs(backgroundDiagnostic.trace.value!),
  );
  expect(
    firstTraceDifference(backgroundDiagnostic.trace.value!, foreground.trace),
  ).toBeUndefined();

  const otherContext = await browser.newContext({ baseURL: String(baseURL) });
  const otherPage = await otherContext.newPage();
  await loginAs(otherPage, 1);
  const isolated = await browserFetch(
    otherPage,
    `/__test/runs/${created.runId}`,
  );
  expect(isolated.status).toBe(404);

  expect(
    firstCalendarSnapshotDifference(
      calendarBefore,
      await calendarSnapshot(independentPage, config),
    ),
  ).toBeUndefined();

  await otherContext.close();
  await independentContext.close();
  expect(config.accounts.length).toBe(2);
});

test("fixed disjoint calendar fixture proves two pages, cursor integrity, and the real 64 KiB result limit", async ({
  page,
}) => {
  const config = await loginAs(page, 1);
  const calendarBefore = await calendarSnapshot(page, config);
  expect(config.pagingWindow).toEqual({
    start: "2026-09-07T00:00:00Z",
    end: "2026-09-08T00:00:00Z",
  });
  const run = await createRun(page, config, 1, "foreground", {
    executionMode: "foreground",
    intent: "read the fixed paging fixture",
    modelProfile: config.profile,
    timezone: "UTC",
    scope: {
      domains: ["calendar"],
      from: config.pagingWindow!.start,
      to: config.pagingWindow!.end,
    },
  });
  const account = config.accounts[1];
  const readCalendar = async (
    callId: string,
    input: Record<string, unknown>,
  ) => {
    const response = await browserFetch<unknown>(
      page,
      `/api/v1/agent/runs/${run.runId}/tools/calendar-read`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": crypto.randomUUID(),
          "X-Device-ID": account.deviceId,
        },
        body: { callId, input },
      },
    );
    if (response.ok) {
      const body = validateToolResult(response.body);
      if (response.status !== 200 || !isCalendarSuccess(body))
        throw new Error("calendar read returned an invalid success body");
      return { ok: true as const, status: 200 as const, body };
    }
    if (!isHTTPErrorEnvelope(response.body))
      throw new Error("calendar read returned an invalid HTTP error body");
    return { ok: false as const, status: response.status, body: response.body };
  };
  const window = {
    start: config.pagingWindow!.start,
    end: config.pagingWindow!.end,
  };
  const first = await readCalendar("paging-first", { ...window, limit: 25 });
  expect(first.status).toBe(200);
  if (!first.ok) throw new Error("first calendar page was rejected");
  expect(first.body.ok).toBe(true);
  expect(first.body.data.events.length).toBe(25);
  expect(first.body.data.hasMore).toBe(true);
  expect(first.body.data.nextCursor).toBeTruthy();

  const second = await readCalendar("paging-second", {
    ...window,
    limit: 25,
    cursor: first.body.data.nextCursor!,
  });
  expect(second.status).toBe(200);
  if (!second.ok) throw new Error("second calendar page was rejected");
  expect(second.body.ok).toBe(true);
  expect(second.body.data.events.length).toBe(25);
  expect(second.body.data.hasMore).toBe(false);
  const events = [...first.body.data.events, ...second.body.data.events];
  expect(new Set(events.map((event) => event.id)).size).toBe(50);
  expect(events.map((event) => event.startAt)).toEqual(
    [...events.map((event) => event.startAt)].sort(),
  );
  expect(
    events.every(
      (event) => event.version === 1 && [...event.title].length === 240,
    ),
  ).toBe(true);

  const tampered = `${first.body.data.nextCursor!.slice(0, -1)}x`;
  const invalidCursor = await readCalendar("paging-tampered", {
    ...window,
    limit: 25,
    cursor: tampered,
  });
  expect(invalidCursor.status).toBe(422);
  if (invalidCursor.ok) throw new Error("tampered cursor was accepted");
  expect(invalidCursor.body.error.code).toBe("VALIDATION_FAILED");

  const oversized = await readCalendar("paging-oversized", {
    ...window,
    limit: 50,
  });
  expect(oversized.status).toBe(422);
  if (oversized.ok) throw new Error("oversized ToolResult was accepted");
  expect(oversized.body.error.code).toBe("VALIDATION_FAILED");

  const latest = await browserFetch<ReadonlyRunView>(
    page,
    `/api/v1/agent/runs/${run.runId}`,
    {
      headers: { "X-Device-ID": account.deviceId },
    },
  );
  const cancelled = await browserFetch<ReadonlyRunView>(
    page,
    `/api/v1/agent/runs/${run.runId}/cancel`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Idempotency-Key": crypto.randomUUID(),
        "If-Match": `"${latest.body.version}"`,
        "X-Device-ID": account.deviceId,
      },
      body: {},
    },
  );
  expect(cancelled.status).toBe(200);
  expect(cancelled.body.status).toBe("stopped");
  expect(
    firstCalendarSnapshotDifference(
      calendarBefore,
      await calendarSnapshot(page, config),
    ),
  ).toBeUndefined();
});

test("browser security boundaries use real cookies, origin, and device identity", async ({
  browser,
  page,
  baseURL,
}) => {
  await page.goto("/favicon.ico");
  const anonymous = await browserFetch(
    page,
    `/__test/runs/${crypto.randomUUID()}`,
  );
  expect(anonymous.status).toBe(401);

  const config = await loginAs(page, 0);
  const calendarBefore = await calendarSnapshot(page, config);
  const forged = await browserFetch<HTTPErrorEnvelope>(
    page,
    "/api/v1/agent/runs",
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Idempotency-Key": crypto.randomUUID(),
        "X-Device-ID": crypto.randomUUID(),
      },
      body: {
        executionMode: "foreground",
        intent: "device boundary",
        modelProfile: config.profile,
        timezone: "UTC",
        scope: {
          domains: ["calendar"],
          from: config.window.start,
          to: config.window.end,
        },
      },
    },
  );
  expect(forged.status).toBe(428);
  expect(forged.body.error.code).toBe("DEVICE_REGISTRATION_REQUIRED");

  const siblingContext = await browser.newContext({ baseURL: String(baseURL) });
  const siblingPage = await siblingContext.newPage();
  await loginAs(siblingPage, 0);
  const rejectedExpiry = await browserFetch(page, "/__test/session/expire", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: { sessionId: crypto.randomUUID() },
  });
  expect(rejectedExpiry.status).toBe(422);
  const expired = await browserFetch(page, "/__test/session/expire", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: {},
  });
  expect(expired.status).toBe(200);
  const currentAfterExpiry = await browserFetch(
    page,
    `/api/v1/agent/runs/${crypto.randomUUID()}`,
    {
      headers: { "X-Device-ID": config.accounts[0].deviceId },
    },
  );
  expect(currentAfterExpiry.status).toBe(401);
  const siblingAfterExpiry = await browserFetch(
    siblingPage,
    `/api/v1/agent/runs/${crypto.randomUUID()}`,
    {
      headers: { "X-Device-ID": config.accounts[0].deviceId },
    },
  );
  expect(siblingAfterExpiry.status).toBe(404);
  expect(
    firstCalendarSnapshotDifference(
      calendarBefore,
      await calendarSnapshot(siblingPage, config),
    ),
  ).toBeUndefined();
  await siblingContext.close();

  const attackerContext = await browser.newContext({
    baseURL: String(baseURL),
  });
  const attackerPage = await attackerContext.newPage();
  const trusted = new URL(String(baseURL));
  const attackerOrigin = `${trusted.protocol}//localhost:${trusted.port}`;
  await attackerPage.goto(`${attackerOrigin}/favicon.ico`);
  const blocked = await attackerPage.evaluate(async (target) => {
    try {
      await fetch(target, { credentials: "include", cache: "no-store" });
      return false;
    } catch {
      return true;
    }
  }, `${trusted.origin}/__test/config`);
  expect(blocked).toBe(true);
  await attackerContext.close();
});
