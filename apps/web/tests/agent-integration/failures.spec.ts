import { expect, test } from "@playwright/test";

import type { ReadonlyRunView } from "../../src/agent/generated/protocol";
import {
  armFault,
  calendarSnapshot,
  controlledRunState,
  firstTraceDifference,
  firstCalendarSnapshotDifference,
  createRun,
  loginAs,
  openHarness,
  readRunState,
  waitForRunState,
  type RuntimeTrace,
  type TestRunState,
} from "./support";

async function runForeground(page: Parameters<typeof openHarness>[0]): Promise<{ run: ReadonlyRunView; trace: RuntimeTrace }> {
  return page.evaluate(async () => {
    return (window as Window & { agentTest: { runForeground(): Promise<{ run: ReadonlyRunView; trace: RuntimeTrace }> } }).agentTest.runForeground();
  });
}

async function runBackground(page: Parameters<typeof openHarness>[0]): Promise<ReadonlyRunView> {
  return page.evaluate(async () => {
    return (window as Window & { agentTest: { runBackground(): Promise<ReadonlyRunView> } }).agentTest.runBackground();
  });
}

async function readyTrace(page: Parameters<typeof openHarness>[0], runId: string): Promise<TestRunState> {
  return waitForRunState(page, runId, (state) => state.outboxStatus === "processed" && state.trace.status === "ready");
}

test("tool and Run timeout outcomes compare across the real browser and Worker hosts", async ({ page }) => {
  const config = await openHarness(page);
  const calendarBefore = await calendarSnapshot(page, config);
  for (const fault of ["tool_timeout", "run_timeout"] as const) {
    await armFault(page, fault);
    const foreground = await runForeground(page);
    await armFault(page, fault);
    const background = await runBackground(page);
    const observed = await readyTrace(page, background.runId);
    expect(observed.trace.value).toBeDefined();
    expect(firstTraceDifference(observed.trace.value!, foreground.trace)).toBeUndefined();
    if (fault === "tool_timeout") {
      expect(foreground.run.status).toBe("completed");
      expect(observed.status).toBe("completed");
      expect(observed.calendarCalls).toBe(1);
      expect(observed.summary.includes("未能完成查询")).toBe(true);
    } else {
      expect(foreground.run.status).toBe("failed");
      expect(observed.status).toBe("failed");
      expect(foreground.run.error?.code).toBe("timeout");
      expect(observed.errorCode).toBe("timeout");
      expect(foreground.run.error?.code).toBe(observed.errorCode);
    }
  }
  expect(firstCalendarSnapshotDifference(calendarBefore, await calendarSnapshot(page, config))).toBeUndefined();
});

