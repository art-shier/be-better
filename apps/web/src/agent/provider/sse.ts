import type { ProviderEnvelope, ProviderEvent } from "../generated/protocol";
import { validateProtocol } from "../protocol/validate";
import { providerEventPayloadError } from "../runtime/reducer";

const MAX_FRAME_BYTES = 64 * 1024;
const MAX_TURN_BYTES = 1024 * 1024;
const MAX_WIRE_LINE_BYTES = MAX_FRAME_BYTES + 7;
const encoder = new TextEncoder();

export interface ProviderStreamIdentity {
  runId: string;
  turnId: string;
}

function abortReason(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException("The operation was aborted", "AbortError");
}

function assertWireLineLimit(line: string): void {
  if (encoder.encode(line).byteLength <= MAX_WIRE_LINE_BYTES) return;
  if (line.startsWith("data:")) throw new Error("provider SSE frame exceeds 64 KiB");
  throw new Error("provider SSE wire line exceeds its buffer limit");
}

function readWithSignal(
  reader: ReadableStreamDefaultReader<Uint8Array>,
  signal: AbortSignal,
): Promise<ReadableStreamReadResult<Uint8Array>> {
  if (signal.aborted) return Promise.reject(abortReason(signal));
  return new Promise((resolve, reject) => {
    const onAbort = (): void => {
      void reader.cancel(abortReason(signal)).catch(() => undefined);
      reject(abortReason(signal));
    };
    signal.addEventListener("abort", onAbort, { once: true });
    reader.read().then(
      (result) => {
        signal.removeEventListener("abort", onAbort);
        if (signal.aborted) reject(abortReason(signal));
        else resolve(result);
      },
      (error: unknown) => {
        signal.removeEventListener("abort", onAbort);
        reject(signal.aborted ? abortReason(signal) : error);
      },
    );
  });
}

export async function* parseProviderSSE(
  body: ReadableStream<Uint8Array>,
  identity: ProviderStreamIdentity,
  signal: AbortSignal,
): AsyncIterable<ProviderEvent> {
  const reader = body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let text = "";
  let dataLines: string[] = [];
  let pendingDataBytes = 0;
  let nextSequence = 1;
  let totalDataBytes = 0;
  let terminal = false;

  const consumeLine = (line: string): ProviderEvent | undefined => {
    if (line !== "") {
      if (line.startsWith(":")) return undefined;
      if (!line.startsWith("data:")) return undefined;
      const value = line.slice(5);
      const field = value.startsWith(" ") ? value.slice(1) : value;
      const nextPendingBytes = pendingDataBytes + (dataLines.length === 0 ? 0 : 1) + encoder.encode(field).byteLength;
      if (nextPendingBytes > MAX_FRAME_BYTES) throw new Error("provider SSE frame exceeds 64 KiB");
      if (totalDataBytes + nextPendingBytes > MAX_TURN_BYTES) {
        throw new Error("provider SSE data exceeds 1 MiB per turn");
      }
      pendingDataBytes = nextPendingBytes;
      dataLines.push(field);
      return undefined;
    }
    if (dataLines.length === 0) return undefined;

    const data = dataLines.join("\n");
    dataLines = [];
    const frameBytes = pendingDataBytes;
    pendingDataBytes = 0;
    // SSE joins data fields with one LF; that inserted byte is part of the
    // cumulative data allowance, while comments and field framing are not.
    totalDataBytes += frameBytes;
    let parsed: unknown;
    try {
      parsed = JSON.parse(data);
    } catch {
      throw new Error("provider SSE frame contains invalid JSON");
    }
    const envelope = validateProtocol<ProviderEnvelope>("ProviderEnvelope", parsed);
    if (envelope.runId !== identity.runId) throw new Error("provider SSE runId does not match stream context");
    if (envelope.turnId !== identity.turnId) throw new Error("provider SSE turnId does not match stream context");
    if (envelope.sequence !== nextSequence) throw new Error(`provider SSE sequence must be ${nextSequence}`);
    nextSequence += 1;
    const payloadError = providerEventPayloadError(envelope.event);
    if (payloadError) throw new Error(`invalid ProviderEvent payload: ${payloadError}`);
    const event = structuredClone(validateProtocol<ProviderEvent>("ProviderEvent", envelope.event));
    if (terminal) throw new Error("provider SSE emitted an event after terminal completion");
    if (event.type === "completed" || event.type === "error") terminal = true;
    return event;
  };

  try {
    while (true) {
      const result = await readWithSignal(reader, signal);
      if (result.done) break;
      text += decoder.decode(result.value, { stream: true });
      while (true) {
        const newline = text.indexOf("\n");
        if (newline < 0) break;
        let line = text.slice(0, newline);
        text = text.slice(newline + 1);
        assertWireLineLimit(line);
        if (line.endsWith("\r")) line = line.slice(0, -1);
        const event = consumeLine(line);
        if (event) yield event;
      }
      assertWireLineLimit(text);
    }
    text += decoder.decode();
    if (text !== "") {
      assertWireLineLimit(text);
      if (text.endsWith("\r")) text = text.slice(0, -1);
      const event = consumeLine(text);
      if (event) yield event;
    }
    const finalEvent = consumeLine("");
    if (finalEvent) yield finalEvent;
    if (!terminal) throw new Error("provider SSE ended without terminal completion");
  } finally {
    try {
      await reader.cancel();
    } catch {
      // Cleanup must not replace the stream result.
    }
    reader.releaseLock();
  }
}
