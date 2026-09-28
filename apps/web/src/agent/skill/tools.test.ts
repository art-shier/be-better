import { describe, expect, it } from "vitest";
import Ajv2020 from "ajv/dist/2020.js";

import type { CapabilitySnapshot, ToolResult, ToolSpec } from "../generated/protocol";
import validSkillMarkdown from "../generated/skills/calendar-management/SKILL.md?raw";
import usageMarkdown from "../generated/skills/calendar-management/references/usage.md?raw";
import { ToolRegistry, type ToolBinding } from "../tool/registry";
import { parseSkillBundle } from "./profile";
import { SkillRegistry } from "./registry";
import { createSkillToolBindings } from "./tools";

const context = { runId: "run-1", callId: "call-1", signal: new AbortController().signal };

async function skillRegistry(disableModelInvocation = false): Promise<SkillRegistry> {
  const markdown = validSkillMarkdown.replace(
    "disable-model-invocation: false",
    `disable-model-invocation: ${disableModelInvocation}`,
  );
  return new SkillRegistry([await parseSkillBundle({
    scope: "system",
    skillMarkdown: new TextEncoder().encode(markdown),
    supportingFiles: [{
      path: "references/usage.md",
      content: new TextEncoder().encode(usageMarkdown),
    }],
  })]);
}

function snapshot(): CapabilitySnapshot {
  return {
    runtimeVersion: "2.0.0",
    executionMode: "foreground",
    toolIds: [],
    skills: [],
    scope: { domains: [] },
  };
}

function calendarBinding(): ToolBinding {
  const spec: ToolSpec = {
    id: "dayorder.calendar.read",
    description: "Read calendar",
    inputSchema: { type: "object" },
    outputSchema: { type: "object" },
    sideEffect: "read",
    requiredDomains: ["calendar"],
    executionTargets: ["client", "server"],
    approvalPolicy: "never",
    idempotent: true,
    timeoutMs: 1_000,
    resultMaxBytes: 1_024,
  };
  return { spec, invoke: async (): Promise<ToolResult> => ({ ok: true, data: {} }) };
}

async function bindings(disableModelInvocation = false): Promise<ToolBinding[]> {
  const tools = new ToolRegistry("foreground");
  tools.register(calendarBinding());
  return createSkillToolBindings(
    await skillRegistry(disableModelInvocation),
    snapshot(),
    tools,
    { allow: ["*"], deny: [], approvalFor: [] },
  );
}

