import { spawn } from "node:child_process";
import { lstat, mkdtemp, realpath, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import { basename, isAbsolute, join, relative, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import {
  exactFakePlaywrightTests,
  validateExactFakePlaywrightRunOutput,
} from "./lib/agent-playwright-reporter.mjs";

export const CONFIGHUB_DATABASE_KEYS = Object.freeze([
  "db_address",
  "db_port",
  "db_username",
  "db_password",
  "db_migrator_password",
  "db_api_password",
  "db_worker_password",
]);

const temporaryPrefix = "dayorder-agent-integration-";
const nativeDatabaseURL = /(?:^|_)DATABASE_URL$/i;

export function parseAgentIntegrationArgs(args) {
  const options = {
    databaseSource: "docker",
    requireDocker: false,
    realProvider: false,
    providerModel: "",
  };
  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index];
    switch (argument) {
      case "--database-source":
        options.databaseSource = args[(index += 1)] ?? "";
        break;
      case "--require-docker":
        options.requireDocker = true;
        break;
      case "--real-provider":
        throw new Error(
          "real Provider mode belongs to Task 16 and is unavailable in this Fake acceptance gate",
        );
      case "--provider-model":
        options.providerModel = args[(index += 1)] ?? "";
        break;
      default:
        throw new Error(`unknown argument: ${argument}`);
    }
  }
  if (!new Set(["docker", "confighub"]).has(options.databaseSource)) {
    throw new Error("database source must be docker or confighub");
  }
  if (options.requireDocker && options.databaseSource === "confighub") {
    throw new Error("require-docker cannot be combined with confighub");
  }
  if (options.providerModel && !options.realProvider) {
    throw new Error("provider-model requires real-provider mode");
  }
  return options;
}

function sensitiveEnvironmentKey(key) {
  return (
    CONFIGHUB_DATABASE_KEYS.includes(key) ||
    nativeDatabaseURL.test(key) ||
    key === "DAYORDER_AGENT_TEST_DB_SOURCE" ||
    key === "DAYORDER_AGENT_TEST_PROVIDER_KEY" ||
    key === "DAYORDER_AGENT_TEST_MODEL" ||
    key === "DAYORDER_AGENT_PROVIDER_KEY" ||
    key === "DAYORDER_AGENT_PROVIDER_MODEL"
  );
}

function scrubbedEnvironment(env) {
  const scrubbed = {};
  for (const key of Object.keys(env)) {
    if (!sensitiveEnvironmentKey(key)) scrubbed[key] = env[key];
  }
  return scrubbed;
}

function hostEnvironment(env, options) {
  const selected = scrubbedEnvironment(env);
  selected.DAYORDER_AGENT_TEST_DB_SOURCE = options.databaseSource;
  if (options.databaseSource === "confighub") {
    for (const key of CONFIGHUB_DATABASE_KEYS) selected[key] = env[key];
  }
  if (options.realProvider) {
    selected.DAYORDER_AGENT_TEST_PROVIDER_KEY =
      env.DAYORDER_AGENT_TEST_PROVIDER_KEY;
    if (options.providerModel || env.DAYORDER_AGENT_TEST_MODEL) {
      selected.DAYORDER_AGENT_TEST_MODEL =
        options.providerModel || env.DAYORDER_AGENT_TEST_MODEL;
    }
  }
  return selected;
}

function validateEnvironment(options, env) {
  if (options.databaseSource === "confighub") {
    for (const key of CONFIGHUB_DATABASE_KEYS) {
      if (typeof env[key] !== "string" || env[key].length === 0)
        throw new Error(`${key} is required for ConfigHub database source`);
    }
  }
  if (
    options.realProvider &&
    (typeof env.DAYORDER_AGENT_TEST_PROVIDER_KEY !== "string" ||
      env.DAYORDER_AGENT_TEST_PROVIDER_KEY.trim() === "")
  ) {
    throw new Error(
      "DAYORDER_AGENT_TEST_PROVIDER_KEY is required for real Provider mode",
    );
  }
}

