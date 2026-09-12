import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, mkdtempSync, mkdirSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import test from "node:test";
import Ajv2020 from "ajv/dist/2020.js";

import { applyAgentSchemaPolicy } from "../apps/web/src/agent/protocol/schema-policy.mjs";
import { copySkillFixtures } from "./generate-agent-contracts.mjs";

const root = resolve(import.meta.dirname, "..");
const at = (path) => resolve(root, path);

function createContractValidator() {
  return applyAgentSchemaPolicy(new Ajv2020({ strict: true }));
}

test("Node contract validation enforces the shared schema policy", () => {
  // Mutation caught: contract validation diverging from Web runtime formats or maxUtf8Bytes behavior.
  const validator = createContractValidator();
  const validate = validator.compile({
    type: "object",
    properties: {
      id: { type: "string", format: "uuid" },
      at: { type: "string", format: "date-time" },
      cursor: { type: "string", maxUtf8Bytes: 4_096 },
    },
    required: ["id", "at", "cursor"],
  });
  const valid = {
    id: "550e8400-e29b-41d4-a716-446655440000",
    at: "2026-09-05T00:00:00Z",
    cursor: "日".repeat(1_365),
  };

  assert.equal(validate(valid), true);
  assert.equal(validate({ ...valid, id: "not-a-uuid" }), false);
  assert.equal(validate({ ...valid, at: "2026-02-30T00:00:00Z" }), false);
  assert.equal(validate({ ...valid, cursor: "日".repeat(1_366) }), false);
  assert.throws(() => validator.compile({ type: "string", maxUtf8Bytes: -1 }));
});

test("agent protocol v2 is canonical while v1 remains historical", () => {
  const schemaPath = at("contracts/agent/v2/protocol.schema.json");
  assert.equal(existsSync(schemaPath), true);
  assert.equal(existsSync(at("contracts/agent/v1/protocol.schema.json")), true);
  const schema = JSON.parse(readFileSync(schemaPath, "utf8"));
  assert.equal(schema.$schema, "https://json-schema.org/draft/2020-12/schema");
  assert.equal(schema.$id, "https://dayorder.local/schemas/agent/v2/protocol.schema.json");
  for (const name of ["RuntimeState", "RuntimeInput", "RuntimeTransition", "ProviderEnvelope", "ToolSpec", "SkillManifest", "WorkerSpawnSpec", "CalendarReadInput", "CalendarReadData", "ReadonlyRunStart", "ReadonlyRunView", "ReadonlyRunFinish", "CalendarReadRequest"]) assert.ok(schema.$defs[name]);
  assert.equal(existsSync(at("apps/web/src/agent/generated/protocol.ts")), true);
  assert.equal(existsSync(at("apps/api/internal/agentprotocol/generated_types.go")), true);
});

test("agent protocol rejects leading-zero SemVer prerelease identifiers", () => {
  const schema = JSON.parse(readFileSync(at("contracts/agent/v2/protocol.schema.json"), "utf8"));
  const ajv = createContractValidator();
  ajv.addSchema(schema);
  const validate = ajv.getSchema(`${schema.$id}#/$defs/SemVer`);
  assert.ok(validate);
  assert.equal(validate("1.0.0-01"), false);
  assert.equal(validate("1.0.0-1"), true);
  assert.equal(validate("1.0.0-alpha.01"), false);
  assert.equal(validate("1.0.0-alpha.1"), true);
});

test("generated DTOs preserve the ordered worker output sections", () => {
  const typescript = readFileSync(at("apps/web/src/agent/generated/protocol.ts"), "utf8").replace(/\s+/g, " ");
  const go = readFileSync(at("apps/api/internal/agentprotocol/generated_types.go"), "utf8");
  assert.match(typescript, /sections: \["summary", "findings", "risks", "next_steps"\];/);
  assert.match(go, /Sections \[4\]WorkerOutputContractSectionsElem `json:"sections"`/);
});

test("generated Web Skill fixtures exactly mirror the canonical files", () => {
  const relativePaths = [
    "calendar-management/SKILL.md",
    "calendar-management/references/usage.md",
    "invalid-script/SKILL.md",
    "invalid-script/scripts/run.js",
  ];
  for (const relativePath of relativePaths) {
    assert.deepEqual(
      readFileSync(at(`apps/web/src/agent/generated/skills/${relativePath}`)),
      readFileSync(at(`contracts/agent/fixtures/skills/${relativePath}`)),
    );
  }
});

