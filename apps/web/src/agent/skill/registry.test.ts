import { describe, expect, it } from "vitest";

import type { CapabilitySnapshot, SideEffect, ToolResult, ToolSpec } from "../generated/protocol";
import validSkillMarkdown from "../generated/skills/calendar-management/SKILL.md?raw";
import usageMarkdown from "../generated/skills/calendar-management/references/usage.md?raw";
import { ToolRegistry, type ToolBinding, type ToolPolicy } from "../tool/registry";
import { parseSkillBundle, type SkillProfile } from "./profile";
import { SkillRegistry } from "./registry";

const encoder = new TextEncoder();

async function profileWith(replacements: Array<[string, string]> = []): Promise<SkillProfile> {
  let markdown = validSkillMarkdown;
  for (const [from, to] of replacements) markdown = markdown.replace(from, to);
  return parseSkillBundle({
    scope: "system",
    skillMarkdown: encoder.encode(markdown),
    supportingFiles: [{
      path: "references/usage.md",
      content: encoder.encode(usageMarkdown),
    }],
  });
}

function snapshot(overrides: Partial<CapabilitySnapshot> = {}): CapabilitySnapshot {
  return {
    runtimeVersion: "2.0.0",
    executionMode: "foreground",
    toolIds: [],
    skills: [],
    scope: { domains: [] },
    ...overrides,
  };
}

function toolSpec(id: string, requiredDomains: string[], executionTargets: ToolSpec["executionTargets"]): ToolSpec {
  return {
    id,
    description: `${id} description`,
    inputSchema: { type: "object" },
    outputSchema: { type: "object" },
    sideEffect: "read" as SideEffect,
    requiredDomains,
    executionTargets,
    approvalPolicy: "never",
    idempotent: true,
    timeoutMs: 1_000,
    resultMaxBytes: 1_024,
  };
}

function binding(spec: ToolSpec): ToolBinding {
  return { spec, invoke: async (): Promise<ToolResult> => ({ ok: true, data: {} }) };
}

const allowAllPolicy: ToolPolicy = { allow: ["*"], deny: [], approvalFor: [] };

