import { parse as parseYAML } from "yaml";

import type { SkillDescriptor, SkillManifest, SupportingFileDescriptor } from "../generated/protocol";
import { validateProtocol } from "../protocol/validate";

const MAX_BUNDLE_FILES = 32;
const MAX_FILE_BYTES = 256 * 1024;
const MAX_BUNDLE_BYTES = 1024 * 1024;
const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8", { fatal: true });

export interface SupportingFile {
  path: string;
  content: Uint8Array;
}

export interface SkillBundleInput {
  scope: SkillDescriptor["scope"];
  skillMarkdown: Uint8Array;
  supportingFiles: SupportingFile[];
}

export interface ProfileSupportingFile extends SupportingFile, SupportingFileDescriptor {}

export interface SkillProfile {
  manifest: SkillManifest;
  descriptor: SkillDescriptor;
  body: string;
  supportingFiles: ProfileSupportingFile[];
}

type SupportingKind = SupportingFileDescriptor["kind"];

interface CheckedPath {
  source: SupportingFile;
  path: string;
  kind: SupportingKind;
}

function compareUTF8(left: string, right: string): number {
  const leftBytes = encoder.encode(left);
  const rightBytes = encoder.encode(right);
  const length = Math.min(leftBytes.length, rightBytes.length);
  for (let index = 0; index < length; index += 1) {
    if (leftBytes[index] !== rightBytes[index]) return leftBytes[index] < rightBytes[index] ? -1 : 1;
  }
  return leftBytes.length < rightBytes.length ? -1 : leftBytes.length > rightBytes.length ? 1 : 0;
}

function checkedSupportingPath(source: SupportingFile): Omit<CheckedPath, "source"> {
  if (typeof source.path !== "string" || source.path.length === 0 || source.path.includes("\0")) {
    throw new Error("invalid supporting file path");
  }

  const slashed = source.path.replaceAll("\\", "/");
  if (slashed.startsWith("/") || /^[A-Za-z]:(?:\/|$)/.test(slashed)) {
    throw new Error(`absolute supporting file path is forbidden: ${source.path}`);
  }

  const segments = slashed.split("/");
  if (segments.some((segment) => segment === "..")) {
    throw new Error(`parent traversal is forbidden: ${source.path}`);
  }
  if (segments.some((segment) => segment.length === 0)) {
    throw new Error(`invalid supporting file path: ${source.path}`);
  }

  const normalized = segments.filter((segment) => segment !== ".").join("/");
  const [root, fileName] = normalized.split("/");
  if (root === "scripts") throw new Error(`scripts/ paths are forbidden: ${source.path}`);
  if (!fileName) throw new Error(`supporting file must be below a static directory: ${source.path}`);

  const kinds: Record<string, SupportingKind | undefined> = {
    references: "reference",
    assets: "asset",
    schemas: "schema",
  };
  const kind = kinds[root];
  if (!kind) throw new Error(`unsupported supporting file path: ${source.path}`);
  return { path: normalized, kind };
}

function checkedPaths(files: SupportingFile[]): CheckedPath[] {
  if (!Array.isArray(files)) throw new Error("supportingFiles must be an array");
  if (files.length + 1 > MAX_BUNDLE_FILES) throw new Error("too many bundle files (limit 32 including SKILL.md)");

  const seen = new Set<string>();
  const checked = files.map((source) => {
    const path = checkedSupportingPath(source);
    if (seen.has(path.path)) throw new Error(`duplicate supporting file path: ${path.path}`);
    seen.add(path.path);
    return { source, ...path };
  });
  return checked.sort((left, right) => compareUTF8(left.path, right.path));
}

function copyAndLimitFiles(checked: CheckedPath[], skillMarkdownBytes: number): ProfileSupportingFile[] {
  let total = skillMarkdownBytes;
  return checked.map(({ source, path, kind }) => {
    const value = source.content;
    if (Object.prototype.toString.call(value) !== "[object Uint8Array]") {
      throw new Error(`supporting file content must be bytes: ${path}`);
    }
    if (value.byteLength > MAX_FILE_BYTES) throw new Error(`supporting file is too large: ${path}`);
    total += value.byteLength;
    if (total > MAX_BUNDLE_BYTES) throw new Error("bundle files exceed total size limit");
    const content = Uint8Array.from(value);
    return { path, kind, size: content.byteLength, content };
  });
}

function splitSkillMarkdown(raw: Uint8Array): { manifest: SkillManifest; body: string } {
  if (!(raw instanceof Uint8Array)) throw new Error("skillMarkdown must be bytes");
  if (raw.byteLength > MAX_FILE_BYTES) throw new Error("SKILL.md is too large");

  const text = decoder.decode(raw);
  const matched = /^---\r?\n([\s\S]*?)\r?\n---\r?\n([\s\S]*)$/.exec(text);
  if (!matched) throw new Error("SKILL.md requires anchored YAML Frontmatter and a body");
  if (matched[2].trim().length === 0) throw new Error("SKILL.md body is required");

  const parsed = parseYAML(matched[1], { uniqueKeys: true });
  const manifest = validateProtocol<SkillManifest>("SkillManifest", parsed);
  return { manifest: structuredClone(manifest), body: matched[2] };
}

async function digestBundle(skillMarkdown: Uint8Array, files: ProfileSupportingFile[]): Promise<string> {
  const totalBytes = skillMarkdown.byteLength + files.reduce(
    (total, file) => total + encoder.encode(file.path).byteLength + 1 + file.content.byteLength,
    0,
  );
  const input = new Uint8Array(totalBytes);
  let offset = 0;
  input.set(skillMarkdown, offset);
  offset += skillMarkdown.byteLength;
  for (const file of files) {
    const path = encoder.encode(file.path);
    input.set(path, offset);
    offset += path.byteLength;
    input[offset] = 0;
    offset += 1;
    input.set(file.content, offset);
    offset += file.content.byteLength;
  }

  const digest = new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", input));
  return [...digest].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

export async function parseSkillBundle(input: SkillBundleInput): Promise<SkillProfile> {
  if (input.scope !== "system") throw new Error("phase one accepts system Skills only");

  // Validate every path before accessing any supporting-file content.
  const paths = checkedPaths(input.supportingFiles);
  const skillMarkdown = new Uint8Array(input.skillMarkdown);
  const { manifest, body } = splitSkillMarkdown(skillMarkdown);
  const supportingFiles = copyAndLimitFiles(paths, skillMarkdown.byteLength);
  const digest = await digestBundle(skillMarkdown, supportingFiles);
  const descriptor = validateProtocol<SkillDescriptor>("SkillDescriptor", {
    name: manifest.name,
    version: manifest.version,
    digest,
    description: manifest.description,
    scope: input.scope,
    executionTarget: manifest["execution-target"],
    backgroundAllowed: manifest["background-allowed"],
    userInvocable: manifest["user-invocable"],
    disableModelInvocation: manifest["disable-model-invocation"],
    riskLevel: manifest["risk-level"],
  });

  return {
    manifest: structuredClone(manifest),
    descriptor: structuredClone(descriptor),
    body,
    supportingFiles,
  };
}