test("generated builtin assets exactly mirror their canonical UTF-8 bytes on both hosts", () => {
  const assets = [
    ["skills/calendar-overview/SKILL.md", "skills/calendar-overview/SKILL.md"],
    ["contracts/agent/tools/calendar-read.json", "tools/calendar-read.json"],
  ];
  for (const [source, generated] of assets) {
    const canonical = readFileSync(at(source));
    const digest = createHash("sha256").update(canonical).digest("hex");
    for (const destination of [
      `apps/web/src/agent/generated/builtin/${generated}`,
      `apps/api/internal/agentassets/generated/${generated}`,
    ]) {
      const copied = readFileSync(at(destination));
      assert.equal(createHash("sha256").update(copied).digest("hex"), digest, destination);
      assert.deepEqual(copied, canonical, destination);
    }
  }
});

test("Skill fixture generation rejects a symlink before following it", (t) => {
  const temporary = mkdtempSync(resolve(tmpdir(), "dayorder-skill-copy-"));
  t.after(() => rmSync(temporary, { recursive: true, force: true }));
  const source = resolve(temporary, "source");
  const destination = resolve(temporary, "destination");
  const outside = resolve(temporary, "outside");
  mkdirSync(resolve(source, "safe"), { recursive: true });
  mkdirSync(outside, { recursive: true });
  writeFileSync(resolve(source, "safe", "SKILL.md"), "safe\n");
  writeFileSync(resolve(outside, "secret.md"), "must not copy\n");
  symlinkSync(outside, resolve(source, "safe", "references"), "junction");

  assert.throws(() => copySkillFixtures(source, destination), /symbolic link/);
  assert.equal(existsSync(resolve(destination, "safe", "references", "secret.md")), false);
});

const caseFiles = [
  "react-tool-success.json",
  "approval-denied.json",
  "capability-unavailable.json",
  "cancelled.json",
  "budget-exhausted.json",
  "repeated-tool-call.json",
  "tool-turn-usage.json",
  "tool-call-incomplete.json",
  "multiple-tool-calls.json",
  "tool-turn-budget-exhausted.json",
  "orphan-tool-use.json",
];

const expectedEffectsByFile = {
  "react-tool-success.json": ["request_model_turn", undefined, "resolve_tool", "execute_tool", "request_model_turn", "emit_text", "complete_run"],
  "approval-denied.json": ["request_model_turn", undefined, "resolve_tool", "request_approval", "request_model_turn"],
  "capability-unavailable.json": ["request_model_turn", undefined, "resolve_tool", "fail_run"],
  "cancelled.json": ["request_model_turn", "cancel_run"],
  "budget-exhausted.json": ["request_model_turn", undefined, "resolve_tool", "execute_tool", "request_model_turn", undefined, "resolve_tool", "execute_tool", "request_model_turn", undefined, "resolve_tool", "execute_tool", "request_model_turn", undefined, "resolve_tool", "execute_tool", "request_model_turn", undefined, "resolve_tool", "fail_run"],
  "repeated-tool-call.json": ["request_model_turn", undefined, "resolve_tool", "execute_tool", "request_model_turn", undefined, "resolve_tool", "execute_tool", "request_model_turn", undefined, "fail_run"],
  "tool-turn-usage.json": ["request_model_turn", undefined, "resolve_tool"],
  "tool-call-incomplete.json": ["request_model_turn", undefined, "fail_run"],
  "multiple-tool-calls.json": ["request_model_turn", undefined, "fail_run"],
  "tool-turn-budget-exhausted.json": ["request_model_turn", undefined, "fail_run"],
  "orphan-tool-use.json": ["request_model_turn", "fail_run"],
};

function canonicalFingerprint(call) {
  const sort = (value) => Array.isArray(value) ? value.map(sort) : value && typeof value === "object" ? Object.fromEntries(Object.keys(value).sort().map((key) => [key, sort(value[key])])) : value;
  return `${call.name}:${JSON.stringify(sort(call.input))}`;
}