function defaultSpawnCommand(spec) {
  const child = spawn(spec.command, spec.args, {
    cwd: spec.cwd,
    env: spec.env,
    windowsHide: true,
    stdio: [
      spec.stdin === "pipe" ? "pipe" : "ignore",
      spec.stdout === "pipe" ? "pipe" : "inherit",
      "inherit",
    ],
  });
  const wait = new Promise((resolveWait, rejectWait) => {
    child.once("error", rejectWait);
    child.once("exit", (code, signal) => resolveWait(code ?? signal ?? 1));
  });
  void wait.catch(() => undefined);
  return {
    process: child,
    stdout: child.stdout,
    stdin: child.stdin,
    wait: () => wait,
  };
}

export async function readHostReadyFromChild(child) {
  if (!child.stdout)
    throw new Error("integration Host stdout was not captured");
  return new Promise((resolveReady, rejectReady) => {
    let buffer = "";
    let settled = false;
    const timeout = setTimeout(
      () => finish(new Error("integration Host readiness timed out")),
      120_000,
    );
    const finish = (error, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      child.stdout.off("data", onData);
      if (error) rejectReady(error);
      else resolveReady(value);
    };
    const onData = (chunk) => {
      buffer += chunk.toString("utf8");
      if (Buffer.byteLength(buffer, "utf8") > 65_536)
        return finish(
          new Error("integration Host readiness output exceeded 65536 bytes"),
        );
      for (;;) {
        const newline = buffer.indexOf("\n");
        if (newline < 0) return;
        const line = buffer.slice(0, newline).trim();
        buffer = buffer.slice(newline + 1);
        if (!line) continue;
        try {
          const value = JSON.parse(line);
          if (value?.type === "ready") return finish(undefined, value);
        } catch {
          return finish(
            new Error("integration Host emitted invalid readiness JSON"),
          );
        }
      }
    };
    child.stdout.on("data", onData);
    void child
      .wait()
      .then(
        (code) =>
          finish(
            new Error(
              `integration Host exited before readiness with code ${code}`,
            ),
          ),
        finish,
      );
  });
}

async function inspectTemporaryDirectory(path) {
  const [stats, resolved] = await Promise.all([lstat(path), realpath(path)]);
  return {
    resolved,
    device: stats.dev,
    inode: stats.ino,
    directory: stats.isDirectory(),
    symbolicLink: stats.isSymbolicLink(),
  };
}

function sameTemporaryIdentity(first, second) {
  return (
    first.directory &&
    second.directory &&
    !first.symbolicLink &&
    !second.symbolicLink &&
    resolve(first.resolved) === resolve(second.resolved) &&
    first.device === second.device &&
    first.inode === second.inode
  );
}

async function defaultWaitForHTTP(target) {
  const stopAt = Date.now() + 30_000;
  let lastError;
  while (Date.now() < stopAt) {
    try {
      const response = await fetch(target, {
        signal: AbortSignal.timeout(2_000),
      });
      if (response.ok) return;
      lastError = new Error(`status ${response.status}`);
    } catch (error) {
      lastError = error;
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 100));
  }
  throw new Error(
    `harness readiness failed: ${lastError instanceof Error ? lastError.message : "request failed"}`,
  );
}

async function reserveLoopbackPort() {
  return new Promise((resolvePort, rejectPort) => {
    const server = net.createServer();
    server.once("error", rejectPort);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      const port = typeof address === "object" && address ? address.port : 0;
      server.close((error) => (error ? rejectPort(error) : resolvePort(port)));
    });
  });
}

async function bounded(promise, milliseconds, signal, label) {
  let timer;
  let onAbort;
  try {
    return await Promise.race([
      promise,
      new Promise((_, rejectWait) => {
        timer = setTimeout(
          () => rejectWait(new Error(`${label} timed out`)),
          milliseconds,
        );
      }),
      new Promise((_, rejectWait) => {
        if (!signal) return;
        onAbort = () =>
          rejectWait(
            signal.reason instanceof Error
              ? signal.reason
              : new Error("agent integration interrupted"),
          );
        if (signal.aborted) onAbort();
        else signal.addEventListener("abort", onAbort, { once: true });
      }),
    ]);
  } finally {
    clearTimeout(timer);
    if (onAbort) signal?.removeEventListener("abort", onAbort);
  }
}

