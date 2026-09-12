# Agent Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the disabled-by-default Phase 1 Agent Foundation: a versioned shared protocol, behaviorally equivalent TypeScript and Go ReAct runtimes, m-agent-style Skill discovery, host-specific Tool bindings, mock providers, bounded worker contracts, and cross-runtime conformance tests.

**Architecture:** JSON Schema under `contracts/agent/v1` is the wire-contract source of truth and generates TypeScript and Go DTOs. Each runtime implements the same pure `state + input -> state + effects` reducer, while Provider, Tool, approval, and persistence remain host adapters; shared fixtures verify semantic equivalence. Phase 1 adds no production Agent route, real provider, domain Tool binding, database migration, or worker registration.

**Tech Stack:** JSON Schema 2020-12, Node.js 24 `node:test`, `json-schema-to-typescript` 16.0.0, TypeScript 5.9.2, Ajv 8.20.0, YAML 2.9.0, Vitest 4.1.11, Go 1.25, `go-jsonschema` 0.24.1, `jsonschema/v6` 6.0.3, `yaml.v3` 3.0.1.

**Spec:** `docs/superpowers/specs/2026-09-03-agent-runtime-skill-architecture-design.md`

## Global Constraints

- Keep `<App />` defaulting to `agentAvailable=false`, keep production Agent HTTP returning `AGENT_NOT_AVAILABLE`, and do not register `agent.run.requested` in the Worker.
- Do not add a real Provider endpoint, API key, vendor SDK, vendor adapter, database migration, domain Tool binding, Native Bridge, or end-to-end integration test in Phase 1.
- Provider keys, endpoints, vendor conversion, retry, and streaming normalization remain server-only.
- `contracts/agent/v1/protocol.schema.json` is the sole wire-contract source; generated files must never be edited by hand.
- Protocol version is exactly `1.0`; unknown event/effect types and missing required fields fail as `protocol_incompatible`.
- Foreground execution mode is TypeScript; background execution mode is Go; Phase 1 uses neither Go nor Rust WASM.
- Runtime reduction is deterministic and performs no clock, random, network, filesystem, database, DOM, or Native API access.
- A Skill may request only the intersection of `allowed-tools`, registered bindings, Run Scope, and system policy; Skill loading never grants user permission.
- Phase 1 loads only system Skills and forbids `scripts/`, dynamic code, absolute paths, parent traversal, and symlink escape.
- A subagent receives a frozen capability snapshot, inherits execution mode, cannot load Skills, and has maximum nesting depth one.
- DayOrder writes continue to use AgentChange, field allowlists, base versions, confirmation, transactions, sync, and audit; Phase 1 creates no real write Tool.
- Phase 1 testing is limited to unit, schema, contract, shared conformance, trace replay, and mock component tests; full integration tests belong to Phase 2.

## File Structure

```text
contracts/agent/v1/protocol.schema.json              canonical wire schema
contracts/agent/conformance/*.json                   cross-runtime golden traces
contracts/agent/fixtures/skills/*                    safe and invalid Skill bundles
scripts/generate-agent-contracts.mjs                 deterministic DTO/schema generation
scripts/agent-contracts.test.mjs                     contract asset tests

apps/web/src/agent/generated/*                       generated DTO/schema/fixtures
apps/web/src/agent/protocol/validate.ts               Ajv boundary validation
apps/web/src/agent/runtime/{reducer,driver}.ts        pure reducer and mock effect driver
apps/web/src/agent/tool/registry.ts                   ToolSpec/ToolBinding registry
apps/web/src/agent/skill/{profile,registry,tools}.ts  SKILL.md support
apps/web/src/agent/provider/{provider,mock}.ts        gateway contract and scripted mock
apps/web/src/agent/worker/spawn.ts                    frozen worker contract

apps/api/internal/agentprotocol/*                     generated DTO/schema and validation
apps/api/internal/agentruntime/{reducer,driver}.go    pure reducer and mock effect driver
apps/api/internal/agenttool/registry.go               ToolSpec/Binding registry
apps/api/internal/agentskill/{profile,registry,tools}.go
apps/api/internal/agentprovider/{stream,mock}.go      stream contract beside legacy provider
apps/api/internal/agentworker/spawn.go                frozen worker contract
```

---

### Task 1: Canonical protocol schema and deterministic DTO generation

**Files:**
- Create: `contracts/agent/v1/protocol.schema.json`
- Create: `scripts/generate-agent-contracts.mjs`
- Create: `scripts/agent-contracts.test.mjs`
- Modify: `package.json`, `package-lock.json`, `apps/web/package.json`
- Modify: `apps/api/go.mod`, `apps/api/go.sum`, `apps/api/tools.mod`, `apps/api/tools.sum`
- Generate: `apps/web/src/agent/generated/protocol.ts`
- Generate: `apps/web/src/agent/generated/protocol.schema.json`
- Generate: `apps/api/internal/agentprotocol/generated_types.go`
- Generate: `apps/api/internal/agentprotocol/protocol.schema.json`

**Interfaces:**
- Schema ID: `https://dayorder.local/schemas/agent/v1/protocol.schema.json`
- Definitions: `AgentError`, `AgentScope`, `Budget`, `Usage`, `Message`, `ToolSpec`, `ToolCall`, `ToolResult`, `SkillManifest`, `SkillDescriptor`, `SkillRef`, `SkillActivation`, `CapabilitySnapshot`, `ProviderEvent`, `ModelTurnRequest`, `RuntimeState`, `RuntimeInput`, `RuntimeEffect`, `RuntimeTransition`, `ConformanceCase`, `WorkerSpawnSpec`, `WorkerResult`
- Commands: `npm run agent:generate`, `npm run agent:generate:check`

Use these exact protocol enums and core shapes:

```ts
type RuntimePhase = "idle" | "model_pending" | "model_streaming" | "tool_pending" | "approval_pending" | "completed" | "failed" | "cancelled";
type ExecutionMode = "foreground" | "background";
type SideEffect = "read" | "reversible_write" | "irreversible_write" | "external_communication";
type ErrorCode = "validation_failed" | "protocol_incompatible" | "capability_unavailable" | "permission_denied" | "approval_denied" | "provider_unavailable" | "provider_rate_limited" | "tool_failed" | "timeout" | "version_conflict" | "cancelled" | "internal_error";

interface RuntimeState {
  protocolVersion: "1.0"; runId: string; executionMode: ExecutionMode;
  phase: RuntimePhase; sequence: number; stepCount: number; messages: Message[];
  capabilitySnapshot: CapabilitySnapshot; budget: Budget; usage: Usage;
  assistantDraft?: string; pendingToolCall?: ToolCall; pendingApprovalId?: string;
  lastToolFingerprint?: string; repeatedToolCalls: number; error?: AgentError;
}

type RuntimeInput = {
  type: "user_message" | "provider_event" | "tool_resolution" | "tool_result" | "approval_response" | "runtime_error" | "cancel";
  text?: string; providerEvent?: ProviderEvent;
  toolResolution?: { callId: string; decision: "execute" | "approval" | "unavailable" | "denied"; approvalId?: string; error?: AgentError };
  toolResult?: { callId: string; result: ToolResult };
  approvalResponse?: { approvalId: string; decision: "allow" | "deny" };
  error?: AgentError; reason?: string;
};

type RuntimeEffect = {
  type: "request_model_turn" | "emit_text" | "resolve_tool" | "execute_tool" | "request_approval" | "complete_run" | "fail_run" | "cancel_run";
  turnId?: string; text?: string; toolCall?: ToolCall; approvalId?: string; error?: AgentError;
};
```