function mostRecentUnresolvedToolCall(messages) {
  for (let messageIndex = messages.length - 1; messageIndex >= 0; messageIndex -= 1) {
    const content = messages[messageIndex].content;
    for (let contentIndex = content.length - 1; contentIndex >= 0; contentIndex -= 1) {
      const block = content[contentIndex];
      if (block.type === "tool_result") return undefined;
      if (block.type === "tool_call") return block.toolCall;
    }
  }
  return undefined;
}

function addUsage(left, right) {
  return {
    inputTokens: left.inputTokens + right.inputTokens,
    outputTokens: left.outputTokens + right.outputTokens,
    totalTokens: left.totalTokens + right.totalTokens,
  };
}

function assertTraceInvariants(file, value) {
  let previous = value.initialState;
  for (let index = 0; index < value.inputs.length; index += 1) {
    const input = value.inputs[index];
    const transition = value.expectedTransitions[index];
    const state = transition.state;
    const effects = transition.effects;
    assert.equal(state.sequence, previous.sequence + 1, `${file} transition ${index} must increment sequence exactly once`);
    for (const field of ["protocolVersion", "runId", "executionMode", "capabilitySnapshot", "budget"]) assert.deepEqual(state[field], previous[field], `${file} transition ${index} must preserve ${field}`);
    const expectedUsage = input.type === "provider_event" && input.providerEvent.type === "completed"
      ? addUsage(previous.usage, input.providerEvent.usage)
      : previous.usage;
    assert.deepEqual(state.usage, expectedUsage, `${file} transition ${index} must account usage only on completed`);
    assert.deepEqual(state.messages.slice(0, previous.messages.length), previous.messages, `${file} transition ${index} must preserve prior messages in order`);
    assert.equal(state.stepCount, previous.stepCount + (effects.some((effect) => effect.type === "execute_tool") ? 1 : 0), `${file} transition ${index} must only increment steps for execute_tool`);
    const stagesToolCall = input.type === "provider_event" && input.providerEvent.type === "tool_call" && previous.pendingToolCall === undefined;
    assert.equal(effects.length, stagesToolCall ? 0 : 1, `${file} transition ${index} must have the effects required by its input`);
    const [effect] = effects;
    if (effect?.type === "request_model_turn") assert.deepEqual(effect, { type: "request_model_turn", turnId: `turn-${state.sequence}` });
    if (effect?.type === "resolve_tool") assert.deepEqual(effect, { type: "resolve_tool", toolCall: previous.pendingToolCall });
    if (effect?.type === "execute_tool") assert.deepEqual(effect, { type: "execute_tool", toolCall: previous.pendingToolCall });
    if (effect?.type === "request_approval") assert.deepEqual(effect, { type: "request_approval", toolCall: previous.pendingToolCall, approvalId: state.pendingApprovalId });
    if (effect?.type === "emit_text") assert.deepEqual(effect, { type: "emit_text", text: input.providerEvent.text });
    if (effect?.type === "fail_run") assert.deepEqual(effect, { type: "fail_run", error: state.error });
    if (["complete_run", "cancel_run"].includes(effect?.type)) assert.deepEqual(effect, { type: effect.type });
    const expectedError = (code, message) => ({ code, message, retryable: false });
    let expectedPhase;
    if (input.type === "user_message") {
      assert.equal(previous.phase, "idle", `${file} user messages start only from idle`);
      expectedPhase = "model_pending";
      assert.deepEqual(state.messages, [...previous.messages, { role: "user", content: [{ type: "text", text: input.text }] }]);
    } else if (input.type === "provider_event" && input.providerEvent.type === "tool_call") {
      assert.ok(["model_pending", "model_streaming"].includes(previous.phase), `${file} tool calls require a model phase`);
      if (previous.pendingToolCall) {
        expectedPhase = "failed";
        assert.equal("pendingToolCall" in state, false, `${file} multiple-call failure clears the staged tool`);
        assert.deepEqual(state.error, expectedError("protocol_incompatible", "multiple tool calls in one turn"));
      } else {
        expectedPhase = "model_streaming";
        assert.deepEqual(state.pendingToolCall, input.providerEvent.call);
        assert.deepEqual(state.messages, previous.messages);
        assert.equal(state.assistantDraft, previous.assistantDraft);
        assert.equal(state.lastToolFingerprint, previous.lastToolFingerprint);
        assert.equal(state.repeatedToolCalls, previous.repeatedToolCalls);
      }
    } else if (input.type === "provider_event" && input.providerEvent.type === "text_delta") {
      assert.ok(["model_pending", "model_streaming"].includes(previous.phase), `${file} text requires a model phase`);
      expectedPhase = "model_streaming";
      assert.deepEqual(state.messages, previous.messages);
      assert.equal(state.assistantDraft, `${previous.assistantDraft ?? ""}${input.providerEvent.text}`);
    } else if (input.type === "provider_event" && input.providerEvent.type === "completed") {
      assert.ok(["model_pending", "model_streaming"].includes(previous.phase), `${file} completion requires a model phase`);
      if (expectedUsage.totalTokens > previous.budget.maxTokens) {
        expectedPhase = "failed";
        assert.equal("pendingToolCall" in state, false, `${file} token failure clears the staged tool`);
        assert.deepEqual(state.error, expectedError("validation_failed", "maxTokens exceeded"));
      } else if (input.providerEvent.stopReason === "tool_use" && previous.pendingToolCall) {
        const call = previous.pendingToolCall;
        const fingerprint = canonicalFingerprint(call);
        const repeated = previous.lastToolFingerprint === fingerprint ? previous.repeatedToolCalls + 1 : 1;
        const content = [
          ...(previous.assistantDraft === undefined ? [] : [{ type: "text", text: previous.assistantDraft }]),
          { type: "tool_call", toolCall: call },
        ];
        assert.deepEqual(state.messages, [...previous.messages, { role: "assistant", content }]);
        assert.equal(state.lastToolFingerprint, fingerprint);
        assert.equal(state.repeatedToolCalls, repeated);
        assert.equal("assistantDraft" in state, false);
        if (repeated > previous.budget.maxRepeatedToolCalls) {
          expectedPhase = "failed";
          assert.equal("pendingToolCall" in state, false);
          assert.deepEqual(state.error, expectedError("validation_failed", "maxRepeatedToolCalls exceeded"));
        } else {
          expectedPhase = "tool_pending";
          assert.deepEqual(state.pendingToolCall, call);
          assert.deepEqual(effect, { type: "resolve_tool", toolCall: call });
        }
      } else if (input.providerEvent.stopReason === "tool_use") {
        expectedPhase = "failed";
        assert.deepEqual(state.error, expectedError("protocol_incompatible", "provider completed with orphan tool_use"));
      } else if (input.providerEvent.stopReason === "end_turn" && previous.pendingToolCall) {
        expectedPhase = "failed";
        assert.equal("pendingToolCall" in state, false);
        assert.deepEqual(state.error, expectedError("protocol_incompatible", "provider completed end_turn with pending tool call"));
      } else if (input.providerEvent.stopReason === "end_turn") {
        expectedPhase = "completed";
        const append = previous.assistantDraft ? [{ role: "assistant", content: [{ type: "text", text: previous.assistantDraft }] }] : [];
        assert.deepEqual(state.messages, [...previous.messages, ...append]);
        assert.equal("assistantDraft" in state, false);
        assert.equal("error" in state, false);
      } else if (input.providerEvent.stopReason === "max_tokens") {
        expectedPhase = "failed";
        assert.deepEqual(state.error, expectedError("validation_failed", "provider stopped at max_tokens"));
      } else if (input.providerEvent.stopReason === "cancelled") {
        expectedPhase = "cancelled";
        assert.deepEqual(state.error, expectedError("cancelled", "provider cancelled"));
      }
    } else if (input.type === "tool_resolution") {
      assert.equal(previous.phase, "tool_pending", `${file} tool resolutions require a pending tool`);
      assert.equal(input.toolResolution.callId, previous.pendingToolCall.id, `${file} resolution must match pending tool`);
      assert.deepEqual(state.messages, previous.messages);
      if (input.toolResolution.decision === "execute" && previous.stepCount >= previous.budget.maxSteps) {
        expectedPhase = "failed";
        assert.equal("pendingToolCall" in state, false, `${file} budget failure clears pending tool`);
        assert.deepEqual(state.error, expectedError("validation_failed", "maxSteps exceeded"));
      } else if (input.toolResolution.decision === "execute") {
        expectedPhase = "tool_pending";
        assert.equal("pendingToolCall" in state, false, `${file} execution must clear the pending decision`);
      } else if (input.toolResolution.decision === "approval") {
        expectedPhase = "approval_pending";
        assert.deepEqual(state.pendingToolCall, previous.pendingToolCall);
        assert.equal(state.pendingApprovalId, input.toolResolution.approvalId);
      } else {
        expectedPhase = "failed";
        assert.equal("pendingToolCall" in state, false, `${file} failed resolution clears pending tool`);
        assert.deepEqual(state.error, input.toolResolution.error);
      }
    } else if (input.type === "tool_result") {
      assert.equal(previous.phase, "tool_pending", `${file} tool results require a pending tool`);
      assert.equal("pendingToolCall" in previous, false, `${file} result requires execution to have started`);
      const inFlightCall = mostRecentUnresolvedToolCall(previous.messages);
      assert.ok(inFlightCall, `${file} result requires an unresolved ToolCall in message history`);
      assert.equal(input.toolResult.callId, inFlightCall.id, `${file} result must match the in-flight tool`);
      expectedPhase = "model_pending";
      assert.deepEqual(state.messages, [...previous.messages, { role: "tool", content: [{ type: "tool_result", toolResult: input.toolResult.result }] }]);
      assert.equal("pendingToolCall" in state, false, `${file} result clears pending tool`);
    } else if (input.type === "approval_response") {
      assert.equal(previous.phase, "approval_pending", `${file} approval response requires pending approval`);
      assert.equal(input.approvalResponse.approvalId, previous.pendingApprovalId, `${file} approval response must match pending approval`);
      expectedPhase = input.approvalResponse.decision === "deny" ? "model_pending" : "tool_pending";
      assert.equal("pendingApprovalId" in state, false, `${file} response clears pending approval`);
      if (input.approvalResponse.decision === "deny") {
        assert.equal("pendingToolCall" in state, false, `${file} denial clears pending tool`);
        assert.deepEqual(state.messages, [...previous.messages, { role: "tool", content: [{ type: "tool_result", toolResult: { ok: false, error: expectedError("approval_denied", "approval denied") } }] }]);
      } else {
        assert.equal("pendingToolCall" in state, false, `${file} approved execution clears the pending decision`);
        assert.deepEqual(effect, { type: "execute_tool", toolCall: previous.pendingToolCall });
        assert.deepEqual(state.messages, previous.messages);
      }
    } else {
      assert.equal(state.repeatedToolCalls, previous.repeatedToolCalls, `${file} transition ${index} must not fabricate repetitions`);
      if (input.type === "cancel") {
        assert.ok(!["completed", "failed", "cancelled"].includes(previous.phase), `${file} cancellation requires a nonterminal phase`);
        expectedPhase = "cancelled";
        assert.deepEqual(state.error, expectedError("cancelled", input.reason ?? "cancelled"));
        assert.deepEqual(state.messages, previous.messages);
        if (file === "cancelled.json") {
          assert.equal(previous.phase, "model_pending", "cancelled fixture cancels the prescribed model-pending state");
          assert.equal(input.reason, "user cancelled", "cancelled fixture has the prescribed reason");
          assert.deepEqual(state.error, expectedError("cancelled", "user cancelled"));
        }
      }
    }
    assert.equal(state.phase, expectedPhase, `${file} transition ${index} must reach the legal next phase`);
    if (!(input.type === "provider_event" && ["text_delta", "tool_call", "completed"].includes(input.providerEvent.type))) assert.equal(state.assistantDraft, previous.assistantDraft, `${file} transition ${index} must preserve the draft`);
    if (!(input.type === "provider_event" && input.providerEvent.type === "completed" && input.providerEvent.stopReason === "tool_use" && previous.pendingToolCall)) assert.equal(state.lastToolFingerprint, previous.lastToolFingerprint, `${file} transition ${index} must preserve fingerprint`);
    if (["failed", "cancelled"].includes(state.phase)) {
      assert.ok(state.error, `${file} terminal state must have an error`);
      assert.deepEqual(effects, state.phase === "failed" ? [{ type: "fail_run", error: state.error }] : [{ type: "cancel_run" }]);
    }
    if (file === "capability-unavailable.json" && state.phase === "failed") {
      assert.deepEqual(state.error, expectedError("capability_unavailable", "test.clock.read is unavailable"));
      assert.deepEqual(effects, [{ type: "fail_run", error: expectedError("capability_unavailable", "test.clock.read is unavailable") }]);
    }
    previous = state;
  }
}

