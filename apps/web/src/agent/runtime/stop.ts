import type { RuntimeInput } from "../generated/protocol";

export type RuntimeStopReason = {
  agentStop: "user" | "timeout" | "interrupted";
};

function stopKind(reason: unknown): RuntimeStopReason["agentStop"] {
  if (typeof reason === "object" && reason !== null && "agentStop" in reason) {
    const agentStop = (reason as { agentStop?: unknown }).agentStop;
    if (agentStop === "user" || agentStop === "timeout" || agentStop === "interrupted") {
      return agentStop;
    }
    return "interrupted";
  }
  if (reason === undefined || reason === null) return "user";
  const nativeDOMException = reason instanceof DOMException ||
    Object.prototype.toString.call(reason) === "[object DOMException]";
  if (nativeDOMException) {
    const name = (reason as DOMException).name;
    if (name === "TimeoutError") return "timeout";
    if (name === "AbortError") return "user";
  }
  return "interrupted";
}

export function stopInput(reason: unknown): RuntimeInput {
  switch (stopKind(reason)) {
    case "user":
      return { type: "cancel", reason: "cancelled" };
    case "timeout":
      return {
        type: "runtime_error",
        error: { code: "timeout", message: "runtime deadline exceeded", retryable: false },
      };
    case "interrupted":
      return {
        type: "runtime_error",
        error: { code: "internal_error", message: "runtime interrupted", retryable: false },
      };
  }
}
