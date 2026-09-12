import { createAgentClient, type AgentClient } from "../../../src/agent/api/client";
import { ApiError } from "../../../src/api/http";
import type { ReadonlyRunStart, ReadonlyRunView } from "../../../src/agent/generated/protocol";
import { runForeground as executeForeground } from "../../../src/agent/host/foreground";
import { createHttpProvider } from "../../../src/agent/provider/http";
import type { RuntimeTrace } from "../../../src/agent/runtime/driver";

interface HarnessConfig {
  accounts: Array<{ email: string; password: string; deviceId: string }>;
  profile: string;
  window: { start: string; end: string };
}

interface AgentHarness {
  runForeground(): Promise<{ run: ReadonlyRunView; trace: RuntimeTrace }>;
  runBackground(): Promise<ReadonlyRunView>;
  cancel(): void;
}

declare global {
  interface Window {
    agentTest: AgentHarness;
  }
}

const foregroundButton = requiredButton("foreground");
const backgroundButton = requiredButton("background");
const cancelButton = requiredButton("cancel");
const output = requiredOutput();

let active: AbortController | undefined;
let activeClient: AgentClient | undefined;
let activeRun: ReadonlyRunView | undefined;
let setup: Promise<{ config: HarnessConfig; client: AgentClient }> | undefined;
let initializing: AbortController | undefined;

const initializationTimeoutMs = 10_000;
const backgroundSettlementMs = 5_000;
const terminalRunStatuses = new Set<ReadonlyRunView["status"]>(["completed", "failed", "stopped"]);

function requiredButton(id: string): HTMLButtonElement {
  const button = document.querySelector<HTMLButtonElement>(`#${id}`);
  if (!button) throw new Error(`agent harness ${id} button is required`);
  return button;
}

function requiredOutput(): HTMLElement {
  const element = document.querySelector<HTMLElement>("#output");
  if (!element) throw new Error("agent harness output is required");
  return element;
}

function setOutput(value: unknown): void {
  output.textContent = typeof value === "string" ? value : JSON.stringify(value, null, 2);
}

function setBusy(busy: boolean): void {
  foregroundButton.disabled = busy;
  backgroundButton.disabled = busy;
  cancelButton.disabled = !busy;
  foregroundButton.setAttribute("aria-busy", String(busy));
  backgroundButton.setAttribute("aria-busy", String(busy));
}

function boundedSignal(milliseconds: number, parent?: AbortSignal, timeoutMessage = "agent harness initialization timed out"): { signal: AbortSignal; dispose(): void } {
  const controller = new AbortController();
  const onParentAbort = () => controller.abort(parent?.reason);
  if (parent?.aborted) onParentAbort();
  else parent?.addEventListener("abort", onParentAbort, { once: true });
  const timer = setTimeout(
    () => controller.abort(new DOMException(timeoutMessage, "TimeoutError")),
    milliseconds,
  );
  return {
    signal: controller.signal,
    dispose() {
      clearTimeout(timer);
      parent?.removeEventListener("abort", onParentAbort);
    },
  };
}

async function prepare(signal: AbortSignal): Promise<{ config: HarnessConfig; client: AgentClient }> {
  if (setup) return setup;
  const pending = (async () => {
    const configResponse = await fetch("/__test/config", { credentials: "include", cache: "no-store", signal });
    if (!configResponse.ok) throw new Error("测试配置不可用");
    const config = await configResponse.json() as HarnessConfig;
    const account = config.accounts[0];
    if (!account) throw new Error("测试账号不可用");
    const loginResponse = await fetch("/api/v1/auth/login", {
      method: "POST",
      credentials: "include",
      cache: "no-store",
      signal,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: account.email, password: account.password }),
    });
    if (!loginResponse.ok) throw new Error("测试账号登录失败");
    return { config, client: createAgentClient({ baseURL: "/api/v1", deviceId: account.deviceId }) };
  })();
  setup = pending;
  try {
    return await pending;
  } catch (error) {
    if (setup === pending) setup = undefined;
    throw error;
  }
}

async function ready(signal: AbortSignal): Promise<{ config: HarnessConfig; client: AgentClient }> {
  const prepared = await prepare(signal);
  window.agentTest = harness;
  return prepared;
}

async function readyWithinTimeout(signal: AbortSignal): Promise<{ config: HarnessConfig; client: AgentClient }> {
  const timeout = boundedSignal(initializationTimeoutMs, signal);
  try {
    return await ready(timeout.signal);
  } finally {
    timeout.dispose();
  }
}

function runInput(config: HarnessConfig, executionMode: "foreground" | "background"): ReadonlyRunStart {
  return {
    executionMode,
    intent: "概览授权时间窗内的日程。",
    modelProfile: config.profile,
    timezone: "UTC",
    scope: { domains: ["calendar"], from: config.window.start, to: config.window.end },
  };
}

