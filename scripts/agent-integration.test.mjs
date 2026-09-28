import assert from "node:assert/strict";
import { PassThrough } from "node:stream";
import test from "node:test";
import { resolve } from "node:path";

import {
  CONFIGHUB_DATABASE_KEYS,
  formatAgentIntegrationError,
  parseAgentIntegrationArgs,
  readHostReadyFromChild,
  runAgentIntegration,
  validateExactFakePlaywrightListOutput,
  validateExactGoTestOutput,
  verifyExactGoTest,
  verifyExactPlaywrightRun,
} from "./agent-integration.mjs";
import AgentPlaywrightReporter, {
  validateExactFakePlaywrightRunOutput,
} from "./lib/agent-playwright-reporter.mjs";

test("rejects unsupported database modes and require-docker with ConfigHub", () => {
  assert.throws(
    () =>
      parseAgentIntegrationArgs([
        "--database-source",
        "postgres://shared/dayorder",
      ]),
    /database source must be docker or confighub/,
  );
  assert.throws(
    () =>
      parseAgentIntegrationArgs([
        "--database-source",
        "confighub",
        "--require-docker",
      ]),
    /require-docker cannot be combined with confighub/,
  );
  assert.throws(
    () => parseAgentIntegrationArgs(["--unknown"]),
    /unknown argument/,
  );
  assert.throws(
    () => parseAgentIntegrationArgs(["--real-provider"]),
    /real Provider mode belongs to Task 16/,
  );
});

async function successfulExactTest(child, options = {}) {
  const code = await Promise.race([
    child.wait(),
    new Promise((_, reject) => {
      if (!options.signal) return;
      const abort = () =>
        reject(
          options.signal.reason ?? new Error("test orchestration aborted"),
        );
      if (options.signal.aborted) abort();
      else options.signal.addEventListener("abort", abort, { once: true });
    }),
  ]);
  if (code !== 0)
    throw new Error(`${child.spec.label} exited with code ${code}`);
}

function successfulDependencies(overrides = {}) {
  return {
    env: { PATH: "/tools" },
    root: "/repo",
    platform: "linux",
    nodeExecutable: "/node24/bin/node",
    tempParent: "/tmp",
    makeTemp: async () => "/tmp/dayorder-agent-integration-owned",
    removeTemp: async () => {},
    reservePort: async () => 43123,
    writeFile: async () => {},
    inspectTemp: async () => ({
      resolved: resolve("/tmp/dayorder-agent-integration-owned"),
      device: 1,
      inode: 10,
      directory: true,
      symbolicLink: false,
    }),
    resolveTempParent: async (path) => resolve(path),
    spawnCommand: (spec) => ({ spec, wait: async () => 0 }),
    readHostReady: async () => ({
      type: "ready",
      apiURL: "http://127.0.0.1:49152",
    }),
    waitForHTTP: async () => {},
    stopChild: async () => {},
    verifyPlaywrightList: async () => {},
    verifyPlaywrightRun: successfulExactTest,
    verifyExactTest: successfulExactTest,
    ...overrides,
  };
}

function fakePlaywrightListOutput() {
  const spec = (file, title) => ({
    title,
    file: `/repo/apps/web/tests/agent-integration/${file}`,
    tests: [{ expectedStatus: "passed", annotations: [], status: "skipped" }],
  });
  return JSON.stringify({
    config: {
      projects: [
        {
          testMatch: ["readonly.spec.ts", "failures.spec.ts"],
          testIgnore: ["real-provider.spec.ts"],
        },
      ],
    },
    suites: [
      {
        title: "failures.spec.ts",
        file: "/repo/apps/web/tests/agent-integration/failures.spec.ts",
        specs: [
          spec(
            "failures.spec.ts",
            "tool and Run timeout outcomes compare across the real browser and Worker hosts",
          ),
          spec(
            "failures.spec.ts",
            "user cancellation stops real Provider and Calendar dependencies within one second and compares traces",
          ),
          spec(
            "failures.spec.ts",
            "remaining Worker fault matrix preserves the readonly terminal boundary",
          ),
        ],
      },
      {
        title: "readonly.spec.ts",
        file: "/repo/apps/web/tests/agent-integration/readonly.spec.ts",
        specs: [
          spec(
            "readonly.spec.ts",
            "foreground and page-independent background runs preserve one ordered readonly contract",
          ),
          spec(
            "readonly.spec.ts",
            "fixed disjoint calendar fixture proves two pages, cursor integrity, and the real 64 KiB result limit",
          ),
          spec(
            "readonly.spec.ts",
            "browser security boundaries use real cookies, origin, and device identity",
          ),
        ],
      },
    ],
    errors: [],
    stats: { expected: 0, skipped: 6, unexpected: 0, flaky: 0 },
  });
}

