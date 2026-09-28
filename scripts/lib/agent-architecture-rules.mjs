import ts from "typescript";
import { spawnSync } from "node:child_process";
import { resolve } from "node:path";

const forbiddenVendorSdks = [
  "openai",
  "@anthropic-ai/sdk",
  "@google/generative-ai",
  "@google/genai",
  "cohere-ai",
];

const providerKeyEnvironmentName = /\b(?:VITE_)?(?:OPENAI|ANTHROPIC|GOOGLE(?:_[A-Z0-9]+)*|GEMINI|COHERE|DEEPSEEK|DOUBAO|ARK|VOLC(?:ENGINE)?)(?:_[A-Z0-9]+)*_(?:API_)?(?:KEY|TOKEN|ACCESS_KEY|SECRET_KEY|ACCESSKEY|SECRETKEY)\b/;
const literalBearerKey = /["'`]Bearer\s+[A-Za-z0-9._~+/=-]{8,}["'`]/;

function executableGoSource(source) {
  let executable = "";

  for (let index = 0; index < source.length;) {
    const character = source[index];
    const next = source[index + 1];

    if (character === "/" && next === "/") {
      index += 2;
      while (index < source.length && source[index] !== "\n" && source[index] !== "\r") index += 1;
      continue;
    }

    if (character === "/" && next === "*") {
      index += 2;
      while (index < source.length && !(source[index] === "*" && source[index + 1] === "/")) {
        if (source[index] === "\n" || source[index] === "\r") executable += source[index];
        index += 1;
      }
      index = Math.min(index + 2, source.length);
      executable += " ";
      continue;
    }

    if (character === "\"" || character === "'" || character === "`") {
      const start = index;
      const delimiter = character;
      index += 1;
      while (index < source.length) {
        if (source[index] === "\\" && delimiter !== "`") {
          index = Math.min(index + 2, source.length);
          continue;
        }
        if (source[index] === delimiter) {
          index += 1;
          break;
        }
        index += 1;
      }
      const literal = source.slice(start, index);
      executable += literal === "\"AGENT_NOT_AVAILABLE\"" ? literal : "\"\"";
      continue;
    }

    executable += character;
    index += 1;
  }

  return executable;
}

function appDisablesAgentByDefault(source) {
  const file = ts.createSourceFile("App.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  if (file.parseDiagnostics.length > 0) return false;

  return file.statements.some((statement) => {
    if (!ts.isFunctionDeclaration(statement) || statement.name?.text !== "App") return false;
    const modifiers = statement.modifiers ?? [];
    if (!modifiers.some(({ kind }) => kind === ts.SyntaxKind.ExportKeyword)
      || !modifiers.some(({ kind }) => kind === ts.SyntaxKind.DefaultKeyword)) {
      return false;
    }

    const parameter = statement.parameters[0];
    if (!parameter || !ts.isObjectBindingPattern(parameter.name)) return false;
    return parameter.name.elements.some((element) => {
      const property = element.propertyName ?? element.name;
      return ts.isIdentifier(property)
        && property.text === "agentAvailable"
        && element.initializer?.kind === ts.SyntaxKind.FalseKeyword;
    });
  });
}

export function validateAgentArchitecture({
  webPackage,
  webAgentSources,
  appSource,
  agentHandlerSource,
  workerMainSource,
  productionGoSources = [],
}) {
  const failures = [];
  const dependencyNames = new Set(
    ["dependencies", "devDependencies", "optionalDependencies", "peerDependencies"]
      .flatMap((field) => Object.keys(webPackage[field] ?? {})),
  );

  for (const dependency of forbiddenVendorSdks) {
    if (dependencyNames.has(dependency)) {
      failures.push(`apps/web/package.json: vendor SDK ${dependency} is forbidden in the Agent foundation`);
    }
  }

  for (const source of webAgentSources) {
    if (providerKeyEnvironmentName.test(source)) {
      failures.push("apps/web/src/agent: provider key environment names are forbidden");
    }
    if (literalBearerKey.test(source)) {
      failures.push("apps/web/src/agent: literal provider key bearer values are forbidden");
    }
  }

  if (!appDisablesAgentByDefault(appSource)) {
    failures.push("apps/web/src/App.tsx: Agent must remain disabled by default");
  }
  const executableAgentHandlerSource = executableGoSource(agentHandlerSource);
  if (!/^\s*func\s+\(router\s+\*Router\)\s+agentUnavailable\s*\([^)]*\)\s*\{[^}]*\brouter\.writeError\s*\(\s*response\s*,\s*request\s*,\s*http\.StatusServiceUnavailable\s*,\s*"AGENT_NOT_AVAILABLE"/m.test(executableAgentHandlerSource)) {
    failures.push("apps/api/internal/httpapi/agent_handlers.go: AGENT_NOT_AVAILABLE guard is required");
  }
  if (/\bNewAgentHandler\b|\bagent\.run\.requested\b/.test(workerMainSource)) {
    failures.push("apps/api/cmd/worker/main.go: Agent registration is forbidden in the Worker");
  }

  failures.push(...validateGoAgentIntegrationBoundary(productionGoSources));

  return failures;
}

export function validateGoAgentIntegrationBoundary(sources) {
  if (sources.length === 0) return [];
  const checker = resolve(import.meta.dirname, "agent-go-architecture-guard.go");
  const result = spawnSync("go", ["run", checker], {
    input: JSON.stringify(sources),
    encoding: "utf8",
    windowsHide: true,
    maxBuffer: 4 * 1024 * 1024,
  });
  if (result.status !== 0) {
    const detail = (result.stderr || result.stdout || "Go AST guard failed").trim();
    return [`apps/api: Agent integration Go AST guard could not run: ${detail}`];
  }
  try {
    const failures = JSON.parse(result.stdout);
    return Array.isArray(failures) ? failures : ["apps/api: Agent integration Go AST guard returned an invalid result"];
  } catch {
    return ["apps/api: Agent integration Go AST guard returned invalid JSON"];
  }
}
