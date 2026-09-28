import { ApiError } from "../../api/http";
import type { AgentClient } from "../api/client";
import { calendarOverviewBundle } from "../assets/builtin";
import type { ProviderEvent, ReadonlyRunFinish, ReadonlyRunStart, ReadonlyRunView, RuntimeState } from "../generated/protocol";
import type { ProviderGateway } from "../provider/provider";
import { driveToCompletion, type RuntimeTrace } from "../runtime/driver";
import { createRuntimeState } from "../runtime/reducer";
import { stopInput } from "../runtime/stop";
import { parseSkillBundle } from "../skill/profile";
import { SkillRegistry } from "../skill/registry";
import { createSkillToolBindings } from "../skill/tools";
import { createCalendarHttpBinding } from "../tool/calendar-http";
import { ToolRegistry, type ToolPolicy } from "../tool/registry";

const terminalStatuses = new Set<ReadonlyRunView["status"]>(["completed", "failed", "stopped"]);

function assertRunCapabilities(run: ReadonlyRunView, expectedSkill: { name: string; version: string; digest: string }): void {
  if (run.protocolVersion !== "2.0") throw new Error("unsupported agent protocol version");
  if (run.executionMode !== "foreground" || run.capabilitySnapshot.executionMode !== "foreground") {
    throw new Error("foreground Run requires foreground execution mode");
  }
  if (run.capabilitySnapshot.runtimeVersion !== "2.0.0") throw new Error("unsupported Runtime version");
  const skill = run.capabilitySnapshot.skills.find((candidate) => candidate.name === expectedSkill.name);
  if (!skill || skill.version !== expectedSkill.version || skill.digest !== expectedSkill.digest) {
    throw new Error("calendar Skill digest does not match the built-in bundle");
  }
}

function finalAssistantText(state: RuntimeState): string {
  for (let index = state.messages.length - 1; index >= 0; index -= 1) {
    const message = state.messages[index];
    if (message.role !== "assistant") continue;
    const text = message.content.filter((block) => block.type === "text").map((block) => block.text ?? "").join("");
    if (text !== "") return text.slice(0, 8_000);
  }
  return (state.assistantDraft ?? "").slice(0, 8_000);
}

function finishInput(state: RuntimeState): ReadonlyRunFinish {
  if (state.phase !== "completed" && state.phase !== "failed" && state.phase !== "cancelled") {
    throw new Error("Runtime did not reach a terminal phase");
  }
  return {
    phase: state.phase,
    summary: finalAssistantText(state),
    ...(state.phase === "completed" || state.error === undefined ? {} : { error: structuredClone(state.error) }),
    steps: [],
  };
}

function boundedSignal(milliseconds = 5_000): { signal: AbortSignal; abort(): void; dispose(): void } {
  const controller = new AbortController();
  const timer = setTimeout(
    () => controller.abort(new DOMException("agent persistence timed out", "TimeoutError")),
    milliseconds,
  );
  return {
    signal: controller.signal,
    abort: () => controller.abort(new DOMException("agent persistence closed", "AbortError")),
    dispose: () => clearTimeout(timer),
  };
}

type CancellationBarrier = () => Promise<unknown> | undefined;

function transportSignalAfterCancellation(
  source: AbortSignal,
  cancellationBarrier: CancellationBarrier,
): { signal: AbortSignal; dispose(): void } {
  const controller = new AbortController();
  const forwardAbort = (): void => {
    const release = () => controller.abort(source.reason);
    const cancellation = cancellationBarrier();
    if (cancellation) {
      void cancellation.then(release, release);
    } else {
      release();
    }
  };
  source.addEventListener("abort", forwardAbort, { once: true });
  if (source.aborted) forwardAbort();
  return {
    signal: controller.signal,
    dispose: () => source.removeEventListener("abort", forwardAbort),
  };
}

function providerAfterCancellation(provider: ProviderGateway, cancellationBarrier: CancellationBarrier): ProviderGateway {
  return {
    async *stream(request, sourceSignal) {
      const guarded = transportSignalAfterCancellation(sourceSignal, cancellationBarrier);
      let iterator: AsyncIterator<ProviderEvent> | undefined;
      let completed = false;
      try {
        iterator = provider.stream(request, guarded.signal)[Symbol.asyncIterator]();
        while (true) {
          const next = await iterator.next();
          if (next.done) {
            completed = true;
            return;
          }
          yield next.value;
        }
      } finally {
        const cancellation = sourceSignal.aborted ? cancellationBarrier() : undefined;
        if (cancellation) {
          try {
            await cancellation;
          } catch {
            // A failed bounded cancel still releases the underlying transport.
          }
        }
        guarded.dispose();
        if (!completed && iterator?.return) await iterator.return();
      }
    },
  };
}

function clientAfterCancellation(client: AgentClient, cancellationBarrier: CancellationBarrier): AgentClient {
  return {
    ...client,
    async calendar(runId, callId, input, sourceSignal) {
      const guarded = transportSignalAfterCancellation(sourceSignal, cancellationBarrier);
      try {
        return await client.calendar(runId, callId, input, guarded.signal);
      } finally {
        guarded.dispose();
      }
    },
  };
}

async function authoritativeAfterConflict(client: AgentClient, runId: string, signal: AbortSignal): Promise<ReadonlyRunView> {
  return client.get(runId, signal);
}

