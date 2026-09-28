import type { ModelTurnRequest, ProviderEvent } from "../generated/protocol";

export interface ProviderGateway {
  stream(request: ModelTurnRequest, signal: AbortSignal): AsyncIterable<ProviderEvent>;
}