test("user cancellation stops real Provider and Calendar dependencies within one second and compares traces", async ({ page }) => {
  const config = await openHarness(page);
  const calendarBefore = await calendarSnapshot(page, config);
  for (const dependencyFault of ["run_timeout", "tool_timeout"] as const) {
    const traces: RuntimeTrace[] = [];
    for (const mode of ["foreground", "background"] as const) {
      await armFault(page, dependencyFault);
      const createdResponse = page.waitForResponse((response) => response.request().method() === "POST" && response.url().endsWith("/api/v1/agent/runs"));
      await page.evaluate((selectedMode) => {
        const harness = (window as Window & { agentTest: {
          runForeground(): Promise<{ run: ReadonlyRunView; trace: RuntimeTrace }>;
          runBackground(): Promise<ReadonlyRunView>;
        } }).agentTest;
        const execution = selectedMode === "foreground" ? harness.runForeground() : harness.runBackground();
        (window as Window & { activeAcceptance?: Promise<unknown> }).activeAcceptance = execution.then(
          (value) => ({ ok: true, value }),
          () => ({ ok: false }),
        );
      }, mode);
      const created = await (await createdResponse).json() as ReadonlyRunView;
      await waitForRunState(page, created.runId, (state) => state.activeDependencies > 0 && (
        dependencyFault === "tool_timeout" ? state.calendarCalls > 0 : state.providerCalls > 0
      ), 5_000, `${dependencyFault}/${mode}: dependency did not enter`);
      const cancelStarted = Date.now();
      await page.evaluate(() => (window as Window & { agentTest: { cancel(): void } }).agentTest.cancel());
      await waitForRunState(
        page,
        created.runId,
        (state) => state.activeDependencies === 0,
        1_000,
        `${dependencyFault}/${mode}: dependency did not exit after cancellation`,
      );
      expect(Date.now() - cancelStarted).toBeLessThanOrEqual(1_000);
      const settled = await page.evaluate(async () => (window as Window & { activeAcceptance?: Promise<unknown> }).activeAcceptance) as { ok: boolean; value?: unknown };
      const observed = await waitForRunState(
        page,
        created.runId,
        (state) => state.status === "stopped",
        5_000,
        `${dependencyFault}/${mode}: cancelled Run was not persisted as stopped`,
      );
      expect(observed.errorCode).toBe("cancelled");
      if (mode === "foreground") {
        expect(settled.ok).toBe(true);
        const foreground = settled.value as { run: ReadonlyRunView; trace: RuntimeTrace };
        expect(foreground.run.status).toBe("stopped");
        traces.push(foreground.trace);
      } else {
        expect(settled.ok).toBe(false);
        const diagnostic = await waitForRunState(
          page,
          created.runId,
          (state) => state.trace.status === "ready",
          5_000,
          `${dependencyFault}/${mode}: cancelled background trace was not recorded`,
        );
        traces.push(diagnostic.trace.value!);
      }
    }
    expect(firstTraceDifference(traces[1], traces[0])).toBeUndefined();
  }
  expect(firstCalendarSnapshotDifference(calendarBefore, await calendarSnapshot(page, config))).toBeUndefined();
});

test("remaining Worker fault matrix preserves the readonly terminal boundary", async ({ page }) => {
  const config = await loginAs(page, 1);
  const calendarBefore = await calendarSnapshot(page, config);
  for (const fault of ["provider_disconnect", "provider_429", "complete_once", "commit_once", "interrupted"] as const) {
    await armFault(page, fault);
    const run = await createRun(page, config, 1, "background");
    const observed = await readyTrace(page, run.runId);
    const failed = ["provider_disconnect", "provider_429", "interrupted"].includes(fault);
    expect(observed.status, `${fault}: ${controlledRunState(observed)}`).toBe(failed ? "failed" : "completed");
    expect(observed.outboxStatus).toBe("processed");
    if (fault === "provider_disconnect") {
      expect(observed.providerCalls).toBe(1);
      expect(observed.errorCode).toBe("provider_unavailable");
    }
    if (fault === "provider_429") {
      expect(observed.errorCode).toBe("provider_unavailable");
      expect(observed.providerCalls).toBe(2);
      expect(observed.attempts).toEqual([2]);
      expect(observed.usage).toEqual({ inputTokens: 10, outputTokens: 5, totalTokens: 15 });
      expect(observed.usageComplete).toBe(false);
      expect(observed.calendarCalls).toBe(0);
      expect(observed.sourceRefs).toEqual([]);
      expect(observed.trace.value).toBeDefined();
      expect(observed.trace.value!.modelTurns).toBe(2);
      expect(observed.trace.value!.state.phase).toBe("failed");
      expect(observed.trace.value!.state.error?.code).toBe("provider_unavailable");
      expect(observed.trace.value!.effects.filter((effect) => effect.type === "request_model_turn").length).toBe(2);
      expect(observed.trace.value!.effects.at(-1)?.type).toBe("fail_run");
    }
    if (["complete_once", "commit_once"].includes(fault)) expect(observed.providerCalls).toBe(4);
    if (fault === "complete_once") expect(observed.deliveries).toBeGreaterThanOrEqual(2);
    if (fault === "interrupted") {
      expect(observed.errorCode).toBe("internal_error");
      expect(observed.providerCalls).toBeLessThanOrEqual(1);
    }
  }
  expect(firstCalendarSnapshotDifference(calendarBefore, await calendarSnapshot(page, config))).toBeUndefined();
});
