import { copyFileSync, lstatSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = resolve(import.meta.dirname, "..");
const schema = resolve(root, "contracts/agent/v2/protocol.schema.json");
const webOut = resolve(root, "apps/web/src/agent/generated");
const goOut = resolve(root, "apps/api/internal/agentprotocol");
const conformanceSource = resolve(root, "contracts/agent/conformance");
const webConformanceOut = resolve(webOut, "conformance");
const skillFixtureSource = resolve(root, "contracts/agent/fixtures/skills");
const webSkillFixtureOut = resolve(webOut, "skills");
const webBuiltinOut = resolve(webOut, "builtin");
const goBuiltinOut = resolve(root, "apps/api/internal/agentassets/generated");
const builtinAssets = [
  [resolve(root, "skills/calendar-overview"), "skills/calendar-overview"],
  [resolve(root, "contracts/agent/tools/calendar-read.json"), "tools/calendar-read.json"],
];

function run(command, args) {
  const useCommandShell = process.platform === "win32" && command.endsWith(".cmd");
  const executable = useCommandShell ? process.env.ComSpec ?? "cmd.exe" : command;
  const commandArgs = useCommandShell ? ["/d", "/s", "/c", command, ...args] : args;
  const result = spawnSync(executable, commandArgs, {
    cwd: root,
    env: command === "go" ? { ...process.env, GOWORK: "off" } : process.env,
    stdio: "inherit",
    shell: false
  });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status ?? 1);
}

function replaceGenerated(path, source, replacement) {
  const generated = readFileSync(path, "utf8");
  if (!generated.includes(source)) throw new Error(`expected generated contract shape missing from ${path}`);
  writeFileSync(path, generated.replace(source, replacement));
}

function copyTreeWithoutLinks(source, destination) {
  const info = lstatSync(source);
  if (info.isSymbolicLink()) throw new Error(`symbolic link is forbidden in generated agent assets: ${source}`);
  if (info.isDirectory()) {
    mkdirSync(destination, { recursive: true });
    for (const entry of readdirSync(source).sort()) {
      copyTreeWithoutLinks(resolve(source, entry), resolve(destination, entry));
    }
    return;
  }
  if (!info.isFile()) throw new Error(`unsupported generated agent asset entry: ${source}`);
  mkdirSync(dirname(destination), { recursive: true });
  copyFileSync(source, destination);
}

export function copySkillFixtures(source, destination) {
  const info = lstatSync(source);
  if (info.isSymbolicLink()) throw new Error(`symbolic link is forbidden in generated agent assets: ${source}`);
  if (!info.isDirectory()) throw new Error(`Skill fixture source must be a directory: ${source}`);
  rmSync(destination, { recursive: true, force: true });
  copyTreeWithoutLinks(source, destination);
}

function copyBuiltinAssets(destination) {
  rmSync(destination, { recursive: true, force: true });
  for (const [source, relativeDestination] of builtinAssets) {
    copyTreeWithoutLinks(source, resolve(destination, relativeDestination));
  }
}

export function generateAgentContracts() {
  mkdirSync(webOut, { recursive: true });
  mkdirSync(goOut, { recursive: true });

  const json2ts = resolve(root, "node_modules/.bin", process.platform === "win32" ? "json2ts.cmd" : "json2ts");
  run(json2ts, ["--input", schema, "--output", resolve(webOut, "protocol.ts"), "--cwd", resolve(root, "contracts/agent/v2"), "--no-enableConstEnums", "--maxItems", "4", "--unknownAny"]);
  run("go", ["tool", "-C", "apps/api", "-modfile", "tools.mod", "go-jsonschema", "--only-models", "--tags", "json", "--capitalization", "ID", "--package", "agentprotocol", "--output", "internal/agentprotocol/generated_types.go", "../../contracts/agent/v2/protocol.schema.json"]);
  replaceGenerated(
    resolve(webOut, "protocol.ts"),
    `sections: [\n    "summary" | "findings" | "risks" | "next_steps",\n    "summary" | "findings" | "risks" | "next_steps",\n    "summary" | "findings" | "risks" | "next_steps",\n    "summary" | "findings" | "risks" | "next_steps"\n  ];`,
    'sections: ["summary", "findings", "risks", "next_steps"];'
  );
  replaceGenerated(
    resolve(webOut, "protocol.ts"),
    `scope: AgentScope1;\n  timezone: string;\n  modelProfile: string;\n}\nexport interface AgentScope1 {\n  domains: string[];\n  entityIds?: string[];\n  from?: string;\n  to?: string;\n}`,
    `scope: AgentScope;\n  timezone: string;\n  modelProfile: string;\n}`
  );
  replaceGenerated(
    resolve(goOut, "generated_types.go"),
    "Sections []WorkerOutputContractSectionsElem `json:\"sections\"`",
    "Sections [4]WorkerOutputContractSectionsElem `json:\"sections\"`"
  );
  replaceGenerated(
    resolve(goOut, "generated_types.go"),
    'import "time"',
    ""
  );
  replaceGenerated(
    resolve(goOut, "generated_types.go"),
    "type DateTime time.Time",
    "type DateTime string"
  );
  copyFileSync(schema, resolve(webOut, "protocol.schema.json"));
  copyFileSync(schema, resolve(goOut, "protocol.schema.json"));

  rmSync(webConformanceOut, { recursive: true, force: true });
  mkdirSync(webConformanceOut, { recursive: true });
  for (const file of readdirSync(conformanceSource).filter((file) => file.endsWith(".json")).sort()) {
    copyFileSync(resolve(conformanceSource, file), resolve(webConformanceOut, file));
  }
  copySkillFixtures(skillFixtureSource, webSkillFixtureOut);
  copyBuiltinAssets(webBuiltinOut);
  copyBuiltinAssets(goBuiltinOut);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) generateAgentContracts();