test("Fake Playwright discovery requires the exact six declared tests without skips", () => {
  const valid = fakePlaywrightListOutput();
  assert.doesNotThrow(() => validateExactFakePlaywrightListOutput(valid));

  const wrongTitle = JSON.parse(valid);
  wrongTitle.suites[0].specs[0].title = "different test with the same count";
  assert.throws(
    () => validateExactFakePlaywrightListOutput(JSON.stringify(wrongTitle)),
    /exact six Fake tests/,
  );

  const skipped = JSON.parse(valid);
  skipped.suites[1].specs[1].tests[0] = {
    expectedStatus: "skipped",
    annotations: [{ type: "skip" }],
    status: "skipped",
  };
  assert.throws(
    () => validateExactFakePlaywrightListOutput(JSON.stringify(skipped)),
    /exact six Fake tests/,
  );

  const missingAnnotations = JSON.parse(valid);
  delete missingAnnotations.suites[0].specs[0].tests[0].annotations;
  assert.throws(
    () =>
      validateExactFakePlaywrightListOutput(JSON.stringify(missingAnnotations)),
    /exact six Fake tests/,
  );

  const unknownFile = JSON.parse(valid);
  unknownFile.suites[1].file =
    "/repo/apps/web/tests/agent-integration/real-provider.spec.ts";
  assert.throws(
    () => validateExactFakePlaywrightListOutput(JSON.stringify(unknownFile)),
    /exact six Fake tests/,
  );

  const empty = JSON.parse(valid);
  empty.suites = [];
  empty.stats.skipped = 0;
  assert.throws(
    () => validateExactFakePlaywrightListOutput(JSON.stringify(empty)),
    /exact six Fake tests/,
  );
});

test("rejects Playwright discovery before creating resources", async () => {
  const calls = [];
  let madeTemp = false;
  await assert.rejects(
    runAgentIntegration(
      ["--database-source", "confighub"],
      successfulDependencies({
        env: Object.fromEntries(
          CONFIGHUB_DATABASE_KEYS.map((key) => [key, `${key}-value`]),
        ),
        spawnCommand: (spec) => {
          calls.push(spec.label);
          return { spec, wait: async () => 0 };
        },
        verifyPlaywrightList: async () => {
          throw new Error(
            "Fake Playwright discovery did not contain the exact six Fake tests",
          );
        },
        makeTemp: async () => {
          madeTemp = true;
          return "/tmp/dayorder-agent-integration-should-not-exist";
        },
      }),
    ),
    /exact six Fake tests/,
  );
  assert.deepEqual(calls, ["playwright-list"]);
  assert.equal(madeTemp, false);
});

function fakePlaywrightExecutionReport() {
  return {
    type: "dayorder-agent-playwright",
    version: 1,
    status: "passed",
    listed: 6,
    errors: false,
    overflow: false,
    tests: Array.from({ length: 6 }, (_, index) => ({
      index,
      status: "passed",
      expectedStatus: "passed",
      outcome: "expected",
      retry: 0,
      disallowedAnnotations: false,
      errors: false,
    })),
  };
}

test("exit-zero Playwright runtime skip fails acceptance and still cleans up owned resources", async () => {
  const report = fakePlaywrightExecutionReport();
  report.tests[0].status = "skipped";
  report.tests[0].expectedStatus = "skipped";
  report.tests[0].outcome = "skipped";
  const cleanup = [];
  await assert.rejects(
    runAgentIntegration(
      [],
      successfulDependencies({
        verifyPlaywrightRun: undefined,
        spawnCommand: (spec) => {
          if (spec.label !== "playwright") return { spec, wait: async () => 0 };
          const stdout = new PassThrough();
          setImmediate(() => stdout.end(JSON.stringify(report)));
          return { spec, stdout, wait: async () => 0 };
        },
        stopChild: async (child, options) =>
          cleanup.push([child.spec.label, options?.input ?? ""]),
        removeTemp: async (path) => cleanup.push(["remove", path]),
      }),
    ),
    /exact six Fake tests.*executed.*passed/,
  );
  assert.deepEqual(cleanup, [
    ["vite", ""],
    ["host", "stop\n"],
    ["remove", "/tmp/dayorder-agent-integration-owned"],
  ]);
});

