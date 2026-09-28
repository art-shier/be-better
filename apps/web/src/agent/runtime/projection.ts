import type { AgentError, RuntimeState } from "../generated/protocol";
import { validateProtocol } from "../protocol/validate";

export interface RuntimeRunProjection {
  status: "ready" | "analyzing" | "waiting" | "completed" | "failed" | "stopped";
  errorCode?: AgentError["code"];
  errorMessage?: string;
}

export class RuntimeProjectionProtocolError extends Error {
  readonly code = "protocol_incompatible";

  constructor(phase: unknown) {
    super(`protocol_incompatible: unsupported RuntimeState phase ${String(phase)}`);
    this.name = "RuntimeProjectionProtocolError";
  }
}

const knownPhases = new Set<RuntimeState["phase"]>([
  "idle",
  "model_pending",
  "model_streaming",
  "tool_pending",
  "approval_pending",
  "completed",
  "failed",
  "cancelled",
]);

function containsUnknownPhase(value: unknown): value is { phase: unknown } {
  return value !== null &&
    typeof value === "object" &&
    "phase" in value &&
    typeof value.phase === "string" &&
    !knownPhases.has(value.phase as RuntimeState["phase"]);
}

function unsupportedPhase(phase: never): never {
  throw new RuntimeProjectionProtocolError(phase);
}

// Projection is deliberately side-effect free. AgentStep, AgentChange,
// SourceRef, and database persistence are Phase 2 adapters, not projection effects.
export function projectRun(state: RuntimeState): RuntimeRunProjection {
  try {
    validateProtocol<RuntimeState>("RuntimeState", state);
  } catch (error) {
    if (containsUnknownPhase(state)) throw new RuntimeProjectionProtocolError(state.phase);
    throw error;
  }

  switch (state.phase) {
    case "idle":
      return { status: "ready" };
    case "model_pending":
    case "model_streaming":
    case "tool_pending":
      return { status: "analyzing" };
    case "approval_pending":
      return { status: "waiting" };
    case "completed":
      return { status: "completed" };
    case "failed":
      return state.error === undefined
        ? { status: "failed" }
        : {
          status: "failed",
          errorCode: state.error.code,
          errorMessage: state.error.message,
        };
    case "cancelled":
      return { status: "stopped" };
    default:
      return unsupportedPhase(state.phase);
  }
}
