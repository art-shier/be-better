import type {
  AgentError,
  Budget,
  CapabilitySnapshot,
  ExecutionMode,
  ProviderEvent,
  RuntimeEffect,
  RuntimeInput,
  RuntimeState,
  RuntimeTransition,
  ToolCall,
  ToolResolution,
  ToolResult,
  Usage,
} from "../generated/protocol";
import { validateProtocol } from "../protocol/validate";
import { canonicalJSONString } from "./canonical-json";

export interface RuntimeConfig {
  runId: string;
  executionMode: ExecutionMode;
  capabilitySnapshot: CapabilitySnapshot;
  budget: Budget;
}

const zeroUsage: Usage = { inputTokens: 0, outputTokens: 0, totalTokens: 0 };

const incompatible = (message: string): AgentError => ({
  code: "protocol_incompatible",
  message,
  retryable: false,
});

const validationFailure = (message: string): AgentError => ({
  code: "validation_failed",
  message,
  retryable: false,
});

function clone<T>(value: T): T {
  return structuredClone(value);
}

export function createRuntimeState(config: RuntimeConfig): RuntimeState {
  const state: RuntimeState = {
    protocolVersion: "2.0",
    runId: config.runId,
    executionMode: config.executionMode,
    phase: "idle",
    sequence: 0,
    stepCount: 0,
    messages: [],
    capabilitySnapshot: clone(config.capabilitySnapshot),
    budget: clone(config.budget),
    usage: clone(zeroUsage),
    repeatedToolCalls: 0,
  };

  return clone(validateProtocol<RuntimeState>("RuntimeState", state));
}

export function canonicalToolFingerprint(call: ToolCall): string {
  return `${call.name}:${canonicalJSONString(call.input)}`;
}

function nextState(state: RuntimeState): RuntimeState {
  const next = clone(state);
  next.sequence += 1;
  return next;
}

function finish(state: RuntimeState, effects: RuntimeEffect[]): RuntimeTransition {
  const transition = {
    state: clone(state),
    effects: clone(effects),
  };
  validateProtocol<RuntimeTransition>("RuntimeTransition", transition);
  return transition;
}

function clearPendingState(state: RuntimeState): void {
  delete state.pendingToolCall;
  delete state.pendingApprovalId;
}

function failed(state: RuntimeState, error: AgentError): RuntimeTransition {
  const next = nextState(state);
  next.phase = "failed";
  clearPendingState(next);
  next.error = clone(error);
  return finish(next, [{ type: "fail_run", error: clone(error) }]);
}

function protocolFailure(state: RuntimeState, message: string): RuntimeTransition {
  return failed(state, incompatible(message));
}

