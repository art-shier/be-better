import { describe, expect, it } from "vitest";

import { parseProviderSSE } from "./sse";

const encoder = new TextEncoder();

function stream(chunks: Array<string | Uint8Array>): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(typeof chunk === "string" ? encoder.encode(chunk) : chunk);
      controller.close();
    },
  });
}

function envelope(sequence: number, event: Record<string, unknown>, ids = { runId: "r", turnId: "t" }): string {
  return JSON.stringify({ protocolVersion: "2.0", ...ids, sequence, event });
}

async function collect(body: ReadableStream<Uint8Array>, signal = new AbortController().signal) {
  const events = [];
  for await (const event of parseProviderSSE(body, { runId: "r", turnId: "t" }, signal)) events.push(event);
  return events;
}

describe("parseProviderSSE", () => {
  it("decodes one-byte UTF-8 chunks and rejects EOF without a terminal event", async () => {
    const raw = encoder.encode(`data: ${envelope(1, { type: "text_delta", text: "日程" })}\n\n`);
    const body = stream([...raw].map((byte) => Uint8Array.of(byte)));

    const seen: string[] = [];
    await expect((async () => {
      for await (const event of parseProviderSSE(body, { runId: "r", turnId: "t" }, new AbortController().signal)) {
        seen.push(event.text!);
      }
    })()).rejects.toThrow(/completion|terminal/i);
    expect(seen).toEqual(["日程"]);
  });

  it("supports BOM, CRLF, comments, and newline-joined data fields", async () => {
    const completed = envelope(2, {
      type: "completed",
      stopReason: "end_turn",
      usage: { inputTokens: 1, outputTokens: 2, totalTokens: 3 },
    });
    const splitAt = completed.indexOf('"event"');
    const body = stream([
      `\uFEFF: heartbeat\r\ndata: ${envelope(1, { type: "text_delta", text: "ok" })}\r\n\r\n`,
      `data: ${completed.slice(0, splitAt)}\r\ndata:${completed.slice(splitAt)}\r\n\r\n`,
    ]);

    await expect(collect(body)).resolves.toEqual([
      { type: "text_delta", text: "ok" },
      { type: "completed", stopReason: "end_turn", usage: { inputTokens: 1, outputTokens: 2, totalTokens: 3 } },
    ]);
  });

  it.each([
    ["malformed JSON", "not-json", /JSON/i],
    ["invalid event payload", envelope(1, { type: "text_delta" }), /ProviderEvent|payload|validation/i],
    ["wrong run", envelope(1, { type: "error", error: { code: "internal_error", message: "x", retryable: false } }, { runId: "other", turnId: "t" }), /runId|context/i],
    ["wrong turn", envelope(1, { type: "error", error: { code: "internal_error", message: "x", retryable: false } }, { runId: "r", turnId: "other" }), /turnId|context/i],
    ["sequence gap", envelope(2, { type: "error", error: { code: "internal_error", message: "x", retryable: false } }), /sequence/i],
  ])("rejects %s", async (_name, data, error) => {
    await expect(collect(stream([`data: ${data}\n\n`]))).rejects.toThrow(error);
  });

  it("enforces the 64 KiB frame limit and 1 MiB turn limit", async () => {
    const oversized = envelope(1, { type: "text_delta", text: "x".repeat(65_536) });
    await expect(collect(stream([`data: ${oversized}\n\n`]))).rejects.toThrow(/frame|64/i);

    const frames: string[] = [];
    for (let sequence = 1; sequence <= 20; sequence += 1) {
      frames.push(`data: ${envelope(sequence, { type: "text_delta", text: "x".repeat(60_000) })}\n\n`);
    }
    await expect(collect(stream(frames))).rejects.toThrow(/turn|1 MiB|1048576/i);
  });

  it("allows near-limit cumulative data when comments and framing push wire bytes over 1 MiB", async () => {
    const frames: string[] = [];
    for (let sequence = 1; sequence <= 16; sequence += 1) {
      frames.push(`: ${"heartbeat".repeat(125)}\n`);
      frames.push(`data: ${envelope(sequence, { type: "text_delta", text: "x".repeat(65_000) })}\n\n`);
    }
    frames.push(`data: ${envelope(17, {
      type: "completed",
      stopReason: "end_turn",
      usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
    })}\n\n`);

    const events = await collect(stream(frames));
    expect(events).toHaveLength(17);
    expect(events.at(-1)?.type).toBe("completed");
  });

  it("rejects an unterminated multi-line data frame as soon as its pending payload exceeds 64 KiB", async () => {
    const pendingFrame = Array.from({ length: 65 }, () => `data: ${"x".repeat(1_024)}\n`).join("");
    let pulls = 0;
    const body = new ReadableStream<Uint8Array>({
      pull(controller) {
        if (pulls === 0) {
          pulls += 1;
          controller.enqueue(encoder.encode(pendingFrame));
          return;
        }
        controller.error(new Error("source read past payload limit"));
      },
    });

    await expect(collect(body)).rejects.toThrow(/frame|64 KiB/i);
    expect(pulls).toBe(1);
  });

  it("propagates caller cancellation and cancels the reader", async () => {
    let cancelled = false;
    const body = new ReadableStream<Uint8Array>({
      pull() {
        return new Promise(() => undefined);
      },
      cancel() {
        cancelled = true;
      },
    });
    const controller = new AbortController();
    const pending = collect(body, controller.signal);
    const reason = new DOMException("user stopped", "AbortError");
    controller.abort(reason);

    await expect(pending).rejects.toBe(reason);
    expect(cancelled).toBe(true);
  });
});