async function waitWithTimeout(child, milliseconds, signal, label = "process") {
  return bounded(child.wait(), milliseconds, signal, label);
}

async function defaultStopChild(child, options = {}) {
  if (!child) return;
  if (options.input && child.stdin?.writable) {
    try {
      child.stdin.write(options.input);
    } catch (error) {
      throw new Error(
        `host stop input failed: ${error instanceof Error ? error.message : "write failed"}`,
      );
    }
  } else child.process?.kill("SIGTERM");
  try {
    const code = await waitWithTimeout(
      child,
      options.timeoutMs ?? 10_000,
      undefined,
      `${child.spec?.label ?? "process"} shutdown`,
    );
    if (options.requireExitZero && code !== 0)
      throw new Error(
        `${child.spec?.label ?? "host"} exited with code ${code}`,
      );
    return;
  } catch (error) {
    if (
      options.requireExitZero &&
      !String(error?.message).includes("timed out")
    )
      throw error;
    child.process?.kill("SIGTERM");
  }
  try {
    await waitWithTimeout(
      child,
      5_000,
      undefined,
      `${child.spec?.label ?? "process"} forced shutdown`,
    );
  } catch {
    child.process?.kill("SIGKILL");
    try {
      await waitWithTimeout(
        child,
        5_000,
        undefined,
        `${child.spec?.label ?? "process"} kill`,
      );
    } catch {
      // The error below deliberately keeps cleanup outcome uncertain.
    }
  }
  throw new Error(
    `${child.spec?.label ?? "process"} did not exit normally; cleanup is uncertain`,
  );
}

async function assertSuccessful(child, label, timeoutMs, signal) {
  const code = await waitWithTimeout(child, timeoutMs, signal, label);
  if (code !== 0) throw new Error(`${label} exited with code ${code}`);
}

export function validateExactGoTestOutput(output, testName) {
  const escaped = testName.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const passes =
    output.match(new RegExp(`^--- PASS: ${escaped} \\(`, "gm")) ?? [];
  const skipped = new RegExp(`^--- SKIP: ${escaped} \\(`, "m").test(output);
  if (passes.length !== 1 || skipped || !/^PASS\r?$/m.test(output)) {
    throw new Error(`${testName} did not report exactly one PASS without SKIP`);
  }
}

function exactStringSet(actual, expected) {
  return (
    Array.isArray(actual) &&
    actual.length === expected.length &&
    new Set(actual).size === expected.length &&
    expected.every((value) => actual.includes(value))
  );
}