function cancelled(state: RuntimeState, reason: string): RuntimeTransition {
  const next = nextState(state);
  next.phase = "cancelled";
  clearPendingState(next);
  next.error = { code: "cancelled", message: reason, retryable: false };
  return finish(next, [{ type: "cancel_run" }]);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function hasPayload(
  value: Record<string, unknown>,
  required: string[],
  allowed: string[],
  taggedFields: string[],
): boolean {
  return required.every((key) => value[key] !== undefined && value[key] !== null) &&
    taggedFields.every((key) => allowed.includes(key) || value[key] === undefined);
}

const runtimeInputPayloadFields = [
  "text",
  "providerEvent",
  "toolResolution",
  "toolResult",
  "approvalResponse",
  "error",
  "reason",
];
const runtimeInputTypes = new Set([
  "user_message",
  "provider_event",
  "tool_resolution",
  "tool_result",
  "approval_response",
  "runtime_error",
  "cancel",
]);

function validInputPayload(input: Record<string, unknown>): boolean {
  switch (input.type) {
    case "user_message":
      return hasPayload(input, ["text"], ["text"], runtimeInputPayloadFields);
    case "provider_event":
      return hasPayload(input, ["providerEvent"], ["providerEvent"], runtimeInputPayloadFields);
    case "tool_resolution":
      return hasPayload(input, ["toolResolution"], ["toolResolution"], runtimeInputPayloadFields);
    case "tool_result":
      return hasPayload(input, ["toolResult"], ["toolResult"], runtimeInputPayloadFields);
    case "approval_response":
      return hasPayload(input, ["approvalResponse"], ["approvalResponse"], runtimeInputPayloadFields);
    case "runtime_error":
      return hasPayload(input, ["error"], ["error"], runtimeInputPayloadFields);
    case "cancel":
      return hasPayload(input, [], ["reason"], runtimeInputPayloadFields);
    default:
      return false;
  }
}

const providerEventPayloadFields = ["text", "call", "stopReason", "usage", "error"];

function validProviderEventPayload(event: Record<string, unknown>): boolean {
  switch (event.type) {
    case "text_delta":
      return hasPayload(event, ["text"], ["text"], providerEventPayloadFields);
    case "tool_call":
      return hasPayload(event, ["call"], ["call"], providerEventPayloadFields);
    case "completed":
      return hasPayload(event, ["stopReason", "usage"], ["stopReason", "usage"], providerEventPayloadFields);
    case "error":
      return hasPayload(event, ["error"], ["error"], providerEventPayloadFields);
    default:
      return false;
  }
}

function validToolResolutionPayload(resolution: Record<string, unknown>): boolean {
  switch (resolution.decision) {
    case "execute":
      return resolution.approvalId === undefined && resolution.error === undefined;
    case "approval":
      return resolution.approvalId !== undefined && resolution.approvalId !== null &&
        resolution.error === undefined;
    case "unavailable":
    case "denied":
      return resolution.approvalId === undefined && resolution.error !== undefined && resolution.error !== null;
    default:
      return false;
  }
}

export function providerEventPayloadError(event: unknown): string | undefined {
  if (!isRecord(event)) return "invalid ProviderEvent";
  if (!validProviderEventPayload(event)) {
    return typeof event.type === "string"
      ? `invalid ${event.type} provider event payload`
      : "invalid ProviderEvent";
  }
  return undefined;
}

function toolResolutionPayloadError(resolution: unknown): string | undefined {
  if (!isRecord(resolution)) return "invalid ToolResolution";
  if (!validToolResolutionPayload(resolution)) {
    return typeof resolution.decision === "string"
      ? `invalid ${resolution.decision} tool resolution payload`
      : "invalid ToolResolution";
  }
  return undefined;
}

function approvalResponsePayloadError(response: unknown): string | undefined {
  if (!isRecord(response)) return "invalid ApprovalResponse";
  if (response.decision !== "allow" && response.decision !== "deny") {
    return typeof response.decision === "string"
      ? `invalid ${response.decision} approval response payload`
      : "invalid ApprovalResponse";
  }
  return undefined;
}

export function runtimeInputPayloadError(input: unknown): string | undefined {
  if (!isRecord(input) || typeof input.type !== "string") return "invalid RuntimeInput";
  if (!runtimeInputTypes.has(input.type)) return "invalid RuntimeInput";
  if (!validInputPayload(input)) return `invalid ${input.type} payload`;
  if (input.type === "provider_event") return providerEventPayloadError(input.providerEvent);
  if (input.type === "tool_resolution") return toolResolutionPayloadError(input.toolResolution);
  if (input.type === "approval_response") return approvalResponsePayloadError(input.approvalResponse);
  return undefined;
}

function requirePhase(
  state: RuntimeState,
  inputType: RuntimeInput["type"],
  phases: RuntimeState["phase"][],
): RuntimeTransition | undefined {
  if (phases.includes(state.phase)) return undefined;
  return protocolFailure(state, `${inputType} is not allowed in ${state.phase}`);
}

function onUserMessage(state: RuntimeState, text: string): RuntimeTransition {
  const illegal = requirePhase(state, "user_message", ["idle"]);
  if (illegal) return illegal;

  const next = nextState(state);
  next.phase = "model_pending";
  next.messages.push({ role: "user", content: [{ type: "text", text }] });
  return finish(next, [{ type: "request_model_turn", turnId: `turn-${next.sequence}` }]);
}

function onToolCall(state: RuntimeState, call: ToolCall): RuntimeTransition {
  if (state.pendingToolCall) return protocolFailure(state, "multiple tool calls in one turn");
  const next = nextState(state);
  next.phase = "model_streaming";
  next.pendingToolCall = clone(call);
  return finish(next, []);
}

function addUsage(current: Usage, update: Usage): Usage {
  return {
    inputTokens: current.inputTokens + update.inputTokens,
    outputTokens: current.outputTokens + update.outputTokens,
    totalTokens: current.totalTokens + update.totalTokens,
  };
}

function onProviderEvent(state: RuntimeState, event: ProviderEvent): RuntimeTransition {
  const illegal = requirePhase(state, "provider_event", ["model_pending", "model_streaming"]);
  if (illegal) return illegal;

  switch (event.type) {
    case "text_delta": {
      const next = nextState(state);
      next.phase = "model_streaming";
      next.assistantDraft = `${state.assistantDraft ?? ""}${event.text!}`;
      return finish(next, [{ type: "emit_text", text: event.text! }]);
    }
    case "tool_call":
      return onToolCall(state, event.call!);
    case "completed": {
      const next = nextState(state);
      next.usage = addUsage(state.usage, event.usage!);
      if (next.usage.totalTokens > state.budget.maxTokens) {
        const error = validationFailure("maxTokens exceeded");
        next.phase = "failed";
        clearPendingState(next);
        delete next.assistantDraft;
        next.error = clone(error);
        return finish(next, [{ type: "fail_run", error }]);
      }

      switch (event.stopReason) {
        case "end_turn": {
          if (state.pendingToolCall) {
            const error = incompatible("provider completed end_turn with pending tool call");
            next.phase = "failed";
            clearPendingState(next);
            next.error = clone(error);
            return finish(next, [{ type: "fail_run", error }]);
          }
          next.phase = "completed";
          if (state.assistantDraft) {
            next.messages.push({ role: "assistant", content: [{ type: "text", text: state.assistantDraft }] });
          }
          delete next.assistantDraft;
          delete next.error;
          return finish(next, [{ type: "complete_run" }]);
        }
        case "max_tokens": {
          const error = validationFailure("provider stopped at max_tokens");
          next.phase = "failed";
          clearPendingState(next);
          next.error = clone(error);
          return finish(next, [{ type: "fail_run", error }]);
        }
        case "cancelled":
          next.phase = "cancelled";
          clearPendingState(next);
          next.error = { code: "cancelled", message: "provider cancelled", retryable: false };
          return finish(next, [{ type: "cancel_run" }]);
        case "tool_use": {
          if (!state.pendingToolCall) {
            const error = incompatible("provider completed with orphan tool_use");
            next.phase = "failed";
            clearPendingState(next);
            next.error = clone(error);
            return finish(next, [{ type: "fail_run", error }]);
          }

          const call = clone(state.pendingToolCall);
          const fingerprint = canonicalToolFingerprint(call);
          const repeatedToolCalls = state.lastToolFingerprint === fingerprint
            ? state.repeatedToolCalls + 1
            : 1;
          const content: RuntimeState["messages"][number]["content"] = state.assistantDraft === undefined
            ? []
            : [{ type: "text", text: state.assistantDraft }];
          content.push({ type: "tool_call", toolCall: clone(call) });
          next.messages.push({ role: "assistant", content });
          delete next.assistantDraft;
          next.lastToolFingerprint = fingerprint;
          next.repeatedToolCalls = repeatedToolCalls;

          if (repeatedToolCalls > state.budget.maxRepeatedToolCalls) {
            const error = validationFailure("maxRepeatedToolCalls exceeded");
            next.phase = "failed";
            clearPendingState(next);
            next.error = clone(error);
            return finish(next, [{ type: "fail_run", error }]);
          }

          next.phase = "tool_pending";
          next.pendingToolCall = clone(call);
          return finish(next, [{ type: "resolve_tool", toolCall: clone(call) }]);
        }
      }
      return protocolFailure(state, "invalid completed provider event payload");
    }
    case "error":
      return failed(state, event.error!);
  }
}

function mismatchedCall(state: RuntimeState, kind: "tool_resolution" | "tool_result"): RuntimeTransition {
  return protocolFailure(state, `${kind} callId does not match pending tool call`);
}

function executePendingTool(state: RuntimeState): RuntimeTransition {
  if (state.stepCount >= state.budget.maxSteps) return failed(state, validationFailure("maxSteps exceeded"));
  const next = nextState(state);
  next.phase = "tool_pending";
  next.stepCount += 1;
  clearPendingState(next);
  return finish(next, [{ type: "execute_tool", toolCall: clone(state.pendingToolCall!) }]);
}

function onToolResolution(state: RuntimeState, resolution: ToolResolution): RuntimeTransition {
  const illegal = requirePhase(state, "tool_resolution", ["tool_pending"]);
  if (illegal) return illegal;
  if (!state.pendingToolCall || resolution.callId !== state.pendingToolCall.id) {
    return mismatchedCall(state, "tool_resolution");
  }

  switch (resolution.decision) {
    case "execute":
      return executePendingTool(state);
    case "approval": {
      const next = nextState(state);
      next.phase = "approval_pending";
      next.pendingApprovalId = resolution.approvalId!;
      return finish(next, [
        {
          type: "request_approval",
          toolCall: clone(state.pendingToolCall),
          approvalId: resolution.approvalId!,
        },
      ]);
    }
    case "unavailable":
    case "denied":
      return failed(state, resolution.error!);
  }
}

function onToolResult(
  state: RuntimeState,
  input: { callId: string; result: ToolResult },
): RuntimeTransition {
  const illegal = requirePhase(state, "tool_result", ["tool_pending"]);
  if (illegal) return illegal;
  if (state.pendingToolCall) {
    return protocolFailure(state, "tool_result is not allowed before tool execution");
  }
  const inFlightCall = mostRecentUnresolvedToolCall(state);
  if (!inFlightCall || input.callId !== inFlightCall.id) {
    return protocolFailure(state, "tool_result callId does not match in-flight tool call");
  }

  const next = nextState(state);
  next.phase = "model_pending";
  next.messages.push({ role: "tool", content: [{ type: "tool_result", toolResult: clone(input.result) }] });
  delete next.pendingToolCall;
  return finish(next, [{ type: "request_model_turn", turnId: `turn-${next.sequence}` }]);
}

function mostRecentUnresolvedToolCall(state: RuntimeState): ToolCall | undefined {
  for (let messageIndex = state.messages.length - 1; messageIndex >= 0; messageIndex -= 1) {
    const content = state.messages[messageIndex].content;
    for (let contentIndex = content.length - 1; contentIndex >= 0; contentIndex -= 1) {
      const block = content[contentIndex];
      if (block.type === "tool_result") return undefined;
      if (block.type === "tool_call") return block.toolCall;
    }
  }
  return undefined;
}

function onApprovalResponse(
  state: RuntimeState,
  response: { approvalId: string; decision: "allow" | "deny" },
): RuntimeTransition {
  const illegal = requirePhase(state, "approval_response", ["approval_pending"]);
  if (illegal) return illegal;
  if (!state.pendingToolCall || !state.pendingApprovalId || response.approvalId !== state.pendingApprovalId) {
    return protocolFailure(state, "approval_response approvalId does not match pending approval");
  }

  if (response.decision === "allow") return executePendingTool(state);

  const error: AgentError = { code: "approval_denied", message: "approval denied", retryable: false };
  const next = nextState(state);
  next.phase = "model_pending";
  next.messages.push({
    role: "tool",
    content: [{ type: "tool_result", toolResult: { ok: false, error: clone(error) } }],
  });
  delete next.pendingToolCall;
  delete next.pendingApprovalId;
  return finish(next, [{ type: "request_model_turn", turnId: `turn-${next.sequence}` }]);
}

const knownRuntimePhases = new Set<RuntimeState["phase"]>([
  "idle",
  "model_pending",
  "model_streaming",
  "tool_pending",
  "approval_pending",
  "completed",
  "failed",
  "cancelled",
]);

export function advance(state: RuntimeState, input: RuntimeInput): RuntimeTransition {
  try {
    validateProtocol<RuntimeState>("RuntimeState", state);
  } catch (error) {
    if (typeof state.phase === "string" && !knownRuntimePhases.has(state.phase)) {
      return protocolFailure(state, `unsupported RuntimeState phase ${state.phase}`);
    }
    throw error;
  }

  const taggedPayloadError = runtimeInputPayloadError(input);
  if (taggedPayloadError) return protocolFailure(state, taggedPayloadError);

  validateProtocol<RuntimeInput>("RuntimeInput", input);
  if (["completed", "failed", "cancelled"].includes(state.phase)) {
    return protocolFailure(state, "terminal_state");
  }
  if (state.executionMode !== state.capabilitySnapshot.executionMode) {
    return protocolFailure(state, "execution mode mismatch");
  }

  switch (input.type) {
    case "user_message":
      return onUserMessage(state, input.text!);
    case "provider_event":
      return onProviderEvent(state, input.providerEvent!);
    case "tool_resolution":
      return onToolResolution(state, input.toolResolution!);
    case "tool_result":
      return onToolResult(state, input.toolResult!);
    case "approval_response":
      return onApprovalResponse(state, input.approvalResponse!);
    case "runtime_error":
      return failed(state, input.error!);
    case "cancel":
      return cancelled(state, input.reason ?? "cancelled");
  }
}
