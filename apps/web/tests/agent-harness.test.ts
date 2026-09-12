import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ReadonlyRunView } from "../src/agent/generated/protocol";

interface TestHarness {
  runBackground(): Promise<ReadonlyRunView>;
  cancel(): void;
}

const account = {
  email: "agent-a@example.test",
  password: "Agent-Fixture-Password-2026",
  deviceId: "22222222-2222-4222-8222-222222222222",
};

const configuration = {
  accounts: [account],
  profile: "readonly-fake",
  window: { start: "2026-09-05T00:00:00Z", end: "2026-09-06T00:00:00Z" },
};

function run(status: ReadonlyRunView["status"], serverNow = "2026-09-05T00:00:00Z", deadlineAt = "2026-09-05T00:02:00Z"): ReadonlyRunView {
  return {
    protocolVersion: "2.0",
    runId: "11111111-1111-4111-8111-111111111111",
    executionMode: "background",
    status,
    version: 1,
    capabilitySnapshot: {
      runtimeVersion: "2.0.0",
      executionMode: "background",
      toolIds: ["dayorder.calendar.read"],
      skills: [],
      scope: { domains: ["calendar"], from: configuration.window.start, to: configuration.window.end },
    },
    budget: {
      maxSteps: 8,
      maxTokens: 16_000,
      maxDurationMs: 120_000,
      maxWorkers: 1,
      maxConcurrency: 1,
      maxRepeatedToolCalls: 2,
    },
    modelProfile: configuration.profile,
    serverNow,
    deadlineAt,
    usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
    usageComplete: false,
    resultOrigin: "server_runtime",
  };
}

function json(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

function mount(): void {
  document.body.innerHTML = `
    <button id="foreground" type="button" disabled>foreground</button>
    <button id="background" type="button" disabled>background</button>
    <button id="cancel" type="button" disabled>cancel</button>
    <pre id="output" role="status"></pre>
  `;
}

function publishedHarness(): TestHarness | undefined {
  return (window as Window & { agentTest?: TestHarness }).agentTest;
}

async function loadHarness(): Promise<void> {
  await import("./agent-integration/harness/main");
}

beforeEach(() => {
  vi.resetModules();
  Reflect.deleteProperty(window, "agentTest");
  mount();
});

afterEach(() => {
  Reflect.deleteProperty(window, "agentTest");
});

describe("agent integration harness readiness", () => {
  it("propagates cancel while the initial config request is pending", async () => {
    let initializationSignal: AbortSignal | undefined;
    vi.stubGlobal("fetch", vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      initializationSignal = init?.signal ?? undefined;
      return new Promise<Response>((_resolve, reject) => {
        initializationSignal?.addEventListener("abort", () => reject(initializationSignal?.reason), { once: true });
      });
    }));

    await loadHarness();
    expect(document.querySelector<HTMLButtonElement>("#cancel")).toBeEnabled();

    document.querySelector<HTMLButtonElement>("#cancel")?.click();

    await vi.waitFor(() => expect(initializationSignal?.aborted).toBe(true));
    await vi.waitFor(() => expect(document.querySelector<HTMLButtonElement>("#background")).toBeEnabled());
    expect(publishedHarness()).toBeUndefined();
  });

  it("publishes automation only after bounded config and real login complete", async () => {
    let releaseConfig!: (response: Response) => void;
    const pendingConfig = new Promise<Response>((resolve) => { releaseConfig = resolve; });
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const target = String(input);
      if (target === "/__test/config") return pendingConfig;
      if (target === "/api/v1/auth/login") return Promise.resolve(json({}));
      throw new Error(`unexpected fetch ${target}`);
    }));

    await loadHarness();

    expect(publishedHarness()).toBeUndefined();
    expect(document.querySelector<HTMLButtonElement>("#foreground")).toBeDisabled();
    expect(document.querySelector<HTMLButtonElement>("#background")).toBeDisabled();
    releaseConfig(json(configuration));
    await vi.waitFor(() => expect(publishedHarness()).toBeDefined());
    expect(document.querySelector<HTMLButtonElement>("#foreground")).toBeEnabled();
    expect(document.querySelector<HTMLButtonElement>("#background")).toBeEnabled();
  });

  it("recovers from a failed initialization on the next user action", async () => {
    let configCalls = 0;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const target = String(input);
      if (target === "/__test/config") {
        configCalls += 1;
        return Promise.resolve(configCalls === 1 ? json({}, 503) : json(configuration));
      }
      if (target === "/api/v1/auth/login") return Promise.resolve(json({}));
      if (target === "/api/v1/agent/runs" && init?.method === "POST") return Promise.resolve(json(run("completed"), 201));
      throw new Error(`unexpected fetch ${target}`);
    }));

    await loadHarness();
    await vi.waitFor(() => expect(document.querySelector("#output")).toHaveTextContent("初始化失败"));
    expect(publishedHarness()).toBeUndefined();
    expect(document.querySelector<HTMLButtonElement>("#background")).toBeEnabled();

    document.querySelector<HTMLButtonElement>("#background")?.click();

    await vi.waitFor(() => expect(publishedHarness()).toBeDefined());
    await vi.waitFor(() => expect(document.querySelector("#output")).toHaveTextContent("completed"));
    expect(configCalls).toBe(2);
  });

  it("cancels a bounded initialization retry without leaving controls busy", async () => {
    let configCalls = 0;
    let retrySignal: AbortSignal | undefined;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const target = String(input);
      if (target === "/__test/config") {
        configCalls += 1;
        if (configCalls === 1) return Promise.resolve(json({}, 503));
        retrySignal = init?.signal ?? undefined;
        return new Promise<Response>((_resolve, reject) => {
          retrySignal?.addEventListener("abort", () => reject(retrySignal?.reason), { once: true });
        });
      }
      throw new Error(`unexpected fetch ${target}`);
    }));

    await loadHarness();
    await vi.waitFor(() => expect(document.querySelector("#output")).toHaveTextContent("初始化失败"));
    document.querySelector<HTMLButtonElement>("#background")?.click();
    await vi.waitFor(() => expect(document.querySelector<HTMLButtonElement>("#cancel")).toBeEnabled());

    document.querySelector<HTMLButtonElement>("#cancel")?.click();

    await vi.waitFor(() => expect(retrySignal?.aborted).toBe(true));
    await vi.waitFor(() => expect(document.querySelector<HTMLButtonElement>("#background")).toBeEnabled());
    expect(publishedHarness()).toBeUndefined();
  });

  it("times out a stalled initialization retry without leaving controls busy", async () => {
    vi.useFakeTimers();
    let configCalls = 0;
    let retrySignal: AbortSignal | undefined;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const target = String(input);
      if (target === "/__test/config") {
        configCalls += 1;
        if (configCalls === 1) return Promise.resolve(json({}, 503));
        retrySignal = init?.signal ?? undefined;
        return new Promise<Response>((_resolve, reject) => {
          retrySignal?.addEventListener("abort", () => reject(retrySignal?.reason), { once: true });
        });
      }
      throw new Error(`unexpected fetch ${target}`);
    }));

    await loadHarness();
    await vi.advanceTimersByTimeAsync(1);
    expect(document.querySelector<HTMLButtonElement>("#background")).toBeEnabled();

    document.querySelector<HTMLButtonElement>("#background")?.click();
    await vi.advanceTimersByTimeAsync(1);
    expect(retrySignal).toBeDefined();
    expect(retrySignal?.aborted).toBe(false);
    expect(document.querySelector<HTMLButtonElement>("#cancel")).toBeEnabled();

    await vi.advanceTimersByTimeAsync(10_000);

    expect(retrySignal?.aborted).toBe(true);
    expect(document.querySelector<HTMLButtonElement>("#background")).toBeEnabled();
    expect(document.querySelector<HTMLButtonElement>("#cancel")).toBeDisabled();
    expect(publishedHarness()).toBeUndefined();
  });
});