test("actual Playwright execution requires six unique, first-attempt passes", async (t) => {
  assert.doesNotThrow(() =>
    validateExactFakePlaywrightRunOutput(
      JSON.stringify(fakePlaywrightExecutionReport()),
    ),
  );
  const invalid = {
    missing: (r) => r.tests.pop(),
    duplicate: (r) => {
      r.tests[5].index = 0;
    },
    unknown: (r) => {
      r.tests[0].index = -1;
    },
    extra: (r) => r.tests.push({ ...r.tests[0] }),
    skipped: (r) => {
      r.tests[0].status = "skipped";
    },
    expectedFailure: (r) => {
      r.tests[0].expectedStatus = "failed";
    },
    failed: (r) => {
      r.tests[0].status = "failed";
    },
    timeout: (r) => {
      r.tests[0].status = "timedOut";
    },
    interrupted: (r) => {
      r.tests[0].status = "interrupted";
    },
    flaky: (r) => {
      r.tests[0].outcome = "flaky";
    },
    unexpected: (r) => {
      r.tests[0].outcome = "unexpected";
    },
    retry: (r) => {
      r.tests[0].retry = 1;
    },
    annotation: (r) => {
      r.tests[0].disallowedAnnotations = true;
    },
    testError: (r) => {
      r.tests[0].errors = true;
    },
    globalError: (r) => {
      r.errors = true;
    },
    overallFailure: (r) => {
      r.status = "failed";
    },
    missingBegin: (r) => {
      r.listed = -1;
    },
    overflow: (r) => {
      r.overflow = true;
    },
    missingField: (r) => {
      delete r.tests[0].errors;
    },
  };
  for (const [name, mutate] of Object.entries(invalid)) {
    await t.test(name, () => {
      const report = fakePlaywrightExecutionReport();
      mutate(report);
      assert.throws(
        () => validateExactFakePlaywrightRunOutput(JSON.stringify(report)),
        /exact six Fake tests/,
      );
    });
  }
  for (const output of [
    "private-canary",
    "null",
    "{}",
    "{}\n{}",
    '{"errors":"private-canary"}',
  ]) {
    assert.throws(
      () => validateExactFakePlaywrightRunOutput(output),
      (error) =>
        /exact six Fake tests/.test(error.message) &&
        !error.message.includes("private-canary"),
    );
  }
});

function collectReporterOutput(t, mutate = () => {}) {
  const reporter = new AgentPlaywrightReporter();
  const rootDir = resolve("/repo/apps/web/tests/agent-integration");
  const cases = JSON.parse(fakePlaywrightListOutput())
    .suites.flatMap((suite) => suite.specs)
    .map((spec) => ({
      title: spec.title,
      location: { file: resolve(spec.file), line: 1, column: 1 },
      expectedStatus: "passed",
      annotations: [],
      outcome: () => "expected",
    }));
  const results = cases.map(() => ({
    status: "passed",
    retry: 0,
    errors: [],
    annotations: [],
    attachments: [
      { name: "private-canary", body: Buffer.from("private-canary") },
    ],
    stdout: ["private-canary"],
    stderr: ["private-canary"],
    steps: [{ title: "private-canary" }],
  }));
  mutate({ reporter, cases, results });
  reporter.onBegin({ rootDir }, { allTests: () => cases });
  cases.forEach((item, index) => reporter.onTestEnd(item, results[index]));
  let output = "";
  const write = t.mock.method(process.stdout, "write", (chunk) => {
    output += chunk;
    return true;
  });
  try {
    reporter.onEnd({ status: "passed" });
  } finally {
    write.mock.restore();
  }
  assert.equal(reporter.printsToStdio(), true);
  assert.ok(Buffer.byteLength(output) < 16_384);
  assert.ok(
    !output.includes("private-canary"),
    "reporter leaked a private payload",
  );
  return output;
}

test("Playwright reporter maps actual file/title identities and emits metadata only", (t) => {
  const output = collectReporterOutput(t);
  assert.deepEqual(JSON.parse(output), fakePlaywrightExecutionReport());
  validateExactFakePlaywrightRunOutput(output);
  for (const mutate of [
    ({ cases }) => {
      cases[0].title = "private-canary";
    },
    ({ cases }) => {
      cases[0].location.file = resolve("/elsewhere/failures.spec.ts");
    },
    ({ cases }) => {
      cases[0].annotations.push({
        type: "skip",
        description: "private-canary",
      });
    },
    ({ cases, results }) => {
      cases[0].expectedStatus = "skipped";
      results[0].status = "skipped";
    },
    ({ cases }) => {
      cases[0].outcome = () => "flaky";
    },
    ({ results }) => {
      results[0].retry = 1;
    },
    ({ results }) => {
      results[0].errors.push({ message: "private-canary" });
    },
    ({ reporter }) => reporter.onError({ message: "private-canary" }),
    ({ cases, results }) => {
      cases.push(cases[0]);
      results.push(results[0]);
    },
    ({ cases, results }) => {
      cases.pop();
      results.pop();
    },
  ])
    assert.throws(
      () =>
        validateExactFakePlaywrightRunOutput(collectReporterOutput(t, mutate)),
      /exact six Fake tests/,
    );
});

