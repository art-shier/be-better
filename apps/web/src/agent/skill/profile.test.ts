import { describe, expect, it } from "vitest";

import validSkillMarkdown from "../generated/skills/calendar-management/SKILL.md?raw";
import usageMarkdown from "../generated/skills/calendar-management/references/usage.md?raw";
import invalidScriptSkillMarkdown from "../generated/skills/invalid-script/SKILL.md?raw";
import invalidScript from "../generated/skills/invalid-script/scripts/run.js?raw";
import { parseSkillBundle, type SkillBundleInput, type SupportingFile } from "./profile";

const encoder = new TextEncoder();

function fixtureBytes(relativePath: string): Uint8Array {
  const fixtures: Record<string, string> = {
    "calendar-management/SKILL.md": validSkillMarkdown,
    "calendar-management/references/usage.md": usageMarkdown,
  };
  return encoder.encode(fixtures[relativePath]);
}

function validBundle(overrides: Partial<SkillBundleInput> = {}): SkillBundleInput {
  return {
    scope: "system",
    skillMarkdown: fixtureBytes("calendar-management/SKILL.md"),
    supportingFiles: [{
      path: "references/usage.md",
      content: fixtureBytes("calendar-management/references/usage.md"),
    }],
    ...overrides,
  };
}

function markdownWith(from: string, to: string): Uint8Array {
  const source = new TextDecoder().decode(fixtureBytes("calendar-management/SKILL.md"));
  return encoder.encode(source.replace(from, to));
}