async function withinActiveRun<T>(operation: (signal: AbortSignal) => Promise<T>): Promise<T> {
  if (active) throw new Error("已有运行中的测试");
  active = new AbortController();
  setBusy(true);
  setOutput("运行中…");
  try {
    const result = await operation(active.signal);
    setOutput(result);
    return result;
  } catch {
    setOutput("运行未完成，请检查受控诊断状态。");
    throw new Error("agent harness run failed");
  } finally {
    active = undefined;
    activeClient = undefined;
    activeRun = undefined;
    setBusy(false);
  }
}

function pollingStopAt(view: ReadonlyRunView, requestStartedAt: number): number {
  const serverRemaining = Date.parse(view.deadlineAt) - Date.parse(view.serverNow);
  if (!Number.isFinite(serverRemaining)) throw new Error("Run deadline timestamps are invalid");
  const requestElapsed = Math.max(0, Date.now() - requestStartedAt);
  return Date.now() + Math.max(0, serverRemaining - requestElapsed) + backgroundSettlementMs;
}

function pollDelay(signal: AbortSignal, milliseconds = 100): Promise<void> {
  if (signal.aborted) return Promise.reject(signal.reason);
  return new Promise<void>((resolve, reject) => {
    const finish = (operation: () => void) => {
      clearTimeout(timer);
      signal.removeEventListener("abort", onAbort);
      operation();
    };
    const onAbort = () => finish(() => reject(signal.reason));
    const timer = setTimeout(() => finish(resolve), milliseconds);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

async function cancelBackgroundRun(client: AgentClient, created: ReadonlyRunView): Promise<void> {
  const cleanup = new AbortController();
  const timer = setTimeout(
    () => cleanup.abort(new DOMException("cancel timed out", "TimeoutError")),
    backgroundSettlementMs,
  );
  try {
    let current = created;
    try {
      current = await client.get(created.runId, cleanup.signal);
      if (terminalRunStatuses.has(current.status)) return;
    } catch {
      // A cancellation attempt using the last known version is still worthwhile.
    }
    try {
      await client.cancel(created.runId, current.version, cleanup.signal);
    } catch (error) {
      if (!(error instanceof ApiError) || error.status !== 409) return;
      current = await client.get(created.runId, cleanup.signal);
      if (!terminalRunStatuses.has(current.status)) {
        await client.cancel(created.runId, current.version, cleanup.signal);
      }
    }
  } catch {
    // Background cancellation is best effort; acceptance observes authoritative state separately.
  } finally {
    clearTimeout(timer);
  }
}

const harness: AgentHarness = {
  runForeground() {
    return withinActiveRun(async (signal) => {
      const { config, client } = await readyWithinTimeout(signal);
      activeClient = client;
      const provider = createHttpProvider({ baseURL: "/api/v1", deviceId: config.accounts[0].deviceId });
      const result = await executeForeground(runInput(config, "foreground"), client, provider, signal);
      activeRun = result.run;
      return result;
    });
  },
  runBackground() {
    return withinActiveRun(async (signal) => {
      const { config, client } = await readyWithinTimeout(signal);
      activeClient = client;
      const requestStartedAt = Date.now();
      activeRun = await client.create(runInput(config, "background"), signal);
      const stopAt = pollingStopAt(activeRun, requestStartedAt);
      while (!terminalRunStatuses.has(activeRun.status)) {
        if (Date.now() >= stopAt) throw new Error("background polling timed out");
        await pollDelay(signal);
        if (Date.now() >= stopAt) throw new Error("background polling timed out");
        const requestTimeout = boundedSignal(stopAt - Date.now(), signal, "background polling timed out");
        try {
          activeRun = await client.get(activeRun.runId, requestTimeout.signal);
        } finally {
          requestTimeout.dispose();
        }
      }
      return activeRun;
    });
  },
  cancel() {
    const controller = active;
    const client = activeClient;
    const run = activeRun;
    const reason = new DOMException("agent run cancelled", "AbortError");
    initializing?.abort(reason);
    controller?.abort(reason);
    if (client && run && !terminalRunStatuses.has(run.status)) {
      void cancelBackgroundRun(client, run);
    }
  },
};

foregroundButton.addEventListener("click", () => { void harness.runForeground().catch(() => undefined); });
backgroundButton.addEventListener("click", () => { void harness.runBackground().catch(() => undefined); });
cancelButton.addEventListener("click", () => harness.cancel());

setBusy(true);
setOutput("正在准备测试环境…");
const initializationController = new AbortController();
initializing = initializationController;
const initializationTimeout = boundedSignal(initializationTimeoutMs, initializationController.signal);
void ready(initializationTimeout.signal)
  .then(() => {
    setOutput("测试环境已就绪。");
    setBusy(false);
  })
  .catch(() => {
    setOutput("初始化失败，请重试运行。");
    setBusy(false);
  })
  .finally(() => {
    initializationTimeout.dispose();
    if (initializing === initializationController) initializing = undefined;
  });
