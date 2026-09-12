import { expect, type Page } from "@playwright/test";

import type { ReadonlyRunStart, ReadonlyRunView, RuntimeEffect, RuntimeInput, RuntimeState, Usage } from "../../src/agent/generated/protocol";

export interface HarnessConfig {
  accounts: Array<{ email: string; password: string; deviceId: string }>;
  profile: string;
  window: { start: string; end: string };
  pagingWindow?: { start: string; end: string };
}

export interface RuntimeTrace {
  state: RuntimeState;
  inputs: RuntimeInput[];
  effects: RuntimeEffect[];
  modelTurns: number;
}

export interface TestRunState {
  status: string;
  errorCode?: string;
  summary: string;
  usage: Usage;
  usageComplete: boolean;
  outboxCount: number;
  outboxStatus?: string;
  deliveries: number;
  providerCalls: number;
  calendarCalls: number;
  activeDependencies: number;
  attempts: number[];
  sourceRefs: Array<{ entityId: string; entityVersion: number }>;
  trace: {
    status: "ready" | "pending" | "missing" | "error";
    errorCode?: string;
    value?: RuntimeTrace;
  };
}

export interface HTTPErrorEnvelope {
  error: {
    code: string;
  };
}

export type CalendarSnapshot = Array<{ id: string; version: number }>;

interface BrowserResponse<T = unknown> {
  ok: boolean;
  status: number;
  body: T;
}

export async function browserFetch<T>(page: Page, path: string, options: {
  method?: string;
  headers?: Record<string, string>;
  body?: unknown;
} = {}): Promise<BrowserResponse<T>> {
  return page.evaluate(async ({ target, init }) => {
    const response = await fetch(target, {
      method: init.method,
      credentials: "include",
      cache: "no-store",
      headers: init.headers,
      body: init.body === undefined ? undefined : JSON.stringify(init.body),
    });
    const text = await response.text();
    return {
      ok: response.ok,
      status: response.status,
      body: text === "" ? undefined : JSON.parse(text),
    };
  }, { target: path, init: options });
}

export async function openHarness(page: Page): Promise<HarnessConfig> {
  await page.goto("/tests/agent-integration/harness/");
  await page.waitForFunction(() => Boolean((window as Window & { agentTest?: unknown }).agentTest));
  return (await browserFetch<HarnessConfig>(page, "/__test/config")).body;
}

export async function loginAs(page: Page, accountIndex: number): Promise<HarnessConfig> {
  const config = await openHarness(page);
  const account = config.accounts[accountIndex];
  if (!account) throw new Error(`fixture account ${accountIndex} is unavailable`);
  const response = await browserFetch(page, "/api/v1/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: { email: account.email, password: account.password },
  });
  expect(response.status).toBe(200);
  return config;
}

export async function armFault(page: Page, fault: string): Promise<void> {
  const response = await browserFetch(page, "/__test/fault", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: { fault },
  });
  expect(response.status).toBe(200);
}

export function runInput(config: HarnessConfig, executionMode: "foreground" | "background", window = config.window): ReadonlyRunStart {
  return {
    executionMode,
    intent: "概览授权时间窗内的日程。",
    modelProfile: config.profile,
    timezone: "UTC",
    scope: { domains: ["calendar"], from: window.start, to: window.end },
  };
}

export async function createRun(page: Page, config: HarnessConfig, accountIndex: number, executionMode: "foreground" | "background", input = runInput(config, executionMode)): Promise<ReadonlyRunView> {
  const account = config.accounts[accountIndex];
  const response = await browserFetch<ReadonlyRunView>(page, "/api/v1/agent/runs", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": crypto.randomUUID(),
      "X-Device-ID": account.deviceId,
    },
    body: input,
  });
  expect(response.status).toBe(201);
  return response.body;
}

export async function readRunState(page: Page, runId: string): Promise<TestRunState> {
  const response = await browserFetch<TestRunState>(page, `/__test/runs/${runId}`);
  expect(response.status).toBe(200);
  return response.body;
}

export function controlledRunState(state: TestRunState): string {
  return JSON.stringify({
    status: state.status,
    errorCode: state.errorCode,
    usage: state.usage,
    usageComplete: state.usageComplete,
    outboxCount: state.outboxCount,
    outboxStatus: state.outboxStatus,
    deliveries: state.deliveries,
    providerCalls: state.providerCalls,
    calendarCalls: state.calendarCalls,
    activeDependencies: state.activeDependencies,
    attempts: state.attempts,
    traceStatus: state.trace.status,
    traceErrorCode: state.trace.errorCode,
  });
}

