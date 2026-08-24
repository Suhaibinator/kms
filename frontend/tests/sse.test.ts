import { describe, expect, it, vi } from "vitest";
import { createSseParser, readEventStream, type SseMessage } from "@/lib/sse";

function collect() {
  const messages: SseMessage[] = [];
  const parser = createSseParser((message) => {
    messages.push(message);
  });
  return { messages, parser };
}

describe("createSseParser", () => {
  it("dispatches event/data frames split on blank lines, across chunk boundaries", () => {
    const { messages, parser } = collect();
    parser.push("event: snap");
    parser.push('shot\ndata: {"a":');
    parser.push("1}\n\nevent: end\ndata: bye\n\n");
    expect(messages).toEqual([
      { event: "snapshot", data: '{"a":1}' },
      { event: "end", data: "bye" },
    ]);
  });

  it("ignores comments, unknown fields and frames without data; joins multi-line data", () => {
    const { messages, parser } = collect();
    parser.push(": keep-alive\n\n");
    parser.push("event: orphan\n\n");
    parser.push("retry: 5000\nid: 7\ndata: first\ndata:second\ndata\n\n");
    parser.push("data:no-space\r\n\r\n");
    expect(messages).toEqual([
      { event: "message", data: "first\nsecond\n", id: "7" },
      { event: "message", data: "no-space" },
    ]);
  });

  it("flushes a final unterminated frame and honours stop", () => {
    const { messages, parser } = collect();
    expect(parser.push("data: tail")).toBeUndefined();
    expect(parser.flush()).toBeUndefined();
    expect(messages).toEqual([{ event: "message", data: "tail" }]);

    const seen: string[] = [];
    const stopping = createSseParser((m) => {
      seen.push(m.event);
      return m.event === "end" ? "stop" : undefined;
    });
    expect(stopping.push("event: a\ndata: 1\n\nevent: end\ndata: x\n\nevent: b\ndata: 2\n\n")).toBe(
      "stop",
    );
    expect(seen).toEqual(["a", "end"]);
  });
});

function bodyOf(chunks: string[], onCancel?: () => void): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  let index = 0;
  return new ReadableStream<Uint8Array>({
    pull(controller) {
      if (index < chunks.length) controller.enqueue(encoder.encode(chunks[index++]));
      else controller.close();
    },
    cancel() {
      onCancel?.();
    },
  });
}

describe("readEventStream", () => {
  it("reads to the end of the stream and decodes multi-byte characters across chunks", async () => {
    const seen: SseMessage[] = [];
    const bytes = new TextEncoder().encode("data: café\n\n");
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(bytes.slice(0, 9));
        controller.enqueue(bytes.slice(9));
        controller.close();
      },
    });
    await readEventStream(stream, (m) => {
      seen.push(m);
    });
    expect(seen).toEqual([{ event: "message", data: "café" }]);
  });

  it("stops reading (and cancels the body) when the handler returns stop", async () => {
    const cancelled = vi.fn();
    const seen: string[] = [];
    await readEventStream(
      bodyOf(
        ["event: snapshot\ndata: 1\n\n", "event: end\ndata: 0\n\n", "data: never\n\n"],
        cancelled,
      ),
      (m) => {
        seen.push(m.event);
        return m.event === "end" ? "stop" : undefined;
      },
    );
    expect(seen).toEqual(["snapshot", "end"]);
    expect(cancelled).toHaveBeenCalled();
  });

  it("rejects with an AbortError when the signal fires mid-stream", async () => {
    const controller = new AbortController();
    const stream = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(new TextEncoder().encode("data: 1\n\n"));
        // Never closes: the abort is the only way out.
      },
    });
    const done = readEventStream(
      stream,
      () => {
        controller.abort();
      },
      controller.signal,
    );
    await expect(done).rejects.toMatchObject({ name: "AbortError" });
  });

  it("rejects immediately when the signal is already aborted", async () => {
    const controller = new AbortController();
    controller.abort();
    await expect(
      readEventStream(bodyOf(["data: 1\n\n"]), () => {}, controller.signal),
    ).rejects.toMatchObject({ name: "AbortError" });
  });
});
