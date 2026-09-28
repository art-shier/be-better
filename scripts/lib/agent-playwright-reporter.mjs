import { resolve } from "node:path";

export const exactFakePlaywrightTests = Object.freeze({
  "failures.spec.ts": Object.freeze([
    "tool and Run timeout outcomes compare across the real browser and Worker hosts",
    "user cancellation stops real Provider and Calendar dependencies within one second and compares traces",
    "remaining Worker fault matrix preserves the readonly terminal boundary",
  ]),
  "readonly.spec.ts": Object.freeze([
    "foreground and page-independent background runs preserve one ordered readonly contract",
    "fixed disjoint calendar fixture proves two pages, cursor integrity, and the real 64 KiB result limit",
    "browser security boundaries use real cookies, origin, and device identity",
  ]),
});

const identities = Object.entries(exactFakePlaywrightTests).flatMap(
  ([file, titles]) => titles.map((title) => ({ file, title })),
);

export function validateExactFakePlaywrightRunOutput(output) {
  const rejectExecution = () => {
    throw new Error(
      "exact six Fake tests must each have executed once and passed without skips or retries",
    );
  };
  let report;
  try {
    report = JSON.parse(output);
  } catch {
    rejectExecution();
  }
  if (
    report?.type !== "dayorder-agent-playwright" ||
    report.version !== 1 ||
    report.status !== "passed" ||
    report.listed !== 6 ||
    report.errors !== false ||
    report.overflow !== false ||
    !Array.isArray(report.tests) ||
    report.tests.length !== 6
  )
    rejectExecution();
  const seen = new Set();
  for (const result of report.tests) {
    if (
      !Number.isInteger(result?.index) ||
      result.index < 0 ||
      result.index >= 6 ||
      seen.has(result.index) ||
      result.status !== "passed" ||
      result.expectedStatus !== "passed" ||
      result.outcome !== "expected" ||
      result.retry !== 0 ||
      result.disallowedAnnotations !== false ||
      result.errors !== false
    )
      rejectExecution();
    seen.add(result.index);
  }
}

function safeEnum(value, allowed) {
  return allowed.includes(value) ? value : "invalid";
}

// Only fixed identities, enums, booleans and bounded counts leave the reporter.
// Never serialize TestCase/TestResult, errors, steps, attachments or worker output.
export default class AgentPlaywrightReporter {
  #root;
  #listed = -1;
  #errors = false;
  #overflow = false;
  #tests = [];

  printsToStdio() {
    return true;
  }

  onBegin(config, suite) {
    if (this.#root !== undefined) this.#errors = true;
    this.#root = config.rootDir;
    this.#listed = Math.min(suite.allTests().length, 7);
  }

  onError() {
    this.#errors = true;
  }

  onTestEnd(test, result) {
    if (this.#tests.length >= 6) {
      this.#overflow = true;
      return;
    }
    const statuses = ["passed", "failed", "timedOut", "skipped", "interrupted"];
    this.#tests.push({
      index: identities.findIndex(
        ({ file, title }) =>
          test.title === title &&
          resolve(test.location.file) === resolve(this.#root, file),
      ),
      status: safeEnum(result.status, statuses),
      expectedStatus: safeEnum(test.expectedStatus, statuses),
      outcome: safeEnum(test.outcome(), [
        "expected",
        "unexpected",
        "flaky",
        "skipped",
      ]),
      retry: result.retry === 0 ? 0 : 1,
      disallowedAnnotations: test.annotations.some(
        ({ type }) => type === "skip" || type === "fixme",
      ),
      errors: result.errors.length !== 0,
    });
  }

  onEnd(result) {
    process.stdout.write(
      JSON.stringify({
        type: "dayorder-agent-playwright",
        version: 1,
        status: safeEnum(result.status, [
          "passed",
          "failed",
          "timedout",
          "interrupted",
        ]),
        listed: this.#listed,
        errors: this.#errors,
        overflow: this.#overflow,
        tests: this.#tests,
      }) + "\n",
    );
  }
}