Define the remaining wire ledger exactly as follows:

```ts
interface AgentError { code: ErrorCode; message: string; retryable: boolean; details?: Record<string, unknown> }
interface AgentScope { domains: string[]; entityIds?: string[]; from?: string; to?: string }
interface Budget { maxSteps: number; maxTokens: number; maxDurationMs: number; maxWorkers: number; maxConcurrency: number; maxRepeatedToolCalls: number }
interface Usage { inputTokens: number; outputTokens: number; totalTokens: number }
interface Message { role: "system" | "user" | "assistant" | "tool"; content: ContentBlock[] }
interface ContentBlock { type: "text" | "tool_call" | "tool_result"; text?: string; toolCall?: ToolCall; toolResult?: ToolResult }
interface ToolSpec {
  id: string; description: string; inputSchema: Record<string, unknown>; outputSchema: Record<string, unknown>;
  sideEffect: SideEffect; requiredDomains: string[]; executionTargets: ("client" | "server")[];
  approvalPolicy: "never" | "if_needed" | "always"; idempotent: boolean; timeoutMs: number; resultMaxBytes: number;
}
interface ToolCall { id: string; name: string; input: Record<string, unknown> }
interface ToolResult { ok: boolean; data?: Record<string, unknown>; display?: string; error?: AgentError }
interface SkillManifest {
  name: string; description: string; version: string; "allowed-tools": string[];
  "execution-target": "client" | "server" | "either"; "background-allowed": boolean;
  "user-invocable": boolean; "disable-model-invocation": boolean;
  "min-runtime-version": string; "risk-level": "low" | "medium" | "high" | "critical";
}
interface SkillRef { name: string; version: string; digest: string }
interface SupportingFileDescriptor { path: string; kind: "reference" | "asset" | "schema"; size: number }
interface SkillDescriptor extends SkillRef {
  description: string; scope: "system" | "user" | "device"; executionTarget: "client" | "server" | "either";
  backgroundAllowed: boolean; userInvocable: boolean; disableModelInvocation: boolean; riskLevel: "low" | "medium" | "high" | "critical";
}
interface SkillActivation { skill: SkillRef; instructions: string; supportingFiles: SupportingFileDescriptor[]; requestedToolIds: string[]; activeToolIds: string[] }
interface CapabilitySnapshot { runtimeVersion: "1.0.0"; executionMode: ExecutionMode; toolIds: string[]; skills: SkillRef[]; scope: AgentScope }
interface ProviderEvent { type: "text_delta" | "tool_call" | "completed" | "error"; text?: string; call?: ToolCall; stopReason?: "end_turn" | "tool_use" | "max_tokens" | "cancelled"; usage?: Usage; error?: AgentError }
interface ModelTurnRequest { protocolVersion: "1.0"; runId: string; turnId: string; modelProfile: string; messages: Message[]; tools: ToolSpec[] }
interface RuntimeTransition { state: RuntimeState; effects: RuntimeEffect[] }
interface ConformanceCase { name: string; protocolVersion: "1.0"; initialState: RuntimeState; inputs: RuntimeInput[]; expectedTransitions: RuntimeTransition[] }
interface WorkerTask { goal: string; successCriteria: string[]; scope: string[]; constraints: string[]; hints: string[] }
interface WorkerOutputContract { sections: ["summary", "findings", "risks", "next_steps"] }
interface WorkerSkillInstructions { skill: SkillRef; instructions: string }
interface WorkerSpawnSpec {
  protocolVersion: "1.0"; parentRunId: string; workerId: string; depth: 1; executionMode: ExecutionMode;
  task: WorkerTask; capabilities: CapabilitySnapshot; budget: Budget;
  skillInstructions: WorkerSkillInstructions[]; outputContract: WorkerOutputContract;
}
interface WorkerResult {
  protocolVersion: "1.0"; workerId: string; state: "succeeded" | "failed" | "cancelled";
  sections?: { summary: string; findings: string; risks: string; next_steps: string };
  error?: AgentError; usage: Usage;
}
```

`ConformanceCase` requires equal-length input/transition arrays. Positive limits apply to every `Budget` field, Tool timeout, and result byte limit. Skill names match `^[a-z0-9][a-z0-9-]{0,63}$`; Tool IDs match `^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`, allowing namespaced domain IDs and the reserved m-agent-compatible `skill_list`/`skill_load` IDs; Skill versions are SemVer. Every protocol-defined envelope uses `additionalProperties:false`; the intentionally opaque `details`, Tool `input`, ToolResult `data`, `inputSchema`, and `outputSchema` maps allow keys and are validated at their owning boundary. Tagged variants use `if`/`then` to require their payload and prohibit contradictory payloads. `ToolResult.ok=true` requires `data` and prohibits `error`; `ok=false` requires `error`. `WorkerResult.succeeded` requires sections and prohibits error; failed/cancelled requires error and prohibits sections.

- [ ] **Step 1: Write the failing contract asset test**

```js
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import test from "node:test";

const root = resolve(import.meta.dirname, "..");
const at = (path) => resolve(root, path);

test("agent protocol has one canonical schema and generated DTOs", () => {
  const schemaPath = at("contracts/agent/v1/protocol.schema.json");
  assert.equal(existsSync(schemaPath), true);
  const schema = JSON.parse(readFileSync(schemaPath, "utf8"));
  assert.equal(schema.$schema, "https://json-schema.org/draft/2020-12/schema");
  assert.equal(schema.$id, "https://dayorder.local/schemas/agent/v1/protocol.schema.json");
  for (const name of ["RuntimeState", "RuntimeInput", "RuntimeTransition", "ToolSpec", "SkillManifest", "WorkerSpawnSpec"]) assert.ok(schema.$defs[name]);
  assert.equal(existsSync(at("apps/web/src/agent/generated/protocol.ts")), true);
  assert.equal(existsSync(at("apps/api/internal/agentprotocol/generated_types.go")), true);
});
```

- [ ] **Step 2: Run it and verify RED**

Run: `node --test scripts/agent-contracts.test.mjs`

Expected: FAIL because the canonical schema does not exist.

- [ ] **Step 3: Add pinned tools, the complete Schema, and generator**

```powershell
npm install --save-dev --save-exact json-schema-to-typescript@16.0.0
npm install --workspace @dayorder/web --save-exact ajv@8.20.0 yaml@2.9.0
go -C apps/api get github.com/santhosh-tekuri/jsonschema/v6@v6.0.3 gopkg.in/yaml.v3@v3.0.1
go -C apps/api get -tool -modfile=tools.mod github.com/atombender/go-jsonschema@v0.24.1
```

The Schema root references every definition listed under Interfaces so both generators emit every DTO. Create `scripts/generate-agent-contracts.mjs` with these exact generator calls and schema copies:

```js
const json2ts = resolve(root, "node_modules/.bin", process.platform === "win32" ? "json2ts.cmd" : "json2ts");
run(json2ts, ["--input", schema, "--output", resolve(webOut, "protocol.ts"), "--cwd", resolve(root, "contracts/agent/v1"), "--no-enableConstEnums", "--unknownAny"]);
run("go", ["tool", "-C", "apps/api", "-modfile", "tools.mod", "go-jsonschema", "--only-models", "--tags", "json", "--capitalization", "ID", "--package", "agentprotocol", "--output", "internal/agentprotocol/generated_types.go", "../../contracts/agent/v1/protocol.schema.json"]);
copyFileSync(schema, resolve(webOut, "protocol.schema.json"));
copyFileSync(schema, resolve(goOut, "protocol.schema.json"));
```

