import { describe, expect, it } from "vitest";

import type { CapabilitySnapshot, SideEffect, ToolResult, ToolSpec } from "../generated/protocol";
import { effectiveToolIDs, ToolRegistry, type ToolBinding } from "./registry";

function toolSpec(
  id: string,
  sideEffect: SideEffect = "read",
  requiredDomains: string[] = [],
  executionTargets: ToolSpec["executionTargets"] = ["client"],
): ToolSpec {
  return {
    id,
    description: `${id} description`,
    inputSchema: { type: "object" },
    outputSchema: { type: "object" },
    sideEffect,
    requiredDomains,
    executionTargets,
    approvalPolicy: "never",
    idempotent: true,
    timeoutMs: 1_000,
    resultMaxBytes: 1_024,
  };
}

function fakeBinding(spec: ToolSpec): ToolBinding {
  return {
    spec,
    invoke: async (): Promise<ToolResult> => ({ ok: true, data: {} }),
  };
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

describe("foreground tool registry", () => {
  it("prevents a requested ID from bypassing missing bindings and run scope", () => {
    const registry = new ToolRegistry("foreground");
    registry.register(fakeBinding(toolSpec("dayorder.notes.search", "read", ["notes"], ["client", "server"])));
    registry.register(fakeBinding(toolSpec("device.calendar.read", "read", ["calendar"], ["client"])));

    expect(effectiveToolIDs(
      ["missing.tool", "device.calendar.read", "dayorder.notes.search"],
      registry,
      snapshot({
        toolIds: ["device.calendar.read", "dayorder.notes.search"],
        scope: { domains: ["notes"] },
      }),
      { allow: ["*"], deny: [], approvalFor: [] },
    )).toEqual(["dayorder.notes.search"]);
  });

  it("prevents a server-only binding from running in foreground mode", () => {
    const registry = new ToolRegistry("foreground");
    registry.register(fakeBinding(toolSpec("dayorder.server.only", "read", [], ["server"])));
    registry.register(fakeBinding(toolSpec("dayorder.client.only", "read", [], ["client"])));

    expect(effectiveToolIDs(
      ["dayorder.client.only", "dayorder.server.only"],
      registry,
      snapshot({ toolIds: ["dayorder.client.only", "dayorder.server.only"] }),
      { allow: ["*"], deny: [], approvalFor: [] },
    )).toEqual(["dayorder.client.only"]);
  });

  it("prevents a client-only binding from running in background mode", () => {
    const registry = new ToolRegistry("foreground");
    registry.register(fakeBinding(toolSpec("dayorder.client.only", "read", [], ["client"])));
    registry.register(fakeBinding(toolSpec("dayorder.server.only", "read", [], ["server"])));

    expect(effectiveToolIDs(
      ["dayorder.client.only", "dayorder.server.only"],
      registry,
      snapshot({
        executionMode: "background",
        toolIds: ["dayorder.client.only", "dayorder.server.only"],
      }),
      { allow: ["*"], deny: [], approvalFor: [] },
    )).toEqual(["dayorder.server.only"]);
  });

  it("prevents an unrequested registered tool from being granted by policy", () => {
    const registry = new ToolRegistry("foreground");
    registry.register(fakeBinding(toolSpec("dayorder.requested")));
    registry.register(fakeBinding(toolSpec("dayorder.not_requested")));

    expect(effectiveToolIDs(
      ["dayorder.requested"],
      registry,
      snapshot({ toolIds: ["dayorder.requested", "dayorder.not_requested"] }),
      { allow: ["*"], deny: [], approvalFor: [] },
    )).toEqual(["dayorder.requested"]);
  });

  it("gives an explicit policy deny precedence over wildcard allow", () => {
    const registry = new ToolRegistry("foreground");
    registry.register(fakeBinding(toolSpec("dayorder.allowed")));
    registry.register(fakeBinding(toolSpec("dayorder.denied")));

    expect(effectiveToolIDs(
      ["dayorder.denied", "dayorder.allowed"],
      registry,
      snapshot({ toolIds: ["dayorder.allowed", "dayorder.denied"] }),
      { allow: ["*"], deny: ["dayorder.denied"], approvalFor: ["irreversible_write"] },
    )).toEqual(["dayorder.allowed"]);
  });

  it("never widens the frozen Run Tool grant", () => {
    // Mutation caught: treating a requested ID as a permission grant.
    const registry = new ToolRegistry("foreground");
    registry.register(fakeBinding(toolSpec("dayorder.calendar.read", "read", ["calendar"])));
    registry.register(fakeBinding(toolSpec("dayorder.calendar.write", "reversible_write", ["calendar"])));

    expect(effectiveToolIDs(
      ["dayorder.calendar.write", "dayorder.calendar.read"],
      registry,
      snapshot({
        toolIds: ["dayorder.calendar.read"],
        scope: { domains: ["calendar"] },
      }),
      { allow: ["*"], deny: [], approvalFor: [] },
    )).toEqual(["dayorder.calendar.read"]);
  });

  it("prevents duplicate IDs and schema-invalid specs from entering the registry", () => {
    const registry = new ToolRegistry("foreground");
    registry.register(fakeBinding(toolSpec("dayorder.duplicate")));

    expect(() => registry.register(fakeBinding(toolSpec("dayorder.duplicate")))).toThrow(/duplicate tool ID/);
    expect(() => registry.register(fakeBinding({
      ...toolSpec("dayorder.invalid"),
      timeoutMs: 0,
    }))).toThrow(/validation_failed/);
    expect(registry.specs().map(({ id }) => id)).toEqual(["dayorder.duplicate"]);
  });

  it("prevents caller mutations of bindings and returned lists from changing registered tools", () => {
    const registry = new ToolRegistry("foreground");
    const binding = fakeBinding(toolSpec("dayorder.alpha", "read", ["notes"]));
    registry.register(binding);

    binding.spec.id = "dayorder.mutated";
    binding.spec.requiredDomains.push("calendar");
    const specs = registry.specs();
    specs[0].id = "dayorder.changed";
    specs[0].requiredDomains.push("calendar");
    const resolved = registry.resolve("dayorder.alpha")!;
    resolved.spec.requiredDomains.push("calendar");

    expect(registry.specs()).toEqual([toolSpec("dayorder.alpha", "read", ["notes"])]);
    expect(registry.resolve("dayorder.alpha")?.spec).toEqual(toolSpec("dayorder.alpha", "read", ["notes"]));
    expect(registry.resolve("dayorder.mutated")).toBeUndefined();
  });

  it("prevents request duplicates and registration order from changing sorted effective IDs", () => {
    const registry = new ToolRegistry("foreground");
    registry.register(fakeBinding(toolSpec("dayorder.zeta")));
    registry.register(fakeBinding(toolSpec("dayorder.alpha")));

    expect(registry.specs().map(({ id }) => id)).toEqual(["dayorder.alpha", "dayorder.zeta"]);
    expect(effectiveToolIDs(
      ["dayorder.zeta", "dayorder.alpha", "dayorder.zeta"],
      registry,
      snapshot({ toolIds: ["dayorder.alpha", "dayorder.zeta"] }),
      { allow: ["dayorder.zeta", "dayorder.alpha"], deny: [], approvalFor: [] },
    )).toEqual(["dayorder.alpha", "dayorder.zeta"]);
  });
});