test("actual Playwright stream validation accepts a complete report, rejects nonzero exit, and bounds output", async () => {
  const options = { label: "playwright", timeoutMs: 100 };
  for (const code of [0, 9]) {
    const stdout = new PassThrough();
    const verification = verifyExactPlaywrightRun(
      { stdout, wait: async () => code },
      options,
    );
    stdout.end(JSON.stringify(fakePlaywrightExecutionReport()));
    if (code === 0) await verification;
    else await assert.rejects(verification, /playwright exited with code 9/);
  }
  const stdout = new PassThrough();
  const tooLarge = verifyExactPlaywrightRun(
    { stdout, wait: () => new Promise(() => {}) },
    options,
  );
  stdout.end("private-canary".repeat(2000));
  await assert.rejects(tooLarge, /output exceeded 16384 bytes/);
});

test("actual Playwright stream validation observes cancellation and incomplete-output timeout", async () => {
  for (const abort of [false, true]) {
    const stdout = new PassThrough();
    const controller = new AbortController();
    const verification = verifyExactPlaywrightRun(
      { stdout, wait: async () => 0 },
      {
        label: "playwright",
        timeoutMs: 20,
        signal: controller.signal,
      },
    );
    if (abort) controller.abort(new Error("controlled cancellation"));
    await assert.rejects(
      verification,
      abort ? /controlled cancellation/ : /playwright timed out/,
    );
    stdout.end();
  }
});

test("orchestration consumes the real successful execution verifier", async () => {
  let browserSpec;
  await runAgentIntegration(
    [],
    successfulDependencies({
      verifyPlaywrightRun: undefined,
      spawnCommand: (spec) => {
        if (spec.label !== "playwright") return { spec, wait: async () => 0 };
        browserSpec = spec;
        const stdout = new PassThrough();
        setImmediate(() =>
          stdout.end(JSON.stringify(fakePlaywrightExecutionReport())),
        );
        return { spec, stdout, wait: async () => 0 };
      },
    }),
  );
  assert.equal(browserSpec.stdout, "pipe");
  assert.deepEqual(browserSpec.args.slice(-2), [
    "--reporter",
    resolve("/repo/scripts/lib/agent-playwright-reporter.mjs"),
  ]);
});

test("requires all seven ConfigHub database fields before creating resources", async () => {
  for (const missing of CONFIGHUB_DATABASE_KEYS) {
    const env = Object.fromEntries(
      CONFIGHUB_DATABASE_KEYS.map((key) => [key, `${key}-value`]),
    );
    delete env[missing];
    let madeTemp = false;
    await assert.rejects(
      runAgentIntegration(["--database-source", "confighub"], {
        env,
        makeTemp: async () => {
          madeTemp = true;
          return "C:/temp/should-not-exist";
        },
      }),
      new RegExp(`${missing} is required`),
    );
    assert.equal(
      madeTemp,
      false,
      `missing ${missing} created a temporary directory`,
    );
  }
});

