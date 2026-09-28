import type { AgentError, CapabilitySnapshot, ToolResult, ToolSpec } from "../generated/protocol";
import { validateProtocol } from "../protocol/validate";
import type { ToolBinding, ToolPolicy, ToolRegistry } from "../tool/registry";
import type { SkillRegistry } from "./registry";

const resultLimit = 256 * 1024;
const skillNamePattern = "^[a-z0-9][a-z0-9-]{0,63}$";
const semVerPattern = "^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\\+[0-9A-Za-z-]+(?:\\.[0-9A-Za-z-]+)*)?$";
const toolIDPattern = "^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$";

const skillRefSchema = {
  type: "object",
  additionalProperties: false,
  properties: {
    name: { type: "string", pattern: skillNamePattern },
    version: { type: "string", pattern: semVerPattern },
    digest: { type: "string" },
  },
  required: ["name", "version", "digest"],
};

const supportingFileDescriptorSchema = {
  type: "object",
  additionalProperties: false,
  properties: {
    path: { type: "string" },
    kind: { enum: ["reference", "asset", "schema"] },
    size: { type: "integer", minimum: 0 },
  },
  required: ["path", "kind", "size"],
};

const skillDescriptorSchema = {
  type: "object",
  additionalProperties: false,
  properties: {
    name: { type: "string", pattern: skillNamePattern },
    version: { type: "string", pattern: semVerPattern },
    digest: { type: "string" },
    description: { type: "string" },
    scope: { enum: ["system", "user", "device"] },
    executionTarget: { enum: ["client", "server", "either"] },
    backgroundAllowed: { type: "boolean" },
    userInvocable: { type: "boolean" },
    disableModelInvocation: { type: "boolean" },
    riskLevel: { enum: ["low", "medium", "high", "critical"] },
  },
  required: [
    "name",
    "version",
    "digest",
    "description",
    "scope",
    "executionTarget",
    "backgroundAllowed",
    "userInvocable",
    "disableModelInvocation",
    "riskLevel",
  ],
};

const skillActivationSchema = {
  type: "object",
  additionalProperties: false,
  properties: {
    skill: skillRefSchema,
    instructions: { type: "string" },
    supportingFiles: { type: "array", items: supportingFileDescriptorSchema },
    requestedToolIds: { type: "array", items: { type: "string", pattern: toolIDPattern } },
    activeToolIds: { type: "array", items: { type: "string", pattern: toolIDPattern } },
  },
  required: ["skill", "instructions", "supportingFiles", "requestedToolIds", "activeToolIds"],
};

const skillListSpec: ToolSpec = {
  id: "skill_list",
  description: "List Skills available to the model without loading their instructions.",
  inputSchema: { type: "object", properties: {}, additionalProperties: false },
  outputSchema: {
    type: "object",
    properties: { skills: { type: "array", items: skillDescriptorSchema } },
    required: ["skills"],
    additionalProperties: false,
  },
  sideEffect: "read",
  requiredDomains: [],
  executionTargets: ["client", "server"],
  approvalPolicy: "never",
  idempotent: true,
  timeoutMs: 5_000,
  resultMaxBytes: resultLimit,
};

const skillLoadSpec: ToolSpec = {
  id: "skill_load",
  description: "Load one model-visible Skill and calculate its effective Tool access.",
  inputSchema: {
    type: "object",
    properties: { name: { type: "string" } },
    required: ["name"],
    additionalProperties: false,
  },
  outputSchema: {
    type: "object",
    properties: { activation: skillActivationSchema },
    required: ["activation"],
    additionalProperties: false,
  },
  sideEffect: "read",
  requiredDomains: [],
  executionTargets: ["client", "server"],
  approvalPolicy: "never",
  idempotent: true,
  timeoutMs: 5_000,
  resultMaxBytes: resultLimit,
};

function success(data: Record<string, unknown>): ToolResult {
  return validateProtocol<ToolResult>("ToolResult", { ok: true, data: structuredClone(data) });
}

function failure(code: AgentError["code"], message: string): ToolResult {
  return validateProtocol<ToolResult>("ToolResult", {
    ok: false,
    error: { code, message, retryable: false },
  });
}

function isPlainObject(input: unknown): input is Record<string, unknown> {
  if (input === null || typeof input !== "object" || Array.isArray(input)) return false;
  const prototype = Object.getPrototypeOf(input);
  return prototype === Object.prototype || prototype === null;
}

function exactKeys(input: Record<string, unknown>, keys: string[]): boolean {
  const actual = Object.keys(input).sort();
  const expected = [...keys].sort();
  return actual.length === expected.length && actual.every((key, index) => key === expected[index]);
}

export function createSkillToolBindings(
  skills: SkillRegistry,
  snapshot: CapabilitySnapshot,
  tools: ToolRegistry,
  policy: ToolPolicy,
): ToolBinding[] {
  const list: ToolBinding = {
    spec: structuredClone(skillListSpec),
    invoke: async (input) => isPlainObject(input) && exactKeys(input, [])
      ? success({ skills: skills.listForModel() })
      : failure("validation_failed", "skill_list input must be an empty object"),
  };
  const load: ToolBinding = {
    spec: structuredClone(skillLoadSpec),
    invoke: async (input) => {
      if (!isPlainObject(input) || !exactKeys(input, ["name"]) || typeof input.name !== "string" || input.name.length === 0) {
        return failure("validation_failed", "skill_load input must contain only a non-empty string name");
      }
      if (!skills.loadForModel(input.name)) {
        return failure("capability_unavailable", `Skill is not available to the model: ${input.name}`);
      }
      try {
        return success({ activation: skills.activate(input.name, snapshot, tools, policy) });
      } catch (error) {
        const message = error instanceof Error ? error.message : "Skill activation failed";
        return failure("capability_unavailable", message);
      }
    },
  };
  return [list, load];
}
