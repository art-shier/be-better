import { describe, expect, it, vi } from "vitest";

import type {
  Budget,
  CapabilitySnapshot,
  ModelTurnRequest,
  ProviderEvent,
  RuntimeInput,
  RuntimeState,
  ToolResult,
} from "../generated/protocol";
import { calendarReadSpec } from "../assets/builtin";
import toolResultSizeFixture from "../../../../../contracts/agent/fixtures/runtime/tool-result-size.json";
import { ScriptedProvider } from "../provider/mock";
import type { ProviderGateway } from "../provider/provider";
import { ToolRegistry, type ToolBinding } from "../tool/registry";
import { driveToCompletion, type ApprovalBroker } from "./driver";
import { advance, createRuntimeState, type RuntimeConfig } from "./reducer";

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

const allowAllApprovals: ApprovalBroker = {
  request: async () => "allow",
};

function completedToolUseEvent(): ProviderEvent {
  return {
    type: "completed",
    stopReason: "tool_use",
    usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  };
}

interface RejectionProcess {
  on(event: "unhandledRejection", listener: (reason: unknown) => void): void;
  off(event: "unhandledRejection", listener: (reason: unknown) => void): void;
}

async function captureUnhandledRejections<T>(run: () => Promise<T>): Promise<{
  value: T;
  reasons: unknown[];
}> {
  const reasons: unknown[] = [];
  const listener = (reason: unknown): void => {
    reasons.push(reason);
  };
  const rejectionProcess = (globalThis as unknown as { process: RejectionProcess }).process;
  rejectionProcess.on("unhandledRejection", listener);
  try {
    const value = await run();
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    return { value, reasons };
  } finally {
    rejectionProcess.off("unhandledRejection", listener);
  }
}

function clockBinding(data: { now: string }): ToolBinding {
  return {
    spec: {
      id: "test.clock.read",
      description: "Read a deterministic test clock",
      inputSchema: { type: "object", additionalProperties: false },
      outputSchema: {
        type: "object",
        properties: { now: { type: "string" } },
        required: ["now"],
        additionalProperties: false,
      },
      sideEffect: "read",
      requiredDomains: ["test"],
      executionTargets: ["client"],
      approvalPolicy: "never",
      idempotent: true,
      timeoutMs: 1_000,
      resultMaxBytes: 1_024,
    },
    invoke: async (): Promise<ToolResult> => ({ ok: true, data }),
  };
}

function calendarState(): RuntimeState {
  return createRuntimeState({
    runId: "run-calendar",
    executionMode: "foreground",
    capabilitySnapshot: {
      runtimeVersion: "2.0.0",
      executionMode: "foreground",
      toolIds: ["dayorder.calendar.read"],
      skills: [],
      scope: {
        domains: ["calendar"],
        from: "2026-09-05T00:00:00Z",
        to: "2026-09-06T00:00:00Z",
      },
    },
    budget,
  });
}

function calendarProvider(input: Record<string, unknown>): ScriptedProvider {
  return new ScriptedProvider([
    [
      {
        type: "tool_call",
        call: { id: "call-calendar", name: "dayorder.calendar.read", input },
      },
      completedToolUseEvent(),
    ],
    [{
      type: "completed",
      stopReason: "end_turn",
      usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
    }],
  ]);
}