export async function runForeground(
  input: ReadonlyRunStart,
  client: AgentClient,
  provider: ProviderGateway,
  signal: AbortSignal,
): Promise<{ run: ReadonlyRunView; trace: RuntimeTrace }> {
  const profile = await parseSkillBundle(calendarOverviewBundle());
  const requestStartedAt = Date.now();
  const created = await client.create(input, signal);
  const elapsedRequestMs = Math.max(0, Date.now() - requestStartedAt);
  assertRunCapabilities(created, profile.descriptor);

  let cleanup: ReturnType<typeof boundedSignal> | undefined;
  let cancelOperation: Promise<ReadonlyRunView> | undefined;
  let releaseDeadlinePersistence!: () => void;
  const deadlinePersistence = new Promise<void>((resolve) => { releaseDeadlinePersistence = resolve; });
  const cleanupSignal = (): AbortSignal => {
    cleanup ??= boundedSignal();
    return cleanup.signal;
  };
  const userAborted = (): boolean => signal.aborted && stopInput(signal.reason).type === "cancel";
  const cancelAndReconcile = (): Promise<ReadonlyRunView> => {
    if (cancelOperation) return cancelOperation;
    cancelOperation = (async () => {
      const bounded = cleanupSignal();
      let current = created;
      try {
        current = await client.get(created.runId, bounded);
        if (terminalStatuses.has(current.status)) return current;
      } catch {
        // The explicit cancel attempt is still worthwhile with the last known version.
      }
      try {
        for (let attempt = 0; attempt < 2; attempt += 1) {
          try {
            return await client.cancel(created.runId, current.version, bounded);
          } catch (error) {
            if (!(error instanceof ApiError) || error.status !== 409) throw error;
            current = await authoritativeAfterConflict(client, created.runId, bounded);
            if (terminalStatuses.has(current.status)) return current;
            if (attempt === 1) throw error;
          }
        }
        throw new Error("unreachable cancellation retry state");
      } catch (error) {
        try {
          const reconciled = await client.get(created.runId, bounded);
          if (terminalStatuses.has(reconciled.status)) return reconciled;
        } catch {
          // Preserve the original cancel failure when reconciliation is unavailable.
        }
        throw error;
      }
    })();
    return cancelOperation;
  };
  const cancellationBarrier = (): Promise<ReadonlyRunView> | undefined => cancelOperation;
  const providerAbortBarrier: CancellationBarrier = () => {
    if (cancelOperation) return cancelOperation;
    return signal.aborted ? undefined : deadlinePersistence;
  };
  const runtimeController = new AbortController();
  const forwardCallerAbort = (): void => {
    if (userAborted()) void cancelAndReconcile().catch(() => undefined);
    runtimeController.abort(signal.reason);
  };
  signal.addEventListener("abort", forwardCallerAbort, { once: true });
  if (signal.aborted) forwardCallerAbort();

  try {
    const snapshot = structuredClone(created.capabilitySnapshot);
    const skills = new SkillRegistry([profile]);
    const tools = new ToolRegistry("foreground");
    const policy: ToolPolicy = { allow: [...snapshot.toolIds], deny: [], approvalFor: [] };
    tools.register(createCalendarHttpBinding(clientAfterCancellation(client, cancellationBarrier)));
    for (const binding of createSkillToolBindings(skills, snapshot, tools, policy)) tools.register(binding);

    const serverRemaining = Date.parse(created.deadlineAt) - Date.parse(created.serverNow);
    if (!Number.isFinite(serverRemaining)) throw new Error("Run deadline timestamps are invalid");
    const localDeadlineAtMs = Date.now() + Math.max(0, serverRemaining - elapsedRequestMs);
    const state = createRuntimeState({
      runId: created.runId,
      executionMode: "foreground",
      capabilitySnapshot: snapshot,
      budget: structuredClone(created.budget),
    });
    const trace = await driveToCompletion(
      state,
      { type: "user_message", text: input.intent },
      {
        provider: providerAfterCancellation(provider, providerAbortBarrier),
        tools,
        approvals: { request: async () => "deny" },
        modelProfile: created.modelProfile,
        policy,
        deadlineAtMs: localDeadlineAtMs,
      },
      runtimeController.signal,
    );

    const runtimeTimedOut = trace.state.phase === "failed" && trace.state.error?.code === "timeout";
    const persistenceSignal = signal.aborted || runtimeTimedOut ? cleanupSignal() : signal;
    if (userAborted()) return { run: await cancelAndReconcile(), trace };

    let latest: ReadonlyRunView;
    try {
      latest = await client.get(created.runId, persistenceSignal);
    } catch (error) {
      if (userAborted()) return { run: await cancelAndReconcile(), trace };
      throw error;
    }
    if (terminalStatuses.has(latest.status)) return { run: latest, trace };
    if (userAborted()) return { run: await cancelAndReconcile(), trace };
    try {
      const run = await client.finish(created.runId, latest.version, finishInput(trace.state), persistenceSignal);
      return { run, trace };
    } catch (error) {
      if (userAborted()) return { run: await cancelAndReconcile(), trace };
      if (error instanceof ApiError && error.status === 409) {
        try {
          return { run: await authoritativeAfterConflict(client, created.runId, persistenceSignal), trace };
        } catch (reconcileError) {
          if (userAborted()) return { run: await cancelAndReconcile(), trace };
          throw reconcileError;
        }
      }
      throw error;
    }
  } finally {
    signal.removeEventListener("abort", forwardCallerAbort);
    releaseDeadlinePersistence();
    cleanup?.abort();
    cleanup?.dispose();
  }
}
