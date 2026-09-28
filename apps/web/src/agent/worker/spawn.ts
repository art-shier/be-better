import type {
  Budget,
  CapabilitySnapshot,
  ExecutionMode,
  SkillRef,
  WorkerOutputContract,
  WorkerResult,
  WorkerSkillInstructions,
  WorkerSpawnSpec,
  WorkerTask,
} from "../generated/protocol";
import { ProtocolValidationError, validateProtocol } from "../protocol/validate";

export interface WorkerParentContext {
  runId: string;
  depth: number;
  executionMode: ExecutionMode;
  capabilities: CapabilitySnapshot;
  budget: Budget;
  remainingBudget: Budget;
  loadedSkillInstructions: WorkerSkillInstructions[];
}

export interface WorkerSpawnRequest {
  workerId: string;
  depth: number;
  executionMode: ExecutionMode;
  task: WorkerTask;
  toolIds: string[];
  skills: SkillRef[];
  budget: Budget;
  outputContract: WorkerOutputContract;
}

export class WorkerResultProtocolError extends Error {
  readonly code = "protocol_incompatible";

  constructor(cause: ProtocolValidationError) {
    super(`protocol_incompatible: ${cause.message.replace(/^validation_failed:\s*/, "")}`);
    this.name = "WorkerResultProtocolError";
  }
}

const outputSections = ["summary", "findings", "risks", "next_steps"] as const;
const forbiddenMetaTools = new Set(["skill_list", "skill_load", "permission_grant", "permission.grant"]);
const budgetFields = [
  "maxSteps",
  "maxTokens",
  "maxDurationMs",
  "maxWorkers",
  "maxConcurrency",
  "maxRepeatedToolCalls",
] as const satisfies readonly (keyof Budget)[];

function fail(message: string): never {
  throw new ProtocolValidationError(message);
}

function assertExactKeys(value: object, keys: readonly string[], name: string): void {
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  if (actual.length !== expected.length || actual.some((key, index) => key !== expected[index])) {
    fail(`validation_failed: ${name} contains unknown or missing properties`);
  }
}

function skillKey(skill: SkillRef): string {
  return `${skill.name}\u0000${skill.version}\u0000${skill.digest}`;
}

function compareSkills(left: SkillRef, right: SkillRef): number {
  return skillKey(left).localeCompare(skillKey(right), "en");
}

function assertUnique<T>(values: readonly T[], key: (value: T) => string, name: string): void {
  const seen = new Set<string>();
  for (const value of values) {
    const identity = key(value);
    if (seen.has(identity)) fail(`validation_failed: duplicate ${name} ${identity}`);
    seen.add(identity);
  }
}

function validateOutputContract(contract: WorkerOutputContract): void {
  if (!contract || typeof contract !== "object" || Array.isArray(contract)) {
    fail("validation_failed: invalid output contract");
  }
  assertExactKeys(contract, ["sections"], "output contract");
  if (
    !Array.isArray(contract.sections) ||
    contract.sections.length !== outputSections.length ||
    contract.sections.some((section, index) => section !== outputSections[index])
  ) {
    fail("validation_failed: output contract must use summary, findings, risks, next_steps in order");
  }
}

function validateRemainingBudget(parent: WorkerParentContext, request: WorkerSpawnRequest): void {
  if (parent.remainingBudget.maxWorkers < 1 || parent.remainingBudget.maxConcurrency < 1) {
    fail("validation_failed: parent worker capacity is exhausted");
  }
  for (const field of budgetFields) {
    const total = parent.budget[field];
    const remaining = parent.remainingBudget[field];
    if (!Number.isInteger(remaining) || remaining < 0 || remaining > total) {
      fail(`validation_failed: parent remaining budget ${field} is invalid`);
    }
    if (request.budget[field] > remaining) {
      fail(`validation_failed: requested ${field} exceeds parent remaining budget`);
    }
  }
}

function deepFreeze<T>(value: T): T {
  if ((typeof value !== "object" && typeof value !== "function") || value === null || Object.isFrozen(value)) {
    return value;
  }
  if (value instanceof Map) {
    for (const [key, entry] of value) {
      deepFreeze(key);
      deepFreeze(entry);
    }
    Object.defineProperties(value, {
      set: { value: () => fail("validation_failed: frozen map mutation"), configurable: false },
      delete: { value: () => fail("validation_failed: frozen map mutation"), configurable: false },
      clear: { value: () => fail("validation_failed: frozen map mutation"), configurable: false },
    });
  } else if (value instanceof Set) {
    for (const entry of value) deepFreeze(entry);
    Object.defineProperties(value, {
      add: { value: () => fail("validation_failed: frozen set mutation"), configurable: false },
      delete: { value: () => fail("validation_failed: frozen set mutation"), configurable: false },
      clear: { value: () => fail("validation_failed: frozen set mutation"), configurable: false },
    });
  } else {
    for (const key of Reflect.ownKeys(value)) {
      deepFreeze((value as Record<PropertyKey, unknown>)[key]);
    }
  }
  return Object.freeze(value);
}

