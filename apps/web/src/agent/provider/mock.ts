import type { ModelTurnRequest, ProviderEvent } from "../generated/protocol";
import type { ProviderGateway } from "./provider";

function copy<T>(value: T): T {
  return structuredClone(value);
}

/** Deterministic test infrastructure. It never performs network I/O. */
export class ScriptedProvider implements ProviderGateway {
  private readonly scripts: ProviderEvent[][];
  private nextScript = 0;
  private count = 0;

  constructor(scripts: ProviderEvent[][]) {
    this.scripts = copy(scripts);
  }

  get requestCount(): number {
    return this.count;
  }

  async *stream(_request: ModelTurnRequest, signal: AbortSignal): AsyncIterable<ProviderEvent> {
    this.count += 1;
    if (signal.aborted) return;

    const script = this.scripts[this.nextScript];
    this.nextScript += 1;
    if (script === undefined) {
      yield {
        type: "error",
        error: {
          code: "provider_unavailable",
          message: "scripted provider exhausted",
          retryable: false,
        },
      };
      return;
    }

    for (const event of script) {
      if (signal.aborted) return;
      yield copy(event);
    }
  }
}
