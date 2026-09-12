import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/http";
import type { AgentClient } from "../api/client";
import { calendarOverviewBundle } from "../assets/builtin";
import type { ModelTurnRequest, ProviderEvent, ReadonlyRunFinish, ReadonlyRunStart, ReadonlyRunView } from "../generated/protocol";
import type { ProviderGateway } from "../provider/provider";
import { parseSkillBundle } from "../skill/profile";
import { runForeground } from "./foreground";

const runId = "550e8400-e29b-41d4-a716-446655440000";
const input: ReadonlyRunStart = {
  intent: "Summarize my calendar without repeating this prompt",
  executionMode: "foreground",
  scope: { domains: ["calendar"], from: "2026-09-05T00:00:00Z", to: "2026-09-06T00:00:00Z" },
  timezone: "Asia/Shanghai",
  modelProfile: "client/default",
};

async function runView(overrides: Partial<ReadonlyRunView> = {}): Promise<ReadonlyRunView> {
  const profile = await parseSkillBundle(calendarOverviewBundle());
  return {
    protocolVersion: "2.0",
    runId,
    executionMode: "foreground",
    status: "ready",
    version: 1,
    capabilitySnapshot: {
      runtimeVersion: "2.0.0",
      executionMode: "foreground",
      toolIds: ["dayorder.calendar.read", "skill_list", "skill_load"],
      skills: [{ name: profile.descriptor.name, version: profile.descriptor.version, digest: profile.descriptor.digest }],
      scope: structuredClone(input.scope),
    },
    budget: { maxSteps: 4, maxTokens: 100, maxDurationMs: 30_000, maxWorkers: 1, maxConcurrency: 1, maxRepeatedToolCalls: 2 },
    modelProfile: "client/default",
    serverNow: "2026-09-05T00:00:00Z",
    deadlineAt: "2026-09-05T00:00:30Z",
    usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
    usageComplete: true,
    resultOrigin: "client_reported",
    ...overrides,
  };
}

function client(methods: Partial<AgentClient>): AgentClient {
  return {
    create: vi.fn(), get: vi.fn(), cancel: vi.fn(), finish: vi.fn(), calendar: vi.fn(),
    ...methods,
  };
}