The script resolves all paths from `import.meta.dirname`, creates output directories, uses `spawnSync` with inherited stdio, and exits nonzero on any generator failure. Add root scripts:

```json
"agent:generate": "node scripts/generate-agent-contracts.mjs",
"agent:generate:check": "npm run agent:generate && git diff --exit-code -- apps/web/src/agent/generated apps/api/internal/agentprotocol/generated_types.go apps/api/internal/agentprotocol/protocol.schema.json"
```

- [ ] **Step 4: Generate and verify GREEN**

```powershell
npm run agent:generate
node --test scripts/agent-contracts.test.mjs
npm run agent:generate:check
npm run typecheck --workspace @dayorder/web
go test ./apps/api/internal/agentprotocol
```

Expected: all commands exit 0 and regeneration produces no diff.

- [ ] **Step 5: Commit**

```powershell
git add contracts/agent/v1 scripts/generate-agent-contracts.mjs scripts/agent-contracts.test.mjs package.json package-lock.json apps/web/package.json apps/api/go.mod apps/api/go.sum apps/api/tools.mod apps/api/tools.sum apps/web/src/agent/generated apps/api/internal/agentprotocol
git commit -m "feat(agent): add shared protocol contract"
```

### Task 2: TypeScript protocol validation

**Files:**
- Create: `apps/web/src/agent/protocol/validate.ts`
- Create: `apps/web/src/agent/protocol/validate.test.ts`

**Interfaces:**
- Produces: `ProtocolDefinition` union for every externally validated definition
- Produces: `validateProtocol<T>(definition: ProtocolDefinition, value: unknown): T`
- Produces: `ProtocolValidationError` with code `validation_failed`

- [ ] **Step 1: Write failing strict-validation tests**

```ts
it("accepts a valid input and rejects unknown fields", () => {
  expect(validateProtocol("RuntimeInput", { type: "user_message", text: "plan today" })).toMatchObject({ type: "user_message" });
  expect(() => validateProtocol("RuntimeInput", { type: "user_message", extra: true })).toThrow(ProtocolValidationError);
  expect(() => validateProtocol("RuntimeInput", { type: "future_event" })).toThrow(/validation_failed/);
});
```

- [ ] **Step 2: Run it and verify RED**

Run: `npm run test --workspace @dayorder/web -- src/agent/protocol/validate.test.ts`

Expected: FAIL because `validate.ts` does not exist.

- [ ] **Step 3: Implement one strict Ajv validator cache**

```ts
import Ajv2020, { type ValidateFunction } from "ajv/dist/2020.js";
import schema from "../generated/protocol.schema.json";

const schemaID = "https://dayorder.local/schemas/agent/v1/protocol.schema.json";
const ajv = new Ajv2020({ allErrors: true, strict: true });
ajv.addSchema(schema, schemaID);
const cache = new Map<string, ValidateFunction>();

export class ProtocolValidationError extends Error { readonly code = "validation_failed"; }
export function validateProtocol<T>(definition: ProtocolDefinition, value: unknown): T {
  const validator = cache.get(definition) ?? ajv.getSchema(`${schemaID}#/$defs/${definition}`);
  if (!validator) throw new Error(`missing protocol definition: ${definition}`);
  cache.set(definition, validator);
  if (!validator(value)) throw new ProtocolValidationError(`validation_failed: ${ajv.errorsText(validator.errors, { separator: "; " })}`);
  return value as T;
}
```

- [ ] **Step 4: Verify**

```powershell
npm run test --workspace @dayorder/web -- src/agent/protocol/validate.test.ts
npm run typecheck --workspace @dayorder/web
```

Expected: tests pass and TypeScript exits 0.

- [ ] **Step 5: Commit**

```powershell
git add apps/web/src/agent/protocol
git commit -m "feat(agent): validate web protocol boundaries"
```

### Task 3: Go protocol validation

**Files:**
- Create: `apps/api/internal/agentprotocol/validate.go`
- Create: `apps/api/internal/agentprotocol/validate_test.go`

**Interfaces:**
- Produces: `type Definition string` constants matching Task 2
- Produces: `Validate(definition Definition, value any) error`
- Produces: `ValidationError.Code() string` returning `validation_failed`

- [ ] **Step 1: Write failing tests**

```go
func TestValidateRuntimeInput(t *testing.T) {
	if err := Validate(DefinitionRuntimeInput, map[string]any{"type": "user_message", "text": "plan today"}); err != nil { t.Fatal(err) }
	var target *ValidationError
	err := Validate(DefinitionRuntimeInput, map[string]any{"type": "user_message", "extra": true})
	if !errors.As(err, &target) || target.Code() != "validation_failed" { t.Fatalf("error = %v", err) }
}
```

- [ ] **Step 2: Run it and verify RED**

Run: `go test ./apps/api/internal/agentprotocol -run TestValidateRuntimeInput -v`

Expected: FAIL because `Validate` does not exist.

- [ ] **Step 3: Compile embedded definitions once**

Use `//go:embed protocol.schema.json`, `sync.Once`, and `jsonschema/v6`. JSON-round-trip the value before validation so structs and maps use identical wire semantics:

```go
func Validate(definition Definition, value any) error {
	validators, err := compiledValidators()
	if err != nil { return fmt.Errorf("compile agent protocol: %w", err) }
	validator, ok := validators[definition]
	if !ok { return fmt.Errorf("unknown protocol definition %q", definition) }
	raw, err := json.Marshal(value)
	if err != nil { return &ValidationError{message: err.Error()} }
	var document any
	if err = json.Unmarshal(raw, &document); err != nil { return &ValidationError{message: err.Error()} }
	if err = validator.Validate(document); err != nil { return &ValidationError{message: err.Error()} }
	return nil
}
```

- [ ] **Step 4: Verify**

Run: `go test ./apps/api/internal/agentprotocol`

Expected: all tests pass.

- [ ] **Step 5: Commit**

```powershell
git add apps/api/internal/agentprotocol apps/api/go.mod apps/api/go.sum
git commit -m "feat(agent): validate backend protocol boundaries"
```

### Task 4: Shared conformance fixtures

**Files:**
- Create: `contracts/agent/conformance/react-tool-success.json`
- Create: `contracts/agent/conformance/approval-denied.json`
- Create: `contracts/agent/conformance/capability-unavailable.json`
- Create: `contracts/agent/conformance/cancelled.json`
- Create: `contracts/agent/conformance/budget-exhausted.json`
- Create: `contracts/agent/conformance/repeated-tool-call.json`
- Modify: `scripts/agent-contracts.test.mjs`, `scripts/generate-agent-contracts.mjs`
- Generate: `apps/web/src/agent/generated/conformance/*.json`

**Interfaces:**
- Produces six Schema-valid `ConformanceCase` documents
- Defines exact behavior for success, denial, missing capability, cancellation, budget exhaustion, and loop detection

- [ ] **Step 1: Require the exact matrix**

```js
const caseFiles = ["react-tool-success.json", "approval-denied.json", "capability-unavailable.json", "cancelled.json", "budget-exhausted.json", "repeated-tool-call.json"];
test("agent conformance matrix is complete", () => {
  const names = new Set();
  for (const file of caseFiles) {
    const value = JSON.parse(readFileSync(at(`contracts/agent/conformance/${file}`), "utf8"));
    assert.equal(value.protocolVersion, "1.0");
    assert.equal(value.inputs.length, value.expectedTransitions.length);
    assert.equal(names.has(value.name), false);
    names.add(value.name);
  }
});
```

