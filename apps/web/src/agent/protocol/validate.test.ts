import { describe, expect, it } from "vitest";

import { ProtocolValidationError, validateProtocol } from "./validate";

describe("validateProtocol", () => {
  it("accepts a valid input and rejects unknown fields", () => {
    expect(validateProtocol("RuntimeInput", { type: "user_message", text: "plan today" })).toMatchObject({
      type: "user_message",
    });
    expect(() => validateProtocol("RuntimeInput", { type: "user_message", extra: true })).toThrow(
      ProtocolValidationError,
    );
    expect(() => validateProtocol("RuntimeInput", { type: "future_event" })).toThrow(/validation_failed/);
  });

  it("rejects a conformance case whose input and transition arrays differ in length", () => {
    const initialState = {
      protocolVersion: "2.0",
      runId: "run-1",
      executionMode: "foreground",
      phase: "idle",
      sequence: 0,
      stepCount: 0,
      messages: [],
      capabilitySnapshot: {
        runtimeVersion: "2.0.0",
        executionMode: "foreground",
        toolIds: [],
        skills: [],
        scope: { domains: [] },
      },
      budget: {
        maxSteps: 1,
        maxTokens: 1,
        maxDurationMs: 1,
        maxWorkers: 1,
        maxConcurrency: 1,
        maxRepeatedToolCalls: 1,
      },
      usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
      repeatedToolCalls: 0,
    };

    expect(() =>
      validateProtocol("ConformanceCase", {
        name: "mismatched arrays",
        protocolVersion: "2.0",
        initialState,
        inputs: [{ type: "user_message", text: "plan today" }],
        expectedTransitions: [],
      }),
    ).toThrow(ProtocolValidationError);
  });

  it.each([
    { type: "completed" },
    { type: "completed", stopReason: "end_turn" },
    { type: "completed", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } },
  ])("rejects an incomplete completed ProviderEvent %#", (event) => {
    expect(() => validateProtocol("ProviderEvent", event)).toThrow(ProtocolValidationError);
  });

  it("accepts a strict v2 ProviderEnvelope and rejects old or extended envelopes", () => {
    const envelope = {
      protocolVersion: "2.0",
      runId: "run-1",
      turnId: "turn-1",
      sequence: 1,
      event: { type: "text_delta", text: "hello" },
    };

    expect(validateProtocol("ProviderEnvelope", envelope)).toEqual(envelope);
    expect(() => validateProtocol("ProviderEnvelope", { ...envelope, protocolVersion: "1.0" })).toThrow(
      ProtocolValidationError,
    );
    expect(() => validateProtocol("ProviderEnvelope", { ...envelope, sequence: 0 })).toThrow(
      ProtocolValidationError,
    );
    expect(() => validateProtocol("ProviderEnvelope", { ...envelope, extra: true })).toThrow(
      ProtocolValidationError,
    );
  });

  it("accepts the cross-host timer maximum and rejects the next Run duration", () => {
    const budget = {
      maxSteps: 1,
      maxTokens: 1,
      maxDurationMs: 2_147_483_647,
      maxWorkers: 1,
      maxConcurrency: 1,
      maxRepeatedToolCalls: 1,
    };

    expect(() => validateProtocol("Budget", budget)).not.toThrow();
    expect(() => validateProtocol("Budget", {
      ...budget,
      maxDurationMs: 2_147_483_648,
    })).toThrow(ProtocolValidationError);
  });

  it("accepts the cross-host timer maximum and rejects the next Tool timeout", () => {
    const tool = {
      id: "test.clock.read",
      description: "Read a deterministic test clock",
      inputSchema: { type: "object" },
      outputSchema: { type: "object" },
      sideEffect: "read",
      requiredDomains: ["test"],
      executionTargets: ["client"],
      approvalPolicy: "never",
      idempotent: true,
      timeoutMs: 2_147_483_647,
      resultMaxBytes: 1,
    };

    expect(() => validateProtocol("ToolSpec", tool)).not.toThrow();
    expect(() => validateProtocol("ToolSpec", {
      ...tool,
      timeoutMs: 2_147_483_648,
    })).toThrow(ProtocolValidationError);
  });

  it("enforces strict calendar input boundaries including UTF-8 cursor bytes", () => {
    const valid = {
      start: "2026-09-05T00:00:00+08:00",
      end: "2026-09-06T00:00:00+08:00",
      cursor: "日".repeat(1_365),
      limit: 50,
    };

    expect(validateProtocol("CalendarReadInput", valid)).toEqual(valid);
    for (const invalid of [
      { ...valid, extra: true },
      { ...valid, start: "not-a-time" },
      { ...valid, limit: 0 },
      { ...valid, limit: 51 },
      { ...valid, limit: 1.5 },
      { ...valid, cursor: "x".repeat(4_097) },
      { ...valid, cursor: "日".repeat(1_366) },
      { end: valid.end },
    ]) {
      expect(() => validateProtocol("CalendarReadInput", invalid)).toThrow(ProtocolValidationError);
    }
  });

  it("validates strict calendar result fields rather than accepting arbitrary Tool data", () => {
    const valid = {
      events: [{
        id: "550e8400-e29b-41d4-a716-446655440000",
        title: "Planning",
        startAt: "2026-09-05T09:00:00Z",
        endAt: "2026-09-05T09:30:00Z",
        timezone: "Asia/Shanghai",
        kind: "meeting",
        version: 1,
      }],
      window: { start: "2026-09-05T00:00:00Z", end: "2026-09-06T00:00:00Z" },
      hasMore: false,
      nextCursor: null,
    };

    expect(validateProtocol("CalendarReadData", valid)).toEqual(valid);
    for (const invalid of [
      { ...valid, unknown: true },
      { ...valid, events: [{ ...valid.events[0], id: "not-a-uuid" }] },
      { ...valid, events: [{ ...valid.events[0], startAt: "tomorrow" }] },
      { ...valid, events: [{ ...valid.events[0], version: 0 }] },
      { ...valid, events: [{ ...valid.events[0], extra: true }] },
    ]) {
      expect(() => validateProtocol("CalendarReadData", invalid)).toThrow(ProtocolValidationError);
    }
  });

  it("enforces readonly run start scope and request envelopes", () => {
    const start = {
      intent: "Summarize my calendar",
      executionMode: "foreground",
      scope: {
        domains: ["calendar"],
        from: "2026-09-05T00:00:00+08:00",
        to: "2026-09-06T00:00:00+08:00",
      },
      timezone: "Asia/Shanghai",
      modelProfile: "client/default",
    };
    const request = {
      callId: "call-1",
      input: { start: "2026-09-05T00:00:00Z", end: "2026-09-06T00:00:00Z" },
    };

    expect(validateProtocol("ReadonlyRunStart", start)).toEqual(start);
    expect(validateProtocol("CalendarReadRequest", request)).toEqual(request);
    for (const invalid of [
      { ...start, intent: "x".repeat(2_001) },
      { ...start, scope: { ...start.scope, domains: ["task"] } },
      { ...start, scope: { domains: ["calendar"], from: start.scope.from } },
      { ...start, scope: { ...start.scope, entityIds: ["event-1"] } },
      { ...start, extra: true },
    ]) {
      expect(() => validateProtocol("ReadonlyRunStart", invalid)).toThrow(ProtocolValidationError);
    }
    expect(() => validateProtocol("CalendarReadRequest", { ...request, extra: true })).toThrow(
      ProtocolValidationError,
    );
  });

  it("enforces readonly run view and finish literal limits", () => {
    const view = {
      protocolVersion: "2.0",
      runId: "550e8400-e29b-41d4-a716-446655440000",
      executionMode: "background",
      status: "completed",
      version: 1,
      capabilitySnapshot: {
        runtimeVersion: "2.0.0",
        executionMode: "background",
        toolIds: ["dayorder.calendar.read"],
        skills: [],
        scope: { domains: ["calendar"] },
      },
      budget: {
        maxSteps: 1,
        maxTokens: 1,
        maxDurationMs: 1,
        maxWorkers: 1,
        maxConcurrency: 1,
        maxRepeatedToolCalls: 1,
      },
      modelProfile: "server/default",
      serverNow: "2026-09-05T00:00:00Z",
      deadlineAt: "2026-09-05T00:01:00Z",
      usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
      usageComplete: true,
      resultOrigin: "server_runtime",
      summary: "Done",
    };
    const finish = {
      phase: "completed",
      summary: "Done",
      steps: [{ title: "Read calendar", detail: "One page" }],
    };

    expect(validateProtocol("ReadonlyRunView", view)).toEqual(view);
    expect(validateProtocol("ReadonlyRunFinish", finish)).toEqual(finish);
    for (const invalid of [
      { ...view, runId: "not-a-uuid" },
      { ...view, serverNow: "today" },
      { ...view, version: 0 },
      { ...view, extra: true },
    ]) {
      expect(() => validateProtocol("ReadonlyRunView", invalid)).toThrow(ProtocolValidationError);
    }
    for (const invalid of [
      { ...finish, summary: "x".repeat(8_001) },
      { ...finish, steps: Array.from({ length: 17 }, () => finish.steps[0]) },
      { ...finish, steps: [{ title: "x".repeat(241), detail: "ok" }] },
      { ...finish, steps: [{ title: "ok", detail: "x".repeat(2_001) }] },
      { ...finish, extra: true },
    ]) {
      expect(() => validateProtocol("ReadonlyRunFinish", invalid)).toThrow(ProtocolValidationError);
    }
  });
});