export function validateExactFakePlaywrightListOutput(output) {
  const rejectDiscovery = () => {
    throw new Error(
      "Fake Playwright discovery did not contain the exact six Fake tests without declared skips",
    );
  };
  let report;
  try {
    report = JSON.parse(output);
  } catch {
    rejectDiscovery();
  }

  const projects = report?.config?.projects;
  const project =
    Array.isArray(projects) && projects.length === 1 ? projects[0] : undefined;
  if (
    !project ||
    !exactStringSet(project.testMatch, [
      "readonly.spec.ts",
      "failures.spec.ts",
    ]) ||
    !Array.isArray(project.testIgnore) ||
    !project.testIgnore.includes("real-provider.spec.ts") ||
    !Array.isArray(report.errors) ||
    report.errors.length !== 0 ||
    report?.stats?.expected !== 0 ||
    report?.stats?.skipped !== 6 ||
    report?.stats?.unexpected !== 0 ||
    report?.stats?.flaky !== 0 ||
    !Array.isArray(report.suites) ||
    report.suites.length !== 2
  ) {
    rejectDiscovery();
  }

  const discoveredFiles = new Set();
  for (const suite of report.suites) {
    const file = basename(typeof suite?.file === "string" ? suite.file : "");
    if (!Object.hasOwn(exactFakePlaywrightTests, file)) rejectDiscovery();
    const expectedTitles = exactFakePlaywrightTests[file];
    if (
      discoveredFiles.has(file) ||
      !Array.isArray(suite.specs) ||
      suite.specs.length !== expectedTitles.length
    ) {
      rejectDiscovery();
    }
    discoveredFiles.add(file);
    const titles = suite.specs.map((spec) => spec?.title);
    if (!exactStringSet(titles, expectedTitles)) rejectDiscovery();
    for (const spec of suite.specs) {
      const specFile = basename(
        typeof spec?.file === "string" ? spec.file : "",
      );
      if (
        specFile !== file ||
        !Array.isArray(spec.tests) ||
        spec.tests.length !== 1
      )
        rejectDiscovery();
      const declared = spec.tests[0];
      if (
        declared?.expectedStatus !== "passed" ||
        declared?.status !== "skipped" ||
        !Array.isArray(declared?.annotations) ||
        declared.annotations.some((annotation) =>
          new Set(["skip", "fixme"]).has(annotation?.type),
        )
      ) {
        rejectDiscovery();
      }
    }
  }
  if (
    !exactStringSet([...discoveredFiles], Object.keys(exactFakePlaywrightTests))
  )
    rejectDiscovery();
}

async function readBoundedChildOutput(
  child,
  writeOutput,
  maxBytes = 2 * 1024 * 1024,
) {
  if (!child.stdout)
    throw new Error(`${child.spec?.label ?? "test"} stdout was not captured`);
  return new Promise((resolveOutput, rejectOutput) => {
    const chunks = [];
    let bytes = 0;
    const finish = (error) => {
      child.stdout.off("data", onData);
      child.stdout.off("end", onEnd);
      child.stdout.off("error", onError);
      if (error) rejectOutput(error);
      else resolveOutput(Buffer.concat(chunks).toString("utf8"));
    };
    const onData = (chunk) => {
      const value = Buffer.from(chunk);
      bytes += value.length;
      if (bytes > maxBytes)
        return finish(
          new Error(
            `${child.spec?.label ?? "test"} output exceeded ${maxBytes} bytes`,
          ),
        );
      chunks.push(value);
      try {
        writeOutput(value);
      } catch {
        return finish(
          new Error(
            `${child.spec?.label ?? "test"} output could not be forwarded`,
          ),
        );
      }
    };
    const onEnd = () => finish();
    const onError = () =>
      finish(
        new Error(`${child.spec?.label ?? "test"} output could not be read`),
      );
    child.stdout.on("data", onData);
    child.stdout.once("end", onEnd);
    child.stdout.once("error", onError);
  });
}

export async function verifyExactGoTest(child, options) {
  const writeOutput =
    options.writeOutput ?? ((chunk) => process.stdout.write(chunk));
  const outputPromise = readBoundedChildOutput(child, writeOutput);
  void outputPromise.catch(() => undefined);
  const code = await waitWithTimeout(
    child,
    options.timeoutMs,
    options.signal,
    options.label,
  );
  const output = await bounded(
    outputPromise,
    5_000,
    options.signal,
    `${options.label} output`,
  );
  if (code !== 0) throw new Error(`${options.label} exited with code ${code}`);
  validateExactGoTestOutput(output, options.testName);
}

export async function verifyExactPlaywrightList(child, options) {
  const outputPromise = readBoundedChildOutput(child, () => undefined);
  void outputPromise.catch(() => undefined);
  const code = await waitWithTimeout(
    child,
    options.timeoutMs,
    options.signal,
    options.label,
  );
  const output = await bounded(
    outputPromise,
    5_000,
    options.signal,
    `${options.label} output`,
  );
  if (code !== 0) throw new Error(`${options.label} exited with code ${code}`);
  validateExactFakePlaywrightListOutput(output);
}