- [ ] **Step 2: Run it and verify RED**

Run: `node --test scripts/agent-contracts.test.mjs`

Expected: FAIL on the missing first fixture.

- [ ] **Step 3: Add fixed-ID fixtures and deterministic copies**

Every initial state uses `run-1`, sequence 0, phase `idle`, foreground mode, no messages, Runtime version `1.0.0`, `test.clock.read`, a `test` Scope, zero Usage, and budgets `{maxSteps:4,maxTokens:1000,maxDurationMs:30000,maxWorkers:1,maxConcurrency:1,maxRepeatedToolCalls:2}`. Encode these outcomes:

- success: user message -> model turn -> Tool Call -> resolution -> execution -> ToolResult -> second model turn -> text -> completion;
- denial: approval resolution -> approval request -> denial ToolResult -> next model turn, with no execute effect;
- unavailable: transition to failed with `capability_unavailable` and `fail_run`;
- cancel: transition to cancelled and `cancel_run`;
- budget: fail before an execution that would exceed `maxSteps`;
- repeated call: fail on the third identical canonical `name + sorted input` fingerprint.

Update generation to remove/recreate the Web fixture output and copy files in sorted order. Go tests read canonical fixtures directly.

- [ ] **Step 4: Verify**

```powershell
npm run agent:generate
node --test scripts/agent-contracts.test.mjs
npm run agent:generate:check
```

Expected: all commands exit 0.

- [ ] **Step 5: Commit**

```powershell
git add contracts/agent/conformance scripts/agent-contracts.test.mjs scripts/generate-agent-contracts.mjs apps/web/src/agent/generated
git commit -m "test(agent): define runtime conformance traces"
```

### Task 5: TypeScript pure Runtime reducer

**Files:**
- Create: `apps/web/src/agent/runtime/reducer.ts`
- Create: `apps/web/src/agent/runtime/reducer.test.ts`
- Create: `apps/web/src/agent/runtime/conformance.test.ts`

**Interfaces:**
- Produces: `createRuntimeState(config: RuntimeConfig): RuntimeState`
- Produces: `advance(state: RuntimeState, input: RuntimeInput): RuntimeTransition`
- Produces: `canonicalToolFingerprint(call: ToolCall): string`
- Preserves input immutability and returns fresh state/effect values

- [ ] **Step 1: Write failing reducer and fixture replay tests**

```ts
it("starts a model turn without mutating state", () => {
  const state = createRuntimeState(config);
  const before = structuredClone(state);
  const transition = advance(state, { type: "user_message", text: "plan today" });
  expect(state).toEqual(before);
  expect(transition.state.phase).toBe("model_pending");
  expect(transition.effects).toEqual([{ type: "request_model_turn", turnId: "turn-1" }]);
});
```

In `conformance.test.ts`, load `../generated/conformance/*.json` using `import.meta.glob(..., { eager:true, import:"default" })`, validate each as `ConformanceCase`, replay all inputs, and compare every complete transition to the corresponding expected transition.

- [ ] **Step 2: Run and verify RED**

Run: `npm run test --workspace @dayorder/web -- src/agent/runtime/reducer.test.ts src/agent/runtime/conformance.test.ts`

Expected: FAIL because `reducer.ts` does not exist.

- [ ] **Step 3: Implement the exhaustive pure reducer**

```ts
export function advance(state: RuntimeState, input: RuntimeInput): RuntimeTransition {
  validateProtocol<RuntimeState>("RuntimeState", state);
  validateProtocol<RuntimeInput>("RuntimeInput", input);
  if (["completed", "failed", "cancelled"].includes(state.phase)) return protocolFailure(state, "terminal_state");
  switch (input.type) {
    case "user_message": return onUserMessage(state, input.text!);
    case "provider_event": return onProviderEvent(state, input.providerEvent!);
    case "tool_resolution": return onToolResolution(state, input.toolResolution!);
    case "tool_result": return onToolResult(state, input.toolResult!);
    case "approval_response": return onApprovalResponse(state, input.approvalResponse!);
    case "runtime_error": return failed(state, input.error!);
    case "cancel": return cancelled(state, input.reason ?? "cancelled");
  }
}
```

Generate Turn IDs from the next sequence (`turn-${sequence}`), never from clock/random. Canonicalize Tool input by recursively sorting object keys. Enforce legal phase/input pairs, call/approval ID matching, total-token budget, step budget, and repeated-call threshold. Validate every returned transition.

- [ ] **Step 4: Verify**

```powershell
npm run test --workspace @dayorder/web -- src/agent/runtime/reducer.test.ts src/agent/runtime/conformance.test.ts
npm run typecheck --workspace @dayorder/web
```

Expected: all six fixtures and focused tests pass.

- [ ] **Step 5: Commit**

```powershell
git add apps/web/src/agent/runtime
git commit -m "feat(agent): add foreground runtime reducer"
```

### Task 6: Go pure Runtime reducer

**Files:**
- Create: `apps/api/internal/agentruntime/reducer.go`
- Create: `apps/api/internal/agentruntime/reducer_test.go`
- Create: `apps/api/internal/agentruntime/conformance_test.go`

**Interfaces:**
- Produces: `NewState(config Config) agentprotocol.RuntimeState`
- Produces: `Advance(state agentprotocol.RuntimeState, input agentprotocol.RuntimeInput) (agentprotocol.RuntimeTransition, error)`
- Produces: `CanonicalToolFingerprint(call agentprotocol.ToolCall) (string, error)`

- [ ] **Step 1: Write failing reducer and canonical-fixture tests**

```go
func TestAdvanceStartsModelTurnWithoutMutatingInput(t *testing.T) {
	state := NewState(testConfig())
	before := mustJSON(t, state)
	transition, err := Advance(state, agentprotocol.RuntimeInput{Type: "user_message", Text: ptr("plan today")})
	if err != nil { t.Fatal(err) }
	if got := mustJSON(t, state); got != before { t.Fatalf("state mutated\nbefore=%s\nafter=%s", before, got) }
	if transition.State.Phase != "model_pending" || transition.Effects[0].Type != "request_model_turn" { t.Fatalf("transition = %#v", transition) }
}
```

The conformance test walks four parents from the package directory to the repository root, sorts `contracts/agent/conformance/*.json`, validates each case, replays inputs, JSON-normalizes actual/expected transitions, and compares them byte-for-byte.

- [ ] **Step 2: Run and verify RED**

Run: `go test ./apps/api/internal/agentruntime -run "TestAdvance|TestConformance" -v`

Expected: FAIL because package `agentruntime` does not exist.

- [ ] **Step 3: Implement matching deterministic semantics**

Deep-copy state with a JSON round trip before changing it. Use the same exhaustive input cases, deterministic IDs, sorted-key fingerprint, phase checks, budgets, and stable errors as Task 5:

```go
func Advance(state agentprotocol.RuntimeState, input agentprotocol.RuntimeInput) (agentprotocol.RuntimeTransition, error) {
	if err := agentprotocol.Validate(agentprotocol.DefinitionRuntimeState, state); err != nil { return agentprotocol.RuntimeTransition{}, err }
	if err := agentprotocol.Validate(agentprotocol.DefinitionRuntimeInput, input); err != nil { return agentprotocol.RuntimeTransition{}, err }
	next, err := cloneState(state)
	if err != nil { return agentprotocol.RuntimeTransition{}, err }
	transition, err := dispatch(next, input)
	if err != nil { return agentprotocol.RuntimeTransition{}, err }
	if err = agentprotocol.Validate(agentprotocol.DefinitionRuntimeTransition, transition); err != nil { return agentprotocol.RuntimeTransition{}, err }
	return transition, nil
}
```