test("orchestrates one host and scrubs database and Provider secrets from every non-host child", async () => {
  const calls = [];
  const cleanup = [];
  const files = [];
  const children = [];
  const env = {
    PATH: "C:/tools",
    DATABASE_URL: "postgres://native-api-secret",
    MIGRATION_DATABASE_URL: "postgres://native-migrator-secret",
    db_address: "fixture.example.internal",
    db_port: "5432",
    db_username: "fixture-admin",
    db_password: "admin-secret",
    db_migrator_password: "migrator-secret",
    db_api_password: "api-secret",
    db_worker_password: "worker-secret",
    DAYORDER_AGENT_TEST_PROVIDER_KEY: "provider-secret",
    DAYORDER_AGENT_TEST_MODEL: "fixture-model",
    DAYORDER_AGENT_PROVIDER_KEY: "legacy-provider-secret",
    DAYORDER_AGENT_PROVIDER_MODEL: "legacy-provider-model",
  };
  const fakeChild = (spec) => {
    const child = { spec, wait: async () => 0 };
    children.push(child);
    return child;
  };

  await runAgentIntegration(["--database-source", "confighub"], {
    env,
    root: "C:/repo",
    platform: "win32",
    tempParent: "C:/temp",
    nodeExecutable: "C:/node24/node.exe",
    makeTemp: async () => "C:/temp/dayorder-agent-integration-owned",
    removeTemp: async (path) => cleanup.push(["remove", path]),
    inspectTemp: async () => ({
      resolved: resolve("C:/temp/dayorder-agent-integration-owned"),
      device: 1,
      inode: 10,
      directory: true,
      symbolicLink: false,
    }),
    resolveTempParent: async (path) => resolve(path),
    reservePort: async () => 43123,
    writeFile: async (path, contents) => files.push({ path, contents }),
    spawnCommand: (spec) => {
      calls.push(spec);
      return fakeChild(spec);
    },
    readHostReady: async () => ({
      type: "ready",
      apiURL: "http://127.0.0.1:49152",
    }),
    waitForHTTP: async (url) => calls.push({ label: "readiness", url }),
    stopChild: async (child, options) =>
      cleanup.push(["stop", child.spec.label, options?.input ?? ""]),
    verifyPlaywrightList: async () => {},
    verifyPlaywrightRun: successfulExactTest,
    verifyExactTest: successfulExactTest,
  });

  assert.deepEqual(
    calls.map((call) => call.label),
    [
      "playwright-list",
      "typecheck-browser",
      "build-host",
      "build-service-conformance",
      "service-conformance",
      "host",
      "vite",
      "readiness",
      "go-conformance",
      "playwright",
    ],
  );
  const browserDiscovery = calls.find(
    (call) => call.label === "playwright-list",
  );
  assert.deepEqual(browserDiscovery.args.slice(-2), [
    "--list",
    "--reporter=json",
  ]);
  assert.equal(browserDiscovery.stdout, "pipe");
  assert.equal(
    browserDiscovery.env.DAYORDER_AGENT_HARNESS_URL,
    "http://127.0.0.1:9",
  );
  assert.equal(browserDiscovery.env.DAYORDER_AGENT_PROVIDER_MODE, "fake");
  assert.equal(browserDiscovery.env.PLAYWRIGHT_NO_COPY_PROMPT, "1");
  const browserTypecheck = calls.find(
    (call) => call.label === "typecheck-browser",
  );
  assert.deepEqual(
    browserTypecheck.args,
    [
      resolve("C:/repo/node_modules/typescript/bin/tsc"),
      "--project",
      resolve("C:/repo/apps/web/tests/agent-integration/tsconfig.json"),
    ],
  );
  const host = calls.find((call) => call.label === "host");
  assert.deepEqual(host.args, [
    "--environment",
    "test",
    "--harness-origin",
    "http://127.0.0.1:43123",
  ]);
  assert.equal(host.env.DAYORDER_AGENT_TEST_DB_SOURCE, "confighub");
  for (const key of CONFIGHUB_DATABASE_KEYS)
    assert.equal(host.env[key], env[key]);
  assert.equal(host.env.DATABASE_URL, undefined);
  assert.equal(host.env.MIGRATION_DATABASE_URL, undefined);
  assert.equal(
    host.env.DAYORDER_AGENT_TEST_PROVIDER_KEY,
    undefined,
    "Fake mode must not receive a Provider key",
  );

  for (const call of calls.filter(
    (candidate) =>
      candidate.env &&
      !["host", "service-conformance"].includes(candidate.label),
  )) {
    for (const key of [
      ...CONFIGHUB_DATABASE_KEYS,
      "DATABASE_URL",
      "MIGRATION_DATABASE_URL",
      "DAYORDER_AGENT_TEST_PROVIDER_KEY",
      "DAYORDER_AGENT_TEST_MODEL",
      "DAYORDER_AGENT_PROVIDER_KEY",
      "DAYORDER_AGENT_PROVIDER_MODEL",
    ]) {
      assert.equal(call.env[key], undefined, `${call.label} received ${key}`);
    }
  }
  const serviceConformance = calls.find(
    (call) => call.label === "service-conformance",
  );
  assert.match(
    serviceConformance.command.replaceAll("\\", "/"),
    /dayorder-agent-service-conformance(?:\.exe)?$/,
  );
  assert.deepEqual(serviceConformance.args, [
    "-test.v",
    "-test.run=^TestAgentIntegrationServiceBinding$",
  ]);
  assert.equal(serviceConformance.stdin, "pipe");
  assert.equal(
    serviceConformance.env.DAYORDER_AGENT_TEST_DB_SOURCE,
    "confighub",
  );
  assert.equal(
    serviceConformance.env.DAYORDER_AGENT_SERVICE_CONFORMANCE_CONTROLLED,
    "1",
  );
  assert.equal(
    serviceConformance.env.DAYORDER_AGENT_SERVICE_CONFORMANCE_REQUIRED,
    "1",
  );
  for (const key of CONFIGHUB_DATABASE_KEYS)
    assert.equal(serviceConformance.env[key], env[key]);
  for (const key of [
    "DATABASE_URL",
    "MIGRATION_DATABASE_URL",
    "DAYORDER_AGENT_TEST_PROVIDER_KEY",
    "DAYORDER_AGENT_TEST_MODEL",
    "DAYORDER_AGENT_PROVIDER_KEY",
    "DAYORDER_AGENT_PROVIDER_MODEL",
  ]) {
    assert.equal(
      serviceConformance.env[key],
      undefined,
      `service-conformance received ${key}`,
    );
  }
  const goConformance = calls.find((call) => call.label === "go-conformance");
  assert.equal(
    goConformance.env.DAYORDER_AGENT_HARNESS_URL,
    "http://127.0.0.1:49152",
  );
  assert.equal(
    goConformance.env.DAYORDER_AGENT_HARNESS_ORIGIN,
    "http://127.0.0.1:43123",
  );
  assert.equal(
    goConformance.env.DAYORDER_AGENT_EXTERNAL_CONFORMANCE_REQUIRED,
    "1",
  );
  assert.match(
    goConformance.args.join(" "),
    /\^TestAgentIntegrationExternalReadonlyBinding\$/,
  );
  const playwright = calls.find((call) => call.label === "playwright");
  assert.equal(
    playwright.env.DAYORDER_AGENT_HARNESS_URL,
    "http://127.0.0.1:43123",
  );
  assert.equal(playwright.env.DAYORDER_AGENT_PROVIDER_MODE, "fake");
  assert.equal(playwright.env.PLAYWRIGHT_NO_COPY_PROMPT, "1");

  assert.equal(files.length, 1);
  assert.match(files[0].contents, /strictPort:\s*true/);
  assert.match(files[0].contents, /"\/api"/);
  assert.match(files[0].contents, /"\/__test"/);
  assert.match(files[0].contents, /http:\/\/127\.0\.0\.1:49152/);
  assert.deepEqual(cleanup, [
    ["stop", "vite", ""],
    ["stop", "host", "stop\n"],
    ["remove", "C:/temp/dayorder-agent-integration-owned"],
  ]);
});