export async function waitForRunState(
  page: Page,
  runId: string,
  predicate: (state: TestRunState) => boolean,
  timeout = 15_000,
  message = "Run did not reach the expected controlled state",
): Promise<TestRunState> {
  let observed: TestRunState | undefined;
  await expect.poll(async () => {
    observed = await readRunState(page, runId);
    return predicate(observed) ? "matched" : controlledRunState(observed);
  }, { timeout, intervals: [25, 50, 100, 200], message }).toBe("matched");
  return observed!;
}

export async function calendarSnapshot(page: Page, config: HarnessConfig): Promise<CalendarSnapshot> {
  const snapshots: CalendarSnapshot = [];
  for (const window of [config.window, config.pagingWindow].filter((candidate): candidate is { start: string; end: string } => candidate !== undefined)) {
    const query = new URLSearchParams({ start: window.start, end: window.end, limit: "100" });
    const response = await browserFetch<{ events: Array<{ id: string; version: number }>; hasMore: boolean }>(page, `/api/v1/calendar-events?${query}`);
    expect(response.status).toBe(200);
    expect(response.body.hasMore).toBe(false);
    snapshots.push(...response.body.events.map((event) => ({ id: event.id, version: event.version })));
  }
  return snapshots.sort((left, right) => left.id.localeCompare(right.id));
}

export function firstCalendarSnapshotDifference(left: CalendarSnapshot, right: CalendarSnapshot): string | undefined {
  if (left.length !== right.length) return "calendar.length";
  for (let index = 0; index < left.length; index += 1) {
    if (left[index].id !== right[index].id) return `calendar[${index}].id`;
    if (left[index].version !== right[index].version) return `calendar[${index}].version`;
  }
  return undefined;
}

export function calendarResultRefs(trace: RuntimeTrace): Array<{ entityId: string; entityVersion: number }> {
  for (const input of trace.inputs) {
    if (input.type !== "tool_result" || !input.toolResult?.result.ok) continue;
    const events = input.toolResult.result.data?.events;
    if (!Array.isArray(events)) continue;
    return events.map((event) => {
      const candidate = event as { id?: unknown; version?: unknown };
      if (typeof candidate.id !== "string" || typeof candidate.version !== "number") throw new Error("calendar ToolResult omitted entity identity");
      return { entityId: candidate.id, entityVersion: candidate.version };
    });
  }
  throw new Error("calendar ToolResult was not observed");
}

export function canonicalTrace(trace: RuntimeTrace): unknown {
  const normalize = (value: unknown): unknown => {
    if (Array.isArray(value)) return value.map(normalize);
    if (value && typeof value === "object") {
      return Object.fromEntries(Object.entries(value).flatMap(([key, item]) => {
        if (["runId", "callId", "turnId", "executionMode"].includes(key)) return [];
        return [[key, normalize(item)]];
      }));
    }
    return value;
  };
  return normalize(trace);
}

export function firstTraceDifference(left: RuntimeTrace, right: RuntimeTrace): string | undefined {
  const compare = (first: unknown, second: unknown, path: string): string | undefined => {
    if (Object.is(first, second)) return undefined;
    if (Array.isArray(first) && Array.isArray(second)) {
      if (first.length !== second.length) return `${path}.length`;
      for (let index = 0; index < first.length; index += 1) {
        const difference = compare(first[index], second[index], `${path}[${index}]`);
        if (difference) return difference;
      }
      return undefined;
    }
    if (first && second && typeof first === "object" && typeof second === "object") {
      const firstRecord = first as Record<string, unknown>;
      const secondRecord = second as Record<string, unknown>;
      const firstKeys = Object.keys(firstRecord).sort();
      const secondKeys = Object.keys(secondRecord).sort();
      if (firstKeys.join("\u0000") !== secondKeys.join("\u0000")) return `${path}.keys`;
      for (const key of firstKeys) {
        const difference = compare(firstRecord[key], secondRecord[key], `${path}.${key}`);
        if (difference) return difference;
      }
      return undefined;
    }
    return path;
  };
  return compare(canonicalTrace(left), canonicalTrace(right), "trace");
}
