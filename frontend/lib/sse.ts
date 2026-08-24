// Fetch-streamed Server-Sent Events. EventSource cannot send an Authorization
// header, so the console reads `text/event-stream` bodies through fetch and
// parses the frames itself. This module is transport + parser only; the
// endpoint, auth header and error mapping live in lib/api.ts.

export interface SseMessage {
  // The `event:` field, or "message" when the frame did not set one.
  event: string;
  // `data:` lines joined with "\n", as the spec prescribes.
  data: string;
  id?: string;
}

// Return "stop" from the message handler to end the read early (the server's
// `end` event); anything else keeps reading until the stream closes.
// biome-ignore lint/suspicious/noConfusingVoidType: handlers that return nothing must stay assignable.
export type SseMessageHandler = (message: SseMessage) => void | "stop";

interface Frame {
  event: string;
  data: string[];
  id?: string;
}

const emptyFrame = (): Frame => ({ event: "", data: [] });

/**
 * Incremental SSE frame parser. Feed it text chunks as they arrive; it
 * dispatches one message per blank-line-terminated frame and ignores
 * `:` comment lines (the server's keep-alives) and unknown fields.
 */
export function createSseParser(onMessage: SseMessageHandler): {
  push: (chunk: string) => "stop" | undefined;
  flush: () => "stop" | undefined;
} {
  let buffer = "";
  let frame = emptyFrame();

  const dispatch = (): "stop" | undefined => {
    const current = frame;
    frame = emptyFrame();
    // A frame with no data lines is dropped, comment-only frames included.
    if (current.data.length === 0) return undefined;
    const message: SseMessage = {
      event: current.event || "message",
      data: current.data.join("\n"),
    };
    if (current.id !== undefined) message.id = current.id;
    return onMessage(message) === "stop" ? "stop" : undefined;
  };

  const handleLine = (rawLine: string): "stop" | undefined => {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    if (line === "") return dispatch();
    if (line.startsWith(":")) return undefined;
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    switch (field) {
      case "event":
        frame.event = value;
        break;
      case "data":
        frame.data.push(value);
        break;
      case "id":
        frame.id = value;
        break;
      default:
        // `retry` and anything unknown: ignored on purpose.
        break;
    }
    return undefined;
  };

  return {
    push(chunk) {
      buffer += chunk;
      let newline = buffer.indexOf("\n");
      while (newline !== -1) {
        const line = buffer.slice(0, newline);
        buffer = buffer.slice(newline + 1);
        if (handleLine(line) === "stop") return "stop";
        newline = buffer.indexOf("\n");
      }
      return undefined;
    },
    // The stream closed: a trailing unterminated line and a pending frame are
    // still delivered, so a server that omits the final blank line is fine.
    flush() {
      if (buffer !== "") {
        const line = buffer;
        buffer = "";
        if (handleLine(line) === "stop") return "stop";
      }
      return dispatch();
    },
  };
}

/**
 * Reads a response body to completion, dispatching each SSE frame. Resolves
 * when the handler returns "stop" or the server closes the stream; rejects
 * with the reader's error (an AbortError once `signal` fires).
 */
export async function readEventStream(
  body: ReadableStream<Uint8Array>,
  onMessage: SseMessageHandler,
  signal?: AbortSignal,
): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  const parser = createSseParser(onMessage);
  const cancel = () => void reader.cancel(signal?.reason).catch(() => undefined);
  if (signal?.aborted) {
    cancel();
    throw signal.reason instanceof Error ? signal.reason : abortError();
  }
  signal?.addEventListener("abort", cancel, { once: true });
  try {
    for (;;) {
      const { value, done } = await reader.read();
      // Our own abort listener cancels the reader, which surfaces here as a
      // clean `done` in some engines; the caller asked to stop, so say so.
      if (signal?.aborted) throw abortError();
      if (done) {
        parser.push(decoder.decode());
        parser.flush();
        return;
      }
      if (parser.push(decoder.decode(value, { stream: true })) === "stop") {
        cancel();
        return;
      }
    }
  } catch (err) {
    // A reader cancelled by our own abort listener resolves `read()` with
    // done=true in some engines and rejects in others; normalise to AbortError.
    if (signal?.aborted) throw abortError();
    throw err;
  } finally {
    signal?.removeEventListener("abort", cancel);
    reader.releaseLock();
  }
}

function abortError(): Error {
  return typeof DOMException !== "undefined"
    ? new DOMException("The stream was aborted.", "AbortError")
    : Object.assign(new Error("The stream was aborted."), { name: "AbortError" });
}
