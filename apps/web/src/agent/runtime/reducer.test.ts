import { describe, expect, it } from "vitest";

import type {
  AgentError,
  Budget,
  CapabilitySnapshot,
  RuntimeInput,
  RuntimeState,
} from "../generated/protocol";
import canonicalJSONFixture from "../../../../../contracts/agent/fixtures/runtime/canonical-json.json";
import {
  advance,
  canonicalToolFingerprint,
  createRuntimeState,
  type RuntimeConfig,
} from "./reducer";

const budget: Budget = {
  maxSteps: 4,
  maxTokens: 100,
  maxDurationMs: 30_000,
  maxWorkers: 1,
  maxConcurrency: 1,
  maxRepeatedToolCalls: 2,
};

const capabilitySnapshot: CapabilitySnapshot = {
  runtimeVersion: "2.0.0",
  executionMode: "foreground",
  toolIds: ["test.clock.read"],
  skills: [],
  scope: { domains: ["test"] },
};

const config: RuntimeConfig = {
  runId: "run-1",
  executionMode: "foreground",
  capabilitySnapshot,
  budget,
};

const runtimeError: AgentError = {
  code: "provider_unavailable",
  message: "provider unavailable",
  retryable: true,
};

function startedState(): RuntimeState {
  return advance(createRuntimeState(config), { type: "user_message", text: "plan today" }).state;
}

function pendingToolState(): RuntimeState {
  const called = advance(startedState(), {
    type: "provider_event",
    providerEvent: {
      type: "tool_call",
      call: { id: "call-1", name: "test.clock.read", input: {} },
    },
  });
  return advance(called.state, {
    type: "provider_event",
    providerEvent: {
      type: "completed",
      stopReason: "tool_use",
      usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
    },
  }).state;
}