test("failure still requests graceful host cleanup before removing its exact temporary directory", async () => {
  const cleanup = [];
  await assert.rejects(
    runAgentIntegration([], {
      env: { PATH: "/tools" },
      root: "/repo",
      platform: "linux",
      tempParent: "/tmp",
      nodeExecutable: "/node24/bin/node",
      makeTemp: async () => "/tmp/dayorder-agent-integration-owned",
      removeTemp: async (path) => cleanup.push(["remove", path]),
      inspectTemp: async () => ({
        resolved: resolve("/tmp/dayorder-agent-integration-owned"),
        device: 1,
        inode: 10,
        directory: true,
        symbolicLink: false,
      }),
      resolveTempParent: async (path) => resolve(path),
      reservePort: async () => 43123,
      writeFile: async () => {},
      spawnCommand: (spec) => ({
        spec,
        wait: async () => (spec.label === "playwright" ? 9 : 0),
      }),
      readHostReady: async () => ({
        type: "ready",
        apiURL: "http://127.0.0.1:49152",
      }),
      waitForHTTP: async () => {},
      stopChild: async (child, options) =>
        cleanup.push(["stop", child.spec.label, options?.input ?? ""]),
      verifyPlaywrightList: async () => {},
      verifyPlaywrightRun: successfulExactTest,
      verifyExactTest: successfulExactTest,
    }),
    /playwright exited with code 9/,
  );
  assert.deepEqual(cleanup, [
    ["stop", "vite", ""],
    ["stop", "host", "stop\n"],
    ["remove", "/tmp/dayorder-agent-integration-owned"],
  ]);
});

test("Fake orchestration never reads a dedicated Provider secret getter", async () => {
  const env = { PATH: "/tools" };
  Object.defineProperty(env, "DAYORDER_AGENT_TEST_PROVIDER_KEY", {
    enumerable: true,
    get() {
      throw new Error("Provider getter was read");
    },
  });
  await runAgentIntegration(
    ["--database-source", "confighub"],
    successfulDependencies({
      env: Object.assign(
        env,
        Object.fromEntries(
          CONFIGHUB_DATABASE_KEYS.map((key) => [key, `${key}-value`]),
        ),
      ),
    }),
  );
});

test("Fake orchestration never reads legacy Provider credential getters", async (t) => {
  for (const [key, message] of [
    ["DAYORDER_AGENT_PROVIDER_KEY", "legacy Provider getter was read"],
    ["DAYORDER_AGENT_PROVIDER_MODEL", "legacy Provider model getter was read"],
  ]) {
    await t.test(key, async () => {
      const env = Object.fromEntries(
        CONFIGHUB_DATABASE_KEYS.map((databaseKey) => [
          databaseKey,
          `${databaseKey}-value`,
        ]),
      );
      Object.defineProperty(env, key, {
        enumerable: true,
        get() {
          throw new Error(message);
        },
      });
      await runAgentIntegration(
        ["--database-source", "confighub"],
        successfulDependencies({ env }),
      );
    });
  }
});

