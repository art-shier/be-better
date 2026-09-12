import { describe, expect, it } from "vitest";

import { stopInput } from "./stop";

describe("Runtime stop classification", () => {
  it.each([
    ["user", "cancel", undefined],
    ["timeout", "runtime_error", "timeout"],
    ["interrupted", "runtime_error", "internal_error"],
  ] as const)("maps %s", (kind, type, code) => {
    const input = stopInput({ agentStop: kind });
    expect(input.type).toBe(type);
    expect(input.error?.code).toBe(code);
  });

  it.each([
    ["undefined", undefined, "cancel", undefined],
    ["null", null, "cancel", undefined],
    ["AbortError", new DOMException("private abort detail", "AbortError"), "cancel", undefined],
    ["TimeoutError", new DOMException("private timeout detail", "TimeoutError"), "runtime_error", "timeout"],
    ["arbitrary Error", new Error("private interruption detail"), "runtime_error", "internal_error"],
    ["unknown structured kind", { agentStop: "shutdown" }, "runtime_error", "internal_error"],
  ] as const)("maps %s without exposing its raw cause", (_name, reason, type, code) => {
    const input = stopInput(reason);
    expect(input.type).toBe(type);
    expect(input.error?.code).toBe(code);
    expect(JSON.stringify(input)).not.toContain("private");
  });
});