describe("Skill registry", () => {
  it("lists descriptors without bodies and loads one body", async () => {
    // Mutation caught: eager disclosure of Markdown in list results.
    const registry = new SkillRegistry([await profileWith()]);

    expect(registry.list()).toEqual([expect.objectContaining({ name: "calendar-management", scope: "system" })]);
    expect(JSON.stringify(registry.list())).not.toContain("Read only the requested date range");
    expect(registry.load("calendar-management")?.body).toContain("Read only the requested date range");
  });

  it("filters model-disabled Skills independently from ordinary user-facing access", async () => {
    // Mutation caught: coupling model visibility to user-invocable access.
    const visible = await profileWith();
    const hidden = await profileWith([
      ["name: calendar-management", "name: hidden-calendar"],
      ["disable-model-invocation: false", "disable-model-invocation: true"],
    ]);
    const modelOnly = await profileWith([
      ["name: calendar-management", "name: model-only-calendar"],
      ["user-invocable: true", "user-invocable: false"],
    ]);
    const registry = new SkillRegistry([modelOnly, hidden, visible]);

    expect(registry.list().map(({ name }) => name)).toEqual([
      "calendar-management",
      "hidden-calendar",
      "model-only-calendar",
    ]);
    expect(registry.listForModel().map(({ name }) => name)).toEqual([
      "calendar-management",
      "model-only-calendar",
    ]);
    expect(registry.load("hidden-calendar")?.body).toContain("Read only the requested date range");
    expect(registry.loadForModel("hidden-calendar")).toBeUndefined();
  });

  it("does not turn allowed-tools into permission", async () => {
    // Mutation caught: returning manifest requests directly as active permissions.
    const registry = new SkillRegistry([await profileWith()]);
    const tools = new ToolRegistry("foreground");
    tools.register(binding(toolSpec("dayorder.calendar.read", ["calendar"], ["client", "server"])));
    tools.register(binding(toolSpec("dayorder.calendar.propose-change", ["calendar"], ["client", "server"])));

    const activation = registry.activate("calendar-management", snapshot({
      toolIds: [],
      scope: { domains: ["calendar"] },
    }), tools, allowAllPolicy);

    expect(activation.requestedToolIds).toEqual([
      "dayorder.calendar.propose-change",
      "dayorder.calendar.read",
    ]);
    expect(activation.activeToolIds).toEqual([]);
  });

  it("delegates activation to the registered-binding, Run Scope, target, and policy intersection", async () => {
    // Mutation caught: omitting one of the existing effectiveToolIDs gates.
    const registry = new SkillRegistry([await profileWith()]);
    const tools = new ToolRegistry("foreground");
    tools.register(binding(toolSpec("dayorder.calendar.read", ["calendar"], ["client", "server"])));
    tools.register(binding(toolSpec("dayorder.calendar.propose-change", ["calendar"], ["client", "server"])));

    expect(registry.activate(
      "calendar-management",
      snapshot({
        toolIds: ["dayorder.calendar.read", "dayorder.calendar.propose-change"],
        scope: { domains: ["calendar"] },
      }),
      tools,
      { allow: ["*"], deny: ["dayorder.calendar.propose-change"], approvalFor: [] },
    ).activeToolIds).toEqual(["dayorder.calendar.read"]);
  });

  it.each([
    ["minimum Runtime", [["min-runtime-version: 1.0.0", "min-runtime-version: 2.1.0"]] as Array<[string, string]>, snapshot()],
    ["foreground execution target", [["execution-target: either", "execution-target: server"]] as Array<[string, string]>, snapshot()],
    ["background execution target", [["execution-target: either", "execution-target: client"]] as Array<[string, string]>, snapshot({ executionMode: "background" })],
    ["background permission", [["background-allowed: true", "background-allowed: false"]] as Array<[string, string]>, snapshot({ executionMode: "background" })],
  ])("rejects activation incompatible with %s before Tool calculation", async (_case, replacements, run) => {
    // Mutation caught: activation proceeding despite Skill/Runtime incompatibility.
    const registry = new SkillRegistry([await profileWith(replacements)]);
    const tools = new ToolRegistry(run.executionMode);

    expect(() => registry.activate("calendar-management", run, tools, allowAllPolicy)).toThrow();
  });

  it("rejects duplicate names instead of silently overriding a Skill", async () => {
    // Mutation caught: Map.set last-wins behavior for duplicate Skill identities.
    const profile = await profileWith();
    expect(() => new SkillRegistry([profile, structuredClone(profile)])).toThrow(/duplicate/);
  });

  it("returns sorted copies without caller or internal aliases", async () => {
    // Mutation caught: storing constructor inputs or returning internal descriptor/profile arrays.
    const alpha = await profileWith([["calendar-management", "alpha-calendar"]]);
    const zeta = await profileWith([["calendar-management", "zeta-calendar"]]);
    const registry = new SkillRegistry([zeta, alpha]);
    alpha.body = "caller mutation";
    alpha.manifest.description = "caller mutation";
    alpha.supportingFiles[0].content.fill(0);

    const listed = registry.list();
    listed[0].name = "return mutation";
    const loaded = registry.load("alpha-calendar")!;
    loaded.body = "return mutation";
    loaded.manifest.description = "return mutation";
    loaded.supportingFiles[0].content.fill(0);

    expect(registry.list().map(({ name }) => name)).toEqual(["alpha-calendar", "zeta-calendar"]);
    expect(registry.load("alpha-calendar")?.body).toContain("Read only the requested date range");
    expect(registry.load("alpha-calendar")?.manifest.description).toBe("Read and propose changes to DayOrder calendar data.");
    expect(new TextDecoder().decode(registry.load("alpha-calendar")?.supportingFiles[0].content)).toContain("# Usage");
  });

  it("returns copied activation arrays and descriptors", async () => {
    // Mutation caught: aliasing activation results across calls.
    const registry = new SkillRegistry([await profileWith()]);
    const tools = new ToolRegistry("foreground");
    const first = registry.activate("calendar-management", snapshot(), tools, allowAllPolicy);
    first.skill.name = "mutated";
    first.requestedToolIds.length = 0;
    first.supportingFiles[0].path = "mutated";

    const second = registry.activate("calendar-management", snapshot(), tools, allowAllPolicy);
    expect(second.skill.name).toBe("calendar-management");
    expect(second.requestedToolIds).toEqual(["dayorder.calendar.propose-change", "dayorder.calendar.read"]);
    expect(second.supportingFiles[0].path).toBe("references/usage.md");
  });
});