test("rejects an unowned mkdtemp result before spawning or recursively removing", async () => {
  let spawned = false;
  let removed = false;
  await assert.rejects(
    runAgentIntegration(
      [],
      successfulDependencies({
        makeTemp: async () => "/workspace/dayorder-agent-integration-forged",
        spawnCommand: (spec) => {
          if (["playwright-list", "docker-check"].includes(spec.label))
            return { spec, wait: async () => 0 };
          spawned = true;
          return { spec, wait: async () => 0 };
        },
        removeTemp: async () => {
          removed = true;
        },
      }),
    ),
    /owned temporary parent/,
  );
  assert.equal(spawned, false);
  assert.equal(removed, false);
});

test("accepts an owned Windows temp directory when realpath expands the parent 8.3 alias", async () => {
  const shortParent = "C:/Users/YESHAO~1/AppData/Local/Temp";
  const shortDirectory = `${shortParent}/dayorder-agent-integration-owned`;
  const longParent = "C:/Users/yeshaopeng/AppData/Local/Temp";
  const longDirectory = `${longParent}/dayorder-agent-integration-owned`;
  await runAgentIntegration(
    ["--database-source", "confighub"],
    successfulDependencies({
      env: Object.fromEntries(
        CONFIGHUB_DATABASE_KEYS.map((key) => [key, `${key}-value`]),
      ),
      platform: "win32",
      tempParent: shortParent,
      makeTemp: async () => shortDirectory,
      resolveTempParent: async () => resolve(longParent),
      inspectTemp: async () => ({
        resolved: resolve(longDirectory),
        device: 1,
        inode: 10,
        directory: true,
        symbolicLink: false,
      }),
    }),
  );
});

test("bounds finite child execution and enters cleanup on timeout", async () => {
  const cleanup = [];
  const execution = runAgentIntegration(
    ["--database-source", "confighub"],
    successfulDependencies({
      env: Object.fromEntries(
        CONFIGHUB_DATABASE_KEYS.map((key) => [key, `${key}-value`]),
      ),
      commandTimeoutMs: 20,
      spawnCommand: (spec) => ({
        spec,
        wait: () =>
          spec.label === "build-host"
            ? new Promise(() => {})
            : Promise.resolve(0),
      }),
      removeTemp: async (path) => cleanup.push(path),
    }),
  );
  await assert.rejects(
    Promise.race([
      execution,
      new Promise((_, reject) =>
        setTimeout(
          () => reject(new Error("test observed no orchestrator timeout")),
          200,
        ),
      ),
    ]),
    /build-host timed out/,
  );
  assert.deepEqual(cleanup, ["/tmp/dayorder-agent-integration-owned"]);
});

test("nonzero graceful Host shutdown is a cleanup failure", async () => {
  let host;
  await assert.rejects(
    runAgentIntegration(
      ["--database-source", "confighub"],
      successfulDependencies({
        env: Object.fromEntries(
          CONFIGHUB_DATABASE_KEYS.map((key) => [key, `${key}-value`]),
        ),
        spawnCommand: (spec) => {
          const child = {
            spec,
            stdin: { writable: true, write() {} },
            process: { kill() {} },
            wait: async () => (spec.label === "host" ? 7 : 0),
          };
          if (spec.label === "host") host = child;
          return child;
        },
        readHostReady: async () => ({
          type: "ready",
          apiURL: "http://127.0.0.1:49152",
        }),
        stopChild: undefined,
      }),
    ),
    /host exited with code 7/,
  );
  assert.ok(host, "Host was never started");
});

test("interruption requests cooperative Service Binding cleanup before generic termination", async () => {
  const controller = new AbortController();
  const cleanup = [];
  await assert.rejects(
    runAgentIntegration(
      ["--database-source", "confighub"],
      successfulDependencies({
        env: Object.fromEntries(
          CONFIGHUB_DATABASE_KEYS.map((key) => [key, `${key}-value`]),
        ),
        signal: controller.signal,
        spawnCommand: (spec) => ({
          spec,
          wait: () => {
            if (spec.label === "service-conformance") {
              controller.abort(new Error("controlled parent interruption"));
              return new Promise(() => {});
            }
            return Promise.resolve(0);
          },
        }),
        stopChild: async (child, options) =>
          cleanup.push([
            child.spec.label,
            options?.input ?? "",
            options?.requireExitZero ?? false,
            options?.timeoutMs ?? 0,
          ]),
        removeTemp: async (path) => cleanup.push(["remove", path]),
      }),
    ),
    /controlled parent interruption/,
  );
  assert.deepEqual(cleanup, [
    ["service-conformance", "stop\n", true, 165_000],
    ["remove", "/tmp/dayorder-agent-integration-owned"],
  ]);
});