function calendarData(): Record<string, unknown> {
  return {
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
}

function forbiddenInitialInputCases(): Array<{
  name: string;
  state: RuntimeState;
  input: RuntimeInput;
}> {
  const idle = createRuntimeState(config);
  const modelPending = advance(idle, { type: "user_message", text: "start" }).state;
  const toolCalled = advance(modelPending, {
    type: "provider_event",
    providerEvent: {
      type: "tool_call",
      call: { id: "call-1", name: "test.clock.read", input: {} },
    },
  }).state;
  const toolPending = advance(toolCalled, {
    type: "provider_event",
    providerEvent: completedToolUseEvent(),
  }).state;
  const executing = advance(toolPending, {
    type: "tool_resolution",
    toolResolution: { callId: "call-1", decision: "execute" },
  }).state;
  const approvalPending = advance(toolPending, {
    type: "tool_resolution",
    toolResolution: { callId: "call-1", decision: "approval", approvalId: "approval-call-1" },
  }).state;

  return [
    {
      name: "provider_event",
      state: modelPending,
      input: { type: "provider_event", providerEvent: { type: "text_delta", text: "caller event" } },
    },
    {
      name: "tool_resolution execute for a policy-denied Tool",
      state: toolPending,
      input: { type: "tool_resolution", toolResolution: { callId: "call-1", decision: "execute" } },
    },
    {
      name: "tool_resolution with an arbitrary approval ID",
      state: toolPending,
      input: {
        type: "tool_resolution",
        toolResolution: { callId: "call-1", decision: "approval", approvalId: "caller-controlled" },
      },
    },
    {
      name: "tool_result",
      state: executing,
      input: {
        type: "tool_result",
        toolResult: { callId: "call-1", result: { ok: true, data: { now: "caller result" } } },
      },
    },
    {
      name: "approval_response allow",
      state: approvalPending,
      input: {
        type: "approval_response",
        approvalResponse: { approvalId: "approval-call-1", decision: "allow" },
      },
    },
  ];
}

function throwingInitialInput(onRead: () => void): RuntimeInput {
  const input = {} as RuntimeInput;
  Object.defineProperty(input, "type", {
    enumerable: true,
    get: () => {
      onRead();
      throw new Error("private caller getter");
    },
  });
  return input;
}

describe("foreground ReAct effect driver", () => {
  it("executes the builtin calendar Tool only for format- and byte-valid input", async () => {
    const validInput = {
      start: "2026-09-05T00:00:00Z",
      end: "2026-09-06T00:00:00Z",
      cursor: "日".repeat(1_365),
    };
    for (const [input, expectedInvocations] of [
      [validInput, 1],
      [{ ...validInput, start: "not-a-time" }, 0],
      [{ ...validInput, cursor: "日".repeat(1_366) }, 0],
    ] as const) {
      let invocations = 0;
      const tools = new ToolRegistry("foreground");
      tools.register({
        spec: calendarReadSpec(),
        invoke: async () => {
          invocations += 1;
          return { ok: true, data: calendarData() };
        },
      });

      const trace = await driveToCompletion(
        calendarState(),
        { type: "user_message", text: "calendar" },
        {
          provider: calendarProvider(input),
          tools,
          approvals: allowAllApprovals,
          modelProfile: "client/default",
          policy: { allow: ["dayorder.calendar.read"], deny: [], approvalFor: [] },
        },
        new AbortController().signal,
      );

      expect(invocations).toBe(expectedInvocations);
      expect(trace.inputs[4].toolResult?.result.ok).toBe(expectedInvocations === 1);
    }
  });

  it("normalizes invalid builtin calendar Tool output before returning it to the model", async () => {
    const tools = new ToolRegistry("foreground");
    tools.register({
      spec: calendarReadSpec(),
      invoke: async () => ({
        ok: true,
        data: {
          ...calendarData(),
          events: [{ ...(calendarData().events as Array<Record<string, unknown>>)[0], id: "not-a-uuid" }],
        },
      }),
    });

    const trace = await driveToCompletion(
      calendarState(),
      { type: "user_message", text: "calendar" },
      {
        provider: calendarProvider({
          start: "2026-09-05T00:00:00Z",
          end: "2026-09-06T00:00:00Z",
        }),
        tools,
        approvals: allowAllApprovals,
        modelProfile: "client/default",
        policy: { allow: ["dayorder.calendar.read"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.inputs[4].toolResult?.result).toEqual({
      ok: false,
      error: {
        code: "validation_failed",
        message: "tool output schema validation failed",
        retryable: false,
      },
    });
  });

  it("accepts the maximum cross-host Run timer without immediate expiry", async () => {
    const provider = new ScriptedProvider([[
      {
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
      },
    ]]);
    const state = createRuntimeState({
      ...config,
      budget: { ...budget, maxDurationMs: 2_147_483_647 },
    });

    const trace = await driveToCompletion(
      state,
      { type: "user_message", text: "bounded Run" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.phase).toBe("completed");
    expect(trace.state.error).toBeUndefined();
  });

  it("accepts the maximum cross-host Tool timer without immediate expiry", async () => {
    const provider = new ScriptedProvider([
      [{ type: "tool_call", call: { id: "call-max-timer", name: "test.clock.read", input: {} } }, completedToolUseEvent()],
      [{
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
      }],
    ]);
    const tools = new ToolRegistry("foreground");
    const binding = clockBinding({ now: "09:00" });
    binding.spec.timeoutMs = 2_147_483_647;
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "bounded Tool" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.phase).toBe("completed");
    expect(trace.inputs[4].toolResult?.result).toEqual({ ok: true, data: { now: "09:00" } });
  });

  it("returns the exact pre-cancelled trace without reading caller input", async () => {
    const state = createRuntimeState(config);
    const provider = new ScriptedProvider([]);
    let inputReads = 0;
    let toolInvocations = 0;
    let approvalRequests = 0;
    const binding = clockBinding({ now: "09:00" });
    binding.invoke = async () => {
      toolInvocations += 1;
      return { ok: true, data: { now: "09:00" } };
    };
    const tools = new ToolRegistry("foreground");
    tools.register(binding);
    const controller = new AbortController();
    controller.abort();
    const error = { code: "cancelled" as const, message: "cancelled", retryable: false };

    const trace = await driveToCompletion(
      state,
      throwingInitialInput(() => {
        inputReads += 1;
      }),
      {
        provider,
        tools,
        approvals: {
          request: async () => {
            approvalRequests += 1;
            return "allow";
          },
        },
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      controller.signal,
    );

    expect(inputReads).toBe(0);
    expect(provider.requestCount).toBe(0);
    expect(toolInvocations).toBe(0);
    expect(approvalRequests).toBe(0);
    expect(trace).toEqual({
      state: { ...state, phase: "cancelled", sequence: 1, error },
      inputs: [{ type: "cancel", reason: "cancelled" }],
      effects: [{ type: "cancel_run" }],
      modelTurns: 0,
    });
  });

  it("returns the exact wrong-host trace without reading caller input", async () => {
    const state = createRuntimeState({
      ...config,
      executionMode: "background",
      capabilitySnapshot: { ...capabilitySnapshot, executionMode: "background" },
    });
    const provider = new ScriptedProvider([]);
    let inputReads = 0;
    let toolInvocations = 0;
    let approvalRequests = 0;
    const binding = clockBinding({ now: "09:00" });
    binding.invoke = async () => {
      toolInvocations += 1;
      return { ok: true, data: { now: "09:00" } };
    };
    const tools = new ToolRegistry("foreground");
    tools.register(binding);
    const error = {
      code: "protocol_incompatible" as const,
      message: "foreground Driver requires foreground execution mode",
      retryable: false,
    };

    const trace = await driveToCompletion(
      state,
      throwingInitialInput(() => {
        inputReads += 1;
      }),
      {
        provider,
        tools,
        approvals: {
          request: async () => {
            approvalRequests += 1;
            return "allow";
          },
        },
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(inputReads).toBe(0);
    expect(provider.requestCount).toBe(0);
    expect(toolInvocations).toBe(0);
    expect(approvalRequests).toBe(0);
    expect(trace).toEqual({
      state: { ...state, phase: "failed", sequence: 1, error },
      inputs: [{ type: "runtime_error", error }],
      effects: [{ type: "fail_run", error }],
      modelTurns: 0,
    });
  });

  it("rejects background mode before starting foreground host effects", async () => {
    const provider = new ScriptedProvider([]);
    const error = {
      code: "protocol_incompatible" as const,
      message: "foreground Driver requires foreground execution mode",
      retryable: false,
    };

    const trace = await driveToCompletion(
      createRuntimeState({
        ...config,
        executionMode: "background",
        capabilitySnapshot: { ...capabilitySnapshot, executionMode: "background" },
      }),
      { type: "user_message", text: "do not start" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(provider.requestCount).toBe(0);
    expect(trace.modelTurns).toBe(0);
    expect(trace.state).toMatchObject({ phase: "failed", error });
    expect(trace.inputs).toEqual([{ type: "runtime_error", error }]);
    expect(trace.effects).toEqual([{ type: "fail_run", error }]);
  });

  it.each(forbiddenInitialInputCases())(
    "rejects caller-originated $name before trusted host work",
    async ({ state, input }) => {
      const provider = new ScriptedProvider([]);
      let toolInvocations = 0;
      let approvalRequests = 0;
      const binding = clockBinding({ now: "09:00" });
      binding.invoke = async () => {
        toolInvocations += 1;
        return { ok: true, data: { now: "09:00" } };
      };
      const tools = new ToolRegistry("foreground");
      tools.register(binding);
      const error = {
        code: "protocol_incompatible" as const,
        message: "Driver initial input must be user_message, cancel, or runtime_error",
        retryable: false,
      };

      const trace = await driveToCompletion(
        state,
        input,
        {
          provider,
          tools,
          approvals: {
            request: async () => {
              approvalRequests += 1;
              return "allow";
            },
          },
          modelProfile: "server/default",
          policy: { allow: ["*"], deny: ["test.clock.read"], approvalFor: [] },
        },
        new AbortController().signal,
      );

      expect(provider.requestCount).toBe(0);
      expect(toolInvocations).toBe(0);
      expect(approvalRequests).toBe(0);
      expect(trace.modelTurns).toBe(0);
      expect(trace.state).toMatchObject({ phase: "failed", error });
      expect(trace.inputs).toEqual([{ type: "runtime_error", error }]);
      expect(trace.effects).toEqual([{ type: "fail_run", error }]);
    },
  );

  it.each([
    {
      name: "missing user_message payload",
      input: { type: "user_message" } as RuntimeInput,
      error: {
        code: "protocol_incompatible" as const,
        message: "invalid user_message payload",
        retryable: false,
      },
    },
    {
      name: "generic invalid runtime_error payload",
      input: {
        type: "runtime_error",
        error: { code: "future_error", message: "invalid", retryable: false },
      } as unknown as RuntimeInput,
      error: {
        code: "validation_failed" as const,
        message: "invalid RuntimeInput",
        retryable: false,
      },
    },
  ])("normalizes $name into a terminal trace", async ({ input, error }) => {
    const provider = new ScriptedProvider([]);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      input,
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(provider.requestCount).toBe(0);
    expect(trace).toEqual({
      state: {
        ...createRuntimeState(config),
        phase: "failed",
        sequence: 1,
        error,
      },
      inputs: [{ type: "runtime_error", error }],
      effects: [{ type: "fail_run", error }],
      modelTurns: 0,
    });
  });

  it("snapshots caller input before reading a stateful discriminator", async () => {
    const modelPending = advance(createRuntimeState(config), {
      type: "user_message",
      text: "start",
    }).state;
    const toolCalled = advance(modelPending, {
      type: "provider_event",
      providerEvent: {
        type: "tool_call",
        call: { id: "call-1", name: "test.clock.read", input: {} },
      },
    }).state;
    const toolPending = advance(toolCalled, {
      type: "provider_event",
      providerEvent: completedToolUseEvent(),
    }).state;
    const provider = new ScriptedProvider([]);
    let toolInvocations = 0;
    let approvalRequests = 0;
    let typeReads = 0;
    let textReads = 0;
    let resolutionReads = 0;
    const input = {} as RuntimeInput;
    Object.defineProperties(input, {
      type: {
        configurable: true,
        enumerable: true,
        get: () => {
          typeReads += 1;
          if (typeReads === 1) {
            Object.defineProperty(input, "text", { enumerable: false });
            Object.defineProperty(input, "toolResolution", { enumerable: true });
            return "user_message";
          }
          return "tool_resolution";
        },
      },
      text: {
        configurable: true,
        enumerable: true,
        get: () => {
          textReads += 1;
          return "caller message";
        },
      },
      toolResolution: {
        configurable: true,
        enumerable: false,
        get: () => {
          resolutionReads += 1;
          return { callId: "call-1", decision: "execute" };
        },
      },
    });
    const binding = clockBinding({ now: "09:00" });
    binding.invoke = async () => {
      toolInvocations += 1;
      return { ok: true, data: { now: "09:00" } };
    };
    const tools = new ToolRegistry("foreground");
    tools.register(binding);
    const error = {
      code: "protocol_incompatible" as const,
      message: "user_message is not allowed in tool_pending",
      retryable: false,
    };

    const trace = await driveToCompletion(
      toolPending,
      input,
      {
        provider,
        tools,
        approvals: {
          request: async () => {
            approvalRequests += 1;
            return "allow";
          },
        },
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: ["test.clock.read"], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect({
      typeReads,
      textReads,
      resolutionReads,
      providerRequests: provider.requestCount,
      toolInvocations,
      approvalRequests,
      modelTurns: trace.modelTurns,
      phase: trace.state.phase,
      error: trace.state.error,
      inputs: trace.inputs,
      effects: trace.effects,
    }).toEqual({
      typeReads: 1,
      textReads: 1,
      resolutionReads: 0,
      providerRequests: 0,
      toolInvocations: 0,
      approvalRequests: 0,
      modelTurns: 0,
      phase: "failed",
      error,
      inputs: [{ type: "user_message", text: "caller message" }],
      effects: [{ type: "fail_run", error }],
    });
  });

  it("drives model to tool and back without HTTP", async () => {
    const provider = new ScriptedProvider([
      [{ type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } }, completedToolUseEvent()],
      [
        { type: "text_delta", text: "done" },
        {
          type: "completed",
          stopReason: "end_turn",
          usage: { inputTokens: 8, outputTokens: 2, totalTokens: 10 },
        },
      ],
    ]);
    const tools = new ToolRegistry("foreground");
    tools.register(clockBinding({ now: "09:00" }));

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "time?" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.modelTurns).toBe(2);
    expect(trace.state.phase).toBe("completed");
    expect(trace.effects.map((value) => value.type)).toEqual([
      "request_model_turn",
      "resolve_tool",
      "execute_tool",
      "request_model_turn",
      "emit_text",
      "complete_run",
    ]);
    expect(provider.requestCount).toBe(2);
    expect(trace.state.messages).toEqual([
      { role: "user", content: [{ type: "text", text: "time?" }] },
      {
        role: "assistant",
        content: [{ type: "tool_call", toolCall: { id: "call-1", name: "test.clock.read", input: {} } }],
      },
      { role: "tool", content: [{ type: "tool_result", toolResult: { ok: true, data: { now: "09:00" } } }] },
      { role: "assistant", content: [{ type: "text", text: "done" }] },
    ]);
  });

  it("preserves streamed Provider event order in inputs, effects, and assistant text", async () => {
    const provider = new ScriptedProvider([[
      { type: "text_delta", text: "first " },
      { type: "text_delta", text: "second" },
      {
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 3, outputTokens: 2, totalTokens: 5 },
      },
    ]]);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "order?" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.inputs).toEqual([
      { type: "user_message", text: "order?" },
      { type: "provider_event", providerEvent: { type: "text_delta", text: "first " } },
      { type: "provider_event", providerEvent: { type: "text_delta", text: "second" } },
      {
        type: "provider_event",
        providerEvent: {
          type: "completed",
          stopReason: "end_turn",
          usage: { inputTokens: 3, outputTokens: 2, totalTokens: 5 },
        },
      },
    ]);
    expect(trace.effects).toEqual([
      { type: "request_model_turn", turnId: "turn-1" },
      { type: "emit_text", text: "first " },
      { type: "emit_text", text: "second" },
      { type: "complete_run" },
    ]);
    expect(trace.state.messages.at(-1)).toEqual({
      role: "assistant",
      content: [{ type: "text", text: "first second" }],
    });
  });

  it("builds a validated model request with only the server profile and effective ToolSpecs", async () => {
    const requests: ModelTurnRequest[] = [];
    const provider: ProviderGateway = {
      async *stream(request): AsyncIterable<ProviderEvent> {
        requests.push(structuredClone(request));
        yield {
          type: "completed",
          stopReason: "end_turn",
          usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
        };
      },
    };
    const tools = new ToolRegistry("foreground");
    tools.register(clockBinding({ now: "09:00" }));

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "request?" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "  server/approved-profile  ",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.phase).toBe("completed");
    expect(requests).toEqual([{
      protocolVersion: "2.0",
      runId: "run-1",
      turnId: "turn-1",
      modelProfile: "server/approved-profile",
      messages: [{ role: "user", content: [{ type: "text", text: "request?" }] }],
      tools: [clockBinding({ now: "ignored" }).spec],
    }]);
    expect(Object.keys(requests[0]).sort()).toEqual([
      "messages",
      "modelProfile",
      "protocolVersion",
      "runId",
      "tools",
      "turnId",
    ]);
  });

  it.each([
    ["", "model Profile is required"],
    ["   ", "model Profile is required"],
    ["https://provider.example/model", "model Profile must not be URL-like"],
    ["//provider.example/model", "model Profile must not be URL-like"],
  ])("rejects invalid model profile %o before Provider work", async (modelProfile, message) => {
    const provider = new ScriptedProvider([]);

    await expect(driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "invalid profile" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile,
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    )).rejects.toThrow(message);
    expect(provider.requestCount).toBe(0);
  });

  it("fails with capability_unavailable when the scripted model calls a missing Binding", async () => {
    const provider = new ScriptedProvider([[
      { type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } },
      completedToolUseEvent(),
    ]]);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "time?" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.phase).toBe("failed");
    expect(trace.state.error).toEqual({
      code: "capability_unavailable",
      message: "test.clock.read is unavailable",
      retryable: false,
    });
    expect(trace.effects.map(({ type }) => type)).toEqual([
      "request_model_turn",
      "resolve_tool",
      "fail_run",
    ]);
  });

  it("fails with permission_denied before execution when policy denies the Tool", async () => {
    const provider = new ScriptedProvider([[
      { type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } },
      completedToolUseEvent(),
    ]]);
    const tools = new ToolRegistry("foreground");
    let invocations = 0;
    const binding = clockBinding({ now: "09:00" });
    binding.invoke = async () => {
      invocations += 1;
      return { ok: true, data: { now: "09:00" } };
    };
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "time?" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: ["test.clock.read"], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.error?.code).toBe("permission_denied");
    expect(trace.inputs.at(-1)).toEqual({
      type: "tool_resolution",
      toolResolution: {
        callId: "call-1",
        decision: "denied",
        error: {
          code: "permission_denied",
          message: "tool denied: test.clock.read",
          retryable: false,
        },
      },
    });
    expect(invocations).toBe(0);
  });

  it("returns an approval denial to the model without executing the Tool", async () => {
    const provider = new ScriptedProvider([
      [{ type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } }, completedToolUseEvent()],
      [{
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 2, outputTokens: 1, totalTokens: 3 },
      }],
    ]);
    const tools = new ToolRegistry("foreground");
    let invocations = 0;
    const binding = clockBinding({ now: "09:00" });
    binding.spec.approvalPolicy = "always";
    binding.invoke = async () => {
      invocations += 1;
      return { ok: true, data: { now: "09:00" } };
    };
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "time?" },
      {
        provider,
        tools,
        approvals: { request: async () => "deny" },
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.phase).toBe("completed");
    expect(trace.effects.map(({ type }) => type)).toEqual([
      "request_model_turn",
      "resolve_tool",
      "request_approval",
      "request_model_turn",
      "complete_run",
    ]);
    expect(trace.inputs[4]).toEqual({
      type: "approval_response",
      approvalResponse: { approvalId: "approval-call-1", decision: "deny" },
    });
    expect(trace.state.messages[2]).toEqual({
      role: "tool",
      content: [{
        type: "tool_result",
        toolResult: {
          ok: false,
          error: { code: "approval_denied", message: "approval denied", retryable: false },
        },
      }],
    });
    expect(invocations).toBe(0);
  });

  it("feeds output-Schema failure back as a validated ToolResult", async () => {
    const provider = new ScriptedProvider([
      [{ type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } }, completedToolUseEvent()],
      [{
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 2, outputTokens: 1, totalTokens: 3 },
      }],
    ]);
    const tools = new ToolRegistry("foreground");
    tools.register(clockBinding({ now: "09:00", extra: "invalid" } as { now: string }));

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "time?" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.inputs[4]).toEqual({
      type: "tool_result",
      toolResult: {
        callId: "call-1",
        result: {
          ok: false,
          error: {
            code: "validation_failed",
            message: "tool output schema validation failed",
            retryable: false,
          },
        },
      },
    });
  });

  it("normalizes a protocol-invalid Binding result as validation_failed", async () => {
    const provider = new ScriptedProvider([
      [{ type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } }, completedToolUseEvent()],
      [{
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 2, outputTokens: 1, totalTokens: 3 },
      }],
    ]);
    const tools = new ToolRegistry("foreground");
    const binding = clockBinding({ now: "09:00" });
    binding.invoke = async () => ({ ok: true, data: { now: "09:00" }, unexpected: true } as ToolResult);
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "time?" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.inputs[4].toolResult?.result.error).toEqual({
      code: "validation_failed",
      message: "invalid ToolResult",
      retryable: false,
    });
  });

  it("snapshots a stateful ToolResult once before validation and tracing", async () => {
    // Mutation caught: validating the live Binding object before snapshotting
    // lets a getter expose different values to validation and the trace.
    const provider = new ScriptedProvider([
      [{ type: "tool_call", call: { id: "call-snapshot", name: "test.clock.read", input: {} } }, completedToolUseEvent()],
      [{
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 2, outputTokens: 1, totalTokens: 3 },
      }],
    ]);
    const tools = new ToolRegistry("foreground");
    const binding = clockBinding({ now: "unused" });
    let reads = 0;
    binding.invoke = async () => {
      const result = { ok: true } as ToolResult;
      Object.defineProperty(result, "data", {
        enumerable: true,
        get: () => ({ now: `snapshot-${++reads}` }),
      });
      return result;
    };
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "stateful result" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(reads).toBe(1);
    expect(trace.inputs[4].toolResult?.result).toEqual({
      ok: true,
      data: { now: "snapshot-1" },
    });
  });

  it("does not replace an invalid ToolResult error with the byte-limit error", async () => {
    // Mutation caught: measuring an internally normalized failure as though it
    // were the Binding's protocol-valid ToolResult.
    const provider = new ScriptedProvider([
      [{ type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } }, completedToolUseEvent()],
      [{
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 2, outputTokens: 1, totalTokens: 3 },
      }],
    ]);
    const tools = new ToolRegistry("foreground");
    const binding = clockBinding({ now: "09:00" });
    binding.spec.resultMaxBytes = 1;
    binding.invoke = async () => ({ ok: true } as ToolResult);
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "time?" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.inputs[4].toolResult?.result).toEqual({
      ok: false,
      error: {
        code: "validation_failed",
        message: "invalid ToolResult",
        retryable: false,
      },
    });
  });

  it("normalizes a generic malformed Provider event as validation_failed", async () => {
    const provider = new ScriptedProvider([[
      { type: "text_delta", text: 42 } as unknown as ProviderEvent,
    ]]);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "invalid?" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.error).toEqual({
      code: "validation_failed",
      message: "invalid ProviderEvent",
      retryable: false,
    });
  });

  it("normalizes a missing tagged Provider payload as protocol_incompatible", async () => {
    const provider = new ScriptedProvider([[{ type: "text_delta" } as ProviderEvent]]);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "invalid?" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.error).toEqual({
      code: "protocol_incompatible",
      message: "invalid text_delta provider event payload",
      retryable: false,
    });
    expect(trace.inputs.at(-1)).toEqual({
      type: "runtime_error",
      error: trace.state.error,
    });
  });

  it("turns approval broker failure into a stable terminal Runtime error", async () => {
    const provider = new ScriptedProvider([[
      { type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } },
      completedToolUseEvent(),
    ]]);
    const tools = new ToolRegistry("foreground");
    const binding = clockBinding({ now: "09:00" });
    binding.spec.approvalPolicy = "always";
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "time?" },
      {
        provider,
        tools,
        approvals: { request: async () => { throw new Error("private broker detail"); } },
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.phase).toBe("failed");
    expect(trace.state.error).toEqual({
      code: "internal_error",
      message: "approval request failed",
      retryable: false,
    });
    expect(trace.effects.at(-1)).toEqual({
      type: "fail_run",
      error: { code: "internal_error", message: "approval request failed", retryable: false },
    });
  });

  it("turns an unknown approval broker decision into protocol_incompatible without executing", async () => {
    const provider = new ScriptedProvider([[
      { type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } },
      completedToolUseEvent(),
    ]]);
    const tools = new ToolRegistry("foreground");
    let invocations = 0;
    const binding = clockBinding({ now: "09:00" });
    binding.spec.approvalPolicy = "always";
    binding.invoke = async () => {
      invocations += 1;
      return { ok: true, data: { now: "09:00" } };
    };
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "invalid approval" },
      {
        provider,
        tools,
        approvals: { request: async () => "later" as "allow" },
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.phase).toBe("failed");
    expect(trace.state.error).toEqual({
      code: "protocol_incompatible",
      message: "invalid ApprovalBroker decision",
      retryable: false,
    });
    expect(trace.inputs.at(-1)).toEqual({
      type: "runtime_error",
      error: {
        code: "protocol_incompatible",
        message: "invalid ApprovalBroker decision",
        retryable: false,
      },
    });
    expect(trace.effects.map(({ type }) => type)).toEqual([
      "request_model_turn",
      "resolve_tool",
      "request_approval",
      "fail_run",
    ]);
    expect(invocations).toBe(0);
  });

  it("cancels before starting any model work when the caller signal is already aborted", async () => {
    const controller = new AbortController();
    controller.abort();

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "do not start" },
      {
        provider: new ScriptedProvider([]),
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      controller.signal,
    );

    expect(trace).toEqual({
      state: {
        ...createRuntimeState(config),
        phase: "cancelled",
        sequence: 1,
        error: { code: "cancelled", message: "cancelled", retryable: false },
      },
      inputs: [{ type: "cancel", reason: "cancelled" }],
      effects: [{ type: "cancel_run" }],
      modelTurns: 0,
    });
  });

  it("classifies a native TimeoutError before starting Provider work", async () => {
    const controller = new AbortController();
    controller.abort(new DOMException("private Host timeout", "TimeoutError"));
    const provider = new ScriptedProvider([]);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "do not start" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      controller.signal,
    );

    expect(provider.requestCount).toBe(0);
    expect(trace.state.phase).toBe("failed");
    expect(trace.state.error?.code).toBe("timeout");
    expect(trace.inputs).toEqual([{
      type: "runtime_error",
      error: { code: "timeout", message: "runtime deadline exceeded", retryable: false },
    }]);
  });

  it("interrupts a non-cooperative Provider stream when the caller aborts", async () => {
    const controller = new AbortController();
    const provider: ProviderGateway = {
      stream(): AsyncIterable<ProviderEvent> {
        return {
          [Symbol.asyncIterator](): AsyncIterator<ProviderEvent> {
            return {
              next(): Promise<IteratorResult<ProviderEvent>> {
                controller.abort();
                return new Promise<IteratorResult<ProviderEvent>>(() => undefined);
              },
            };
          },
        };
      },
    };

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "cancel during stream" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      controller.signal,
    );

    expect(trace.state.phase).toBe("cancelled");
    expect(trace.inputs).toEqual([
      { type: "user_message", text: "cancel during stream" },
      { type: "cancel", reason: "cancelled" },
    ]);
    expect(trace.effects).toEqual([
      { type: "request_model_turn", turnId: "turn-1" },
      { type: "cancel_run" },
    ]);
  }, 300);

  it("classifies an arbitrary interruption while Provider work is active", async () => {
    const controller = new AbortController();
    const provider: ProviderGateway = {
      stream(): AsyncIterable<ProviderEvent> {
        return {
          [Symbol.asyncIterator](): AsyncIterator<ProviderEvent> {
            return {
              next(): Promise<IteratorResult<ProviderEvent>> {
                controller.abort(new Error("private Provider interruption"));
                return new Promise<IteratorResult<ProviderEvent>>(() => undefined);
              },
            };
          },
        };
      },
    };

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "interrupt during stream" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      controller.signal,
    );

    expect(trace.state.phase).toBe("failed");
    expect(trace.state.error).toEqual({
      code: "internal_error",
      message: "runtime interrupted",
      retryable: false,
    });
    expect(trace.inputs.map(({ type }) => type)).toEqual(["user_message", "runtime_error"]);
  }, 300);

  it("observes a Provider next rejection that arrives after a cancelled trace", async () => {
    const controller = new AbortController();
    let rejectLater!: (reason: unknown) => void;
    const lateRejection = new Promise<IteratorResult<ProviderEvent>>((_, reject) => {
      rejectLater = reject;
    });
    const provider: ProviderGateway = {
      stream(): AsyncIterable<ProviderEvent> {
        return {
          [Symbol.asyncIterator](): AsyncIterator<ProviderEvent> {
            return {
              next(): Promise<IteratorResult<ProviderEvent>> {
                controller.abort();
                return lateRejection;
              },
            };
          },
        };
      },
    };

    const captured = await captureUnhandledRejections(async () => {
      const trace = await driveToCompletion(
        createRuntimeState(config),
        { type: "user_message", text: "late Provider rejection" },
        {
          provider,
          tools: new ToolRegistry("foreground"),
          approvals: allowAllApprovals,
          modelProfile: "server/default",
          policy: { allow: ["*"], deny: [], approvalFor: [] },
        },
        controller.signal,
      );
      rejectLater(new Error("late Provider failure"));
      return trace;
    });

    expect(captured.value.state.phase).toBe("cancelled");
    expect(captured.reasons).toEqual([]);
  });

  it("interrupts a non-cooperative Tool Binding when the caller aborts", async () => {
    const controller = new AbortController();
    const provider = new ScriptedProvider([[
      { type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } },
      completedToolUseEvent(),
    ]]);
    const tools = new ToolRegistry("foreground");
    let invocations = 0;
    const binding = clockBinding({ now: "09:00" });
    binding.invoke = async () => {
      invocations += 1;
      controller.abort();
      return new Promise<ToolResult>(() => undefined);
    };
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "cancel during tool" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      controller.signal,
    );

    expect(trace.state.phase).toBe("cancelled");
    expect(trace.inputs.map(({ type }) => type)).toEqual([
      "user_message",
      "provider_event",
      "provider_event",
      "tool_resolution",
      "cancel",
    ]);
    expect(trace.effects.map(({ type }) => type)).toEqual([
      "request_model_turn",
      "resolve_tool",
      "execute_tool",
      "cancel_run",
    ]);
    expect(invocations).toBe(1);
  }, 300);

  it("observes a Tool rejection that arrives after a cancelled trace", async () => {
    const controller = new AbortController();
    const provider = new ScriptedProvider([[
      { type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } },
      completedToolUseEvent(),
    ]]);
    let rejectLater!: (reason: unknown) => void;
    const lateRejection = new Promise<ToolResult>((_, reject) => {
      rejectLater = reject;
    });
    const tools = new ToolRegistry("foreground");
    const binding = clockBinding({ now: "09:00" });
    binding.invoke = () => {
      controller.abort();
      return lateRejection;
    };
    tools.register(binding);

    const captured = await captureUnhandledRejections(async () => {
      const trace = await driveToCompletion(
        createRuntimeState(config),
        { type: "user_message", text: "late Tool rejection" },
        {
          provider,
          tools,
          approvals: allowAllApprovals,
          modelProfile: "server/default",
          policy: { allow: ["*"], deny: [], approvalFor: [] },
        },
        controller.signal,
      );
      rejectLater(new Error("late Tool failure"));
      return trace;
    });

    expect(captured.value.state.phase).toBe("cancelled");
    expect(captured.reasons).toEqual([]);
  });

  it("interrupts a non-cooperative approval broker when the caller aborts", async () => {
    const controller = new AbortController();
    const provider = new ScriptedProvider([[
      { type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } },
      completedToolUseEvent(),
    ]]);
    const tools = new ToolRegistry("foreground");
    const binding = clockBinding({ now: "09:00" });
    binding.spec.approvalPolicy = "always";
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "cancel during approval" },
      {
        provider,
        tools,
        approvals: {
          request: async () => {
            controller.abort();
            return new Promise<"allow" | "deny">(() => undefined);
          },
        },
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      controller.signal,
    );

    expect(trace.state.phase).toBe("cancelled");
    expect(trace.inputs.map(({ type }) => type)).toEqual([
      "user_message",
      "provider_event",
      "provider_event",
      "tool_resolution",
      "cancel",
    ]);
    expect(trace.effects.map(({ type }) => type)).toEqual([
      "request_model_turn",
      "resolve_tool",
      "request_approval",
      "cancel_run",
    ]);
  }, 300);

  it("observes an approval rejection that arrives after a cancelled trace", async () => {
    const controller = new AbortController();
    const provider = new ScriptedProvider([[
      { type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } },
      completedToolUseEvent(),
    ]]);
    const tools = new ToolRegistry("foreground");
    const binding = clockBinding({ now: "09:00" });
    binding.spec.approvalPolicy = "always";
    tools.register(binding);
    let rejectLater!: (reason: unknown) => void;
    const lateRejection = new Promise<"allow" | "deny">((_, reject) => {
      rejectLater = reject;
    });

    const captured = await captureUnhandledRejections(async () => {
      const trace = await driveToCompletion(
        createRuntimeState(config),
        { type: "user_message", text: "late approval rejection" },
        {
          provider,
          tools,
          approvals: {
            request: () => {
              controller.abort();
              return lateRejection;
            },
          },
          modelProfile: "server/default",
          policy: { allow: ["*"], deny: [], approvalFor: [] },
        },
        controller.signal,
      );
      rejectLater(new Error("late approval failure"));
      return trace;
    });

    expect(captured.value.state.phase).toBe("cancelled");
    expect(captured.reasons).toEqual([]);
  });

  it("fails with timeout when the Host deadline expires during Provider work", async () => {
    const provider: ProviderGateway = {
      stream(): AsyncIterable<ProviderEvent> {
        return {
          [Symbol.asyncIterator](): AsyncIterator<ProviderEvent> {
            return { next: async () => new Promise<IteratorResult<ProviderEvent>>(() => undefined) };
          },
        };
      },
    };
    const timeoutConfig: RuntimeConfig = {
      ...config,
      budget: { ...budget, maxDurationMs: 5 },
    };

    const trace = await driveToCompletion(
      createRuntimeState(timeoutConfig),
      { type: "user_message", text: "wait forever" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.phase).toBe("failed");
    expect(trace.state.error).toEqual({
      code: "timeout",
      message: "runtime deadline exceeded",
      retryable: false,
    });
    expect(trace.inputs).toEqual([
      { type: "user_message", text: "wait forever" },
      {
        type: "runtime_error",
        error: { code: "timeout", message: "runtime deadline exceeded", retryable: false },
      },
    ]);
    expect(trace.effects).toEqual([
      { type: "request_model_turn", turnId: "turn-1" },
      {
        type: "fail_run",
        error: { code: "timeout", message: "runtime deadline exceeded", retryable: false },
      },
    ]);
  }, 300);

  it("does not start Provider work after the absolute Host deadline", async () => {
    const provider = new ScriptedProvider([[
      {
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
      },
    ]]);

    const trace = await driveToCompletion(
      createRuntimeState({
        ...config,
        budget: { ...budget, maxDurationMs: 120_000 },
      }),
      { type: "user_message", text: "already expired" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
        deadlineAtMs: Date.now() - 1,
      },
      new AbortController().signal,
    );

    expect(provider.requestCount).toBe(0);
    expect(trace.state.phase).toBe("failed");
    expect(trace.state.error?.code).toBe("timeout");
  });

  it("uses the 20 ms remaining Host deadline instead of the 120 s budget", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-05T00:00:00.000Z"));
    try {
      let providerStarted = false;
      const provider: ProviderGateway = {
        stream(): AsyncIterable<ProviderEvent> {
          providerStarted = true;
          return {
            [Symbol.asyncIterator](): AsyncIterator<ProviderEvent> {
              return { next: () => new Promise<IteratorResult<ProviderEvent>>(() => undefined) };
            },
          };
        },
      };
      let completedTrace: Awaited<ReturnType<typeof driveToCompletion>> | undefined;
      const run = driveToCompletion(
        createRuntimeState({
          ...config,
          budget: { ...budget, maxDurationMs: 120_000 },
        }),
        { type: "user_message", text: "use remaining time" },
        {
          provider,
          tools: new ToolRegistry("foreground"),
          approvals: allowAllApprovals,
          modelProfile: "server/default",
          policy: { allow: ["*"], deny: [], approvalFor: [] },
          deadlineAtMs: Date.now() + 20,
        },
        new AbortController().signal,
      ).then((trace) => {
        completedTrace = trace;
      });

      await vi.advanceTimersByTimeAsync(0);
      expect(providerStarted).toBe(true);
      await vi.advanceTimersByTimeAsync(19);
      expect(completedTrace).toBeUndefined();
      await vi.advanceTimersByTimeAsync(1);
      await run;
      expect(completedTrace?.state.phase).toBe("failed");
      expect(completedTrace?.state.error?.code).toBe("timeout");
    } finally {
      vi.useRealTimers();
    }
  });

  it("gives the absolute Run deadline precedence after a Tool starts", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-05T00:00:00.000Z"));
    try {
      const provider = new ScriptedProvider([[
        { type: "tool_call", call: { id: "call-run-deadline", name: "test.clock.read", input: {} } },
        completedToolUseEvent(),
      ]]);
      const tools = new ToolRegistry("foreground");
      const binding = clockBinding({ now: "09:00" });
      binding.spec.timeoutMs = 1_000;
      let bindingStarted = false;
      binding.invoke = async (_input, context) => new Promise<ToolResult>((resolve) => {
        bindingStarted = true;
        context.signal.addEventListener("abort", () => {
          resolve({ ok: true, data: { now: "too late" } });
        }, { once: true });
      });
      tools.register(binding);

      const run = driveToCompletion(
        createRuntimeState({
          ...config,
          budget: { ...budget, maxDurationMs: 120_000 },
        }),
        { type: "user_message", text: "absolute Run deadline" },
        {
          provider,
          tools,
          approvals: allowAllApprovals,
          modelProfile: "server/default",
          policy: { allow: ["*"], deny: [], approvalFor: [] },
          deadlineAtMs: Date.now() + 20,
        },
        new AbortController().signal,
      );

      await vi.advanceTimersByTimeAsync(0);
      expect(bindingStarted).toBe(true);
      await vi.advanceTimersByTimeAsync(20);
      const trace = await run;
      expect(trace.state.phase).toBe("failed");
      expect(trace.state.error?.code).toBe("timeout");
      expect(trace.inputs.some(({ type }) => type === "tool_result")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it.each([
    { name: "cooperative", cooperative: true },
    { name: "non-cooperative", cooperative: false },
  ])("enforces the per-Tool timeout for a $name Binding", async ({ cooperative }) => {
    // Mutation caught: invoking a Tool with only the overall Run signal.
    const provider = new ScriptedProvider([
      [{ type: "tool_call", call: { id: "call-timeout", name: "test.clock.read", input: {} } }, completedToolUseEvent()],
      [{
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 2, outputTokens: 1, totalTokens: 3 },
      }],
    ]);
    const tools = new ToolRegistry("foreground");
    const binding = clockBinding({ now: "09:00" });
    binding.spec.timeoutMs = 5;
    let bindingSignalAborted = false;
    binding.invoke = async (_input, context) => new Promise<ToolResult>((resolve) => {
      if (!cooperative) return;
      context.signal.addEventListener("abort", () => {
        bindingSignalAborted = true;
        resolve({ ok: true, data: { now: "too late" } });
      }, { once: true });
    });
    tools.register(binding);
    const state = createRuntimeState({
      ...config,
      budget: { ...budget, maxDurationMs: 100 },
    });

    const trace = await driveToCompletion(
      state,
      { type: "user_message", text: "bounded tool" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.phase).toBe("completed");
    expect(trace.inputs[4]).toEqual({
      type: "tool_result",
      toolResult: {
        callId: "call-timeout",
        result: {
          ok: false,
          error: { code: "timeout", message: "tool deadline exceeded", retryable: false },
        },
      },
    });
    expect(trace.effects.map(({ type }) => type)).toEqual([
      "request_model_turn",
      "resolve_tool",
      "execute_tool",
      "request_model_turn",
      "complete_run",
    ]);
    expect(bindingSignalAborted).toBe(cooperative);
  }, 1_000);

  it("executes exactly once after an allowed deterministic approval", async () => {
    const provider = new ScriptedProvider([
      [{ type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } }, completedToolUseEvent()],
      [{
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 2, outputTokens: 1, totalTokens: 3 },
      }],
    ]);
    const tools = new ToolRegistry("foreground");
    let invocations = 0;
    const binding = clockBinding({ now: "09:00" });
    binding.spec.approvalPolicy = "if_needed";
    binding.spec.sideEffect = "reversible_write";
    binding.invoke = async () => {
      invocations += 1;
      return { ok: true, data: { now: "09:00" } };
    };
    tools.register(binding);
    const approvals: Array<{ approvalId: string; call: unknown }> = [];

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "approve?" },
      {
        provider,
        tools,
        approvals: {
          request: async (approvalId, call) => {
            approvals.push({ approvalId, call: structuredClone(call) });
            return "allow";
          },
        },
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: ["reversible_write"] },
      },
      new AbortController().signal,
    );

    expect(approvals).toEqual([{
      approvalId: "approval-call-1",
      call: { id: "call-1", name: "test.clock.read", input: {} },
    }]);
    expect(invocations).toBe(1);
    expect(trace.state.phase).toBe("completed");
    expect(trace.effects.map(({ type }) => type)).toEqual([
      "request_model_turn",
      "resolve_tool",
      "request_approval",
      "execute_tool",
      "request_model_turn",
      "complete_run",
    ]);
  });

  it("rejects invalid Tool input before invoking the Binding", async () => {
    const provider = new ScriptedProvider([
      [{
        type: "tool_call",
        call: { id: "call-1", name: "test.clock.read", input: { unexpected: true } },
      }, completedToolUseEvent()],
      [{
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 2, outputTokens: 1, totalTokens: 3 },
      }],
    ]);
    const tools = new ToolRegistry("foreground");
    let invocations = 0;
    const binding = clockBinding({ now: "09:00" });
    binding.invoke = async () => {
      invocations += 1;
      return { ok: true, data: { now: "09:00" } };
    };
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "bad input" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(invocations).toBe(0);
    expect(trace.inputs[4].toolResult?.result).toEqual({
      ok: false,
      error: {
        code: "validation_failed",
        message: "tool input schema validation failed",
        retryable: false,
      },
    });
  });

  it("normalizes a thrown Binding error as tool_failed without exposing its details", async () => {
    const provider = new ScriptedProvider([
      [{ type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } }, completedToolUseEvent()],
      [{
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 2, outputTokens: 1, totalTokens: 3 },
      }],
    ]);
    const tools = new ToolRegistry("foreground");
    const binding = clockBinding({ now: "09:00" });
    binding.invoke = async () => { throw new Error("private tool detail"); };
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "fail tool" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.inputs[4].toolResult?.result).toEqual({
      ok: false,
      error: { code: "tool_failed", message: "tool invocation failed", retryable: false },
    });
    expect(JSON.stringify(trace)).not.toContain("private tool detail");
  });

  it("enforces resultMaxBytes before returning Tool data to the model", async () => {
    const provider = new ScriptedProvider([
      [{ type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } }, completedToolUseEvent()],
      [{
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 2, outputTokens: 1, totalTokens: 3 },
      }],
    ]);
    const tools = new ToolRegistry("foreground");
    const binding = clockBinding({ now: "x".repeat(200) });
    binding.spec.resultMaxBytes = 100;
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "large result" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.inputs[4].toolResult?.result).toEqual({
      ok: false,
      error: {
        code: "validation_failed",
        message: "tool result exceeds resultMaxBytes",
        retryable: false,
      },
    });
  });

  it("applies resultMaxBytes to the complete ToolResult envelope at the exact boundary", async () => {
    // Mutation caught: measuring only ToolResult.data or excluding display/error fields.
    const run = async (resultMaxBytes: number) => {
      const provider = new ScriptedProvider([
        [{ type: "tool_call", call: { id: "call-size", name: "test.clock.read", input: {} } }, completedToolUseEvent()],
        [{
          type: "completed",
          stopReason: "end_turn",
          usage: { inputTokens: 2, outputTokens: 1, totalTokens: 3 },
        }],
      ]);
      const tools = new ToolRegistry("foreground");
      const binding = clockBinding({ now: "unused" });
      binding.spec.outputSchema = {
        type: "object",
        properties: { value: { type: "string" } },
        required: ["value"],
        additionalProperties: false,
      };
      binding.spec.resultMaxBytes = resultMaxBytes;
      binding.invoke = async () => structuredClone(toolResultSizeFixture.result) as ToolResult;
      tools.register(binding);
      return driveToCompletion(
        createRuntimeState(config),
        { type: "user_message", text: "measure the result envelope" },
        {
          provider,
          tools,
          approvals: allowAllApprovals,
          modelProfile: "server/default",
          policy: { allow: ["*"], deny: [], approvalFor: [] },
        },
        new AbortController().signal,
      );
    };

    expect(new TextEncoder().encode(JSON.stringify(toolResultSizeFixture.result)).byteLength)
      .toBe(toolResultSizeFixture.canonicalUtf8Bytes);
    const exact = await run(toolResultSizeFixture.canonicalUtf8Bytes);
    expect(exact.inputs[4].toolResult?.result).toEqual(toolResultSizeFixture.result);

    const below = await run(toolResultSizeFixture.canonicalUtf8Bytes - 1);
    expect(below.inputs[4].toolResult?.result).toEqual({
      ok: false,
      error: {
        code: "validation_failed",
        message: "tool result exceeds resultMaxBytes",
        retryable: false,
      },
    });
  });

  it("rejects a Tool result that cannot cross the JSON output boundary", async () => {
    const provider = new ScriptedProvider([
      [{ type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } }, completedToolUseEvent()],
      [{
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 2, outputTokens: 1, totalTokens: 3 },
      }],
    ]);
    const tools = new ToolRegistry("foreground");
    const binding = clockBinding({ now: "09:00" });
    binding.spec.outputSchema = { type: "object", additionalProperties: true };
    binding.invoke = async () => ({ ok: true, data: { value: 1n } });
    tools.register(binding);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "non-json result" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.inputs[4].toolResult?.result).toEqual({
      ok: false,
      error: {
        code: "validation_failed",
        message: "tool result is not JSON serializable",
        retryable: false,
      },
    });
  });

  it("preserves a validated Provider failure event as the terminal error", async () => {
    const provider = new ScriptedProvider([[
      {
        type: "error",
        error: { code: "provider_rate_limited", message: "provider rate limited", retryable: true },
      },
    ]]);

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "provider event failure" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.error).toEqual({
      code: "provider_rate_limited",
      message: "provider rate limited",
      retryable: true,
    });
    expect(trace.effects.at(-1)).toEqual({
      type: "fail_run",
      error: { code: "provider_rate_limited", message: "provider rate limited", retryable: true },
    });
  });

  it("normalizes a thrown Provider failure without exposing its details", async () => {
    const provider: ProviderGateway = {
      async *stream(): AsyncIterable<ProviderEvent> {
        throw new Error("private provider detail");
      },
    };

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "provider throws" },
      {
        provider,
        tools: new ToolRegistry("foreground"),
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(trace.state.error).toEqual({
      code: "provider_unavailable",
      message: "provider stream failed",
      retryable: false,
    });
    expect(JSON.stringify(trace)).not.toContain("private provider detail");
  });

  it("fails deterministically when the ScriptedProvider is exhausted on the second turn", async () => {
    const provider = new ScriptedProvider([[
      { type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } },
      completedToolUseEvent(),
    ]]);
    const tools = new ToolRegistry("foreground");
    tools.register(clockBinding({ now: "09:00" }));

    const trace = await driveToCompletion(
      createRuntimeState(config),
      { type: "user_message", text: "exhaust provider" },
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );

    expect(provider.requestCount).toBe(2);
    expect(trace.state.error).toEqual({
      code: "provider_unavailable",
      message: "scripted provider exhausted",
      retryable: false,
    });
    expect(trace.effects.map(({ type }) => type)).toEqual([
      "request_model_turn",
      "resolve_tool",
      "execute_tool",
      "request_model_turn",
      "fail_run",
    ]);
  });

  it("copies scripted events and stops yielding after its AbortSignal aborts", async () => {
    const original: ProviderEvent = { type: "text_delta", text: "original" };
    const scripts = [[original, original]];
    const provider = new ScriptedProvider(scripts);
    original.text = "caller mutation";
    scripts[0].push({ type: "text_delta", text: "late mutation" });
    const controller = new AbortController();
    const request: ModelTurnRequest = {
      protocolVersion: "2.0",
      runId: "run-1",
      turnId: "turn-1",
      modelProfile: "server/default",
      messages: [],
      tools: [],
    };
    const iterator = provider.stream(request, controller.signal)[Symbol.asyncIterator]();

    const first = await iterator.next();
    expect(first).toEqual({ done: false, value: { type: "text_delta", text: "original" } });
    if (!first.done) first.value.text = "returned mutation";
    const second = await iterator.next();
    expect(second).toEqual({ done: false, value: { type: "text_delta", text: "original" } });
    controller.abort();
    expect(await iterator.next()).toEqual({ done: true, value: undefined });
    expect(provider.requestCount).toBe(1);
  });

  it("keeps final state, normalized inputs, and effects in independent trace copies", async () => {
    const scriptedCall = { id: "call-1", name: "test.clock.read", input: {} };
    const provider = new ScriptedProvider([
      [{ type: "tool_call", call: scriptedCall }, completedToolUseEvent()],
      [{
        type: "completed",
        stopReason: "end_turn",
        usage: { inputTokens: 2, outputTokens: 1, totalTokens: 3 },
      }],
    ]);
    scriptedCall.id = "caller-mutated";
    const resultData = { now: "09:00" };
    const tools = new ToolRegistry("foreground");
    tools.register(clockBinding(resultData));
    const initialInput = { type: "user_message" as const, text: "copy?" };

    const trace = await driveToCompletion(
      createRuntimeState(config),
      initialInput,
      {
        provider,
        tools,
        approvals: allowAllApprovals,
        modelProfile: "server/default",
        policy: { allow: ["*"], deny: [], approvalFor: [] },
      },
      new AbortController().signal,
    );
    initialInput.text = "mutated after run";
    resultData.now = "mutated after run";
    trace.effects[1].toolCall!.id = "effect-mutated";
    trace.inputs[3].toolResolution!.callId = "input-mutated";

    expect(trace.inputs[0]).toEqual({ type: "user_message", text: "copy?" });
    expect(trace.inputs[1].providerEvent?.call).toEqual({
      id: "call-1",
      name: "test.clock.read",
      input: {},
    });
    expect(trace.effects[2].toolCall).toEqual({
      id: "call-1",
      name: "test.clock.read",
      input: {},
    });
    expect(trace.state.messages[1].content[0].toolCall).toEqual({
      id: "call-1",
      name: "test.clock.read",
      input: {},
    });
    expect(trace.state.messages[2].content[0].toolResult).toEqual({
      ok: true,
      data: { now: "09:00" },
    });
  });
});