describe("Skill meta-tools", () => {
  it("publishes the exact read-only side-effect and resource metadata", async () => {
    // Mutation caught: a meta-tool accidentally becoming writable, approvable, or unbounded.
    const specs = (await bindings()).map(({ spec }) => spec);

    expect(specs.map(({ id }) => id)).toEqual(["skill_list", "skill_load"]);
    for (const spec of specs) {
      expect(spec).toMatchObject({
        sideEffect: "read",
        requiredDomains: [],
        executionTargets: ["client", "server"],
        approvalPolicy: "never",
        idempotent: true,
        timeoutMs: 5_000,
        resultMaxBytes: 256 * 1024,
      });
    }
    expect(specs[0].inputSchema).toEqual({ type: "object", properties: {}, additionalProperties: false });
    expect(specs[0].outputSchema).toMatchObject({
      type: "object",
      required: ["skills"],
      additionalProperties: false,
    });
    expect(specs[1].inputSchema).toEqual({
      type: "object",
      properties: { name: { type: "string" } },
      required: ["name"],
      additionalProperties: false,
    });
    expect(specs[1].outputSchema).toMatchObject({
      type: "object",
      required: ["activation"],
      additionalProperties: false,
    });
  });

  it("declares complete closed SkillDescriptor and SkillActivation output schemas", async () => {
    // Mutation caught: replacing canonical nested wire shapes with unrestricted objects.
    const specs = (await bindings()).map(({ spec }) => spec);
    const ajv = new Ajv2020({ strict: true });
    const validateList = ajv.compile(specs[0].outputSchema);
    const validateLoad = ajv.compile(specs[1].outputSchema);
    const descriptor = {
      name: "calendar-management",
      version: "1.0.0",
      digest: "f2c538af41506502037bc4a1d83e4abb220b2ed1bb8207d461793ef257dee739",
      description: "Read and propose changes to DayOrder calendar data.",
      scope: "system",
      executionTarget: "either",
      backgroundAllowed: true,
      userInvocable: true,
      disableModelInvocation: false,
      riskLevel: "medium",
    };
    const activation = {
      skill: {
        name: "calendar-management",
        version: "1.0.0",
        digest: "f2c538af41506502037bc4a1d83e4abb220b2ed1bb8207d461793ef257dee739",
      },
      instructions: "# Calendar Management\n",
      supportingFiles: [{ path: "references/usage.md", kind: "reference", size: 74 }],
      requestedToolIds: ["dayorder.calendar.read"],
      activeToolIds: [],
    };

    expect(validateList({ skills: [descriptor] })).toBe(true);
    expect(validateLoad({ activation })).toBe(true);
    expect(validateList({ skills: [{ ...descriptor, riskLevel: undefined }] })).toBe(false);
    expect(validateList({ skills: [{ ...descriptor, body: "must stay undisclosed" }] })).toBe(false);
    expect(validateLoad({ activation: { ...activation, activeToolIds: undefined } })).toBe(false);
    expect(validateLoad({
      activation: {
        ...activation,
        supportingFiles: [{ ...activation.supportingFiles[0], content: "must stay internal" }],
      },
    })).toBe(false);
    expect(validateLoad({ activation: { ...activation, skill: { name: "calendar-management", version: "1.0.0" } } })).toBe(false);
  });

  it("lists only model-visible descriptors without progressively loaded instructions", async () => {
    // Mutation caught: using ordinary list() or leaking SkillProfile bodies from skill_list.
    const visible = await bindings();
    const list = visible.find(({ spec }) => spec.id === "skill_list")!;
    const result = await list.invoke({}, context);

    expect(result.ok).toBe(true);
    expect(result.data?.skills).toEqual([expect.objectContaining({ name: "calendar-management" })]);
    expect(JSON.stringify(result)).not.toContain("Read only the requested date range");

    const hidden = await bindings(true);
    expect((await hidden.find(({ spec }) => spec.id === "skill_list")!.invoke({}, context)).data).toEqual({ skills: [] });
  });

  it("loads model-visible instructions while preserving the Tool authorization intersection", async () => {
    // Mutation caught: skill_load granting every manifest allowed-tool.
    const load = (await bindings()).find(({ spec }) => spec.id === "skill_load")!;
    const result = await load.invoke({ name: "calendar-management" }, context);

    expect(result.ok).toBe(true);
    expect(result.data?.activation).toEqual(expect.objectContaining({
      instructions: expect.stringContaining("Read only the requested date range"),
      requestedToolIds: ["dayorder.calendar.propose-change", "dayorder.calendar.read"],
      activeToolIds: [],
    }));
    expect(JSON.stringify(result)).not.toContain("permissionGrant");
  });

  it("rejects model-disabled Skill loading even when ordinary user-facing loading remains possible", async () => {
    // Mutation caught: using load() rather than loadForModel() in skill_load.
    const load = (await bindings(true)).find(({ spec }) => spec.id === "skill_load")!;
    const result = await load.invoke({ name: "calendar-management" }, context);

    expect(result).toEqual({
      ok: false,
      error: {
        code: "capability_unavailable",
        message: "Skill is not available to the model: calendar-management",
        retryable: false,
      },
    });
  });

  it.each([
    ["skill_list", { unexpected: true }],
    ["skill_load", {}],
    ["skill_load", { name: "calendar-management", unexpected: true }],
    ["skill_load", { name: 4 }],
  ])("returns validation_failed when %s receives malformed contract input", async (id, input) => {
    // Mutation caught: trusting model Tool input without enforcing the fixed shape.
    const binding = (await bindings()).find(({ spec }) => spec.id === id)!;
    const result = await binding.invoke(input, context);
    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe("validation_failed");
  });

  it.each([
    ["skill_list", null],
    ["skill_list", []],
    ["skill_list", "not-an-object"],
    ["skill_list", 4],
    ["skill_list", false],
    ["skill_load", null],
    ["skill_load", []],
    ["skill_load", "not-an-object"],
    ["skill_load", 4],
    ["skill_load", false],
  ])("returns a stable validation_failed result when %s receives non-object input %j", async (id, input) => {
    // Mutation caught: Object.keys accepting primitives/arrays or throwing on null.
    const binding = (await bindings()).find(({ spec }) => spec.id === id)!;
    const result = await binding.invoke(input as unknown as Record<string, unknown>, context);

    expect(result).toEqual({
      ok: false,
      error: {
        code: "validation_failed",
        message: id === "skill_list"
          ? "skill_list input must be an empty object"
          : "skill_load input must contain only a non-empty string name",
        retryable: false,
      },
    });
  });

  it("returns copied data without aliases between invocations", async () => {
    // Mutation caught: exposing registry-owned descriptor/activation arrays to ToolResult callers.
    const meta = await bindings();
    const list = meta.find(({ spec }) => spec.id === "skill_list")!;
    const load = meta.find(({ spec }) => spec.id === "skill_load")!;
    const firstList = await list.invoke({}, context);
    ((firstList.data?.skills as Array<{ name: string }>)[0]).name = "mutated";
    const firstLoad = await load.invoke({ name: "calendar-management" }, context);
    ((firstLoad.data?.activation as { requestedToolIds: string[] }).requestedToolIds).length = 0;

    expect((await list.invoke({}, context)).data?.skills).toEqual([expect.objectContaining({ name: "calendar-management" })]);
    expect((await load.invoke({ name: "calendar-management" }, context)).data?.activation).toEqual(
      expect.objectContaining({ requestedToolIds: ["dayorder.calendar.propose-change", "dayorder.calendar.read"] }),
    );
  });
});
