import calendarReadAsset from "../generated/builtin/tools/calendar-read.json";
import calendarOverviewMarkdown from "../generated/builtin/skills/calendar-overview/SKILL.md?raw";

import type { ToolSpec } from "../generated/protocol";
import type { SkillBundleInput } from "../skill/profile";

const encoder = new TextEncoder();

export function calendarReadSpec(): ToolSpec {
  return structuredClone(calendarReadAsset) as unknown as ToolSpec;
}

export function calendarOverviewBundle(): SkillBundleInput {
  return {
    scope: "system",
    skillMarkdown: encoder.encode(calendarOverviewMarkdown),
    supportingFiles: [],
  };
}
