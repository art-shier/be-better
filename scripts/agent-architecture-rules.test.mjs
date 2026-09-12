import assert from "node:assert/strict";
import test from "node:test";

import { validateAgentArchitecture, validateGoAgentIntegrationBoundary } from "./lib/agent-architecture-rules.mjs";

test("Go AST guard rejects aliased multiline production wiring without matching strings or comments", () => {
  const failures = validateGoAgentIntegrationBoundary([
    {
      path: "apps/api/internal/httpapi/forbidden.go",
      source: `package httpapi
import (
  integrationalias "dayorder.local/api/internal/agentintegration"
)
func forbidden() { _, _ = integrationalias.NewHost(nil) }
`,
    },
    {
      path: "apps/api/internal/httpapi/example.go",
      source: `package httpapi
// import integrationalias "dayorder.local/api/internal/agentintegration"
const example = "integrationalias.NewHost(nil)"
`,
    },
    {
      path: "apps/api/internal/agentintegration/host.go",
      source: `package agentintegration
func allowed() { _, _ = NewHost(nil) }
`,
    },
  ]);

  assert.equal(failures.filter((failure) => failure.includes("forbidden.go") && failure.includes("import")).length, 1);
  assert.equal(failures.filter((failure) => failure.includes("forbidden.go") && failure.includes("constructor")).length, 1);
  assert.equal(failures.some((failure) => failure.includes("example.go")), false);
  assert.equal(failures.some((failure) => failure.includes("agentintegration/host.go")), false);
});

test("Go AST guard rejects every production Agent integration router constructor reference", () => {
  const failures = validateGoAgentIntegrationBoundary([
    {
      path: "apps/api/cmd/server/aliased.go",
      source: `package main
import (
  h "dayorder.local/api/internal/httpapi"
)
func forbidden() { _, _ = h.NewAgentIntegrationRouter(nil, nil) }
`,
    },
    {
      path: "apps/api/cmd/server/dot.go",
      source: `package main
import . "dayorder.local/api/internal/httpapi"
var forbidden = NewAgentIntegrationRouter
`,
    },
    {
      path: "apps/api/internal/httpapi/wiring.go",
      source: `package httpapi
func forbidden() { _, _ = NewAgentIntegrationRouter(nil, nil) }
`,
    },
    {
      path: "apps/api/cmd/server/example.go",
      source: `package main
import h "dayorder.local/api/internal/httpapi"
// var forbidden = h.NewAgentIntegrationRouter
const example = "h.NewAgentIntegrationRouter(nil, nil)"
var allowed = h.NewRouter
`,
    },
    {
      path: "apps/api/internal/agentintegration/host.go",
      source: `package agentintegration
import h "dayorder.local/api/internal/httpapi"
var allowed = h.NewAgentIntegrationRouter
`,
    },
    {
      path: "apps/api/cmd/agent-integration/main.go",
      source: `package main
import h "dayorder.local/api/internal/httpapi"
var allowed = h.NewAgentIntegrationRouter
`,
    },
  ]);

  for (const file of ["aliased.go", "dot.go", "wiring.go"]) {
    assert.equal(
      failures.filter((failure) => failure.includes(file) && failure.includes("NewAgentIntegrationRouter")).length,
      1,
      `${file}: ${failures.join("\n")}`,
    );
  }
  assert.equal(failures.some((failure) => failure.includes("example.go")), false);
  assert.equal(failures.some((failure) => failure.includes("agentintegration/host.go")), false);
  assert.equal(failures.some((failure) => failure.includes("cmd/agent-integration/main.go")), false);
});