- [ ] **Step 4: Verify**

Run: `go test ./apps/api/internal/agentprotocol ./apps/api/internal/agentruntime`

Expected: all six shared fixtures and focused tests pass.

- [ ] **Step 5: Commit**

```powershell
git add apps/api/internal/agentruntime
git commit -m "feat(agent): add background runtime reducer"
```

### Task 7: TypeScript Tool registry and capability intersection

**Files:**
- Create: `apps/web/src/agent/tool/registry.ts`
- Create: `apps/web/src/agent/tool/registry.test.ts`

**Interfaces:**
- Produces: `ToolContext { runId: string; callId: string; signal: AbortSignal }`
- Produces: `ToolBinding { spec: ToolSpec; invoke(input: Record<string, unknown>, context: ToolContext): Promise<ToolResult> }`
- Produces: `ToolRegistry.register`, `resolve`, `specs`
- Produces: `effectiveToolIDs(requested, registry, snapshot, policy): string[]`
- Policy: `{ allow: string[]; deny: string[]; approvalFor: SideEffect[] }`; deny wins

- [ ] **Step 1: Write failing registry tests**

```ts
it("intersects requests, bindings, runtime, scope, and policy", () => {
  const registry = new ToolRegistry("foreground");
  registry.register(fakeBinding(toolSpec("dayorder.notes.search", "read", ["notes"], ["client", "server"])));
  registry.register(fakeBinding(toolSpec("device.calendar.read", "read", ["calendar"], ["client"])));
  expect(effectiveToolIDs(
    ["missing.tool", "device.calendar.read", "dayorder.notes.search"], registry,
    snapshot({ domains: ["notes"] }), { allow: ["*"], deny: [], approvalFor: [] },
  )).toEqual(["dayorder.notes.search"]);
});
```

Also assert duplicate IDs and Schema-invalid ToolSpecs are rejected and returned lists cannot mutate registry state.

- [ ] **Step 2: Run and verify RED**

Run: `npm run test --workspace @dayorder/web -- src/agent/tool/registry.test.ts`

Expected: FAIL because Tool Registry does not exist.

- [ ] **Step 3: Implement deterministic registration and intersection**

```ts
export function effectiveToolIDs(requested, registry, snapshot, policy): string[] {
  const allowed = new Set(policy.allow);
  const denied = new Set(policy.deny);
  return [...new Set(requested)].filter((id) => {
    const binding = registry.resolve(id);
    if (!binding || denied.has(id) || !(allowed.has("*") || allowed.has(id))) return false;
    if (!supportsMode(binding.spec.executionTargets, snapshot.executionMode)) return false;
    return binding.spec.requiredDomains.every((domain) => snapshot.scope.domains.includes(domain));
  }).sort();
}
```

Validate ToolSpec on registration. Map foreground to client and background to server. Return copied, Tool-ID-sorted values. Do not add Fetch, application data, or Provider code.

- [ ] **Step 4: Verify**

```powershell
npm run test --workspace @dayorder/web -- src/agent/tool/registry.test.ts
npm run typecheck --workspace @dayorder/web
```

Expected: tests and typecheck pass.

- [ ] **Step 5: Commit**

```powershell
git add apps/web/src/agent/tool
git commit -m "feat(agent): add foreground tool registry"
```

### Task 8: Go Tool registry and capability intersection

**Files:**
- Create: `apps/api/internal/agenttool/registry.go`
- Create: `apps/api/internal/agenttool/registry_test.go`

**Interfaces:**
- Produces: `Context { RunID string; CallID string }`
- Produces: `Binding.Spec() agentprotocol.ToolSpec`
- Produces: `Binding.Invoke(context.Context, map[string]any, Context) (agentprotocol.ToolResult, error)`
- Produces: `Registry.Register`, `Resolve`, `Specs`, and `EffectiveToolIDs`

- [ ] **Step 1: Write failing parity tests**

Use table tests for sorted output, deny precedence, runtime mismatch, Scope mismatch, duplicate ID, invalid ToolSpec, and returned-slice isolation:

```go
got := EffectiveToolIDs(
	[]string{"missing.tool", "device.calendar.read", "dayorder.notes.search"}, registry,
	snapshot([]string{"notes"}), Policy{Allow: []string{"*"}},
)
if !slices.Equal(got, []string{"dayorder.notes.search"}) { t.Fatalf("got %v", got) }
```

- [ ] **Step 2: Run and verify RED**

Run: `go test ./apps/api/internal/agenttool -v`

Expected: FAIL because package `agenttool` does not exist.

- [ ] **Step 3: Implement the Go-only abstraction**

Validate ToolSpec on registration, protect the map with `sync.RWMutex`, reject duplicates, copy all returned slices, and sort ToolSpecs/IDs. Match Task 7 wildcard and deny rules. This package must not import HTTP, Application Service, Repository, database, or Provider packages.

- [ ] **Step 4: Verify**

Run: `go test ./apps/api/internal/agentprotocol ./apps/api/internal/agenttool`

Expected: all tests pass.

- [ ] **Step 5: Commit**

```powershell
git add apps/api/internal/agenttool
git commit -m "feat(agent): add background tool registry"
```

### Task 9: Shared Skill fixtures and TypeScript Skill foundation

**Files:**
- Create: `contracts/agent/fixtures/skills/calendar-management/SKILL.md`
- Create: `contracts/agent/fixtures/skills/calendar-management/references/usage.md`
- Create: `contracts/agent/fixtures/skills/invalid-script/SKILL.md`
- Create: `contracts/agent/fixtures/skills/invalid-script/scripts/run.js`
- Modify: `scripts/generate-agent-contracts.mjs`
- Create: `apps/web/src/agent/skill/profile.ts`, `profile.test.ts`
- Create: `apps/web/src/agent/skill/registry.ts`, `registry.test.ts`
- Create: `apps/web/src/agent/skill/tools.ts`, `tools.test.ts`

**Interfaces:**
- Produces: `SupportingFile`, `SkillBundleInput`, `SkillProfile`
- Produces: `parseSkillBundle(input): Promise<SkillProfile>`
- Produces: `SkillRegistry.list`, `listForModel`, `load`, `loadForModel`, `activate`
- Produces read-only ToolBindings `skill_list` and `skill_load`

Use this exact valid `SKILL.md`, including its final LF:

```markdown
---
name: calendar-management
description: Read and propose changes to DayOrder calendar data.
version: 1.0.0
allowed-tools:
  - dayorder.calendar.read
  - dayorder.calendar.propose-change
execution-target: either
background-allowed: true
user-invocable: true
disable-model-invocation: false
min-runtime-version: 1.0.0
risk-level: medium
---
# Calendar Management

Read only the requested date range.
Treat calendar writes as proposals and never apply a change without the required confirmation.
```

Use this exact `references/usage.md`, including its final LF:

```markdown
# Usage

Use `dayorder.calendar.read` before proposing a calendar change.
```

The Digest input is the raw SKILL.md bytes followed by sorted repetitions of `relative path + NUL byte + file bytes`. The expected SHA-256 is `f2c538af41506502037bc4a1d83e4abb220b2ed1bb8207d461793ef257dee739`. The invalid-script bundle uses the same valid Frontmatter and body and contains `scripts/run.js` with exactly `throw new Error("must not execute");` plus a final LF; the parser must reject its path before reading or hashing script content.