test("agent conformance matrix is complete and schema-valid", () => {
  const conformanceDirectory = at("contracts/agent/conformance");
  assert.deepEqual(readdirSync(conformanceDirectory).sort(), [...caseFiles].sort());

  const schema = JSON.parse(readFileSync(at("contracts/agent/v2/protocol.schema.json"), "utf8"));
  const ajv = createContractValidator();
  ajv.addSchema(schema);
  const validate = ajv.getSchema(`${schema.$id}#/$defs/ConformanceCase`);
  assert.ok(validate);

  const names = new Set();
  for (const file of caseFiles) {
    const value = JSON.parse(readFileSync(at(`contracts/agent/conformance/${file}`), "utf8"));
    assert.equal(validate(value), true, `${file}: ${ajv.errorsText(validate.errors)}`);
    assert.equal(value.protocolVersion, "2.0");
    assert.equal(value.inputs.length, value.expectedTransitions.length);
    assert.equal(names.has(value.name), false);
    names.add(value.name);

    assert.equal(value.initialState.protocolVersion, "2.0");
    assert.equal(value.initialState.capabilitySnapshot.runtimeVersion, "2.0.0");
    assert.equal(value.initialState.phase, "idle");
    assert.equal(value.initialState.sequence, 0);
    assert.deepEqual(value.initialState.usage, { inputTokens: 0, outputTokens: 0, totalTokens: 0 });

    for (const transition of value.expectedTransitions) {
      assert.ok(transition.state, `${file} must retain a complete state snapshot`);
      assert.equal(transition.state.runId, value.initialState.runId);
      assert.deepEqual(transition.state.budget, value.initialState.budget);
    }
    assert.deepEqual(value.expectedTransitions.map((transition) => transition.effects[0]?.type), expectedEffectsByFile[file]);
    assertTraceInvariants(file, value);
  }
});