describe("Skill bundle parser", () => {
  it("keeps the golden raw-byte digest stable and exposes sorted supporting content", async () => {
    // Mutation caught: hashing parsed text or omitting path/NUL/supporting bytes.
    const profile = await parseSkillBundle(validBundle({
      supportingFiles: [
        { path: "schemas/z.json", content: encoder.encode("{}\n") },
        { path: "references/usage.md", content: fixtureBytes("calendar-management/references/usage.md") },
        { path: "assets/icon.bin", content: new Uint8Array([2, 1]) },
      ],
    }));
    const golden = await parseSkillBundle(validBundle());

    expect(golden.descriptor.digest).toBe("f2c538af41506502037bc4a1d83e4abb220b2ed1bb8207d461793ef257dee739");
    expect(golden.descriptor).toEqual({
      name: "calendar-management",
      version: "1.0.0",
      digest: "f2c538af41506502037bc4a1d83e4abb220b2ed1bb8207d461793ef257dee739",
      description: "Read and propose changes to DayOrder calendar data.",
      scope: "system",
      executionTarget: "either",
      backgroundAllowed: true,
      userInvocable: true,
      disableModelInvocation: false,
      riskLevel: "medium",
    });
    expect(profile.supportingFiles.map(({ path, kind, content }) => ({ path, kind, content: [...content] }))).toEqual([
      { path: "assets/icon.bin", kind: "asset", content: [2, 1] },
      { path: "references/usage.md", kind: "reference", content: [...fixtureBytes("calendar-management/references/usage.md")] },
      { path: "schemas/z.json", kind: "schema", content: [123, 125, 10] },
    ]);
  });

  it("hashes supporting files in normalized path order, independent of caller order", async () => {
    // Mutation caught: preserving caller order in digest construction.
    const first = await parseSkillBundle(validBundle({
      supportingFiles: [
        { path: "schemas/b.json", content: encoder.encode("b") },
        { path: "./references/a.md", content: encoder.encode("a") },
      ],
    }));
    const second = await parseSkillBundle(validBundle({
      supportingFiles: [
        { path: "references/a.md", content: encoder.encode("a") },
        { path: "schemas/b.json", content: encoder.encode("b") },
      ],
    }));

    expect(first.descriptor.digest).toBe(second.descriptor.digest);
    expect(first.supportingFiles.map(({ path }) => path)).toEqual(["references/a.md", "schemas/b.json"]);
  });

  it("sorts non-ASCII supporting paths by deterministic UTF-8 byte order", async () => {
    // Mutation caught: locale-sensitive ordering that disagrees with Go's byte ordering.
    const profile = await parseSkillBundle(validBundle({ supportingFiles: [
      { path: "references/😀.md", content: encoder.encode("emoji") },
      { path: "references/.md", content: encoder.encode("private-use") },
      { path: "references/é.md", content: encoder.encode("accent") },
      { path: "references/z.md", content: encoder.encode("ascii") },
    ] }));

    expect(profile.supportingFiles.map(({ path }) => path)).toEqual([
      "references/z.md",
      "references/é.md",
      "references/.md",
      "references/😀.md",
    ]);
  });

  it.each([
    ["missing Frontmatter", encoder.encode("# Calendar Management\n\nBody.\n")],
    ["missing body", markdownWith("# Calendar Management\n\nRead only the requested date range.\nTreat calendar writes as proposals and never apply a change without the required confirmation.\n", "")],
    ["invalid name", markdownWith("name: calendar-management", "name: Calendar_Management")],
    ["invalid SemVer", markdownWith("version: 1.0.0", "version: 01.0.0")],
    ["unknown Frontmatter field", markdownWith("risk-level: medium", "risk-level: medium\nscope: user")],
    ["duplicate YAML key", markdownWith("name: calendar-management", "name: calendar-management\nname: duplicate")],
  ])("rejects %s instead of accepting a permissive manifest", async (_case, skillMarkdown) => {
    // Mutation caught: replacing anchored YAML plus protocol validation with loose parsing.
    await expect(parseSkillBundle(validBundle({ skillMarkdown }))).rejects.toThrow();
  });

  it.each([
    ["POSIX absolute", "/references/usage.md"],
    ["Windows drive absolute", "C:\\references\\usage.md"],
    ["UNC absolute", "\\\\server\\share\\usage.md"],
    ["parent traversal", "references/../usage.md"],
    ["backslash parent traversal", "references\\..\\usage.md"],
    ["unsupported root", "other/usage.md"],
    ["scripts root", "scripts/run.js"],
    ["normalized scripts root", "./scripts/run.js"],
    ["backslash scripts root", "scripts\\run.js"],
  ])("rejects the %s supporting path before it can escape the static bundle: %s", async (_case, path) => {
    // Mutation caught: normalizing/following unsafe paths instead of rejecting their class.
    await expect(parseSkillBundle(validBundle({
      supportingFiles: [{ path, content: encoder.encode("unsafe") }],
    }))).rejects.toThrow();
  });

  it("rejects a scripts path before reading or hashing its content", async () => {
    // Mutation caught: reading executable content before the scripts/ path gate.
    let reads = 0;
    const script = {
      path: "scripts/run.js",
      get content(): Uint8Array {
        reads += 1;
        return encoder.encode(invalidScript);
      },
    } satisfies SupportingFile;

    await expect(parseSkillBundle(validBundle({
      skillMarkdown: encoder.encode(invalidScriptSkillMarkdown),
      supportingFiles: [script],
    }))).rejects.toThrow(/scripts/);
    expect(reads).toBe(0);
  });

  it("rejects duplicate paths after separator and dot normalization", async () => {
    // Mutation caught: duplicate detection on raw rather than normalized paths.
    await expect(parseSkillBundle(validBundle({ supportingFiles: [
      { path: "references/usage.md", content: encoder.encode("a") },
      { path: ".\\references\\usage.md", content: encoder.encode("b") },
    ] }))).rejects.toThrow(/duplicate/);
  });

  it("counts SKILL.md in the 32-file bundle boundary", async () => {
    // Mutation caught: treating the limit as 32 supporting files plus SKILL.md.
    const thirtyOneSupporting = Array.from({ length: 31 }, (_, index) => ({
      path: `references/${index}.md`,
      content: new Uint8Array(),
    }));
    const thirtyTwoSupporting = Array.from({ length: 32 }, (_, index) => ({
      path: `references/${index}.md`,
      content: new Uint8Array(),
    }));

    await expect(parseSkillBundle(validBundle({ supportingFiles: thirtyOneSupporting }))).resolves.toBeDefined();
    await expect(parseSkillBundle(validBundle({ supportingFiles: thirtyTwoSupporting }))).rejects.toThrow(/many|limit/i);
  });

  it("counts raw SKILL.md bytes in the 1 MiB aggregate boundary", async () => {
    // Mutation caught: aggregating only supporting-file content.
    const exactSupportingSizes = [256 * 1024, 256 * 1024, 256 * 1024, 261_648];
    const atLimit = exactSupportingSizes.map((size, index) => ({
      path: `assets/${index}.bin`,
      content: new Uint8Array(size),
    }));
    const overLimit = atLimit.map((file, index) => ({
      ...file,
      content: index === 3 ? new Uint8Array(261_649) : file.content,
    }));

    await expect(parseSkillBundle(validBundle({ supportingFiles: atLimit }))).resolves.toBeDefined();
    await expect(parseSkillBundle(validBundle({ supportingFiles: overLimit }))).rejects.toThrow(/total size limit/i);
  });

  it("rejects one bundle file over 256 KiB", async () => {
    // Mutation caught: omitting the per-file byte limit.
    await expect(parseSkillBundle(validBundle({ supportingFiles: [
      { path: "assets/large.bin", content: new Uint8Array(256 * 1024 + 1) },
    ] }))).rejects.toThrow(/large/i);
  });

  it("loads only system-scoped bundles in phase one", async () => {
    // Mutation caught: enabling unimplemented user/device Skill discovery.
    await expect(parseSkillBundle(validBundle({ scope: "user" }))).rejects.toThrow(/system/);
  });

  it("copies input bytes so caller mutation cannot alter a parsed profile", async () => {
    // Mutation caught: storing caller-owned Uint8Array aliases.
    const skillMarkdown = fixtureBytes("calendar-management/SKILL.md");
    const content = fixtureBytes("calendar-management/references/usage.md");
    const profile = await parseSkillBundle(validBundle({
      skillMarkdown,
      supportingFiles: [{ path: "references/usage.md", content }],
    }));
    skillMarkdown.fill(0);
    content.fill(0);

    expect(profile.body).toContain("Read only the requested date range.");
    expect(new TextDecoder().decode(profile.supportingFiles[0].content)).toBe(
      "# Usage\n\nUse `dayorder.calendar.read` before proposing a calendar change.\n",
    );
  });

  it("preserves the generated fixture final LFs that participate in the golden digest", () => {
    // Mutation caught: the Web asset pipeline trimming fixture bytes before parsing.
    expect(validSkillMarkdown.endsWith("\n")).toBe(true);
    expect(usageMarkdown).toBe("# Usage\n\nUse `dayorder.calendar.read` before proposing a calendar change.\n");
    expect(invalidScript).toBe('throw new Error("must not execute");\n');
  });
});