The meta Tool contracts are fixed:

```ts
skill_list: input {}; output { skills: SkillDescriptor[] }
skill_load: input { name: string }; output { activation: SkillActivation }
```

Both ToolSpecs are read-only, client/server capable, idempotent, approval `never`, require no data domains, use a 5-second timeout, and cap results at 256 KiB.

- [ ] **Step 1: Write failing progressive-disclosure and safety tests**

```ts
it("lists descriptors without bodies and loads one body", async () => {
  const profile = await parseSkillBundle(validBundle);
  const registry = new SkillRegistry([profile]);
  expect(registry.list()).toEqual([expect.objectContaining({ name: "calendar-management", scope: "system" })]);
  expect(JSON.stringify(registry.list())).not.toContain("Read only the requested date range");
  expect(registry.load("calendar-management")?.body).toContain("Read only the requested date range");
});

it("does not turn allowed-tools into permission", () => {
  const activation = registry.activate("calendar-management", noCalendarScope, tools, allowAllPolicy);
  expect(activation.requestedToolIds).toContain("dayorder.calendar.read");
  expect(activation.activeToolIds).toEqual([]);
});
```

Also reject missing Frontmatter/body, invalid name/SemVer, duplicates, absolute/parent paths, more than 32 files, files over 256 KiB, total files over 1 MiB, and every `scripts/` path.

- [ ] **Step 2: Run and verify RED**

Run: `npm run test --workspace @dayorder/web -- src/agent/skill`

Expected: FAIL because Skill modules do not exist.

- [ ] **Step 3: Implement parsing, digest, registry, and meta-tools**

Parse an anchored YAML Frontmatter block with `yaml.parse`, validate `SkillManifest`, normalize/sort file paths, and hash raw UTF-8 plus each `path + NUL + content` with `crypto.subtle.digest("SHA-256", ...)`. `scope` comes from `SkillBundleInput`, never Frontmatter.

Registry output is sorted and copied. `activate` rejects incompatible Runtime version/execution target and rejects `background-allowed:false` in Background mode before delegating Tool calculation to Task 7. `listForModel` filters `disable-model-invocation:true`; `loadForModel` rejects it, while the ordinary `list`/`load` methods remain available for a future user-invoked UI. `skill_list` and `skill_load` use only the model-filtered methods, return descriptors/instructions plus requested and effective Tool IDs, and never grant permission. Extend generation to copy only the canonical fixture bundles to Web generated assets; the copier uses `lstat`, rejects symbolic links, and never follows them.

- [ ] **Step 4: Verify**

```powershell
npm run agent:generate
npm run test --workspace @dayorder/web -- src/agent/skill src/agent/tool
npm run agent:generate:check
npm run typecheck --workspace @dayorder/web
```

Expected: all commands exit 0.

- [ ] **Step 5: Commit**

```powershell
git add contracts/agent/fixtures scripts/generate-agent-contracts.mjs apps/web/src/agent/generated apps/web/src/agent/skill
git commit -m "feat(agent): add foreground skill registry"
```

### Task 10: Go Skill parser, registry, and meta-tools

**Files:**
- Create: `apps/api/internal/agentskill/profile.go`, `profile_test.go`
- Create: `apps/api/internal/agentskill/registry.go`, `registry_test.go`
- Create: `apps/api/internal/agentskill/tools.go`, `tools_test.go`

**Interfaces:**
- Produces Go forms of `SupportingFile`, `SkillBundleInput`, `SkillProfile`
- Produces: `ParseBundle(input SkillBundleInput) (SkillProfile, error)`
- Produces: `Registry.List`, `ListForModel`, `Load`, `LoadForModel`, `Activate`
- Produces: `MetaBindings(registry, snapshot, tools, policy) []agenttool.Binding`

- [ ] **Step 1: Write failing parity tests against shared fixtures**

Walk four parents to `contracts/agent/fixtures/skills`, read the valid bundle, and run the same valid/invalid cases as Task 9. Fix the expected SHA-256 Digest in both TypeScript and Go tests so byte-order drift is visible. Assert `skill_list` omits Markdown instructions and `skill_load` cannot activate a Tool outside Run Scope:

```go
activation, err := registry.Activate("calendar-management", noCalendarScope, tools, allowAllPolicy)
if err != nil { t.Fatal(err) }
if len(activation.ActiveToolIDs) != 0 || !slices.Contains(activation.RequestedToolIDs, "dayorder.calendar.read") {
	t.Fatalf("activation = %#v", activation)
}
```

- [ ] **Step 2: Run and verify RED**

Run: `go test ./apps/api/internal/agentskill -v`

Expected: FAIL because package `agentskill` does not exist.

- [ ] **Step 3: Implement without filesystem discovery**

Use `yaml.v3` for Frontmatter, normalize YAML maps into JSON-compatible maps, validate through `agentprotocol.Validate`, enforce the same byte/path/file limits, sort supporting files, and hash the exact byte sequence from Task 9. `ParseBundle` receives content explicitly and never scans home/project/plugin directories.

Implement the same Runtime-version, execution-target, background-allowed, and disable-model-invocation behavior as Task 9. Implement `skill_list` and `skill_load` as `agenttool.Binding` values with the same ToolSpecs and ToolResult JSON shapes as TypeScript. Validate their results before return and never emit a permission grant.

- [ ] **Step 4: Verify**

Run: `go test ./apps/api/internal/agentprotocol ./apps/api/internal/agenttool ./apps/api/internal/agentskill`

Expected: all tests pass and the shared Skill Digest equals the TypeScript expected value.

- [ ] **Step 5: Commit**

```powershell
git add apps/api/internal/agentskill apps/api/go.mod apps/api/go.sum
git commit -m "feat(agent): add backend skill registry"
```

### Task 11: TypeScript Provider contract, scripted mock, and ReAct driver

**Files:**
- Create: `apps/web/src/agent/provider/provider.ts`
- Create: `apps/web/src/agent/provider/mock.ts`
- Create: `apps/web/src/agent/runtime/driver.ts`
- Create: `apps/web/src/agent/runtime/driver.test.ts`

**Interfaces:**
- Produces: `ProviderGateway.stream(request, signal): AsyncIterable<ProviderEvent>`
- Produces: `ScriptedProvider(scripts: ProviderEvent[][])` for tests only
- Produces: `ApprovalBroker.request(approvalId, call, signal): Promise<"allow" | "deny">`
- Produces: `driveToCompletion(initialState, initialInput, host, signal): Promise<RuntimeTrace>`
- Runtime Host contains a server-defined `modelProfile` string; it never accepts a Provider URL or key
- `RuntimeTrace` contains final state, normalized inputs, effects, and model-turn count

- [ ] **Step 1: Write a failing two-turn mock test**

```ts
it("drives model to tool and back without HTTP", async () => {
  const provider = new ScriptedProvider([
    [{ type: "tool_call", call: { id: "call-1", name: "test.clock.read", input: {} } }],
    [{ type: "text_delta", text: "done" }, { type: "completed", stopReason: "end_turn", usage: usage(8, 2) }],
  ]);
  const tools = new ToolRegistry("foreground");
  tools.register(clockBinding({ now: "09:00" }));
  const trace = await driveToCompletion(createRuntimeState(config), { type: "user_message", text: "time?" },
    { provider, tools, approvals: allowAllApprovals }, new AbortController().signal);
  expect(trace.modelTurns).toBe(2);
  expect(trace.state.phase).toBe("completed");
  expect(trace.effects.map((value) => value.type)).toEqual([
    "request_model_turn", "resolve_tool", "execute_tool", "request_model_turn", "emit_text", "complete_run",
  ]);
});
```

