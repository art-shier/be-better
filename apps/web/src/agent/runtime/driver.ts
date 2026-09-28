import type {
  AgentError,
  ModelTurnRequest,
  ProviderEvent,
  RuntimeEffect,
  RuntimeInput,
  RuntimeState,
  ToolCall,
  ToolResult,
} from "../generated/protocol";
import type { ProviderGateway } from "../provider/provider";
import { validateProtocol } from "../protocol/validate";
import { createSchemaValidator } from "../protocol/schema";
import { effectiveToolIDs, type ToolPolicy, type ToolRegistry } from "../tool/registry";
import { advance, providerEventPayloadError, runtimeInputPayloadError } from "./reducer";
import { canonicalJSONString } from "./canonical-json";
import { stopInput } from "./stop";

export interface ApprovalBroker {
  request(approvalId: string, call: ToolCall, signal: AbortSignal): Promise<"allow" | "deny">;
}

export interface RuntimeHost {
  provider: ProviderGateway;
  tools: ToolRegistry;
  approvals: ApprovalBroker;
  modelProfile: string;
  policy: ToolPolicy;
  deadlineAtMs?: number;
}

export interface RuntimeTrace {
  state: RuntimeState;
  inputs: RuntimeInput[];
  effects: RuntimeEffect[];
  modelTurns: number;
}

const terminalPhases = new Set<RuntimeState["phase"]>(["completed", "failed", "cancelled"]);
const schemaValidator = createSchemaValidator();
const aborted = Symbol("aborted");

function copy<T>(value: T): T {
  return structuredClone(value);
}

function agentError(code: AgentError["code"], message: string): AgentError {
  return { code, message, retryable: false };
}

function resultFailure(code: AgentError["code"], message: string): ToolResult {
  return validateProtocol<ToolResult>("ToolResult", { ok: false, error: agentError(code, message) });
}

function validateDynamicSchema(schema: Record<string, unknown>, value: unknown): boolean {
  try {
    return schemaValidator.compile(schema)(value) === true;
  } catch {
    return false;
  }
}

function resultBytes(result: ToolResult): number | undefined {
  try {
    const serialized = canonicalJSONString(result);
    if (serialized === undefined) return undefined;
    return new TextEncoder().encode(serialized).byteLength;
  } catch {
    return undefined;
  }
}

function waitForAbort<T>(start: () => PromiseLike<T>, signal: AbortSignal): Promise<T | typeof aborted> {
  if (signal.aborted) return Promise.resolve(aborted);

  return new Promise<T | typeof aborted>((resolve, reject) => {
    let settled = false;
    const finish = (complete: () => void): void => {
      if (settled) return;
      settled = true;
      signal.removeEventListener("abort", onAbort);
      complete();
    };
    const onAbort = (): void => finish(() => resolve(aborted));

    signal.addEventListener("abort", onAbort, { once: true });
    if (signal.aborted) {
      onAbort();
      return;
    }

    let value: PromiseLike<T>;
    try {
      value = start();
    } catch (error) {
      finish(() => reject(error));
      return;
    }
    Promise.resolve(value).then(
      (result) => finish(() => resolve(result)),
      (error: unknown) => finish(() => reject(error)),
    );
  });
}

function closeIterator(iterator: AsyncIterator<ProviderEvent>): void {
  if (!iterator.return) return;
  try {
    void Promise.resolve(iterator.return()).catch(() => undefined);
  } catch {
    // A Provider cleanup failure must not replace the normalized Runtime outcome.
  }
}

function needsApproval(call: ToolCall, host: RuntimeHost): boolean {
  const binding = host.tools.resolve(call.name)!;
  return binding.spec.approvalPolicy === "always" ||
    (binding.spec.approvalPolicy === "if_needed" && host.policy.approvalFor.includes(binding.spec.sideEffect));
}

function urlLikeProfile(value: string): boolean {
  if (value.startsWith("//")) return true;
  const colon = value.indexOf(":");
  if (colon <= 0) return false;
  return /^[A-Za-z][A-Za-z0-9+.-]*$/.test(value.slice(0, colon));
}

function normalizeModelProfile(value: string): string {
  const profile = value.trim();
  if (profile === "") throw new Error("model Profile is required");
  if (urlLikeProfile(profile)) throw new Error("model Profile must not be URL-like");
  return profile;
}