describe("runtime reducer", () => {
  it("creates a fresh zeroed runtime state", () => {
    const state = createRuntimeState(config);

    expect(state).toEqual({
      protocolVersion: "2.0",
      runId: "run-1",
      executionMode: "foreground",
      phase: "idle",
      sequence: 0,
      stepCount: 0,
      messages: [],
      capabilitySnapshot,
      budget,
      usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
      repeatedToolCalls: 0,
    });
    expect(state.capabilitySnapshot).not.toBe(capabilitySnapshot);
    expect(state.budget).not.toBe(budget);
  });

  it("accepts a user message when background execution modes match", () => {
    const transition = advance(createRuntimeState({
      ...config,
      executionMode: "background",
      capabilitySnapshot: { ...capabilitySnapshot, executionMode: "background" },
    }), { type: "user_message", text: "plan today" });

    expect(transition.state).toMatchObject({
      executionMode: "background",
      phase: "model_pending",
      sequence: 1,
    });
    expect(transition.effects).toEqual([{ type: "request_model_turn", turnId: "turn-1" }]);
  });

  it.each([
    { stateMode: "foreground" as const, snapshotMode: "background" as const },
    { stateMode: "background" as const, snapshotMode: "foreground" as const },
  ])("rejects mismatched $stateMode/$snapshotMode execution modes", ({ stateMode, snapshotMode }) => {
    const transition = advance(createRuntimeState({
      ...config,
      executionMode: stateMode,
      capabilitySnapshot: { ...capabilitySnapshot, executionMode: snapshotMode },
    }), { type: "user_message", text: "plan today" });
    const error = {
      code: "protocol_incompatible" as const,
      message: "execution mode mismatch",
      retryable: false,
    };

    expect(transition.state).toMatchObject({ phase: "failed", sequence: 1, error });
    expect(transition.effects).toEqual([{ type: "fail_run", error }]);
  });

  it("starts a model turn without mutating or aliasing state", () => {
    const state = createRuntimeState(config);
    const before = structuredClone(state);

    const transition = advance(state, { type: "user_message", text: "plan today" });

    expect(state).toEqual(before);
    expect(transition.state.phase).toBe("model_pending");
    expect(transition.effects).toEqual([{ type: "request_model_turn", turnId: "turn-1" }]);
    expect(transition.state).not.toBe(state);
    expect(transition.state.messages).not.toBe(state.messages);

    transition.state.messages[0].content[0].text = "changed";
    expect(state.messages).toEqual([]);
  });

  it("canonicalizes tool input by recursively sorting object keys while preserving array order", () => {
    const first = {
      id: "call-1",
      name: "test.clock.read",
      input: { zone: "UTC", nested: { z: 2, a: [{ y: 1, x: 2 }, 3] }, format: "iso" },
    };
    const reordered = {
      id: "call-2",
      name: "test.clock.read",
      input: { format: "iso", nested: { a: [{ x: 2, y: 1 }, 3], z: 2 }, zone: "UTC" },
    };

    expect(canonicalToolFingerprint(first)).toBe(
      'test.clock.read:{"format":"iso","nested":{"a":[{"x":2,"y":1},3],"z":2},"zone":"UTC"}',
    );
    expect(canonicalToolFingerprint(reordered)).toBe(canonicalToolFingerprint(first));
  });

  it("matches the shared canonical JSON fingerprint fixture", () => {
    const fingerprint = canonicalToolFingerprint({
      id: "call-canonical",
      name: "test.clock.read",
      input: canonicalJSONFixture.input,
    });

    expect(fingerprint).toBe(`test.clock.read:${canonicalJSONFixture.canonical}`);
  });

  it.each([
    [{ type: "future_event" }, "invalid RuntimeInput"],
    [{ type: "provider_event" }, "invalid provider_event payload"],
    [{ type: "cancel", text: "contradictory" }, "invalid cancel payload"],
    [{
      type: "approval_response",
      approvalResponse: { approvalId: "approval-1", decision: "later" },
    }, "invalid later approval response payload"],
  ])("turns incompatible runtime traffic %o into a stable failure", (input, message) => {
    const transition = advance(startedState(), input as RuntimeInput);

    expect(transition.state).toMatchObject({
      phase: "failed",
      sequence: 2,
      error: { code: "protocol_incompatible", message, retryable: false },
    });
    expect(transition.effects).toEqual([{ type: "fail_run", error: transition.state.error }]);
  });

  it("keeps generic malformed RuntimeInput validation failures stable", () => {
    let thrown: unknown;
    try {
      advance(startedState(), {
        type: "provider_event",
        providerEvent: {
          type: "completed",
          stopReason: "end_turn",
          usage: { inputTokens: -1, outputTokens: 0, totalTokens: 0 },
        },
      });
    } catch (error) {
      thrown = error;
    }

    expect(thrown).toMatchObject({ code: "validation_failed" });
  });

  it("leaves unrelated extra properties to generic schema validation", () => {
    let thrown: unknown;
    try {
      advance(startedState(), {
        type: "runtime_error",
        error: runtimeError,
        metadata: "not a tagged payload",
      } as unknown as RuntimeInput);
    } catch (error) {
      thrown = error;
    }

    expect(thrown).toMatchObject({ code: "validation_failed" });
  });

  it("classifies an unsupported RuntimeState phase as protocol incompatible", () => {
    const transition = advance(
      { ...startedState(), phase: "future_phase" } as unknown as RuntimeState,
      { type: "cancel", reason: "stop" },
    );

    expect(transition.state).toMatchObject({
      phase: "failed",
      sequence: 2,
      error: {
        code: "protocol_incompatible",
        message: "unsupported RuntimeState phase future_phase",
        retryable: false,
      },
    });
  });

  it("keeps generic malformed RuntimeState validation failures stable", () => {
    let thrown: unknown;
    try {
      advance({ ...startedState(), sequence: -1 }, { type: "cancel", reason: "stop" });
    } catch (error) {
      thrown = error;
    }

    expect(thrown).toMatchObject({ code: "validation_failed" });
  });

  it("rejects illegal phase/input pairs and mismatched call IDs", () => {
    const illegal = advance(createRuntimeState(config), {
      type: "provider_event",
      providerEvent: { type: "text_delta", text: "too early" },
    });
    expect(illegal.state.error).toEqual({
      code: "protocol_incompatible",
      message: "provider_event is not allowed in idle",
      retryable: false,
    });

    const mismatch = advance(pendingToolState(), {
      type: "tool_resolution",
      toolResolution: { callId: "call-other", decision: "execute" },
    });
    expect(mismatch.state.error).toEqual({
      code: "protocol_incompatible",
      message: "tool_resolution callId does not match pending tool call",
      retryable: false,
    });
  });

  it("counts approval execution as a step and enforces the step budget", () => {
    const awaitingApproval = advance(pendingToolState(), {
      type: "tool_resolution",
      toolResolution: { callId: "call-1", decision: "approval", approvalId: "approval-1" },
    }).state;
    const allowed = advance(awaitingApproval, {
      type: "approval_response",
      approvalResponse: { approvalId: "approval-1", decision: "allow" },
    });

    expect(allowed.state).toMatchObject({ phase: "tool_pending", stepCount: 1 });
    expect(allowed.effects).toEqual([
      { type: "execute_tool", toolCall: { id: "call-1", name: "test.clock.read", input: {} } },
    ]);

    const exhausted = {
      ...awaitingApproval,
      stepCount: awaitingApproval.budget.maxSteps,
    };
    const deniedByBudget = advance(exhausted, {
      type: "approval_response",
      approvalResponse: { approvalId: "approval-1", decision: "allow" },
    });
    expect(deniedByBudget.state.error).toEqual({
      code: "validation_failed",
      message: "maxSteps exceeded",
      retryable: false,
    });
  });

  it("aggregates completed-turn usage and fails when the total-token budget is exceeded", () => {
    const state = {
      ...startedState(),
      usage: { inputTokens: 40, outputTokens: 10, totalTokens: 50 },
    };
    const transition = advance(state, {
      type: "provider_event",
      providerEvent: {
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 30, outputTokens: 21, totalTokens: 51 },
      },
    });

    expect(transition.state.usage).toEqual({ inputTokens: 70, outputTokens: 31, totalTokens: 101 });
    expect(transition.state.error).toEqual({
      code: "validation_failed",
      message: "maxTokens exceeded",
      retryable: false,
    });
    expect(transition.effects).toEqual([{ type: "fail_run", error: transition.state.error }]);
  });

  it("stages a tool call until completed tool_use reports usage", () => {
    const initial = createRuntimeState({
      runId: "r-v2",
      executionMode: "foreground",
      capabilitySnapshot: {
        runtimeVersion: "2.0.0",
        executionMode: "foreground",
        toolIds: ["dayorder.calendar.read"],
        skills: [],
        scope: { domains: ["calendar"] },
      },
      budget: {
        maxSteps: 8,
        maxTokens: 16_000,
        maxDurationMs: 120_000,
        maxWorkers: 1,
        maxConcurrency: 1,
        maxRepeatedToolCalls: 2,
      },
    });
    const started = advance(initial, { type: "user_message", text: "查询日程" });
    const called = advance(started.state, {
      type: "provider_event",
      providerEvent: {
        type: "tool_call",
        call: { id: "c1", name: "dayorder.calendar.read", input: {} },
      },
    });

    expect(called.effects).toEqual([]);
    expect(called.state.phase).toBe("model_streaming");

    const ended = advance(called.state, {
      type: "provider_event",
      providerEvent: {
        type: "completed",
        stopReason: "tool_use",
        usage: { inputTokens: 10, outputTokens: 5, totalTokens: 15 },
      },
    });
    expect(ended.state.usage.totalTokens).toBe(15);
    expect(ended.effects.map((effect) => effect.type)).toEqual(["resolve_tool"]);
  });

  it.each([
    {
      stopReason: "end_turn" as const,
      phase: "completed" as const,
      error: undefined,
      effect: { type: "complete_run" as const },
    },
    {
      stopReason: "max_tokens" as const,
      phase: "failed" as const,
      error: {
        code: "validation_failed" as const,
        message: "provider stopped at max_tokens",
        retryable: false,
      },
      effect: {
        type: "fail_run" as const,
        error: {
          code: "validation_failed" as const,
          message: "provider stopped at max_tokens",
          retryable: false,
        },
      },
    },
    {
      stopReason: "cancelled" as const,
      phase: "cancelled" as const,
      error: { code: "cancelled" as const, message: "provider cancelled", retryable: false },
      effect: { type: "cancel_run" as const },
    },
    {
      stopReason: "tool_use" as const,
      phase: "failed" as const,
      error: {
        code: "protocol_incompatible" as const,
        message: "provider completed with orphan tool_use",
        retryable: false,
      },
      effect: {
        type: "fail_run" as const,
        error: {
          code: "protocol_incompatible" as const,
          message: "provider completed with orphan tool_use",
          retryable: false,
        },
      },
    },
  ])("handles completed stop reason $stopReason explicitly", ({ stopReason, phase, error, effect }) => {
    const transition = advance(startedState(), {
      type: "provider_event",
      providerEvent: {
        type: "completed",
        stopReason,
        usage: { inputTokens: 3, outputTokens: 2, totalTokens: 5 },
      },
    });

    expect(transition.state).toMatchObject({
      phase,
      usage: { inputTokens: 3, outputTokens: 2, totalTokens: 5 },
    });
    expect(transition.state.error).toEqual(error);
    expect(transition.effects).toEqual([effect]);
  });

  it("fails from either runtime or provider errors without retaining caller aliases", () => {
    const error = structuredClone(runtimeError);
    const input: RuntimeInput = { type: "runtime_error", error };
    const transition = advance(startedState(), input);

    expect(transition.state.error).toEqual(runtimeError);
    expect(transition.state.error).not.toBe(error);
    expect(transition.effects[0].error).not.toBe(transition.state.error);

    error.message = "mutated outside";
    expect(transition.state.error?.message).toBe("provider unavailable");

    const providerTransition = advance(startedState(), {
      type: "provider_event",
      providerEvent: {
        type: "error",
        error: { code: "provider_rate_limited", message: "slow down", retryable: true },
      },
    });
    expect(providerTransition.state.error?.code).toBe("provider_rate_limited");
  });

  it("preserves an accumulated assistant draft when a later runtime error ends the run", () => {
    const streaming = advance(startedState(), {
      type: "provider_event",
      providerEvent: { type: "text_delta", text: "partial answer" },
    }).state;

    const transition = advance(streaming, {
      type: "runtime_error",
      error: { code: "timeout", message: "timed out", retryable: true },
    });

    expect(transition.state.assistantDraft).toBe("partial answer");
  });

  it("rejects a duplicate tool resolution after execution has started", () => {
    const executing = advance(pendingToolState(), {
      type: "tool_resolution",
      toolResolution: { callId: "call-1", decision: "execute" },
    }).state;

    expect(executing.pendingToolCall).toBeUndefined();
    const duplicate = advance(executing, {
      type: "tool_resolution",
      toolResolution: { callId: "call-1", decision: "execute" },
    });
    const error = {
      code: "protocol_incompatible" as const,
      message: "tool_resolution callId does not match pending tool call",
      retryable: false,
    };
    expect(duplicate.state).toMatchObject({ phase: "failed", stepCount: 1, error });
    expect(duplicate.effects).toEqual([{ type: "fail_run", error }]);
  });

  it("rejects a tool result before resolution and execution", () => {
    const transition = advance(pendingToolState(), {
      type: "tool_result",
      toolResult: { callId: "call-1", result: { ok: true, data: { time: "10:00" } } },
    });
    const error = {
      code: "protocol_incompatible" as const,
      message: "tool_result is not allowed before tool execution",
      retryable: false,
    };

    expect(transition.state).toMatchObject({ phase: "failed", stepCount: 0, error });
    expect(transition.effects).toEqual([{ type: "fail_run", error }]);
  });

  it("rejects an approval response whose ID does not match the pending approval", () => {
    const awaitingApproval = advance(pendingToolState(), {
      type: "tool_resolution",
      toolResolution: { callId: "call-1", decision: "approval", approvalId: "approval-1" },
    }).state;
    const transition = advance(awaitingApproval, {
      type: "approval_response",
      approvalResponse: { approvalId: "approval-other", decision: "allow" },
    });

    expect(transition.state.error).toEqual({
      code: "protocol_incompatible",
      message: "approval_response approvalId does not match pending approval",
      retryable: false,
    });
  });

  it("rejects a result ID that does not match the in-flight tool call", () => {
    const executing = advance(pendingToolState(), {
      type: "tool_resolution",
      toolResolution: { callId: "call-1", decision: "execute" },
    }).state;
    const transition = advance(executing, {
      type: "tool_result",
      toolResult: { callId: "call-other", result: { ok: true, data: {} } },
    });

    expect(transition.state.error).toEqual({
      code: "protocol_incompatible",
      message: "tool_result callId does not match in-flight tool call",
      retryable: false,
    });
  });

  it("flushes streamed text into the tool-call message and starts the next model draft cleanly", () => {
    const streaming = advance(startedState(), {
      type: "provider_event",
      providerEvent: { type: "text_delta", text: "I will check." },
    }).state;
    const stagedCall = advance(streaming, {
      type: "provider_event",
      providerEvent: {
        type: "tool_call",
        call: { id: "call-1", name: "test.clock.read", input: {} },
      },
    }).state;

    expect(stagedCall.assistantDraft).toBe("I will check.");
    expect(stagedCall.messages).toEqual([
      { role: "user", content: [{ type: "text", text: "plan today" }] },
    ]);

    const awaitingResolution = advance(stagedCall, {
      type: "provider_event",
      providerEvent: {
        type: "completed",
        stopReason: "tool_use",
        usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
      },
    }).state;

    expect(awaitingResolution.assistantDraft).toBeUndefined();
    expect(awaitingResolution.messages).toEqual([
      { role: "user", content: [{ type: "text", text: "plan today" }] },
      {
        role: "assistant",
        content: [
          { type: "text", text: "I will check." },
          {
            type: "tool_call",
            toolCall: { id: "call-1", name: "test.clock.read", input: {} },
          },
        ],
      },
    ]);

    const executing = advance(awaitingResolution, {
      type: "tool_resolution",
      toolResolution: { callId: "call-1", decision: "execute" },
    }).state;
    const afterResult = advance(executing, {
      type: "tool_result",
      toolResult: { callId: "call-1", result: { ok: true, data: { time: "10:00" } } },
    }).state;
    const nextDraft = advance(afterResult, {
      type: "provider_event",
      providerEvent: { type: "text_delta", text: "It is 10:00." },
    }).state;
    const completed = advance(nextDraft, {
      type: "provider_event",
      providerEvent: {
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
      },
    }).state;

    expect(completed.messages).toEqual([
      { role: "user", content: [{ type: "text", text: "plan today" }] },
      {
        role: "assistant",
        content: [
          { type: "text", text: "I will check." },
          {
            type: "tool_call",
            toolCall: { id: "call-1", name: "test.clock.read", input: {} },
          },
        ],
      },
      {
        role: "tool",
        content: [{ type: "tool_result", toolResult: { ok: true, data: { time: "10:00" } } }],
      },
      { role: "assistant", content: [{ type: "text", text: "It is 10:00." }] },
    ]);
  });

  it("keeps pending call mutations isolated from historical assistant messages", () => {
    const staged = advance(startedState(), {
      type: "provider_event",
      providerEvent: {
        type: "tool_call",
        call: {
          id: "call-1",
          name: "test.clock.read",
          input: { options: { format: "iso" } },
        },
      },
    });
    const transition = advance(staged.state, {
      type: "provider_event",
      providerEvent: {
        type: "completed",
        stopReason: "tool_use",
        usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
      },
    });

    const pendingOptions = transition.state.pendingToolCall!.input.options as { format: string };
    pendingOptions.format = "mutated";

    expect(transition.state.messages[1].content[0].toolCall?.input).toEqual({
      options: { format: "iso" },
    });
  });
});