function frozenCopy<T>(value: T): T {
  return deepFreeze(structuredClone(value));
}

export function createWorkerSpawnSpec(
  parent: WorkerParentContext,
  request: WorkerSpawnRequest,
): Readonly<WorkerSpawnSpec> {
  assertExactKeys(
    request,
    ["workerId", "depth", "executionMode", "task", "toolIds", "skills", "budget", "outputContract"],
    "worker spawn request",
  );
  validateProtocol<CapabilitySnapshot>("CapabilitySnapshot", parent.capabilities);
  validateProtocol<Budget>("Budget", parent.budget);
  if (parent.executionMode !== parent.capabilities.executionMode) {
    fail("validation_failed: parent execution mode does not match its capability snapshot");
  }
  if (parent.depth !== 0 || request.depth !== 1) {
    fail("validation_failed: worker depth exceeds the maximum nesting depth of one");
  }
  if (request.executionMode !== parent.executionMode) {
    fail("validation_failed: worker execution mode must be inherited from parent");
  }
  validateOutputContract(request.outputContract);
  validateRemainingBudget(parent, request);

  assertUnique(request.toolIds, (toolId) => toolId, "tool");
  assertUnique(request.skills, skillKey, "skill");
  const availableTools = new Set(parent.capabilities.toolIds);
  for (const toolId of request.toolIds) {
    if (forbiddenMetaTools.has(toolId)) {
      fail(`validation_failed: forbidden meta-tool ${toolId} cannot be delegated`);
    }
    if (!availableTools.has(toolId)) {
      fail(`capability_unavailable: tool ${toolId} is not available from the parent`);
    }
  }

  const availableSkills = new Map(parent.capabilities.skills.map((skill) => [skillKey(skill), skill]));
  const loadedSkills = new Map(
    parent.loadedSkillInstructions.map((instructions) => [skillKey(instructions.skill), instructions]),
  );
  const selectedInstructions: WorkerSkillInstructions[] = [];
  for (const skill of request.skills) {
    const key = skillKey(skill);
    if (!availableSkills.has(key)) {
      fail(`capability_unavailable: skill ${skill.name}@${skill.version} with digest ${skill.digest} is unavailable`);
    }
    const loaded = loadedSkills.get(key);
    if (!loaded) {
      fail(`capability_unavailable: loaded instructions for skill ${skill.name}@${skill.version} are unavailable`);
    }
    selectedInstructions.push(loaded);
  }

  const skills = structuredClone(request.skills).sort(compareSkills);
  const spec: WorkerSpawnSpec = {
    protocolVersion: "2.0",
    parentRunId: parent.runId,
    workerId: request.workerId,
    depth: 1,
    executionMode: parent.executionMode,
    task: structuredClone(request.task),
    capabilities: {
      runtimeVersion: parent.capabilities.runtimeVersion,
      executionMode: parent.executionMode,
      toolIds: [...request.toolIds].sort((left, right) => left.localeCompare(right, "en")),
      skills,
      scope: {
        ...structuredClone(parent.capabilities.scope),
        domains: [...parent.capabilities.scope.domains].sort((left, right) => left.localeCompare(right, "en")),
        ...(parent.capabilities.scope.entityIds
          ? { entityIds: [...parent.capabilities.scope.entityIds].sort((left, right) => left.localeCompare(right, "en")) }
          : {}),
      },
    },
    budget: structuredClone(request.budget),
    skillInstructions: structuredClone(selectedInstructions).sort((left, right) =>
      compareSkills(left.skill, right.skill),
    ),
    outputContract: structuredClone(request.outputContract),
  };

  validateProtocol<WorkerSpawnSpec>("WorkerSpawnSpec", spec);
  return deepFreeze(spec);
}

export function validateWorkerResult(
  result: unknown,
  contract: WorkerOutputContract,
): Readonly<WorkerResult> {
  validateOutputContract(contract);
  let copy: WorkerResult;
  try {
    copy = validateProtocol<WorkerResult>("WorkerResult", structuredClone(result));
  } catch (error) {
    if (error instanceof ProtocolValidationError) throw new WorkerResultProtocolError(error);
    throw error;
  }
  return frozenCopy(copy);
}