Add cancellation, missing Binding, denied approval, and output-Schema failure cases.

- [ ] **Step 2: Run and verify RED**

Run: `npm run test --workspace @dayorder/web -- src/agent/runtime/driver.test.ts`

Expected: FAIL because Provider and Driver modules do not exist.

- [ ] **Step 3: Implement host-only Effect execution**

The Driver repeatedly invokes `advance`, records normalized inputs/effects, and handles:

- `request_model_turn`: build `ModelTurnRequest` from messages and effective ToolSpecs, then consume Provider events;
- `resolve_tool`: feed execute, approval, unavailable, or denied resolution from Registry/policy;
- `execute_tool`: validate call input against the Binding's `inputSchema`, invoke with AbortSignal, validate ToolResult `data` against `outputSchema`, enforce `resultMaxBytes`, and feed it back;
- `request_approval`: call ApprovalBroker and feed its response;
- terminal/UI effects: record only, with no UI, HTTP, or persistence in Phase 1.

`ScriptedProvider` copies scripts, counts requests, yields in order, returns `provider_unavailable` when exhausted, and stops when aborted. The Driver assigns `approval-${toolCall.id}` deterministically and combines the caller AbortSignal with a Host deadline of `budget.maxDurationMs`; deadline expiry feeds a `timeout` failure. Do not add Fetch, SSE, vendor SDK, environment variable, or API-key code.

- [ ] **Step 4: Verify**

```powershell
npm run test --workspace @dayorder/web -- src/agent
npm run typecheck --workspace @dayorder/web
```

Expected: all frontend Agent component tests pass.

- [ ] **Step 5: Commit**

```powershell
git add apps/web/src/agent/provider apps/web/src/agent/runtime/driver.ts apps/web/src/agent/runtime/driver.test.ts
git commit -m "feat(agent): drive foreground mock react turns"
```

### Task 12: Go Provider stream contract, scripted mock, and ReAct driver

**Files:**
- Create: `apps/api/internal/agentprovider/stream.go`
- Create: `apps/api/internal/agentprovider/mock.go`
- Create: `apps/api/internal/agentruntime/driver.go`
- Create: `apps/api/internal/agentruntime/driver_test.go`

**Interfaces:**
- Produces: `StreamProvider.Stream(context.Context, ModelTurnRequest) iter.Seq2[ProviderEvent, error]`
- Produces: `agentprovider.NewScriptedProvider(scripts)` for tests only
- Produces: `ApprovalBroker.Request(context.Context, approvalID, call) (Decision, error)`
- Produces: `Driver.Run(context.Context, initialState, initialInput) (Trace, error)`
- Driver construction requires a server-defined model Profile and rejects empty values or URL-like strings
- Leaves legacy `HTTPProvider.Analyze` and `service.AgentProvider` disconnected

- [ ] **Step 1: Write failing two-turn and cancellation tests**

Mirror Task 11 and assert the exact Effect sequence:

```go
want := []string{"request_model_turn", "resolve_tool", "execute_tool", "request_model_turn", "emit_text", "complete_run"}
if trace.ModelTurns != 2 || trace.State.Phase != "completed" || !slices.Equal(effectTypes(trace.Effects), want) {
	t.Fatalf("trace = %#v", trace)
}
```

Cancel Context during the first Provider stream and assert the Runtime ends cancelled without consuming later scripted events.

- [ ] **Step 2: Run and verify RED**

Run: `go test ./apps/api/internal/agentruntime ./apps/api/internal/agentprovider -run "TestDriver|TestScripted" -v`

Expected: FAIL because stream and Driver types do not exist.

- [ ] **Step 3: Implement the server-only stream contract and Driver**

```go
type StreamProvider interface {
	Stream(context.Context, agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error]
}
```

Implement the same Effect table, deterministic `approval-${toolCall.id}`, and dynamic Tool input/output Schema and result-byte validation as Task 11. Wrap execution in `context.WithTimeout` using `budget.maxDurationMs`; caller cancellation feeds `cancelled`, while deadline expiry feeds `timeout`. Binding failures become validated `tool_failed` ToolResults; terminal Provider failures become stable provider errors. Do not call HTTP, database, Repository, Application Service, Outbox, or the legacy one-shot Provider.

- [ ] **Step 4: Verify**

Run: `go test ./apps/api/internal/agentprotocol ./apps/api/internal/agenttool ./apps/api/internal/agentskill ./apps/api/internal/agentprovider ./apps/api/internal/agentruntime`

Expected: all tests pass, including existing legacy `agentprovider` tests.

- [ ] **Step 5: Commit**

```powershell
git add apps/api/internal/agentprovider/stream.go apps/api/internal/agentprovider/mock.go apps/api/internal/agentruntime/driver.go apps/api/internal/agentruntime/driver_test.go
git commit -m "feat(agent): drive background mock react turns"
```

### Task 13: Frozen multi-Agent worker contracts

**Files:**
- Create: `apps/web/src/agent/worker/spawn.ts`
- Create: `apps/web/src/agent/worker/spawn.test.ts`
- Create: `apps/api/internal/agentworker/spawn.go`
- Create: `apps/api/internal/agentworker/spawn_test.go`

**Interfaces:**
- TypeScript: `createWorkerSpawnSpec(parent, request): Readonly<WorkerSpawnSpec>`, `validateWorkerResult(result, contract)`
- Go: `FreezeSpawnSpec(parent, request) (agentprotocol.WorkerSpawnSpec, error)`, `ValidateResult(result, contract) error`
- Enforces subset Skills/Tools, inherited mode, remaining-budget ceiling, depth one, no `skill_list`/`skill_load`
- Result sections: `summary`, `findings`, `risks`, `next_steps`

- [ ] **Step 1: Write failing subset and immutability tests**

```ts
const spec = createWorkerSpawnSpec(parent, validRequest);
expect(Object.isFrozen(spec)).toBe(true);
expect(() => createWorkerSpawnSpec(parent, { ...validRequest, toolIds: ["missing.tool"] })).toThrow(/capability_unavailable/);
expect(() => createWorkerSpawnSpec(parent, { ...validRequest, executionMode: "background" })).toThrow(/execution mode/);
```

```go
spawn, err := FreezeSpawnSpec(parent, request)
if err != nil { t.Fatal(err) }
request.ToolIDs[0] = "mutated.tool"
if spawn.Capabilities.ToolIDs[0] != "dayorder.notes.search" { t.Fatal("spawn aliases caller slice") }
```

Both suites reject excess budgets, depth above one, unknown Skill Digest, missing result sections, and meta-tools in a child.

- [ ] **Step 2: Run and verify RED**

```powershell
npm run test --workspace @dayorder/web -- src/agent/worker/spawn.test.ts
go test ./apps/api/internal/agentworker -v
```

Expected: both commands fail because worker modules do not exist.

- [ ] **Step 3: Implement frozen capability shaping**

Intersect capabilities and deep-copy every array/object/map. Validate the final WorkerSpawnSpec with the shared protocol. The parent injects selected Skill bodies; the Worker receives no Registry or Skill meta-tools. Validate WorkerResult against JSON Schema and exact ordered sections.

- [ ] **Step 4: Verify**