test("conformance trace invariants reject semantic snapshot corruption", () => {
  const fixture = JSON.parse(readFileSync(at("contracts/agent/conformance/react-tool-success.json"), "utf8"));
  const mutations = [
    (value) => { value.expectedTransitions[1].state.phase = "tool_pending"; },
    (value) => { value.expectedTransitions[2].state.messages = []; },
    (value) => { delete value.expectedTransitions[2].state.pendingToolCall; },
    (value) => { value.expectedTransitions[3].state.lastToolFingerprint = "wrong:{}"; },
    (value) => { value.expectedTransitions[6].state.error = { code: "internal_error", message: "wrong", retryable: true }; },
  ];
  for (const mutate of mutations) {
    const corrupted = structuredClone(fixture);
    mutate(corrupted);
    assert.throws(() => assertTraceInvariants("controlled corruption", corrupted));
  }
  const approval = JSON.parse(readFileSync(at("contracts/agent/conformance/approval-denied.json"), "utf8"));
  approval.expectedTransitions[4].state.pendingToolCall = approval.expectedTransitions[3].state.pendingToolCall;
  assert.throws(() => assertTraceInvariants("approval-denied.json", approval));

  const unavailable = JSON.parse(readFileSync(at("contracts/agent/conformance/capability-unavailable.json"), "utf8"));
  unavailable.inputs[3].toolResolution.error.message = "different unavailable error";
  unavailable.expectedTransitions[3].state.error.message = "different unavailable error";
  unavailable.expectedTransitions[3].effects[0].error.message = "different unavailable error";
  assert.throws(() => assertTraceInvariants("capability-unavailable.json", unavailable));

  const cancelled = JSON.parse(readFileSync(at("contracts/agent/conformance/cancelled.json"), "utf8"));
  cancelled.inputs[1].reason = "different cancellation";
  cancelled.expectedTransitions[1].state.error.message = "different cancellation";
  assert.throws(() => assertTraceInvariants("cancelled.json", cancelled));
});