export async function verifyExactPlaywrightRun(child, options) {
  const outputPromise = readBoundedChildOutput(child, () => undefined, 16_384);
  const [code, output] = await bounded(
    Promise.all([child.wait(), outputPromise]),
    options.timeoutMs,
    options.signal,
    options.label,
  );
  if (code !== 0) throw new Error(`${options.label} exited with code ${code}`);
  validateExactFakePlaywrightRunOutput(output);
}

function validateReady(value) {
  let parsed;
  try {
    parsed = new URL(value?.apiURL);
  } catch {
    throw new Error("integration Host returned an invalid apiURL");
  }
  if (
    value?.type !== "ready" ||
    parsed.protocol !== "http:" ||
    !new Set(["127.0.0.1", "localhost", "[::1]"]).has(parsed.hostname)
  ) {
    throw new Error(
      "integration Host readiness must name a loopback HTTP apiURL",
    );
  }
  return parsed.origin;
}

function viteConfig(root, port, apiURL) {
  return `export default {
  root: ${JSON.stringify(resolve(root, "apps/web"))},
  server: {
    host: "127.0.0.1",
    port: ${port},
    strictPort: true,
    proxy: {
      "/api": { target: ${JSON.stringify(apiURL)}, changeOrigin: false },
      "/__test": { target: ${JSON.stringify(apiURL)}, changeOrigin: false },
    },
  },
};
`;
}