test("Go AST guard rejects old and readonly Agent Worker constructors through real imports", () => {
  const failures = validateGoAgentIntegrationBoundary([
    {
      path: "apps/api/cmd/worker/readonly.go",
      source: `package main
import queuealias "dayorder.local/api/internal/worker"
var forbidden = queuealias.NewReadonlyAgentHandler
`,
    },
    {
      path: "apps/api/cmd/worker/background.go",
      source: `package main
import hostalias "dayorder.local/api/internal/agenthost"
var forbidden = hostalias.NewBackground
`,
    },
    {
      path: "apps/api/cmd/worker/legacy.go",
      source: `package main
import . "dayorder.local/api/internal/worker"
var forbidden = NewAgentHandler
`,
    },
    {
      path: "apps/api/internal/worker/wiring.go",
      source: `package worker
var forbidden = NewReadonlyAgentHandler
`,
    },
    {
      path: "apps/api/internal/agenthost/wiring.go",
      source: `package agenthost
var forbidden = NewBackground
`,
    },
    {
      path: "apps/api/cmd/worker/example.go",
      source: `package main
import (
  hostalias "dayorder.local/api/internal/agenthost"
  queuealias "dayorder.local/api/internal/worker"
)
// var forbidden = queuealias.NewReadonlyAgentHandler
const readonlyExample = "queuealias.NewReadonlyAgentHandler"
const backgroundExample = "hostalias.NewBackground"
var allowed = queuealias.NewRunner
`,
    },
    {
      path: "apps/api/internal/agentintegration/host.go",
      source: `package agentintegration
import (
  hostalias "dayorder.local/api/internal/agenthost"
  queuealias "dayorder.local/api/internal/worker"
)
var readonly = queuealias.NewReadonlyAgentHandler
var background = hostalias.NewBackground
`,
    },
  ]);

  for (const [file, symbol] of [
    ["readonly.go", "NewReadonlyAgentHandler"],
    ["background.go", "NewBackground"],
    ["legacy.go", "NewAgentHandler"],
    ["internal/worker/wiring.go", "NewReadonlyAgentHandler"],
    ["internal/agenthost/wiring.go", "NewBackground"],
  ]) {
    assert.equal(
      failures.filter((failure) => failure.includes(file) && failure.includes(symbol)).length,
      1,
      `${file}: ${failures.join("\n")}`,
    );
  }
  assert.equal(failures.some((failure) => failure.includes("example.go")), false);
  assert.equal(failures.some((failure) => failure.includes("agentintegration/host.go")), false);
});

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
  assert.ok(failures.some((value) => value.includes("AGENT_NOT_AVAILABLE")));
  assert.ok(failures.some((value) => value.includes("Worker")));
});

test("rejects every forbidden Web vendor SDK", () => {
  const dependencies = {
    openai: "1.0.0",
    "@anthropic-ai/sdk": "1.0.0",
    "@google/generative-ai": "1.0.0",
    "@google/genai": "1.0.0",
    "cohere-ai": "1.0.0",
  };
  const failures = validateAgentArchitecture({
    webPackage: { dependencies },
    webAgentSources: [],
    appSource: "function App({ agentAvailable = false }) {}",
    agentHandlerSource: "const code = 'AGENT_NOT_AVAILABLE';",
    workerMainSource: "worker.NewRunner(repository, emailHandlers)",
  });

  for (const dependency of Object.keys(dependencies)) {
    assert.ok(failures.some((value) => value.includes(`vendor SDK ${dependency}`)));
  }
});

test("rejects provider environment names and literal vendor bearer keys", () => {
  const failures = validateAgentArchitecture({
    webPackage: { dependencies: {} },
    webAgentSources: [
      "const key = process.env.ANTHROPIC_API_KEY;",
      "const authorization = 'Bearer sk-proj-example';",
      "const googleAuthorization = 'Bearer AIzaExample';",
    ],
    appSource: "function App({ agentAvailable = false }) {}",
    agentHandlerSource: "const code = 'AGENT_NOT_AVAILABLE';",
    workerMainSource: "worker.NewRunner(repository, emailHandlers)",
  });

  assert.equal(failures.filter((value) => value.includes("provider key")).length, 3);
});

test("rejects DeepSeek and Doubao-family credential names", () => {
  const failures = validateAgentArchitecture({
    webPackage: { dependencies: {} },
    webAgentSources: [
      "const deepseek = process.env.DEEPSEEK_API_KEY;",
      "const doubao = import.meta.env.VITE_DOUBAO_TOKEN;",
      "const ark = process.env.ARK_API_KEY;",
      "const volcengine = process.env.VOLCENGINE_ACCESS_KEY;",
    ],
    appSource: "export default function App({ agentAvailable = false }) {}",
    agentHandlerSource: `
      func (router *Router) agentUnavailable(response http.ResponseWriter, request *http.Request) {
        router.writeError(response, request, http.StatusServiceUnavailable, "AGENT_NOT_AVAILABLE", "disabled", false, nil)
      }
    `,
    workerMainSource: "worker.NewRunner(repository, emailHandlers)",
  });

  assert.equal(failures.filter((value) => value.includes("provider key")).length, 4);
});

test("does not accept production-off markers found only in comments or dead strings", () => {
  const failures = validateAgentArchitecture({
    webPackage: { dependencies: {} },
    webAgentSources: [],
    appSource: `
      // export default function App({ agentAvailable = false }) {}
      export default function App({ agentAvailable = true }) {}
    `,
    agentHandlerSource: `
      /*
      func (router *Router) agentUnavailable(response http.ResponseWriter, request *http.Request) {
        router.writeError(response, request, http.StatusServiceUnavailable, "AGENT_NOT_AVAILABLE", "disabled", false, nil)
      }
      */
      const legacyMarker = "AGENT_NOT_AVAILABLE"
    `,
    workerMainSource: "worker.NewRunner(repository, emailHandlers)",
  });

  assert.ok(failures.some((value) => value.includes("disabled")));
  assert.ok(failures.some((value) => value.includes("AGENT_NOT_AVAILABLE")));
});

