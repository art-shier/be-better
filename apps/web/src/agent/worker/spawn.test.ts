import { describe, expect, it } from "vitest";

import type {
  Budget,
  WorkerOutputContract,
  WorkerResult,
} from "../generated/protocol";
import {
  createWorkerSpawnSpec,
  validateWorkerResult,
  type WorkerParentContext,
  type WorkerSpawnRequest,
} from "./spawn";

const fullBudget: Budget = {
  maxSteps: 8,
  maxTokens: 2_000,
  maxDurationMs: 60_000,
  maxWorkers: 3,
  maxConcurrency: 2,
  maxRepeatedToolCalls: 2,
};

const outputContract: WorkerOutputContract = {
  sections: ["summary", "findings", "risks", "next_steps"],
};

function parent(overrides: Partial<WorkerParentContext> = {}): WorkerParentContext {
  return {
    runId: "parent-1",
    depth: 0,
    executionMode: "foreground",
    capabilities: {
      runtimeVersion: "2.0.0",
      executionMode: "foreground",
      toolIds: ["dayorder.notes.write", "dayorder.notes.search"],
      skills: [
        { name: "notes", version: "1.2.0", digest: "sha256:notes" },
        { name: "calendar", version: "2.0.0", digest: "sha256:calendar" },
      ],
      scope: { domains: ["notes"], entityIds: ["note-2", "note-1"] },
    },
    budget: structuredClone(fullBudget),
    remainingBudget: { ...fullBudget, maxTokens: 1_200, maxWorkers: 2, maxConcurrency: 1 },
    loadedSkillInstructions: [
      {
        skill: { name: "notes", version: "1.2.0", digest: "sha256:notes" },
        instructions: "Use the notes API carefully.",
      },
      {
        skill: { name: "calendar", version: "2.0.0", digest: "sha256:calendar" },
        instructions: "Use the calendar API carefully.",
      },
    ],
    ...overrides,
  };
}

function request(overrides: Partial<WorkerSpawnRequest> = {}): WorkerSpawnRequest {
  return {
    workerId: "worker-1",
    depth: 1,
    executionMode: "foreground",
    task: {
      goal: "Find the relevant note",
      successCriteria: ["Return one match"],
      scope: ["notes"],
      constraints: ["Read only"],
      hints: ["Search titles first"],
    },
    toolIds: ["dayorder.notes.search"],
    skills: [{ name: "notes", version: "1.2.0", digest: "sha256:notes" }],
    budget: {
      maxSteps: 4,
      maxTokens: 1_200,
      maxDurationMs: 30_000,
      maxWorkers: 1,
      maxConcurrency: 1,
      maxRepeatedToolCalls: 1,
    },
    outputContract,
    ...overrides,
  };
}