export async function runAgentIntegration(args = [], overrides = {}) {
  const options = parseAgentIntegrationArgs(args);
  const env = overrides.env ?? process.env;
  validateEnvironment(options, env);

  const root = resolve(overrides.root ?? resolve(import.meta.dirname, ".."));
  const platform = overrides.platform ?? process.platform;
  const nodeExecutable = overrides.nodeExecutable ?? process.execPath;
  const goExecutable = overrides.goExecutable ?? "go";
  const spawnCommand = overrides.spawnCommand ?? defaultSpawnCommand;
  const tempParent = resolve(overrides.tempParent ?? tmpdir());
  const makeTemp =
    overrides.makeTemp ?? (() => mkdtemp(join(tempParent, temporaryPrefix)));
  const removeTemp =
    overrides.removeTemp ??
    (async (path) => {
      if (!basename(path).startsWith(temporaryPrefix))
        throw new Error("refusing to remove an unowned temporary directory");
      await rm(path, { recursive: true, force: true });
    });
  const writeOwnedFile = overrides.writeFile ?? writeFile;
  const reservePort = overrides.reservePort ?? reserveLoopbackPort;
  const readHostReady = overrides.readHostReady ?? readHostReadyFromChild;
  const waitForHTTP = overrides.waitForHTTP ?? defaultWaitForHTTP;
  const stopChild = overrides.stopChild ?? defaultStopChild;
  const verifyPlaywrightList =
    overrides.verifyPlaywrightList ?? verifyExactPlaywrightList;
  const verifyPlaywrightRun =
    overrides.verifyPlaywrightRun ?? verifyExactPlaywrightRun;
  const verifyExactTest = overrides.verifyExactTest ?? verifyExactGoTest;
  const inspectTemp = overrides.inspectTemp ?? inspectTemporaryDirectory;
  const resolveTempParent = overrides.resolveTempParent ?? realpath;
  const signal = overrides.signal;
  const commandTimeoutMs = overrides.commandTimeoutMs ?? 120_000;
  const safeEnv = scrubbedEnvironment(env);

  let temporaryDirectory;
  let ownsTemporaryDirectory = false;
  let temporaryIdentity;
  let host;
  let vite;
  let serviceConformance;
  const activeChildren = new Set();
  const launch = (spec) => {
    const child = spawnCommand(spec);
    activeChildren.add(child);
    return child;
  };
  const finish = async (child, label, timeoutMs = commandTimeoutMs) => {
    try {
      await assertSuccessful(child, label, timeoutMs, signal);
      activeChildren.delete(child);
    } catch (error) {
      if (/exited with code/.test(String(error?.message)))
        activeChildren.delete(child);
      throw error;
    }
  };
  const finishExactTest = async (child, label, testName, timeoutMs) => {
    try {
      await verifyExactTest(child, { label, testName, timeoutMs, signal });
      activeChildren.delete(child);
    } catch (error) {
      if (/exited with code/.test(String(error?.message)))
        activeChildren.delete(child);
      throw error;
    }
  };
  const finishPlaywrightList = async (child, timeoutMs) => {
    try {
      await verifyPlaywrightList(child, {
        label: "playwright-list",
        timeoutMs,
        signal,
      });
      activeChildren.delete(child);
    } catch (error) {
      if (/exited with code|exact six Fake tests/.test(String(error?.message)))
        activeChildren.delete(child);
      throw error;
    }
  };
  let primaryError;
  const cleanupErrors = [];
  try {
    const browserDiscovery = launch({
      label: "playwright-list",
      command: nodeExecutable,
      args: [
        resolve(root, "node_modules/@playwright/test/cli.js"),
        "test",
        "--config",
        resolve(root, "apps/web/tests/agent-integration/playwright.config.ts"),
        "--list",
        "--reporter=json",
      ],
      cwd: root,
      env: {
        ...safeEnv,
        DAYORDER_AGENT_HARNESS_URL: "http://127.0.0.1:9",
        DAYORDER_AGENT_PROVIDER_MODE: "fake",
        PLAYWRIGHT_NO_COPY_PROMPT: "1",
      },
      stdout: "pipe",
    });
    await finishPlaywrightList(
      browserDiscovery,
      Math.min(commandTimeoutMs, 30_000),
    );

    if (options.databaseSource === "docker") {
      const docker = launch({
        label: "docker-check",
        command: "docker",
        args: ["version", "--format", "{{.Server.Version}}"],
        cwd: root,
        env: safeEnv,
      });
      await finish(docker, "docker check", Math.min(commandTimeoutMs, 10_000));
    }

    const canonicalTempParent = await resolveTempParent(tempParent);
    temporaryDirectory = await makeTemp();
    const relativeTemp = relative(tempParent, resolve(temporaryDirectory));
    if (
      relativeTemp === "" ||
      relativeTemp.startsWith("..") ||
      isAbsolute(relativeTemp) ||
      !basename(temporaryDirectory).startsWith(temporaryPrefix)
    ) {
      throw new Error(
        `mkdtemp result is outside the owned temporary parent: ${temporaryDirectory}`,
      );
    }
    temporaryIdentity = await inspectTemp(temporaryDirectory);
    const identityRelative = relative(
      resolve(canonicalTempParent),
      resolve(temporaryIdentity.resolved),
    );
    if (
      !temporaryIdentity.directory ||
      temporaryIdentity.symbolicLink ||
      identityRelative === "" ||
      identityRelative.startsWith("..") ||
      isAbsolute(identityRelative)
    ) {
      throw new Error(
        `mkdtemp result did not retain an owned temporary directory identity: ${temporaryDirectory}`,
      );
    }
    ownsTemporaryDirectory = true;
    const browserTypecheck = launch({
      label: "typecheck-browser",
      command: nodeExecutable,
      args: [
        resolve(root, "node_modules/typescript/bin/tsc"),
        "--project",
        resolve(root, "apps/web/tests/agent-integration/tsconfig.json"),
      ],
      cwd: root,
      env: safeEnv,
    });
    await finish(browserTypecheck, "typecheck-browser");

    const port = await reservePort();
    const harnessOrigin = `http://127.0.0.1:${port}`;
    const binaryName =
      platform === "win32" ? "agent-integration.exe" : "agent-integration";
    const binary = resolve(temporaryDirectory, binaryName);
    const build = launch({
      label: "build-host",
      command: goExecutable,
      args: ["build", "-o", binary, "./apps/api/cmd/agent-integration"],
      cwd: root,
      env: safeEnv,
    });
    await finish(build, "build-host");

    const serviceConformanceBinary = resolve(
      temporaryDirectory,
      platform === "win32"
        ? "dayorder-agent-service-conformance.exe"
        : "dayorder-agent-service-conformance",
    );
    const serviceConformanceBuild = launch({
      label: "build-service-conformance",
      command: goExecutable,
      args: [
        "test",
        "-c",
        "-o",
        serviceConformanceBinary,
        "./apps/api/internal/agentintegration",
      ],
      cwd: root,
      env: safeEnv,
    });
    await finish(serviceConformanceBuild, "build-service-conformance");

    serviceConformance = launch({
      label: "service-conformance",
      command: serviceConformanceBinary,
      args: ["-test.v", "-test.run=^TestAgentIntegrationServiceBinding$"],
      cwd: root,
      env: {
        ...hostEnvironment(env, options),
        DAYORDER_AGENT_SERVICE_CONFORMANCE_CONTROLLED: "1",
        DAYORDER_AGENT_SERVICE_CONFORMANCE_REQUIRED: "1",
      },
      stdin: "pipe",
      stdout: "pipe",
    });
    await finishExactTest(
      serviceConformance,
      "service-conformance",
      "TestAgentIntegrationServiceBinding",
      Math.max(commandTimeoutMs, 300_000),
    );

    const hostArgs = [
      "--environment",
      "test",
      "--harness-origin",
      harnessOrigin,
    ];
    if (options.realProvider) hostArgs.push("--real-provider");
    if (options.providerModel)
      hostArgs.push("--provider-model", options.providerModel);
    host = launch({
      label: "host",
      command: binary,
      args: hostArgs,
      cwd: root,
      env: hostEnvironment(env, options),
      stdin: "pipe",
      stdout: "pipe",
    });
    const apiURL = validateReady(
      await bounded(
        readHostReady(host),
        commandTimeoutMs,
        signal,
        "host readiness",
      ),
    );

    const configPath = resolve(temporaryDirectory, "vite.config.mjs");
    await writeOwnedFile(configPath, viteConfig(root, port, apiURL), "utf8");
    vite = launch({
      label: "vite",
      command: nodeExecutable,
      args: [
        resolve(root, "node_modules/vite/bin/vite.js"),
        "--config",
        configPath,
      ],
      cwd: root,
      env: safeEnv,
    });
    await bounded(
      Promise.race([
        waitForHTTP(`${harnessOrigin}/tests/agent-integration/harness/`),
        vite.wait().then((code) => {
          throw new Error(`vite exited with code ${code}`);
        }),
      ]),
      Math.min(commandTimeoutMs, 30_000),
      signal,
      "vite readiness",
    );

    const externalEnv = {
      ...safeEnv,
      DAYORDER_AGENT_HARNESS_URL: apiURL,
      DAYORDER_AGENT_HARNESS_ORIGIN: harnessOrigin,
    };
    const conformance = launch({
      label: "go-conformance",
      command: goExecutable,
      args: [
        "test",
        "-v",
        "-count=1",
        "./apps/api/internal/agentintegration",
        "-run",
        "^TestAgentIntegrationExternalReadonlyBinding$",
      ],
      cwd: root,
      env: {
        ...externalEnv,
        DAYORDER_AGENT_EXTERNAL_CONFORMANCE_REQUIRED: "1",
      },
      stdout: "pipe",
    });
    await finishExactTest(
      conformance,
      "go-conformance",
      "TestAgentIntegrationExternalReadonlyBinding",
      Math.max(commandTimeoutMs, 300_000),
    );

    const browser = launch({
      label: "playwright",
      command: nodeExecutable,
      args: [
        resolve(root, "node_modules/@playwright/test/cli.js"),
        "test",
        "--config",
        resolve(root, "apps/web/tests/agent-integration/playwright.config.ts"),
        "--reporter",
        resolve(root, "scripts/lib/agent-playwright-reporter.mjs"),
      ],
      cwd: root,
      env: {
        ...safeEnv,
        DAYORDER_AGENT_HARNESS_URL: harnessOrigin,
        DAYORDER_AGENT_PROVIDER_MODE: options.realProvider ? "real" : "fake",
        PLAYWRIGHT_NO_COPY_PROMPT: "1",
      },
      stdout: "pipe",
    });
    try {
      await verifyPlaywrightRun(browser, {
        label: "playwright",
        timeoutMs: Math.max(commandTimeoutMs, 900_000),
        signal,
      });
      activeChildren.delete(browser);
    } catch (error) {
      if (/exited with code|exact six Fake tests/.test(String(error?.message)))
        activeChildren.delete(browser);
      throw error;
    }
  } catch (error) {
    primaryError = error;
  } finally {
    if (vite) {
      try {
        await stopChild(vite, { timeoutMs: 10_000 });
      } catch (error) {
        cleanupErrors.push(error);
      } finally {
        activeChildren.delete(vite);
      }
    }
    if (host) {
      try {
        await stopChild(host, {
          input: "stop\n",
          timeoutMs: 35_000,
          requireExitZero: true,
        });
      } catch (error) {
        cleanupErrors.push(error);
      } finally {
        activeChildren.delete(host);
      }
    }
    if (serviceConformance && activeChildren.has(serviceConformance)) {
      try {
        await stopChild(serviceConformance, {
          input: "stop\n",
          timeoutMs: 165_000,
          requireExitZero: true,
        });
      } catch (error) {
        cleanupErrors.push(
          new Error(
            `Service Binding fixture cleanup is uncertain: ${error instanceof Error ? error.message : "controlled stop failed"}`,
          ),
        );
      } finally {
        activeChildren.delete(serviceConformance);
      }
    }
    for (const child of [...activeChildren].reverse()) {
      try {
        await stopChild(child, { timeoutMs: 5_000 });
        activeChildren.delete(child);
      } catch (error) {
        cleanupErrors.push(error);
      }
    }
    if (temporaryDirectory && ownsTemporaryDirectory) {
      try {
        const currentIdentity = await inspectTemp(temporaryDirectory);
        if (!sameTemporaryIdentity(temporaryIdentity, currentIdentity))
          throw new Error(
            "temporary directory identity changed; refusing recursive cleanup",
          );
        await removeTemp(temporaryDirectory);
      } catch (error) {
        cleanupErrors.push(error);
      }
    }
  }
  if (primaryError && cleanupErrors.length > 0)
    throw new AggregateError(
      [primaryError, ...cleanupErrors],
      primaryError.message,
    );
  if (primaryError) throw primaryError;
  if (cleanupErrors.length === 1)
    throw new AggregateError(
      cleanupErrors,
      `agent integration cleanup failed: ${cleanupErrors[0]?.message ?? "unknown cleanup error"}`,
    );
  if (cleanupErrors.length > 1)
    throw new AggregateError(cleanupErrors, "agent integration cleanup failed");
}

