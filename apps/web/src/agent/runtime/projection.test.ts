import { describe, expect, it } from "vitest";

import type { AgentError, RuntimePhase, RuntimeState } from "../generated/protocol";
import { createRuntimeState, type RuntimeConfig } from "./reducer";
import { projectRun } from "./projection";

const config: RuntimeConfig = {
  runId: "run-projection",
  executionMode: "foreground",
  capabilitySnapshot: {
    runtimeVersion: "2.0.0",
    executionMode: "foreground",
    toolIds: [],
    skills: [],
    scope: { domains: ["test"] },
  },
  budget: {
    maxSteps: 4,
    maxTokens: 100,
    maxDurationMs: 30_000,
    maxWorkers: 1,
    maxConcurrency: 1,
    maxRepeatedToolCalls: 2,
  },
};

const baseState = createRuntimeState(config);
const runtimeError: AgentError = {
  code: "provider_unavailable",
  message: "provider unavailable",
  retryable: true,
};

describe("projectRun", () => {
  it.each([
    ["idle", "ready"],
    ["model_pending", "analyzing"],
    ["model_streaming", "analyzing"],
    ["tool_pending", "analyzing"],
    ["approval_pending", "waiting"],
    ["completed", "completed"],
    ["failed", "failed"],
    ["cancelled", "stopped"],
  ] as const)("projects %s to %s", (phase, status) => {
    const projection = projectRun({
      ...baseState,
      phase: phase as RuntimePhase,
      ...(phase === "failed" ? { error: runtimeError } : {}),
    });

    expect(projection.status).toBe(status);
    if (phase === "failed") {
      expect(projection).toEqual({
        status: "failed",
        errorCode: "provider_unavailable",
        errorMessage: "provider unavailable",
      });
    } else {
      expect(projection).toEqual({ status });
      expect(projection.errorCode).toBeUndefined();
      expect(projection.errorMessage).toBeUndefined();
    }
  });

  it("keeps generic malformed RuntimeState validation failures stable", () => {
    let thrown: unknown;
    try {
      projectRun({ ...baseState, sequence: -1 } as RuntimeState);
    } catch (error) {
      thrown = error;
    }

    expect(thrown).toMatchObject({ code: "validation_failed" });
  });

  it("classifies an unknown RuntimeState phase as protocol incompatible", () => {
    let thrown: unknown;
    try {
      projectRun({ ...baseState, phase: "future_phase" } as unknown as RuntimeState);
    } catch (error) {
      thrown = error;
    }

    expect(thrown).toMatchObject({ code: "protocol_incompatible" });
    expect(thrown).toBeInstanceOf(Error);
  });
});
