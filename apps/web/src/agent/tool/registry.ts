import type {
  CapabilitySnapshot,
  ExecutionMode,
  SideEffect,
  ToolResult,
  ToolSpec,
} from "../generated/protocol";
import { validateProtocol } from "../protocol/validate";

export interface ToolContext {
  runId: string;
  callId: string;
  signal: AbortSignal;
}

export interface ToolBinding {
  spec: ToolSpec;
  invoke(input: Record<string, unknown>, context: ToolContext): Promise<ToolResult>;
}

export interface ToolPolicy {
  allow: string[];
  deny: string[];
  approvalFor: SideEffect[];
}

function copySpec(spec: ToolSpec): ToolSpec {
  return structuredClone(spec);
}

function copyBinding(binding: ToolBinding): ToolBinding {
  return { spec: copySpec(binding.spec), invoke: binding.invoke };
}

function supportsMode(targets: ToolSpec["executionTargets"], mode: ExecutionMode): boolean {
  return targets.includes(mode === "foreground" ? "client" : "server");
}

export class ToolRegistry {
  private readonly bindings = new Map<string, ToolBinding>();

  constructor(readonly mode: ExecutionMode) {}

  register(binding: ToolBinding): void {
    const spec = copySpec(binding.spec);
    validateProtocol<ToolSpec>("ToolSpec", spec);
    if (this.bindings.has(spec.id)) throw new Error(`duplicate tool ID: ${spec.id}`);
    this.bindings.set(spec.id, { spec, invoke: binding.invoke });
  }

  resolve(id: string): ToolBinding | undefined {
    const binding = this.bindings.get(id);
    return binding === undefined ? undefined : copyBinding(binding);
  }

  specs(): ToolSpec[] {
    return [...this.bindings.values()]
      .map((binding) => copySpec(binding.spec))
      .sort((left, right) => left.id.localeCompare(right.id));
  }
}

export function effectiveToolIDs(
  requested: string[],
  registry: ToolRegistry,
  snapshot: CapabilitySnapshot,
  policy: ToolPolicy,
): string[] {
  const allowed = new Set(policy.allow);
  const denied = new Set(policy.deny);
  const granted = new Set(snapshot.toolIds);

  return [...new Set(requested)]
    .filter((id) => {
      const binding = registry.resolve(id);
      if (!granted.has(id) || !binding || denied.has(id) || !(allowed.has("*") || allowed.has(id))) {
        return false;
      }
      if (!supportsMode(binding.spec.executionTargets, snapshot.executionMode)) return false;
      return binding.spec.requiredDomains.every((domain) => snapshot.scope.domains.includes(domain));
    })
    .sort();
}
