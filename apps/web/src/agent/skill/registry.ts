import type {
  CapabilitySnapshot,
  SkillActivation,
  SkillDescriptor,
  SkillManifest,
} from "../generated/protocol";
import { validateProtocol } from "../protocol/validate";
import { effectiveToolIDs, type ToolPolicy, type ToolRegistry } from "../tool/registry";
import type { SkillProfile } from "./profile";

function copyProfile(profile: SkillProfile): SkillProfile {
  return {
    manifest: structuredClone(profile.manifest),
    descriptor: structuredClone(profile.descriptor),
    body: profile.body,
    supportingFiles: profile.supportingFiles.map((file) => ({
      path: file.path,
      kind: file.kind,
      size: file.size,
      content: new Uint8Array(file.content),
    })),
  };
}

function assertProfile(profile: SkillProfile): void {
  validateProtocol<SkillManifest>("SkillManifest", profile.manifest);
  validateProtocol<SkillDescriptor>("SkillDescriptor", profile.descriptor);
  if (profile.descriptor.scope !== "system") throw new Error("phase one accepts system Skills only");
  if (!/^[0-9a-f]{64}$/.test(profile.descriptor.digest)) throw new Error("invalid Skill digest");
  if (
    profile.descriptor.name !== profile.manifest.name
    || profile.descriptor.version !== profile.manifest.version
    || profile.descriptor.description !== profile.manifest.description
    || profile.descriptor.executionTarget !== profile.manifest["execution-target"]
    || profile.descriptor.backgroundAllowed !== profile.manifest["background-allowed"]
    || profile.descriptor.userInvocable !== profile.manifest["user-invocable"]
    || profile.descriptor.disableModelInvocation !== profile.manifest["disable-model-invocation"]
    || profile.descriptor.riskLevel !== profile.manifest["risk-level"]
  ) {
    throw new Error("Skill profile descriptor does not match its manifest");
  }
}

interface ParsedSemVer {
  core: [number, number, number];
  prerelease: string[];
}

function parsedSemVer(value: string): ParsedSemVer {
  const [withoutBuild] = value.split("+");
  const [core, prerelease = ""] = withoutBuild.split("-", 2);
  const parts = core.split(".").map(Number);
  return { core: [parts[0], parts[1], parts[2]], prerelease: prerelease === "" ? [] : prerelease.split(".") };
}

function compareSemVer(left: string, right: string): number {
  const a = parsedSemVer(left);
  const b = parsedSemVer(right);
  for (let index = 0; index < 3; index += 1) {
    if (a.core[index] !== b.core[index]) return a.core[index] < b.core[index] ? -1 : 1;
  }
  if (a.prerelease.length === 0 || b.prerelease.length === 0) {
    return a.prerelease.length === b.prerelease.length ? 0 : a.prerelease.length === 0 ? 1 : -1;
  }
  const length = Math.max(a.prerelease.length, b.prerelease.length);
  for (let index = 0; index < length; index += 1) {
    const leftPart = a.prerelease[index];
    const rightPart = b.prerelease[index];
    if (leftPart === undefined || rightPart === undefined) return leftPart === undefined ? -1 : 1;
    if (leftPart === rightPart) continue;
    const leftNumber = /^\d+$/.test(leftPart);
    const rightNumber = /^\d+$/.test(rightPart);
    if (leftNumber && rightNumber) return Number(leftPart) < Number(rightPart) ? -1 : 1;
    if (leftNumber !== rightNumber) return leftNumber ? -1 : 1;
    return leftPart < rightPart ? -1 : 1;
  }
  return 0;
}

function assertCompatible(profile: SkillProfile, snapshot: CapabilitySnapshot): void {
  if (compareSemVer(snapshot.runtimeVersion, profile.manifest["min-runtime-version"]) < 0) {
    throw new Error(`Skill requires Runtime ${profile.manifest["min-runtime-version"]}`);
  }
  const requiredTarget = snapshot.executionMode === "foreground" ? "client" : "server";
  if (profile.descriptor.executionTarget !== "either" && profile.descriptor.executionTarget !== requiredTarget) {
    throw new Error(`Skill does not support ${snapshot.executionMode} execution`);
  }
  if (snapshot.executionMode === "background" && !profile.descriptor.backgroundAllowed) {
    throw new Error("Skill does not allow background execution");
  }
}

export class SkillRegistry {
  private readonly profiles = new Map<string, SkillProfile>();

  constructor(profiles: SkillProfile[]) {
    for (const source of profiles) {
      const profile = copyProfile(source);
      assertProfile(profile);
      if (this.profiles.has(profile.descriptor.name)) {
        throw new Error(`duplicate Skill name: ${profile.descriptor.name}`);
      }
      this.profiles.set(profile.descriptor.name, profile);
    }
  }

  list(): SkillDescriptor[] {
    return [...this.profiles.values()]
      .map(({ descriptor }) => structuredClone(descriptor))
      .sort((left, right) => left.name < right.name ? -1 : left.name > right.name ? 1 : 0);
  }

  listForModel(): SkillDescriptor[] {
    return this.list().filter((descriptor) => !descriptor.disableModelInvocation);
  }

  load(name: string): SkillProfile | undefined {
    const profile = this.profiles.get(name);
    return profile === undefined ? undefined : copyProfile(profile);
  }

  loadForModel(name: string): SkillProfile | undefined {
    const profile = this.profiles.get(name);
    if (profile === undefined || profile.descriptor.disableModelInvocation) return undefined;
    return copyProfile(profile);
  }

  activate(
    name: string,
    snapshot: CapabilitySnapshot,
    tools: ToolRegistry,
    policy: ToolPolicy,
  ): SkillActivation {
    const profile = this.profiles.get(name);
    if (!profile) throw new Error(`Skill not found: ${name}`);
    assertCompatible(profile, snapshot);

    const requestedToolIds = [...new Set(profile.manifest["allowed-tools"])].sort();
    const activation = validateProtocol<SkillActivation>("SkillActivation", {
      skill: {
        name: profile.descriptor.name,
        version: profile.descriptor.version,
        digest: profile.descriptor.digest,
      },
      instructions: profile.body,
      supportingFiles: profile.supportingFiles.map(({ path, kind, size }) => ({ path, kind, size })),
      requestedToolIds,
      activeToolIds: effectiveToolIDs(requestedToolIds, tools, snapshot, policy),
    });
    return structuredClone(activation);
  }
}