describe("createWorkerSpawnSpec", () => {
  it("creates a deterministic recursively frozen copy with selected loaded skill bodies", () => {
    const sourceParent = parent();
    const sourceRequest = request({
      toolIds: ["dayorder.notes.write", "dayorder.notes.search"],
      skills: [
        { name: "notes", version: "1.2.0", digest: "sha256:notes" },
        { name: "calendar", version: "2.0.0", digest: "sha256:calendar" },
      ],
    });

    const spawn = createWorkerSpawnSpec(sourceParent, sourceRequest);

    expect(spawn).toEqual({
      protocolVersion: "2.0",
      parentRunId: "parent-1",
      workerId: "worker-1",
      depth: 1,
      executionMode: "foreground",
      task: {
        goal: "Find the relevant note",
        successCriteria: ["Return one match"],
        scope: ["notes"],
        constraints: ["Read only"],
        hints: ["Search titles first"],
      },
      capabilities: {
        runtimeVersion: "2.0.0",
        executionMode: "foreground",
        toolIds: ["dayorder.notes.search", "dayorder.notes.write"],
        skills: [
          { name: "calendar", version: "2.0.0", digest: "sha256:calendar" },
          { name: "notes", version: "1.2.0", digest: "sha256:notes" },
        ],
        scope: { domains: ["notes"], entityIds: ["note-1", "note-2"] },
      },
      budget: {
        maxSteps: 4,
        maxTokens: 1_200,
        maxDurationMs: 30_000,
        maxWorkers: 1,
        maxConcurrency: 1,
        maxRepeatedToolCalls: 1,
      },
      skillInstructions: [
        {
          skill: { name: "calendar", version: "2.0.0", digest: "sha256:calendar" },
          instructions: "Use the calendar API carefully.",
        },
        {
          skill: { name: "notes", version: "1.2.0", digest: "sha256:notes" },
          instructions: "Use the notes API carefully.",
        },
      ],
      outputContract: { sections: ["summary", "findings", "risks", "next_steps"] },
    });
    expect(Object.isFrozen(spawn)).toBe(true);
    expect(Object.isFrozen(spawn.task.successCriteria)).toBe(true);
    expect(Object.isFrozen(spawn.capabilities.scope.entityIds)).toBe(true);
    expect(Object.isFrozen(spawn.skillInstructions[0]?.skill)).toBe(true);
    expect(Object.isFrozen(spawn.outputContract.sections)).toBe(true);

    sourceRequest.task.successCriteria[0] = "mutated";
    sourceRequest.skills[0]!.digest = "mutated";
    sourceParent.capabilities.scope.entityIds![0] = "mutated";
    sourceParent.loadedSkillInstructions[0]!.instructions = "mutated";
    expect(spawn.task.successCriteria[0]).toBe("Return one match");
    expect(spawn.capabilities.skills[1]!.digest).toBe("sha256:notes");
    expect(spawn.capabilities.scope.entityIds![0]).toBe("note-1");
    expect(spawn.skillInstructions[1]!.instructions).toBe("Use the notes API carefully.");
    expect(() => {
      spawn.capabilities.scope.entityIds![0] = "attempted mutation";
    }).toThrow(TypeError);
  });

  it.each([
    ["unknown tool", { toolIds: ["missing.tool"] }, /capability_unavailable/],
    ["unknown skill digest", { skills: [{ name: "notes", version: "1.2.0", digest: "sha256:unknown" }] }, /capability_unavailable/],
    ["wrong execution mode", { executionMode: "background" as const }, /execution mode/],
    ["nested worker", { depth: 2 as 1 }, /depth/],
  ])("rejects %s", (_name, overrides, error) => {
    expect(() => createWorkerSpawnSpec(parent(), request(overrides))).toThrow(error);
  });

  it.each(["skill_list", "skill_load", "permission_grant", "permission.grant"])(
    "rejects forbidden meta-tool %s even when the parent lists it",
    (toolId) => {
      const sourceParent = parent({
        capabilities: { ...parent().capabilities, toolIds: [toolId] },
      });
      expect(() => createWorkerSpawnSpec(sourceParent, request({ toolIds: [toolId] }))).toThrow(
        /forbidden meta-tool/,
      );
    },
  );

  it.each(Object.keys(fullBudget) as (keyof Budget)[])(
    "rejects %s above the parent's remaining budget",
    (field) => {
      const requested = request();
      requested.budget[field] = parent().remainingBudget[field] + 1;
      expect(() => createWorkerSpawnSpec(parent(), requested)).toThrow(/remaining budget/);
    },
  );

  it("accepts every budget exactly at its remaining boundary", () => {
    const remaining = parent().remainingBudget;
    const spawn = createWorkerSpawnSpec(parent(), request({ budget: structuredClone(remaining) }));
    expect(spawn.budget).toEqual(remaining);
  });

  it("rejects an inconsistent parent budget and exhausted worker capacity", () => {
    expect(() =>
      createWorkerSpawnSpec(
        parent({ remainingBudget: { ...fullBudget, maxTokens: fullBudget.maxTokens + 1 } }),
        request(),
      ),
    ).toThrow(/remaining budget/);
    expect(() =>
      createWorkerSpawnSpec(
        parent({ remainingBudget: { ...fullBudget, maxWorkers: 0 } }),
        request(),
      ),
    ).toThrow(/worker capacity/);
  });

  it("rejects malformed parent and request values through strict protocol validation", () => {
    expect(() =>
      createWorkerSpawnSpec(parent(), request({ workerId: "worker-1", extra: true } as never)),
    ).toThrow(/validation_failed/);
    expect(() =>
      createWorkerSpawnSpec(parent(), request({ outputContract: { sections: ["summary"] } as never })),
    ).toThrow(/validation_failed/);
  });
});

describe("validateWorkerResult", () => {
  function validResult(): WorkerResult {
    return {
      protocolVersion: "2.0",
      workerId: "worker-1",
      state: "succeeded",
      sections: { summary: "done", findings: "one", risks: "none", next_steps: "ship" },
      usage: { inputTokens: 10, outputTokens: 20, totalTokens: 30 },
    };
  }

  it("validates and returns a recursively frozen copy without caller aliases", () => {
    const source = validResult();
    const result = validateWorkerResult(source, outputContract);
    source.sections!.findings = "mutated";
    expect(result.sections!.findings).toBe("one");
    expect(Object.isFrozen(result)).toBe(true);
    expect(Object.isFrozen(result.sections)).toBe(true);
  });

  it.each([
    ["missing section", () => ({ ...validResult(), sections: { summary: "done", findings: "one", risks: "none" } })],
    ["extra section", () => ({ ...validResult(), sections: { ...validResult().sections!, appendix: "no" } })],
    ["wrong protocol", () => ({ ...validResult(), protocolVersion: "1.0" })],
    ["malformed failed result", () => ({ ...validResult(), state: "failed" })],
  ])("classifies %s as protocol_incompatible", (_name, build) => {
    let thrown: unknown;
    try {
      validateWorkerResult(build(), outputContract);
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toMatchObject({ code: "protocol_incompatible" });
    expect(thrown).toBeInstanceOf(Error);
    expect((thrown as Error).message).toMatch(/protocol_incompatible/);
  });

  it.each([
    ["missing", ["summary", "findings", "risks"]],
    ["extra", ["summary", "findings", "risks", "next_steps", "summary"]],
    ["duplicate", ["summary", "findings", "findings", "next_steps"]],
    ["reordered", ["findings", "summary", "risks", "next_steps"]],
  ])("rejects a %s output contract", (_name, sections) => {
    expect(() =>
      validateWorkerResult(validResult(), { sections } as unknown as WorkerOutputContract),
    ).toThrow(/output contract|validation_failed/);
  });
});