function provider(events: ProviderEvent[], seen: ModelTurnRequest[] = []): ProviderGateway {
  return {
    async *stream(request) {
      seen.push(structuredClone(request));
      for (const event of events) yield structuredClone(event);
    },
  };
}

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("runForeground", () => {
  it("assembles the real Runtime, three bindings, and a summary containing only final assistant text", async () => {
    const created = await runView();
    const latest = { ...created, status: "analyzing" as const, version: 2 };
    const finished = { ...created, status: "completed" as const, version: 3, summary: "Calendar is clear." };
    const finish = vi.fn().mockResolvedValue(finished);
    const seen: ModelTurnRequest[] = [];
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get: vi.fn().mockResolvedValue(latest),
      finish,
    });

    const result = await runForeground(input, api, provider([
      { type: "text_delta", text: "Calendar is clear." },
      { type: "completed", stopReason: "end_turn", usage: { inputTokens: 4, outputTokens: 3, totalTokens: 7 } },
    ], seen), new AbortController().signal);

    expect(result.run).toEqual(finished);
    expect(result.trace.state.phase).toBe("completed");
    expect(seen[0].tools.map((tool) => tool.id).sort()).toEqual(["dayorder.calendar.read", "skill_list", "skill_load"]);
    expect(seen[0].messages[0].content[0]).toEqual({ type: "text", text: input.intent });
    const finishInput = finish.mock.calls[0][2];
    expect(finishInput).toEqual({ phase: "completed", summary: "Calendar is clear.", steps: [] });
    expect(JSON.stringify(finishInput)).not.toContain(input.intent);
    expect(JSON.stringify(finishInput)).not.toContain("effects");
  });

  it("rejects a server capability snapshot with a mismatched built-in Skill digest", async () => {
    const created = await runView();
    created.capabilitySnapshot.skills[0].digest = "f".repeat(64);
    const stream = vi.fn();
    const api = client({ create: vi.fn().mockResolvedValue(created) });

    await expect(runForeground(input, api, { stream }, new AbortController().signal)).rejects.toThrow(/digest/i);
    expect(stream).not.toHaveBeenCalled();
    expect(api.get).not.toHaveBeenCalled();
    expect(api.finish).not.toHaveBeenCalled();
  });

  it("on user abort stops locally and attempts cancel with an independent non-aborted signal", async () => {
    const created = await runView();
    const current = { ...created, status: "analyzing" as const, version: 2 };
    const stopped = { ...created, status: "stopped" as const, version: 3, error: { code: "cancelled" as const, message: "cancelled", retryable: false } };
    const cancel = vi.fn().mockImplementation(async (_runId, _version, signal: AbortSignal) => {
      expect(signal.aborted).toBe(false);
      return stopped;
    });
    const api = client({ create: vi.fn().mockResolvedValue(created), get: vi.fn().mockResolvedValue(current), cancel });
    const stalled: ProviderGateway = {
      async *stream() {
        await new Promise(() => undefined);
      },
    };
    const controller = new AbortController();
    const pending = runForeground(input, api, stalled, controller.signal);
    controller.abort(new DOMException("user stopped", "AbortError"));

    await expect(pending).resolves.toMatchObject({ run: stopped, trace: { state: { phase: "cancelled" } } });
    expect(cancel).toHaveBeenCalledWith(runId, 2, expect.any(AbortSignal));
    expect(api.finish).not.toHaveBeenCalled();
  });

  it("persists explicit cancellation before an aborted transport can win with execution_interrupted", async () => {
    const created = await runView();
    const analyzing = { ...created, status: "analyzing" as const, version: 2 };
    const interrupted = {
      ...created,
      status: "failed" as const,
      version: 3,
      error: { code: "internal_error" as const, message: "execution_interrupted", retryable: false },
    };
    const stopped = {
      ...created,
      status: "stopped" as const,
      version: 3,
      error: { code: "cancelled" as const, message: "cancelled", retryable: false },
    };
    let authoritative: ReadonlyRunView = analyzing;
    let providerStarted!: () => void;
    const started = new Promise<void>((resolve) => { providerStarted = resolve; });
    const transport: ProviderGateway = {
      async *stream(_request, signal) {
        providerStarted();
        await new Promise<void>((resolve) => {
          signal.addEventListener("abort", () => {
            if (authoritative.status !== "stopped") authoritative = interrupted;
            resolve();
          }, { once: true });
        });
      },
    };
    const cancel = vi.fn().mockImplementation(async () => {
      authoritative = stopped;
      return stopped;
    });
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get: vi.fn().mockImplementation(async () => authoritative),
      cancel,
    });
    const controller = new AbortController();
    const pending = runForeground(input, api, transport, controller.signal);
    await started;

    controller.abort(new DOMException("user stopped", "AbortError"));

    await expect(pending).resolves.toMatchObject({ run: { status: "stopped", error: { code: "cancelled" } } });
    expect(cancel).toHaveBeenCalledTimes(1);
    expect(authoritative).toEqual(stopped);
  });

  it("persists a local Run deadline before aborting Provider transport", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-05T00:00:00.000Z"));
    const created = await runView({
      serverNow: "2026-09-05T00:00:00.010Z",
      deadlineAt: "2026-09-05T00:00:00.040Z",
      budget: { maxSteps: 4, maxTokens: 100, maxDurationMs: 120_000, maxWorkers: 1, maxConcurrency: 1, maxRepeatedToolCalls: 2 },
    });
    let authoritative: ReadonlyRunView = { ...created, status: "analyzing", version: 2 };
    const interrupted: ReadonlyRunView = {
      ...created,
      status: "failed",
      version: 3,
      error: { code: "internal_error", message: "execution_interrupted", retryable: false },
    };
    const order: string[] = [];
    let createStarted!: () => void;
    let providerStarted!: () => void;
    const creating = new Promise<void>((resolve) => { createStarted = resolve; });
    const streaming = new Promise<void>((resolve) => { providerStarted = resolve; });
    const transport: ProviderGateway = {
      async *stream(_request, requestSignal) {
        order.push("transport:started");
        providerStarted();
        await new Promise<void>((resolve) => {
          requestSignal.addEventListener("abort", () => {
            order.push("transport:aborted");
            if (Date.now() < Date.parse(created.deadlineAt) && authoritative.status === "analyzing") {
              authoritative = interrupted;
            }
            resolve();
          }, { once: true });
        });
      },
    };
    const finish = vi.fn().mockImplementation(async (
      requestedRunId: string,
      version: number,
      result: ReadonlyRunFinish,
      requestSignal: AbortSignal,
    ) => {
      expect(requestedRunId).toBe(runId);
      expect(version).toBe(authoritative.version);
      expect(requestSignal.aborted).toBe(false);
      order.push(`finish:${result.error?.code ?? result.phase}`);
      if (authoritative.status === "completed" || authoritative.status === "failed" || authoritative.status === "stopped") {
        throw new ApiError(409, { message: "Run is already terminal" });
      }
      const status: ReadonlyRunView["status"] = result.phase === "completed"
        ? "completed"
        : result.phase === "cancelled" ? "stopped" : "failed";
      authoritative = {
        ...authoritative,
        status,
        version: authoritative.version + 1,
        summary: result.summary,
        ...(result.error === undefined ? {} : { error: structuredClone(result.error) }),
      };
      return structuredClone(authoritative);
    });
    const api = client({
      create: vi.fn().mockImplementation(async (requested: ReadonlyRunStart, requestSignal: AbortSignal) => {
        expect(requested).toEqual(input);
        expect(requestSignal.aborted).toBe(false);
        createStarted();
        await new Promise<void>((resolve) => setTimeout(resolve, 10));
        return structuredClone(created);
      }),
      get: vi.fn().mockImplementation(async (requestedRunId: string, requestSignal: AbortSignal) => {
        expect(requestedRunId).toBe(runId);
        expect(requestSignal.aborted).toBe(false);
        order.push(`get:${authoritative.status}:${authoritative.error?.code ?? "none"}`);
        return structuredClone(authoritative);
      }),
      finish,
    });

    const pending = runForeground(input, api, transport, new AbortController().signal);
    await creating;
    await vi.advanceTimersByTimeAsync(10);
    await streaming;
    await vi.advanceTimersByTimeAsync(19);
    expect(authoritative.status).toBe("analyzing");
    await vi.advanceTimersByTimeAsync(1);
    const result = await pending;
    await vi.advanceTimersByTimeAsync(0);

    expect({
      runCode: result.run.error?.code,
      traceCode: result.trace.state.error?.code,
      order,
    }).toEqual({
      runCode: "timeout",
      traceCode: "timeout",
      order: ["transport:started", "get:analyzing:none", "finish:timeout", "transport:aborted"],
    });
  });

  it("preserves an ordinary transport interruption before the local Run deadline", async () => {
    const created = await runView();
    let authoritative: ReadonlyRunView = { ...created, status: "analyzing", version: 2 };
    const order: string[] = [];
    const transport: ProviderGateway = {
      async *stream(_request, requestSignal) {
        expect(requestSignal.aborted).toBe(false);
        order.push("transport:interrupted");
        authoritative = {
          ...authoritative,
          status: "failed",
          version: authoritative.version + 1,
          error: { code: "internal_error", message: "execution_interrupted", retryable: false },
        };
        throw new Error("transport interrupted before deadline");
      },
    };
    const api = client({
      create: vi.fn().mockImplementation(async (requested: ReadonlyRunStart, requestSignal: AbortSignal) => {
        expect(requested).toEqual(input);
        expect(requestSignal.aborted).toBe(false);
        return structuredClone(created);
      }),
      get: vi.fn().mockImplementation(async (requestedRunId: string, requestSignal: AbortSignal) => {
        expect(requestedRunId).toBe(runId);
        expect(requestSignal.aborted).toBe(false);
        order.push(`get:${authoritative.status}:${authoritative.error?.code ?? "none"}`);
        return structuredClone(authoritative);
      }),
    });

    const result = await runForeground(input, api, transport, new AbortController().signal);

    expect(result.run.error?.code).toBe("internal_error");
    expect(result.trace.state.error?.code).toBe("provider_unavailable");
    expect(order).toEqual(["transport:interrupted", "get:failed:internal_error"]);
    expect(api.finish).not.toHaveBeenCalled();
  });

  it("bounds deadline persistence before releasing Provider transport", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-05T00:00:00.000Z"));
    const created = await runView({
      serverNow: "2026-09-05T00:00:00.000Z",
      deadlineAt: "2026-09-05T00:00:00.010Z",
      budget: { maxSteps: 4, maxTokens: 100, maxDurationMs: 120_000, maxWorkers: 1, maxConcurrency: 1, maxRepeatedToolCalls: 2 },
    });
    const caller = new AbortController();
    let providerStarted!: () => void;
    let getStarted!: () => void;
    let transportSignal: AbortSignal | undefined;
    let cleanupSignal: AbortSignal | undefined;
    const streaming = new Promise<void>((resolve) => { providerStarted = resolve; });
    const reading = new Promise<void>((resolve) => { getStarted = resolve; });
    const transport: ProviderGateway = {
      async *stream(_request, requestSignal) {
        transportSignal = requestSignal;
        providerStarted();
        await new Promise<void>((resolve) => requestSignal.addEventListener("abort", () => resolve(), { once: true }));
      },
    };
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get: vi.fn().mockImplementation((_requestedRunId: string, requestSignal: AbortSignal) => {
        cleanupSignal = requestSignal;
        getStarted();
        return new Promise<ReadonlyRunView>((_resolve, reject) => {
          requestSignal.addEventListener("abort", () => reject(requestSignal.reason), { once: true });
        });
      }),
    });

    let outcome: { ok: true } | { ok: false; error: unknown } | undefined;
    const pending = runForeground(input, api, transport, caller.signal).then(
      () => { outcome = { ok: true }; },
      (error: unknown) => { outcome = { ok: false, error }; },
    );
    await streaming;
    await vi.advanceTimersByTimeAsync(10);
    await reading;

    expect(cleanupSignal).not.toBe(caller.signal);
    expect(cleanupSignal?.aborted).toBe(false);
    expect(transportSignal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(4_999);
    expect(outcome).toBeUndefined();
    expect(transportSignal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    await pending;
    await vi.advanceTimersByTimeAsync(0);

    expect(outcome).toMatchObject({ ok: false, error: { name: "TimeoutError" } });
    expect(transportSignal?.aborted).toBe(true);
    expect(api.get).toHaveBeenCalledTimes(1);
    expect(api.finish).not.toHaveBeenCalled();
  });

  it("preserves an authoritative terminal Run when the local deadline releases Provider transport", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-05T00:00:00.000Z"));
    const created = await runView({
      serverNow: "2026-09-05T00:00:00.000Z",
      deadlineAt: "2026-09-05T00:00:00.010Z",
      budget: { maxSteps: 4, maxTokens: 100, maxDurationMs: 120_000, maxWorkers: 1, maxConcurrency: 1, maxRepeatedToolCalls: 2 },
    });
    const terminal: ReadonlyRunView = {
      ...created,
      status: "failed",
      version: 3,
      error: { code: "timeout", message: "authoritative server timeout", retryable: false },
    };
    let authoritative: ReadonlyRunView = { ...created, status: "analyzing", version: 2 };
    let providerStarted!: () => void;
    const streaming = new Promise<void>((resolve) => { providerStarted = resolve; });
    const transport: ProviderGateway = {
      async *stream(_request, requestSignal) {
        providerStarted();
        await new Promise<void>((resolve) => {
          requestSignal.addEventListener("abort", () => {
            if (authoritative.status === "analyzing") {
              authoritative = {
                ...authoritative,
                status: "failed",
                version: authoritative.version + 1,
                error: { code: "internal_error", message: "execution_interrupted", retryable: false },
              };
            }
            resolve();
          }, { once: true });
        });
      },
    };
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get: vi.fn().mockImplementation(async () => structuredClone(authoritative)),
    });
    setTimeout(() => { authoritative = terminal; }, 5);

    const pending = runForeground(input, api, transport, new AbortController().signal);
    await streaming;
    await vi.advanceTimersByTimeAsync(10);
    const result = await pending;
    await vi.advanceTimersByTimeAsync(0);

    expect(result.run).toEqual(terminal);
    expect(authoritative).toEqual(terminal);
    expect(result.trace.state.error?.code).toBe("timeout");
    expect(api.finish).not.toHaveBeenCalled();
  });

  it("keeps the Provider transport open until the bounded cancel attempt settles", async () => {
    const created = await runView();
    const analyzing = { ...created, status: "analyzing" as const, version: 2 };
    const stopped = { ...created, status: "stopped" as const, version: 3 };
    let providerStarted!: () => void;
    let cancelStarted!: () => void;
    let releaseCancel!: () => void;
    let transportSignal: AbortSignal | undefined;
    const started = new Promise<void>((resolve) => { providerStarted = resolve; });
    const cancelling = new Promise<void>((resolve) => { cancelStarted = resolve; });
    const transport: ProviderGateway = {
      async *stream(_request, requestSignal) {
        transportSignal = requestSignal;
        providerStarted();
        await new Promise<void>((resolve) => requestSignal.addEventListener("abort", () => resolve(), { once: true }));
      },
    };
    const cancel = vi.fn().mockImplementation(() => new Promise<ReadonlyRunView>((resolve) => {
      cancelStarted();
      releaseCancel = () => resolve(stopped);
    }));
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get: vi.fn().mockResolvedValue(analyzing),
      cancel,
    });
    const controller = new AbortController();
    const pending = runForeground(input, api, transport, controller.signal);
    await started;

    controller.abort({ agentStop: "user" });
    await cancelling;
    const transportAbortedBeforeCancelSettled = transportSignal?.aborted;
    releaseCancel();
    await pending;

    expect(transportAbortedBeforeCancelSettled).toBe(false);
  });

  it.each([
    {
      name: "permission error",
      result: { ok: false, error: { code: "permission_denied" as const, message: "denied", retryable: false } },
    },
    {
      name: "successful result",
      result: {
        ok: true,
        data: {
          events: [],
          window: { start: input.scope.from!, end: input.scope.to! },
          hasMore: false,
          nextCursor: null,
        },
      },
    },
  ])("does not consume a Calendar $name while explicit cancellation is pending", async ({ result: calendarResult }) => {
    const created = await runView();
    const analyzing = { ...created, status: "analyzing" as const, version: 2 };
    const stopped = {
      ...created,
      status: "stopped" as const,
      version: 3,
      error: { code: "cancelled" as const, message: "cancelled", retryable: false },
    };
    let calendarStarted!: () => void;
    let releaseCalendar!: () => void;
    let cancelStarted!: () => void;
    let releaseCancel!: () => void;
    const calendarPending = new Promise<void>((resolve) => { calendarStarted = resolve; });
    const cancelling = new Promise<void>((resolve) => { cancelStarted = resolve; });
    let turns = 0;
    const transport: ProviderGateway = {
      async *stream() {
        turns += 1;
        if (turns === 1) {
          yield {
            type: "tool_call",
            call: { id: "call-1", name: "dayorder.calendar.read", input: { start: input.scope.from, end: input.scope.to } },
          };
          yield { type: "completed", stopReason: "tool_use", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } };
          return;
        }
        yield { type: "completed", stopReason: "end_turn", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } };
      },
    };
    const cancel = vi.fn().mockImplementation(() => new Promise<ReadonlyRunView>((resolve) => {
      cancelStarted();
      releaseCancel = () => resolve(stopped);
    }));
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get: vi.fn().mockImplementation(() => Promise.resolve(structuredClone(analyzing))),
      cancel,
      finish: vi.fn().mockResolvedValue({ ...created, status: "completed" as const, version: 3 }),
      calendar: vi.fn().mockImplementation(() => new Promise((resolve) => {
        calendarStarted();
        releaseCalendar = () => resolve(structuredClone(calendarResult));
      })),
    });
    const controller = new AbortController();
    const pending = runForeground(input, api, transport, controller.signal);
    await calendarPending;

    controller.abort({ agentStop: "user" });
    releaseCalendar();
    await cancelling;
    releaseCancel();

    const outcome = await pending;
    expect(outcome.run).toEqual(stopped);
    expect(outcome.trace.state.phase).toBe("cancelled");
    expect(outcome.trace.modelTurns).toBe(1);
    expect(turns).toBe(1);
    expect(api.calendar).toHaveBeenCalledTimes(1);
    expect(api.finish).not.toHaveBeenCalled();
  });

  it("does not consume a Provider cancelled event that arrives before the cancel response", async () => {
    const created = await runView();
    const analyzing = { ...created, status: "analyzing" as const, version: 2 };
    const stopped = {
      ...created,
      status: "stopped" as const,
      version: 3,
      error: { code: "cancelled" as const, message: "cancelled", retryable: false },
    };
    let providerStarted!: () => void;
    let releaseProvider!: () => void;
    let cancelStarted!: () => void;
    let releaseCancel!: () => void;
    const started = new Promise<void>((resolve) => { providerStarted = resolve; });
    const cancelling = new Promise<void>((resolve) => { cancelStarted = resolve; });
    let turns = 0;
    const transport: ProviderGateway = {
      async *stream() {
        turns += 1;
        providerStarted();
        await new Promise<void>((resolve) => { releaseProvider = resolve; });
        yield {
          type: "error",
          error: { code: "cancelled", message: "provider stream cancelled", retryable: false },
        };
      },
    };
    const cancel = vi.fn().mockImplementation(() => new Promise<ReadonlyRunView>((resolve) => {
      cancelStarted();
      releaseCancel = () => resolve(stopped);
    }));
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get: vi.fn().mockResolvedValue(analyzing),
      cancel,
    });
    const controller = new AbortController();
    const pending = runForeground(input, api, transport, controller.signal);
    await started;

    controller.abort({ agentStop: "user" });
    await cancelling;
    releaseProvider();
    await Promise.resolve();
    releaseCancel();

    const outcome = await pending;
    expect(outcome.run).toEqual(stopped);
    expect(outcome.trace.state.phase).toBe("cancelled");
    expect(outcome.trace.modelTurns).toBe(1);
    expect(turns).toBe(1);
    expect(api.finish).not.toHaveBeenCalled();
  });

  it("releases the Provider transport and exits when cancel and reconciliation fail", async () => {
    const created = await runView();
    const analyzing = { ...created, status: "analyzing" as const, version: 2 };
    const cancelFailure = new Error("cancel unavailable");
    let providerStarted!: () => void;
    let transportSignal: AbortSignal | undefined;
    const started = new Promise<void>((resolve) => { providerStarted = resolve; });
    const transport: ProviderGateway = {
      async *stream(_request, requestSignal) {
        transportSignal = requestSignal;
        providerStarted();
        await new Promise<void>((resolve) => requestSignal.addEventListener("abort", () => resolve(), { once: true }));
      },
    };
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get: vi.fn().mockResolvedValueOnce(analyzing).mockRejectedValueOnce(new Error("reconciliation unavailable")),
      cancel: vi.fn().mockRejectedValue(cancelFailure),
    });
    const controller = new AbortController();
    const pending = runForeground(input, api, transport, controller.signal);
    await started;

    controller.abort({ agentStop: "user" });

    await expect(pending).rejects.toBe(cancelFailure);
    expect(transportSignal?.aborted).toBe(true);
    expect(api.finish).not.toHaveBeenCalled();
  });

  it("bounds explicit cancellation cleanup at five seconds before releasing transport", async () => {
    const created = await runView();
    vi.useFakeTimers();
    let providerStarted!: () => void;
    let transportSignal: AbortSignal | undefined;
    const started = new Promise<void>((resolve) => { providerStarted = resolve; });
    const rejectsOnAbort = <T>(requestSignal: AbortSignal): Promise<T> => new Promise((_resolve, reject) => {
      const rejectWithReason = () => reject(requestSignal.reason);
      if (requestSignal.aborted) {
        rejectWithReason();
      } else {
        requestSignal.addEventListener("abort", rejectWithReason, { once: true });
      }
    });
    const transport: ProviderGateway = {
      async *stream(_request, requestSignal) {
        transportSignal = requestSignal;
        providerStarted();
        await rejectsOnAbort(requestSignal);
      },
    };
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get: vi.fn().mockImplementation((_runId, requestSignal) => rejectsOnAbort(requestSignal)),
      cancel: vi.fn().mockImplementation((_runId, _version, requestSignal) => rejectsOnAbort(requestSignal)),
    });
    const controller = new AbortController();
    let settled = false;
    const outcome = runForeground(input, api, transport, controller.signal).then(
      () => ({ error: undefined }),
      (error: unknown) => ({ error }),
    ).finally(() => { settled = true; });
    await started;

    controller.abort({ agentStop: "user" });
    await vi.advanceTimersByTimeAsync(4_999);
    expect(settled).toBe(false);
    await vi.advanceTimersByTimeAsync(1);

    const result = await outcome;
    expect(result.error).toMatchObject({ name: "TimeoutError" });
    expect(transportSignal?.aborted).toBe(true);
    expect(api.finish).not.toHaveBeenCalled();
  });

  it("removes its caller abort listener after returning a completed Run", async () => {
    const created = await runView();
    const analyzing = { ...created, status: "analyzing" as const, version: 2 };
    const completed = { ...created, status: "completed" as const, version: 3 };
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get: vi.fn().mockResolvedValue(analyzing),
      finish: vi.fn().mockResolvedValue(completed),
    });
    const controller = new AbortController();

    await runForeground(input, api, provider([
      { type: "completed", stopReason: "end_turn", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } },
    ]), controller.signal);
    controller.abort({ agentStop: "user" });
    await Promise.resolve();

    expect(api.cancel).not.toHaveBeenCalled();
    expect(api.get).toHaveBeenCalledTimes(1);
  });

  it("aborts an in-flight cancel reconciliation before returning an already committed finish", async () => {
    const created = await runView();
    const analyzing = { ...created, status: "analyzing" as const, version: 2 };
    const completed = { ...created, status: "completed" as const, version: 3 };
    let finishStarted!: () => void;
    let releaseFinish!: () => void;
    let cleanupGetStarted!: () => void;
    let cleanupGetSignal: AbortSignal | undefined;
    const finishing = new Promise<void>((resolve) => { finishStarted = resolve; });
    const reconciling = new Promise<void>((resolve) => { cleanupGetStarted = resolve; });
    const get = vi.fn()
      .mockResolvedValueOnce(analyzing)
      .mockImplementationOnce((_runId: string, requestSignal: AbortSignal) => {
        cleanupGetSignal = requestSignal;
        cleanupGetStarted();
        return new Promise<ReadonlyRunView>(() => undefined);
      });
    const finish = vi.fn().mockImplementation(() => new Promise<ReadonlyRunView>((resolve) => {
      finishStarted();
      releaseFinish = () => resolve(completed);
    }));
    const api = client({ create: vi.fn().mockResolvedValue(created), get, finish });
    const controller = new AbortController();
    const pending = runForeground(input, api, provider([
      { type: "completed", stopReason: "end_turn", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } },
    ]), controller.signal);
    await finishing;

    controller.abort({ agentStop: "user" });
    await reconciling;
    releaseFinish();

    await expect(pending).resolves.toMatchObject({ run: completed });
    expect(cleanupGetSignal?.aborted).toBe(true);
    expect(api.cancel).not.toHaveBeenCalled();
  });

  it("removes its caller abort listener when post-create setup rejects", async () => {
    const created = await runView({ deadlineAt: "not-a-date" });
    const analyzing = { ...created, status: "analyzing" as const, version: 2 };
    const stopped = { ...created, status: "stopped" as const, version: 3 };
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get: vi.fn().mockResolvedValue(analyzing),
      cancel: vi.fn().mockResolvedValue(stopped),
    });
    const controller = new AbortController();

    await expect(runForeground(input, api, provider([]), controller.signal)).rejects.toThrow(/deadline/i);
    controller.abort({ agentStop: "user" });
    await Promise.resolve();
    await Promise.resolve();

    expect(api.get).not.toHaveBeenCalled();
    expect(api.cancel).not.toHaveBeenCalled();
  });

  it("releases a guarded Provider signal when Provider stream construction throws", async () => {
    const created = await runView();
    const analyzing = { ...created, status: "analyzing" as const, version: 2 };
    const stopped = { ...created, status: "stopped" as const, version: 3 };
    let transportSignal: AbortSignal | undefined;
    let persistenceStarted!: () => void;
    const started = new Promise<void>((resolve) => { persistenceStarted = resolve; });
    const transport: ProviderGateway = {
      stream(_request, requestSignal) {
        transportSignal = requestSignal;
        throw new Error("Provider setup failed");
      },
    };
    const get = vi.fn()
      .mockImplementationOnce((_runId: string, requestSignal: AbortSignal) => new Promise((_resolve, reject) => {
        persistenceStarted();
        requestSignal.addEventListener("abort", () => reject(requestSignal.reason), { once: true });
      }))
      .mockResolvedValueOnce(analyzing);
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get,
      cancel: vi.fn().mockResolvedValue(stopped),
    });
    const controller = new AbortController();
    const pending = runForeground(input, api, transport, controller.signal);
    await started;

    controller.abort({ agentStop: "user" });

    await expect(pending).resolves.toMatchObject({ run: stopped });
    expect(transportSignal?.aborted).toBe(false);
  });

  it("retries explicit cancellation with the latest active version after a conflict", async () => {
    const created = await runView();
    const analyzingV2 = { ...created, status: "analyzing" as const, version: 2 };
    const analyzingV3 = { ...created, status: "analyzing" as const, version: 3 };
    const stopped = { ...created, status: "stopped" as const, version: 4 };
    let providerStarted!: () => void;
    const started = new Promise<void>((resolve) => { providerStarted = resolve; });
    const transport: ProviderGateway = {
      async *stream(_request, requestSignal) {
        providerStarted();
        await new Promise<void>((resolve) => requestSignal.addEventListener("abort", () => resolve(), { once: true }));
      },
    };
    const get = vi.fn().mockResolvedValueOnce(analyzingV2).mockResolvedValueOnce(analyzingV3);
    const cancel = vi.fn()
      .mockRejectedValueOnce(new ApiError(409, { message: "version conflict" }))
      .mockResolvedValueOnce(stopped);
    const api = client({ create: vi.fn().mockResolvedValue(created), get, cancel });
    const controller = new AbortController();
    const pending = runForeground(input, api, transport, controller.signal);
    await started;

    controller.abort({ agentStop: "user" });

    await expect(pending).resolves.toMatchObject({ run: stopped, trace: { state: { phase: "cancelled" } } });
    expect(cancel).toHaveBeenNthCalledWith(1, runId, 2, expect.any(AbortSignal));
    expect(cancel).toHaveBeenNthCalledWith(2, runId, 3, expect.any(AbortSignal));
  });

  it.each([
    { status: "completed" as const, error: undefined },
    { status: "failed" as const, error: { code: "internal_error" as const, message: "already failed", retryable: false } },
    { status: "stopped" as const, error: { code: "cancelled" as const, message: "already stopped", retryable: false } },
  ])("preserves an authoritative $status Run after a cancel conflict", async ({ status, error }) => {
    const created = await runView();
    const analyzing = { ...created, status: "analyzing" as const, version: 2 };
    const terminal = { ...created, status, version: 3, ...(error ? { error } : {}) };
    let providerStarted!: () => void;
    const started = new Promise<void>((resolve) => { providerStarted = resolve; });
    const transport: ProviderGateway = {
      async *stream(_request, requestSignal) {
        providerStarted();
        await new Promise<void>((resolve) => requestSignal.addEventListener("abort", () => resolve(), { once: true }));
      },
    };
    const cancel = vi.fn().mockRejectedValue(new ApiError(409, { message: "version conflict" }));
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get: vi.fn().mockResolvedValueOnce(analyzing).mockResolvedValueOnce(terminal),
      cancel,
    });
    const controller = new AbortController();
    const pending = runForeground(input, api, transport, controller.signal);
    await started;

    controller.abort({ agentStop: "user" });

    const outcome = await pending;
    expect(outcome.run).toEqual(terminal);
    expect(cancel).toHaveBeenCalledTimes(1);
  });

  it("returns the authoritative terminal Run when a late finish conflicts", async () => {
    const created = await runView();
    const analyzing = { ...created, status: "analyzing" as const, version: 2 };
    const stopped = {
      ...created,
      status: "stopped" as const,
      version: 3,
      summary: "Server cancellation won",
      usage: { inputTokens: 9, outputTokens: 8, totalTokens: 17 },
      usageComplete: false,
    };
    const get = vi.fn().mockResolvedValueOnce(analyzing).mockResolvedValueOnce(stopped);
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get,
      finish: vi.fn().mockRejectedValue(new ApiError(409, { message: "sensitive conflict" })),
    });

    const result = await runForeground(input, api, provider([
      { type: "completed", stopReason: "end_turn", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } },
    ]), new AbortController().signal);

    expect(result.run).toEqual(stopped);
    expect(result.run.usage).toEqual({ inputTokens: 9, outputTokens: 8, totalTokens: 17 });
    expect(result.run.usageComplete).toBe(false);
    expect(get).toHaveBeenCalledTimes(2);
  });

  it("switches to one bounded cancel cleanup when user aborts during the final GET", async () => {
    const created = await runView();
    const current = { ...created, status: "analyzing" as const, version: 2 };
    const stopped = { ...created, status: "stopped" as const, version: 3 };
    const caller = new AbortController();
    let getStarted!: () => void;
    let sharedCleanupSignal: AbortSignal | undefined;
    const started = new Promise<void>((resolve) => { getStarted = resolve; });
    const get = vi.fn()
      .mockImplementationOnce((_runId: string, requestSignal: AbortSignal) => new Promise((_resolve, reject) => {
        expect(requestSignal).toBe(caller.signal);
        getStarted();
        requestSignal.addEventListener("abort", () => reject(requestSignal.reason), { once: true });
      }))
      .mockImplementationOnce((_runId: string, requestSignal: AbortSignal) => {
        sharedCleanupSignal = requestSignal;
        return Promise.resolve(current);
      });
    const cancel = vi.fn().mockImplementation(async (_runId, _version, cleanupSignal: AbortSignal) => {
      expect(cleanupSignal).not.toBe(caller.signal);
      expect(cleanupSignal.aborted).toBe(false);
      expect(cleanupSignal).toBe(sharedCleanupSignal);
      return stopped;
    });
    const api = client({ create: vi.fn().mockResolvedValue(created), get, cancel });
    const pending = runForeground(input, api, provider([
      { type: "completed", stopReason: "end_turn", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } },
    ]), caller.signal);
    await started;
    caller.abort(new DOMException("user stopped", "AbortError"));

    await expect(pending).resolves.toMatchObject({ run: stopped });
    expect(get).toHaveBeenCalledTimes(2);
    expect(cancel).toHaveBeenCalledWith(runId, 2, expect.any(AbortSignal));
    expect(api.finish).not.toHaveBeenCalled();
  });

  it("cancels with the reconciled version when user aborts during an uncommitted finish", async () => {
    const created = await runView();
    const beforeFinish = { ...created, status: "analyzing" as const, version: 2 };
    const reconciled = { ...created, status: "analyzing" as const, version: 3 };
    const stopped = { ...created, status: "stopped" as const, version: 4 };
    const caller = new AbortController();
    let finishStarted!: () => void;
    const started = new Promise<void>((resolve) => { finishStarted = resolve; });
    const get = vi.fn().mockResolvedValueOnce(beforeFinish).mockResolvedValueOnce(reconciled);
    const finish = vi.fn().mockImplementation((_runId, _version, _result, requestSignal: AbortSignal) => new Promise((_resolve, reject) => {
      expect(requestSignal).toBe(caller.signal);
      finishStarted();
      requestSignal.addEventListener("abort", () => reject(requestSignal.reason), { once: true });
    }));
    const cancel = vi.fn().mockResolvedValue(stopped);
    const api = client({ create: vi.fn().mockResolvedValue(created), get, finish, cancel });
    const pending = runForeground(input, api, provider([
      { type: "completed", stopReason: "end_turn", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } },
    ]), caller.signal);
    await started;
    caller.abort({ agentStop: "user" });

    await expect(pending).resolves.toMatchObject({ run: stopped });
    expect(cancel).toHaveBeenCalledWith(runId, 3, expect.any(AbortSignal));
    expect(get).toHaveBeenCalledTimes(2);
  });

  it("preserves a finish that committed before user abort cancelled its response", async () => {
    const created = await runView();
    const beforeFinish = { ...created, status: "analyzing" as const, version: 2 };
    const committed = {
      ...created,
      status: "completed" as const,
      version: 3,
      summary: "Authoritative result",
      usage: { inputTokens: 7, outputTokens: 5, totalTokens: 12 },
      usageComplete: false,
    };
    const caller = new AbortController();
    let finishStarted!: () => void;
    const started = new Promise<void>((resolve) => { finishStarted = resolve; });
    const get = vi.fn().mockResolvedValueOnce(beforeFinish).mockResolvedValueOnce(committed);
    const finish = vi.fn().mockImplementation((_runId, _version, _result, requestSignal: AbortSignal) => new Promise((_resolve, reject) => {
      finishStarted();
      requestSignal.addEventListener("abort", () => reject(requestSignal.reason), { once: true });
    }));
    const api = client({ create: vi.fn().mockResolvedValue(created), get, finish });
    const pending = runForeground(input, api, provider([
      { type: "completed", stopReason: "end_turn", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } },
    ]), caller.signal);
    await started;
    caller.abort(new DOMException("user stopped", "AbortError"));

    const result = await pending;
    expect(result.run).toEqual(committed);
    expect(result.run.usageComplete).toBe(false);
    expect(result.run.usage).toEqual({ inputTokens: 7, outputTokens: 5, totalTokens: 12 });
    expect(api.cancel).not.toHaveBeenCalled();
    expect(get).toHaveBeenCalledTimes(2);
  });

  it("lets the Driver turn a Tool timeout into a result and continue the next model turn", async () => {
    vi.useFakeTimers();
    const created = await runView();
    const latest = { ...created, status: "analyzing" as const, version: 2 };
    const finished = { ...created, status: "completed" as const, version: 3 };
    const turns: ModelTurnRequest[] = [];
    let turn = 0;
    let calendarStarted!: () => void;
    const started = new Promise<void>((resolve) => { calendarStarted = resolve; });
    const scripted: ProviderGateway = {
      async *stream(request) {
        turns.push(structuredClone(request));
        turn += 1;
        if (turn === 1) {
          yield { type: "tool_call", call: { id: "call-1", name: "dayorder.calendar.read", input: { start: input.scope.from, end: input.scope.to } } };
          yield { type: "completed", stopReason: "tool_use", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } };
        } else {
          yield { type: "completed", stopReason: "end_turn", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } };
        }
      },
    };
    const api = client({
      create: vi.fn().mockResolvedValue(created),
      get: vi.fn().mockResolvedValue(latest),
      finish: vi.fn().mockResolvedValue(finished),
      calendar: vi.fn().mockImplementation((_runId, _callId, _input, signal: AbortSignal) => new Promise((_resolve, reject) => {
        calendarStarted();
        signal.addEventListener("abort", () => reject(signal.reason), { once: true });
      })),
    });

    const pending = runForeground(input, api, scripted, new AbortController().signal);
    await started;
    await vi.advanceTimersByTimeAsync(10_001);
    await pending;

    expect(turns).toHaveLength(2);
    expect(JSON.stringify(turns[1].messages)).toContain('"code":"timeout"');
  });
});