test("bounds Host readiness output before a newline", async () => {
  const stdout = new PassThrough();
  const readiness = readHostReadyFromChild({
    stdout,
    wait: () => new Promise(() => {}),
  });
  stdout.write("x".repeat(65_537));
  await assert.rejects(readiness, /readiness output exceeded 65536 bytes/);
});

test("observes Vite early exit while waiting for readiness", async () => {
  await assert.rejects(
    runAgentIntegration(
      ["--database-source", "confighub"],
      successfulDependencies({
        env: Object.fromEntries(
          CONFIGHUB_DATABASE_KEYS.map((key) => [key, `${key}-value`]),
        ),
        spawnCommand: (spec) => ({
          spec,
          wait: () => Promise.resolve(spec.label === "vite" ? 3 : 0),
        }),
        waitForHTTP: () => new Promise(() => {}),
        commandTimeoutMs: 100,
      }),
    ),
    /vite exited with code 3/,
  );
});

test("refuses cleanup after the owned temporary directory identity changes", async () => {
  let inspections = 0;
  let removed = false;
  await assert.rejects(
    runAgentIntegration(
      ["--database-source", "confighub"],
      successfulDependencies({
        env: Object.fromEntries(
          CONFIGHUB_DATABASE_KEYS.map((key) => [key, `${key}-value`]),
        ),
        inspectTemp: async () => ({
          resolved: resolve("/tmp/dayorder-agent-integration-owned"),
          device: 1,
          inode: inspections++ === 0 ? 10 : 11,
          directory: true,
          symbolicLink: false,
        }),
        removeTemp: async () => {
          removed = true;
        },
      }),
    ),
    /temporary directory identity changed/,
  );
  assert.equal(removed, false);
});

test("formats primary and cleanup uncertainty without secret values", () => {
  const error = new AggregateError(
    [
      new Error("playwright exited with code 1"),
      new Error("host did not exit normally; cleanup is uncertain"),
    ],
    "playwright exited with code 1",
  );
  assert.deepEqual(formatAgentIntegrationError(error), [
    "agent integration failed: playwright exited with code 1",
    "agent integration cleanup failed: host did not exit normally; cleanup is uncertain",
  ]);
});

test("exact Go test output rejects skips, empty filters, and duplicate target execution", () => {
  const name = "TestAgentIntegrationServiceBinding";
  assert.doesNotThrow(() =>
    validateExactGoTestOutput(
      `=== RUN   ${name}\n--- PASS: ${name} (0.12s)\nPASS\n`,
      name,
    ),
  );
  assert.throws(
    () =>
      validateExactGoTestOutput(
        "testing: warning: no tests to run\nPASS\n",
        name,
      ),
    /did not report exactly one PASS/,
  );
  assert.throws(
    () =>
      validateExactGoTestOutput(
        `=== RUN   ${name}\n--- SKIP: ${name} (0.00s)\nPASS\n`,
        name,
      ),
    /did not report exactly one PASS/,
  );
  assert.throws(
    () =>
      validateExactGoTestOutput(
        `--- PASS: ${name} (0.01s)\n--- PASS: ${name} (0.01s)\nPASS\n`,
        name,
      ),
    /did not report exactly one PASS/,
  );
});

test("exact Go test output keeps forwarding guarded cleanup after parent cancellation", async () => {
  const stdout = new PassThrough();
  const controller = new AbortController();
  let forwarded = "";
  const child = {
    spec: { label: "service-conformance" },
    stdout,
    wait: () => new Promise(() => {}),
  };
  const verification = verifyExactGoTest(child, {
    label: "service-conformance",
    testName: "TestAgentIntegrationServiceBinding",
    timeoutMs: 1_000,
    signal: controller.signal,
    writeOutput: (chunk) => {
      forwarded += chunk;
    },
  });
  stdout.write("created isolated PostgreSQL database owned-fixture\n");
  controller.abort(new Error("controlled parent cancellation"));
  await assert.rejects(verification, /controlled parent cancellation/);
  stdout.end(
    "deleted isolated PostgreSQL database owned-fixture after guarded cleanup\n",
  );
  await new Promise((resolveWait) => stdout.once("close", resolveWait));
  assert.equal(
    forwarded,
    [
      "created isolated PostgreSQL database owned-fixture",
      "deleted isolated PostgreSQL database owned-fixture after guarded cleanup",
      "",
    ].join("\n"),
  );
});