test("rejects complete production-off guards embedded only in multiline strings", () => {
  // Mutation caught: stripping comments but leaving string bodies lets a
  // complete guard-shaped example satisfy the executable-source regex.
  const failures = validateAgentArchitecture({
    webPackage: { dependencies: {} },
    webAgentSources: [],
    appSource: [
      "const disabledExample = `",
      "export default function App({ agentAvailable = false }) {}",
      "`;",
      "export default function App({ agentAvailable = true }) {}",
    ].join("\n"),
    agentHandlerSource: [
      "package httpapi",
      "const disabledExample = `",
      "func (router *Router) agentUnavailable(response http.ResponseWriter, request *http.Request) {",
      "  router.writeError(response, request, http.StatusServiceUnavailable, \"AGENT_NOT_AVAILABLE\", \"disabled\", false, nil)",
      "}",
      "`",
      "func (router *Router) agentUnavailable(response http.ResponseWriter, request *http.Request) {",
      "  router.writeError(response, request, http.StatusOK, \"OK\", \"enabled\", false, nil)",
      "}",
    ].join("\n"),
    workerMainSource: "worker.NewRunner(repository, emailHandlers)",
  });

  assert.ok(failures.some((value) => value.includes("disabled")));
  assert.ok(failures.some((value) => value.includes("AGENT_NOT_AVAILABLE")));
});

test("keeps escaped backticks inside TypeScript template strings non-executable", () => {
  const failures = validateAgentArchitecture({
    webPackage: { dependencies: {} },
    webAgentSources: [],
    appSource: [
      "const disabledExample = `escaped backtick: \\`",
      "export default function App({ agentAvailable = false }) {}",
      "`;",
      "export default function App({ agentAvailable = true }) {}",
    ].join("\n"),
    agentHandlerSource: `
      func (router *Router) agentUnavailable(response http.ResponseWriter, request *http.Request) {
        router.writeError(response, request, http.StatusServiceUnavailable, "AGENT_NOT_AVAILABLE", "disabled", false, nil)
      }
    `,
    workerMainSource: "worker.NewRunner(repository, emailHandlers)",
  });

  assert.ok(failures.some((value) => value.includes("disabled")));
});

test("keeps nested templates inside TypeScript interpolation non-executable", () => {
  const failures = validateAgentArchitecture({
    webPackage: { dependencies: {} },
    webAgentSources: [],
    appSource: [
      "const disabledExample = `${`",
      "export default function App({ agentAvailable = false }) {}",
      "`}`;",
      "export default function App({ agentAvailable = true }) {}",
    ].join("\n"),
    agentHandlerSource: `
      func (router *Router) agentUnavailable(response http.ResponseWriter, request *http.Request) {
        router.writeError(response, request, http.StatusServiceUnavailable, "AGENT_NOT_AVAILABLE", "disabled", false, nil)
      }
    `,
    workerMainSource: "worker.NewRunner(repository, emailHandlers)",
  });

  assert.ok(failures.some((value) => value.includes("disabled")));
});

test("rejects Agent event registration in the Worker", () => {
  const failures = validateAgentArchitecture({
    webPackage: { dependencies: {} },
    webAgentSources: [],
    appSource: "function App({ agentAvailable = false }) {}",
    agentHandlerSource: "const code = 'AGENT_NOT_AVAILABLE';",
    workerMainSource: 'handlers["agent.run.requested"] = handler',
  });

  assert.ok(failures.some((value) => value.includes("Worker")));
});

test("accepts foundation dependencies while production stays disabled", () => {
  const failures = validateAgentArchitecture({
    webPackage: { dependencies: { ajv: "8.20.0", yaml: "2.9.0" } },
    webAgentSources: ["import Ajv from 'ajv';", "import { parse } from 'yaml';"],
    appSource: "export default function App({ agentAvailable = false }) {}",
    agentHandlerSource: `
      func (router *Router) agentUnavailable(response http.ResponseWriter, request *http.Request) {
        router.writeError(response, request, http.StatusServiceUnavailable, "AGENT_NOT_AVAILABLE", "disabled", false, nil)
      }
    `,
    workerMainSource: "worker.NewRunner(repository, emailHandlers)",
  });

  assert.deepEqual(failures, []);
});
