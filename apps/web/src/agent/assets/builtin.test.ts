import { describe, expect, it } from "vitest";

import { parseSkillBundle } from "../skill/profile";
import { calendarOverviewBundle, calendarReadSpec } from "./builtin";

describe("builtin agent assets", () => {
  it("exposes the readonly calendar Tool contract", () => {
    const spec = calendarReadSpec();

    expect(spec).toMatchObject({
      id: "dayorder.calendar.read",
      sideEffect: "read",
      requiredDomains: ["calendar"],
      executionTargets: ["client", "server"],
      approvalPolicy: "never",
      idempotent: true,
      timeoutMs: 10_000,
      resultMaxBytes: 65_536,
    });
  });

  it("parses the system calendar overview Skill against the real profile parser", async () => {
    const spec = calendarReadSpec();
    const profile = await parseSkillBundle(calendarOverviewBundle());

    expect(profile.manifest).toMatchObject({
      name: "calendar-overview",
      version: "1.0.0",
      "allowed-tools": [spec.id],
      "execution-target": "either",
      "background-allowed": true,
      "min-runtime-version": "2.0.0",
      "risk-level": "low",
    });
    expect(profile.descriptor.scope).toBe("system");
    expect(profile.supportingFiles).toEqual([]);
  });

  it("returns isolated copies of every mutable builtin asset", () => {
    const firstSpec = calendarReadSpec();
    const firstBundle = calendarOverviewBundle();
    firstSpec.executionTargets[0] = "server";
    firstSpec.inputSchema.type = "array";
    firstBundle.skillMarkdown.fill(0);
    firstBundle.supportingFiles.push({ path: "references/injected.md", content: new Uint8Array() });

    expect(calendarReadSpec().executionTargets).toEqual(["client", "server"]);
    expect(calendarReadSpec().inputSchema.type).toBe("object");
    expect(new TextDecoder().decode(calendarOverviewBundle().skillMarkdown)).toContain(
      "name: calendar-overview",
    );
    expect(calendarOverviewBundle().supportingFiles).toEqual([]);
  });
});