```powershell
npm run test --workspace @dayorder/web -- src/agent
go test ./apps/api/internal/agentprotocol ./apps/api/internal/agentruntime ./apps/api/internal/agenttool ./apps/api/internal/agentskill ./apps/api/internal/agentprovider ./apps/api/internal/agentworker
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```powershell
git add apps/web/src/agent/worker apps/api/internal/agentworker
git commit -m "feat(agent): freeze subagent capabilities"
```

### Task 14: Pure projection to existing AgentRun status

**Files:**
- Create: `apps/web/src/agent/runtime/projection.ts`
- Create: `apps/web/src/agent/runtime/projection.test.ts`
- Create: `apps/api/internal/agentruntime/projection.go`
- Create: `apps/api/internal/agentruntime/projection_test.go`

**Interfaces:**
- TypeScript: `projectRun(state: RuntimeState): RuntimeRunProjection`
- Go: `ProjectRun(state agentprotocol.RuntimeState) (RunProjection, error)`
- Projection fields: `status`, optional `errorCode`, optional `errorMessage`
- Does not persist, emit HTTP, create AgentStep/AgentChange/SourceRef, or modify current model types

Use this exact phase mapping:

```text
idle                                      -> ready
model_pending, model_streaming, tool_pending -> analyzing
approval_pending                          -> waiting
completed                                 -> completed
failed                                    -> failed
cancelled                                 -> stopped
```

- [ ] **Step 1: Write failing mapping tests in both languages**

```ts
it.each([
  ["idle", "ready"], ["model_pending", "analyzing"], ["model_streaming", "analyzing"],
  ["tool_pending", "analyzing"], ["approval_pending", "waiting"], ["completed", "completed"],
  ["failed", "failed"], ["cancelled", "stopped"],
])("projects %s to %s", (phase, status) => {
  expect(projectRun({ ...baseState, phase } as RuntimeState).status).toBe(status);
});
```

The Go table uses the same eight rows. Both suites assert that failed projections copy the stable Runtime error code/message and non-failed projections omit both.

- [ ] **Step 2: Run and verify RED**

```powershell
npm run test --workspace @dayorder/web -- src/agent/runtime/projection.test.ts
go test ./apps/api/internal/agentruntime -run TestProjectRun -v
```

Expected: both commands fail because projection modules do not exist.

- [ ] **Step 3: Implement pure, exhaustive mapping**

Validate RuntimeState before mapping. Use exhaustive switches; unknown phases return `protocol_incompatible` instead of defaulting to analyzing. Keep this package free of API, database, Outbox, UI, and persistence calls. Document in code that AgentStep, AgentChange, SourceRef, and database persistence are Phase 2 adapters rather than hidden side effects of projection.

- [ ] **Step 4: Verify**

```powershell
npm run test --workspace @dayorder/web -- src/agent/runtime/projection.test.ts
go test ./apps/api/internal/agentruntime
```

Expected: all projection and existing Runtime tests pass.

- [ ] **Step 5: Commit**

```powershell
git add apps/web/src/agent/runtime/projection.ts apps/web/src/agent/runtime/projection.test.ts apps/api/internal/agentruntime/projection.go apps/api/internal/agentruntime/projection_test.go
git commit -m "feat(agent): project runtime status safely"
```

### Task 15: Architecture guards, developer documentation, and full verification

**Files:**
- Create: `scripts/lib/agent-architecture-rules.mjs`
- Create: `scripts/agent-architecture-rules.test.mjs`
- Modify: `scripts/validate-architecture.mjs`
- Modify: `README.md`
- Modify: `package.json`, `package-lock.json`

**Interfaces:**
- Produces: `validateAgentArchitecture(input): string[]`
- Adds: `npm run test:agent-foundation`
- Documents generation, focused tests, generated ownership, and production-off boundary

- [ ] **Step 1: Write failing pure-rule tests**

```js
test("rejects credentials, vendor SDKs, and production wiring", () => {
  const failures = validateAgentArchitecture({
    webPackage: { dependencies: { openai: "1.0.0" } },
    webAgentSources: ["const key = import.meta.env.VITE_OPENAI_API_KEY"],
    appSource: "export default function App({ agentAvailable = true }) {}",
    agentHandlerSource: "router.writeError(response, request, 200, 'OK')",
    workerMainSource: "worker.NewAgentHandler(processor)",
  });
  assert.ok(failures.some((value) => value.includes("vendor SDK")));
  assert.ok(failures.some((value) => value.includes("provider key")));
  assert.ok(failures.some((value) => value.includes("disabled")));
  assert.ok(failures.some((value) => value.includes("Worker")));
});
```

Add a passing case with Ajv/YAML, `agentAvailable = false`, `AGENT_NOT_AVAILABLE`, and no Worker registration.

- [ ] **Step 2: Run and verify RED**

Run: `node --test scripts/agent-architecture-rules.test.mjs`

Expected: FAIL because the pure rules module does not exist.

- [ ] **Step 3: Implement and wire the guards**

Reject Web dependencies `openai`, `@anthropic-ai/sdk`, `@google/generative-ai`, `@google/genai`, and `cohere-ai`. Reject provider-key environment names and literal vendor bearer keys below `apps/web/src/agent`. Require `App.tsx` to retain `agentAvailable = false`, require `agent_handlers.go` to contain `AGENT_NOT_AVAILABLE`, and reject `NewAgentHandler` or Agent event registration from `cmd/worker/main.go`.

Recursively read only `.ts`/`.tsx` files under `apps/web/src/agent`, call the pure rule from `validate-architecture.mjs`, and append its failures. Add:

```json
"test:agent-foundation": "node --test scripts/agent-contracts.test.mjs scripts/agent-architecture-rules.test.mjs && npm run test --workspace @dayorder/web -- src/agent && go test ./apps/api/internal/agentprotocol ./apps/api/internal/agentruntime ./apps/api/internal/agenttool ./apps/api/internal/agentskill ./apps/api/internal/agentprovider ./apps/api/internal/agentworker"
```

README documents commands and boundaries without Provider-key setup or a claim that Agent is available.

- [ ] **Step 4: Run all Phase 1 verification**

```powershell
npm run agent:generate:check
npm run test:agent-foundation
npm run test:architecture
npm run test:web
npm run typecheck
npm run build:web
npm run test:api
npm run build:api
git diff --check
```

Expected: every command exits 0. Do not run or claim Phase 2 database/provider/end-to-end integration coverage.

Verify production remains off:

```powershell
rg -n "agentAvailable = false|AGENT_NOT_AVAILABLE" apps/web/src/App.tsx apps/api/internal/httpapi/agent_handlers.go
rg -n "NewAgentHandler|agent.run.requested" apps/api/cmd/worker/main.go
```

Expected: the first command finds both guards; the second returns no matches.

- [ ] **Step 5: Commit**

```powershell
git add scripts/lib/agent-architecture-rules.mjs scripts/agent-architecture-rules.test.mjs scripts/validate-architecture.mjs README.md package.json package-lock.json
git commit -m "chore(agent): enforce foundation boundaries"
```

## Completion Evidence

Before declaring Phase 1 complete, record:

- commit hashes for Tasks 1–15;
- exact `npm run agent:generate:check` output;
- Web Agent and Go Agent package test counts;
- all six Conformance Case names passing in both runtimes;
- `test:architecture` output proving Provider and production-wiring boundaries;
- a diff against the plan starting commit showing no migration, real Provider, domain Tool, Worker registration, or Agent enablement.