describe("agent integration harness background lifecycle", () => {
  it("polls through the Run deadline plus a bounded settlement window", async () => {
    vi.useFakeTimers();
    let getCalls = 0;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const target = String(input);
      if (target === "/__test/config") return Promise.resolve(json(configuration));
      if (target === "/api/v1/auth/login") return Promise.resolve(json({}));
      if (target === "/api/v1/agent/runs" && init?.method === "POST") {
        return Promise.resolve(json(run("analyzing"), 201));
      }
      if (target.endsWith("/agent/runs/11111111-1111-4111-8111-111111111111") && (!init?.method || init.method === "GET")) {
        getCalls += 1;
        return Promise.resolve(json(run(getCalls > 151 ? "completed" : "analyzing")));
      }
      throw new Error(`unexpected fetch ${target}`);
    }));

    await loadHarness();
    await vi.advanceTimersByTimeAsync(1);
    expect(publishedHarness()).toBeDefined();
    const result = publishedHarness()!.runBackground();

    await vi.advanceTimersByTimeAsync(16_000);

    await expect(result).resolves.toMatchObject({ status: "completed" });
    expect(getCalls).toBeGreaterThan(151);
  });

  it("aborts an outstanding polling GET at the Run deadline plus settlement window", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-05T00:00:00Z"));
    let pollingSignal: AbortSignal | undefined;
    let cancelCalls = 0;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const target = String(input);
      if (target === "/__test/config") return Promise.resolve(json(configuration));
      if (target === "/api/v1/auth/login") return Promise.resolve(json({}));
      if (target === "/api/v1/agent/runs" && init?.method === "POST") {
        return Promise.resolve(json(run("analyzing", "2026-09-05T00:00:00Z", "2026-09-05T00:00:01Z"), 201));
      }
      if (target.endsWith("/agent/runs/11111111-1111-4111-8111-111111111111") && (!init?.method || init.method === "GET")) {
        pollingSignal = init?.signal ?? undefined;
        return new Promise<Response>((_resolve, reject) => {
          pollingSignal?.addEventListener("abort", () => reject(pollingSignal?.reason), { once: true });
        });
      }
      if (target.endsWith("/cancel")) {
        cancelCalls += 1;
        return Promise.resolve(json(run("stopped")));
      }
      throw new Error(`unexpected fetch ${target}`);
    }));

    await loadHarness();
    await vi.advanceTimersByTimeAsync(1);
    const result = publishedHarness()!.runBackground().catch((error: unknown) => error);

    await vi.advanceTimersByTimeAsync(100);
    expect(pollingSignal).toBeDefined();
    expect(pollingSignal?.aborted).toBe(false);

    await vi.advanceTimersByTimeAsync(5_899);
    expect(pollingSignal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);

    expect(pollingSignal?.aborted).toBe(true);
    await expect(result).resolves.toEqual(new Error("agent harness run failed"));
    expect(document.querySelector<HTMLButtonElement>("#background")).toBeEnabled();
    expect(cancelCalls).toBe(0);
  });

  it("removes each abort listener after a polling delay settles", async () => {
    vi.useFakeTimers();
    let getCalls = 0;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const target = String(input);
      if (target === "/__test/config") return Promise.resolve(json(configuration));
      if (target === "/api/v1/auth/login") return Promise.resolve(json({}));
      if (target === "/api/v1/agent/runs" && init?.method === "POST") return Promise.resolve(json(run("analyzing"), 201));
      if (target.includes("/agent/runs/11111111-1111-4111-8111-111111111111")) {
        getCalls += 1;
        return Promise.resolve(json(run(getCalls > 2 ? "completed" : "analyzing")));
      }
      throw new Error(`unexpected fetch ${target}`);
    }));
    const add = vi.spyOn(AbortSignal.prototype, "addEventListener");
    const remove = vi.spyOn(AbortSignal.prototype, "removeEventListener");

    await loadHarness();
    await vi.advanceTimersByTimeAsync(1);
    const result = publishedHarness()!.runBackground();
    await vi.advanceTimersByTimeAsync(500);
    await result;

    const added = add.mock.calls.filter(([type]) => type === "abort").length;
    const removed = remove.mock.calls.filter(([type]) => type === "abort").length;
    expect(added).toBeGreaterThan(0);
    expect(removed).toBe(added);
  });

  it("reconciles a Worker-advanced Run version before background cancellation", async () => {
    vi.useFakeTimers();
    const cancelVersions: string[] = [];
    let reconciliationSignal: AbortSignal | undefined;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const target = String(input);
      if (target === "/__test/config") return Promise.resolve(json(configuration));
      if (target === "/api/v1/auth/login") return Promise.resolve(json({}));
      if (target === "/api/v1/agent/runs" && init?.method === "POST") {
        return Promise.resolve(json(run("analyzing"), 201));
      }
      if (target.endsWith("/agent/runs/11111111-1111-4111-8111-111111111111") && (!init?.method || init.method === "GET")) {
        reconciliationSignal = init?.signal ?? undefined;
        return Promise.resolve(json({ ...run("analyzing"), version: 2 }));
      }
      if (target.endsWith("/cancel")) {
        cancelVersions.push(new Headers(init?.headers).get("If-Match") ?? "");
        if (cancelVersions.at(-1) !== `"2"`) {
          return Promise.resolve(json({ error: { code: "ENTITY_VERSION_CONFLICT", message: "controlled", retryable: false } }, 409));
        }
        return Promise.resolve(json({ ...run("stopped"), version: 3 }));
      }
      throw new Error(`unexpected fetch ${target}`);
    }));

    await loadHarness();
    await vi.advanceTimersByTimeAsync(1);
    const background = publishedHarness()!.runBackground().catch(() => undefined);
    await vi.advanceTimersByTimeAsync(0);

    publishedHarness()!.cancel();
    await vi.advanceTimersByTimeAsync(1);

    expect(reconciliationSignal?.aborted).toBe(false);
    expect(cancelVersions).toEqual([`"2"`]);
    await background;
  });

  it("handles a rejected best-effort cancel request", async () => {
    let cancelRequested = false;
    let polling = false;
    let getCalls = 0;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const target = String(input);
      if (target === "/__test/config") return Promise.resolve(json(configuration));
      if (target === "/api/v1/auth/login") return Promise.resolve(json({}));
      if (target === "/api/v1/agent/runs" && init?.method === "POST") return Promise.resolve(json(run("analyzing"), 201));
      if (target.endsWith("/cancel")) {
        cancelRequested = true;
        return Promise.resolve(json({ error: { code: "internal_error", message: "controlled", retryable: false } }, 500));
      }
      if (target.includes("/agent/runs/11111111-1111-4111-8111-111111111111")) {
        getCalls += 1;
        if (getCalls > 1) return Promise.resolve(json(run("analyzing")));
        polling = true;
        return new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () => reject(init.signal?.reason), { once: true });
        });
      }
      throw new Error(`unexpected fetch ${target}`);
    }));

    await loadHarness();
    await vi.waitFor(() => expect(publishedHarness()).toBeDefined());
    const background = publishedHarness()!.runBackground().catch(() => undefined);
    await vi.waitFor(() => expect(polling).toBe(true));

    publishedHarness()!.cancel();

    await vi.waitFor(() => expect(cancelRequested).toBe(true));
    await background;
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
});