export async function driveToCompletion(
  initialState: RuntimeState,
  initialInput: RuntimeInput,
  host: RuntimeHost,
  callerSignal: AbortSignal,
): Promise<RuntimeTrace> {
  const modelProfile = normalizeModelProfile(host.modelProfile);
  let state = copy(initialState);
  let modelTurns = 0;
  const inputs: RuntimeInput[] = [];
  const effects: RuntimeEffect[] = [];
  const pendingEffects: RuntimeEffect[] = [];
  const deadlineController = new AbortController();
  const remaining = host.deadlineAtMs === undefined
    ? state.budget.maxDurationMs
    : Math.max(0, host.deadlineAtMs - Date.now());
  const duration = Math.min(state.budget.maxDurationMs, remaining);
  const deadlineTimer = setTimeout(() => deadlineController.abort(), duration);
  if (duration === 0) deadlineController.abort();
  const signal = AbortSignal.any([callerSignal, deadlineController.signal]);

  const feed = (candidate: RuntimeInput): void => {
    const input = copy(validateProtocol<RuntimeInput>("RuntimeInput", copy(candidate)));
    const transition = advance(state, input);
    state = copy(transition.state);
    inputs.push(copy(input));
    for (const effect of transition.effects) {
      const recorded = copy(validateProtocol<RuntimeEffect>("RuntimeEffect", copy(effect)));
      effects.push(recorded);
      pendingEffects.push(copy(recorded));
    }
  };

  const feedAbort = (): boolean => {
    if (terminalPhases.has(state.phase)) return true;
    if (callerSignal.aborted) {
      feed(stopInput(callerSignal.reason));
      return true;
    }
    if (deadlineController.signal.aborted) {
      feed({ type: "runtime_error", error: agentError("timeout", "runtime deadline exceeded") });
      return true;
    }
    return false;
  };

  const requestModelTurn = async (effect: RuntimeEffect): Promise<void> => {
    modelTurns += 1;
    const effectiveIDs = effectiveToolIDs(
      state.capabilitySnapshot.toolIds,
      host.tools,
      state.capabilitySnapshot,
      host.policy,
    );
    const request = validateProtocol<ModelTurnRequest>("ModelTurnRequest", {
      protocolVersion: "2.0",
      runId: state.runId,
      turnId: effect.turnId!,
      modelProfile,
      messages: copy(state.messages),
      tools: effectiveIDs.map((id) => host.tools.resolve(id)!.spec),
    });

    let iterator: AsyncIterator<ProviderEvent> | undefined;
    try {
      iterator = host.provider.stream(copy(request), signal)[Symbol.asyncIterator]();
      while (true) {
        const next = await waitForAbort(() => iterator!.next(), signal);
        if (next === aborted) {
          closeIterator(iterator);
          feedAbort();
          return;
        }
        if (next.done) break;
        if (feedAbort()) {
          closeIterator(iterator);
          return;
        }
        const candidate = next.value;
        const payloadError = providerEventPayloadError(candidate);
        if (payloadError) {
          closeIterator(iterator);
          feed({
            type: "runtime_error",
            error: agentError("protocol_incompatible", payloadError),
          });
          return;
        }
        let event: ProviderEvent;
        try {
          event = copy(validateProtocol<ProviderEvent>("ProviderEvent", copy(candidate)));
        } catch {
          closeIterator(iterator);
          feed({
            type: "runtime_error",
            error: agentError("validation_failed", "invalid ProviderEvent"),
          });
          return;
        }
        feed({ type: "provider_event", providerEvent: event });
        if (state.phase !== "model_pending" && state.phase !== "model_streaming") {
          closeIterator(iterator);
          break;
        }
      }
    } catch {
      if (iterator) closeIterator(iterator);
      if (feedAbort()) return;
      feed({
        type: "runtime_error",
        error: agentError("provider_unavailable", "provider stream failed"),
      });
      return;
    }

    if (feedAbort()) return;
    if (state.phase === "model_pending" || state.phase === "model_streaming") {
      feed({
        type: "runtime_error",
        error: agentError("provider_unavailable", "provider stream ended before turn completion"),
      });
    }
  };

  const resolveTool = (call: ToolCall): void => {
    const binding = host.tools.resolve(call.name);
    if (!binding) {
      feed({
        type: "tool_resolution",
        toolResolution: {
          callId: call.id,
          decision: "unavailable",
          error: agentError("capability_unavailable", `${call.name} is unavailable`),
        },
      });
      return;
    }

    const effectiveIDs = effectiveToolIDs(
      state.capabilitySnapshot.toolIds,
      host.tools,
      state.capabilitySnapshot,
      host.policy,
    );
    if (!effectiveIDs.includes(call.name)) {
      feed({
        type: "tool_resolution",
        toolResolution: {
          callId: call.id,
          decision: "denied",
          error: agentError("permission_denied", `tool denied: ${call.name}`),
        },
      });
      return;
    }

    if (needsApproval(call, host)) {
      feed({
        type: "tool_resolution",
        toolResolution: {
          callId: call.id,
          decision: "approval",
          approvalId: `approval-${call.id}`,
        },
      });
      return;
    }

    feed({
      type: "tool_resolution",
      toolResolution: { callId: call.id, decision: "execute" },
    });
  };

  const executeTool = async (call: ToolCall): Promise<void> => {
    const binding = host.tools.resolve(call.name);
    let result: ToolResult;

    if (!binding) {
      result = resultFailure("capability_unavailable", `${call.name} is unavailable`);
    } else if (!validateDynamicSchema(binding.spec.inputSchema, call.input)) {
      result = resultFailure("validation_failed", "tool input schema validation failed");
    } else {
      let outcome:
        | { kind: "result"; value: ToolResult }
        | { kind: "failed" }
        | { kind: "timeout" };
      const toolDeadlineController = new AbortController();
      const toolDeadlineTimer = setTimeout(
        () => toolDeadlineController.abort(),
        binding.spec.timeoutMs,
      );
      const toolSignal = AbortSignal.any([signal, toolDeadlineController.signal]);
      try {
        const invocation = await waitForAbort(
          () => binding.invoke(copy(call.input), { runId: state.runId, callId: call.id, signal: toolSignal }),
          toolSignal,
        );
        if (invocation === aborted) {
          if (feedAbort()) return;
          outcome = { kind: "timeout" };
        } else {
          outcome = { kind: "result", value: invocation };
        }
      } catch {
        if (feedAbort()) return;
        outcome = { kind: "failed" };
      } finally {
        clearTimeout(toolDeadlineTimer);
      }

      if (outcome.kind === "timeout") {
        result = resultFailure("timeout", "tool deadline exceeded");
      } else if (outcome.kind === "failed") {
        result = resultFailure("tool_failed", "tool invocation failed");
      } else {
        let validResult = true;
        try {
          const snapshot = copy(outcome.value);
          validateProtocol<ToolResult>("ToolResult", snapshot);
          result = snapshot;
        } catch {
          result = resultFailure("validation_failed", "invalid ToolResult");
          validResult = false;
        }
        if (validResult && result.data !== undefined && !validateDynamicSchema(binding.spec.outputSchema, result.data)) {
          result = resultFailure("validation_failed", "tool output schema validation failed");
        } else if (validResult) {
          const byteLength = resultBytes(result);
          if (byteLength === undefined) {
            result = resultFailure("validation_failed", "tool result is not JSON serializable");
          } else if (byteLength > binding.spec.resultMaxBytes) {
            result = resultFailure("validation_failed", "tool result exceeds resultMaxBytes");
          }
        }
      }
    }

    if (feedAbort()) return;
    feed({ type: "tool_result", toolResult: { callId: call.id, result: copy(result) } });
  };

  try {
    if (feedAbort()) {
      return copy({ state, inputs, effects, modelTurns });
    }
    if (state.executionMode !== "foreground") {
      feed({
        type: "runtime_error",
        error: agentError("protocol_incompatible", "foreground Driver requires foreground execution mode"),
      });
      return copy({ state, inputs, effects, modelTurns });
    }
    const initialInputSnapshot = copy(initialInput);
    if (!["user_message", "cancel", "runtime_error"].includes(initialInputSnapshot.type)) {
      feed({
        type: "runtime_error",
        error: agentError(
          "protocol_incompatible",
          "Driver initial input must be user_message, cancel, or runtime_error",
        ),
      });
      return copy({ state, inputs, effects, modelTurns });
    }
    const payloadError = runtimeInputPayloadError(initialInputSnapshot);
    if (payloadError) {
      feed({
        type: "runtime_error",
        error: agentError("protocol_incompatible", payloadError),
      });
      return copy({ state, inputs, effects, modelTurns });
    }
    try {
      validateProtocol<RuntimeInput>("RuntimeInput", initialInputSnapshot);
    } catch {
      feed({
        type: "runtime_error",
        error: agentError("validation_failed", "invalid RuntimeInput"),
      });
      return copy({ state, inputs, effects, modelTurns });
    }
    feed(initialInputSnapshot);

    while (!terminalPhases.has(state.phase)) {
      if (feedAbort()) break;
      const effect = pendingEffects.shift();
      if (!effect) {
        feed({ type: "runtime_error", error: agentError("internal_error", "runtime stalled without an effect") });
        break;
      }

      switch (effect.type) {
        case "request_model_turn":
          await requestModelTurn(effect);
          break;
        case "resolve_tool":
          resolveTool(effect.toolCall!);
          break;
        case "execute_tool":
          await executeTool(effect.toolCall!);
          break;
        case "request_approval": {
          let decision: "allow" | "deny";
          try {
            const response = await waitForAbort<unknown>(
              () => host.approvals.request(effect.approvalId!, copy(effect.toolCall!), signal),
              signal,
            );
            if (response === aborted) {
              feedAbort();
              break;
            }
            if (response !== "allow" && response !== "deny") {
              feed({
                type: "runtime_error",
                error: agentError("protocol_incompatible", "invalid ApprovalBroker decision"),
              });
              break;
            }
            decision = response;
          } catch {
            if (feedAbort()) break;
            feed({
              type: "runtime_error",
              error: agentError("internal_error", "approval request failed"),
            });
            break;
          }
          if (!feedAbort()) {
            feed({
              type: "approval_response",
              approvalResponse: { approvalId: effect.approvalId!, decision },
            });
          }
          break;
        }
        case "emit_text":
        case "complete_run":
        case "fail_run":
        case "cancel_run":
          break;
      }
    }

    return copy({ state, inputs, effects, modelTurns });
  } finally {
    clearTimeout(deadlineTimer);
  }
}