export function formatAgentIntegrationError(error) {
  if (!(error instanceof AggregateError)) {
    return [
      `agent integration failed: ${error instanceof Error ? error.message : "unknown error"}`,
    ];
  }
  const errors = [...error.errors];
  if (error.message.startsWith("agent integration cleanup failed")) {
    return errors.map(
      (item) =>
        `agent integration cleanup failed: ${item instanceof Error ? item.message : "unknown cleanup error"}`,
    );
  }
  const lines = [
    `agent integration failed: ${errors[0] instanceof Error ? errors[0].message : error.message}`,
  ];
  for (const item of errors.slice(1)) {
    lines.push(
      `agent integration cleanup failed: ${item instanceof Error ? item.message : "unknown cleanup error"}`,
    );
  }
  return lines;
}

async function main() {
  const controller = new AbortController();
  const interrupt = () =>
    controller.abort(new Error("agent integration interrupted"));
  process.once("SIGINT", interrupt);
  process.once("SIGTERM", interrupt);
  try {
    await runAgentIntegration(process.argv.slice(2), {
      signal: controller.signal,
    });
  } catch (error) {
    for (const line of formatAgentIntegrationError(error)) console.error(line);
    process.exitCode = 1;
  } finally {
    process.removeListener("SIGINT", interrupt);
    process.removeListener("SIGTERM", interrupt);
  }
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(resolve(process.argv[1])).href
)
  await main();
