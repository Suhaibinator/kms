import { describe, expect, it, vi } from "vitest";

import { KmsClient, type Logger } from "../src/client.js";
import type { SubscribeEvent, SubscribeRequest } from "../src/generated/kms.js";
import {
  createPolicyPublisher,
  definePublicProjection,
  type PolicySnapshot,
} from "../src/publishing.js";
import { type FakeDuplex, FakeTransport, waitFor } from "./helpers/fake-transport.js";

const namespace = "prod/api";

describe("bounded concurrency and lifecycle stress", () => {
  it("resumes one shared watch through a reconnect storm without live stream or task leaks", async () => {
    const random = vi.spyOn(Math, "random").mockReturnValue(0);
    const logger: Logger = { warn: vi.fn() };
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({ transport, namespace, logger });
    const stop = await client.watch(() => undefined);

    try {
      await waitFor(() => transport.streams.length === 1);
      const first = streamAt(transport, 0);
      await waitFor(() => first.sent.length === 1);
      first.emit({
        event: { $case: "snapshot", value: { parameters: [] } },
        revision: 0n,
      });

      const reconnects = 24;
      for (let index = 0; index < reconnects; index++) {
        const stream = streamAt(transport, index);
        const revision = BigInt(index + 1);
        await waitFor(() => stream.sent.length === 1);
        expect(stream.sent[0]).toMatchObject({
          lastSeenRevision: BigInt(index),
          namespaces: [{ env: "prod", app: "api" }],
        });

        stream.emit({
          event: { $case: "heartbeat", value: { serverTimeUnixMs: revision } },
          revision,
        });
        await waitFor(() => client.currentRevision === revision && stream.sent.length === 2);
        expect(stream.sent[1]).toMatchObject({ ackedRevision: revision });

        stream.cancel();
        await waitFor(() => transport.streams.length === index + 2);
        expect(stream.closed).toBe(true);
        expect(stream.pendingReadCount).toBe(0);
      }

      const live = streamAt(transport, reconnects);
      await waitFor(() => live.sent.length === 1 && live.pendingReadCount === 1);
      expect(live.sent[0]).toMatchObject({ lastSeenRevision: BigInt(reconnects) });
      expect(client.watchStatus).toMatchObject({
        state: "connected",
        currentRevision: BigInt(reconnects),
        reconnectCount: reconnects,
        watcherCount: 1,
      });
      expect(transport.streams.slice(0, -1).every((candidate) => candidate.closed)).toBe(true);
      expect(
        transport.streams.slice(0, -1).every((candidate) => candidate.pendingReadCount === 0),
      ).toBe(true);
    } finally {
      stop();
      await client.close();
      random.mockRestore();
    }

    expect(client.watchStatus).toMatchObject({ state: "stopped", watcherCount: 0 });
    expect(transport.streams.every((stream) => stream.closed)).toBe(true);
    expect(transport.streams.every((stream) => stream.pendingReadCount === 0)).toBe(true);
  });

  it("bounds callback backpressure and yields to unrelated event-loop work during a burst", async () => {
    const warnings: string[] = [];
    const logger: Logger = { warn: (message) => warnings.push(message) };
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({ transport, namespace, logger });
    let delivered = 0;
    const stop = await client.watch(() => {
      delivered += 1;
    });
    await waitFor(() => transport.streams.length === 1);
    const stream = streamAt(transport, 0);
    await waitFor(() => stream.sent.length === 1);

    const burst = 1_152;
    for (let index = 1; index <= burst; index++) {
      stream.emit(parameterChange(index));
    }
    await waitFor(() => client.currentRevision === BigInt(burst));

    let unrelatedImmediateRan = false;
    setImmediate(() => {
      unrelatedImmediateRan = true;
    });
    await waitFor(() => unrelatedImmediateRan);

    const dropped = warnings.filter((message) => message.includes("callback queue full"));
    expect(dropped).toHaveLength(burst - 1_024);
    expect(delivered).toBeLessThan(1_024);
    expect(dropped.every((message) => message.includes("/prod/api/hot"))).toBe(true);
    expect(warnings.join("\n")).not.toContain("burst-value");

    stop();
    await client.close();
    const deliveredAfterClose = delivered;
    await new Promise<void>((resolve) => setImmediate(resolve));
    expect(delivered).toBe(deliveredAfterClose);
    expect(stream.closed).toBe(true);
    expect(stream.pendingReadCount).toBe(0);
  });

  it("serializes callback promises so pending application work remains bounded", async () => {
    const warnings: string[] = [];
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({
      transport,
      namespace,
      logger: { warn: (message) => warnings.push(message) },
    });
    let releaseFirst: (() => void) | undefined;
    const firstPending = new Promise<void>((resolve) => {
      releaseFirst = resolve;
    });
    let delivered = 0;
    const stop = await client.watch(() => {
      delivered += 1;
      return delivered === 1 ? firstPending : undefined;
    });
    await waitFor(() => transport.streams.length === 1);
    const stream = streamAt(transport, 0);
    await waitFor(() => stream.sent.length === 1);
    stream.emit(parameterChange(1));
    await waitFor(() => delivered === 1);

    const burst = 1_100;
    for (let index = 2; index <= burst; index++) stream.emit(parameterChange(index));
    await waitFor(() => client.currentRevision === BigInt(burst));
    await new Promise<void>((resolve) => setImmediate(resolve));
    expect(delivered).toBe(1);
    expect(warnings.some((message) => message.includes("callback queue full"))).toBe(true);

    releaseFirst?.();
    await waitFor(() => delivered > 1);
    stop();
    await client.close();
  });

  it("removes watcher AbortSignal listeners across scaled stop, abort, and client-close paths", async () => {
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({
      transport,
      namespace,
      logger: { warn: vi.fn() },
    });

    for (let index = 0; index < 512; index++) {
      const controller = new AbortController();
      const add = vi.spyOn(controller.signal, "addEventListener");
      const remove = vi.spyOn(controller.signal, "removeEventListener");
      const stop = client.watchNamespace(namespace, () => undefined, {
        signal: controller.signal,
      });
      if (index % 2 === 0) stop();
      else controller.abort();

      expect(abortListenerCalls(add.mock.calls)).toBe(1);
      expect(abortListenerCalls(remove.mock.calls)).toBe(1);
      add.mockRestore();
      remove.mockRestore();
    }
    expect(client.watchStatus.watcherCount).toBe(0);

    const outstanding = Array.from({ length: 64 }, () => {
      const controller = new AbortController();
      const add = vi.spyOn(controller.signal, "addEventListener");
      const remove = vi.spyOn(controller.signal, "removeEventListener");
      client.watchNamespace(namespace, () => undefined, { signal: controller.signal });
      return { add, remove };
    });
    expect(client.watchStatus.watcherCount).toBe(outstanding.length);
    await client.close();

    for (const { add, remove } of outstanding) {
      expect(abortListenerCalls(add.mock.calls)).toBe(1);
      expect(abortListenerCalls(remove.mock.calls)).toBe(1);
      add.mockRestore();
      remove.mockRestore();
    }
    expect(client.watchStatus).toMatchObject({ state: "stopped", watcherCount: 0 });
    expect(transport.streams.every((stream) => stream.closed)).toBe(true);
    expect(transport.streams.every((stream) => stream.pendingReadCount === 0)).toBe(true);
  });

  it("releases distinct namespace scopes and idles background work after unsubscribe", async () => {
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({ transport, logger: { warn: vi.fn() } });
    const registrations = Array.from({ length: 256 }, (_, index) =>
      client.watchNamespace(`prod/app-${index}`, () => undefined),
    );
    expect(client.watchStatus).toMatchObject({
      namespaceCount: 256,
      watcherCount: 256,
    });

    for (const stop of registrations) stop();
    await waitFor(() => client.watchStatus.state === "idle");
    expect(client.watchStatus).toMatchObject({
      state: "idle",
      reconciliation: "not_started",
      namespaceCount: 0,
      watcherCount: 0,
      trackedParameterCount: 0,
    });
    expect(transport.streams.every((stream) => stream.closed)).toBe(true);
    expect(transport.streams.every((stream) => stream.pendingReadCount === 0)).toBe(true);
    await client.close();
  });

  it("prunes unique live tombstones instead of retaining path history", async () => {
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({ transport, namespace, logger: { warn: vi.fn() } });
    const stop = await client.watch(() => undefined);
    await waitFor(() => transport.streams.length === 1);
    const stream = streamAt(transport, 0);
    await waitFor(() => stream.sent.length === 1);

    const keys = 512;
    for (let index = 0; index < keys; index++) {
      const putRevision = BigInt(index * 2 + 1);
      stream.emit(parameterChangeForKey(`key-${index}`, "put", putRevision));
      stream.emit(parameterChangeForKey(`key-${index}`, "delete", putRevision + 1n));
    }
    await waitFor(() => client.currentRevision === BigInt(keys * 2));
    expect(client.watchStatus.trackedParameterCount).toBe(0);

    stop();
    await client.close();
  });

  it("prunes delete-only tombstones without inventing watcher events", async () => {
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({ transport, namespace, logger: { warn: vi.fn() } });
    let delivered = 0;
    const stop = await client.watch(() => {
      delivered += 1;
    });
    await waitFor(() => transport.streams.length === 1);
    const stream = streamAt(transport, 0);
    await waitFor(() => stream.sent.length === 1);

    const keys = 512;
    for (let index = 1; index <= keys; index++) {
      stream.emit(parameterChangeForKey(`deleted-${index}`, "delete", BigInt(index)));
    }
    await waitFor(() => client.currentRevision === BigInt(keys));
    expect(client.watchStatus.trackedParameterCount).toBe(0);
    expect(delivered).toBe(0);

    stop();
    await client.close();
  });

  it("keeps sustained snapshot reads coherent while generations swap and reads each source once", async () => {
    interface Policy {
      readonly generation: number;
      readonly generationLabel: string;
    }

    let active = generation(0);
    let sourceReads = 0;
    const source = {
      current(): PolicySnapshot<Policy> {
        sourceReads += 1;
        return active;
      },
    };
    const publisher = createPolicyPublisher({
      source,
      projection: definePublicProjection<Policy>()({
        generation: (policy) => policy.generation,
        generationLabel: (policy) => policy.generationLabel,
      }),
      validate: () => ({ valid: true as const }),
    });

    let operations = 0;
    const reader = async (): Promise<void> => {
      for (let index = 0; index < 400; index++) {
        const readsBefore = sourceReads;
        const snapshot = publisher.read();
        expect(sourceReads - readsBefore).toBe(1);
        if (!snapshot) throw new Error("stress source unexpectedly unavailable");
        expect(snapshot.config.generationLabel).toBe(`generation-${snapshot.revision}`);
        expect(BigInt(snapshot.config.generation)).toBe(snapshot.revision);
        operations += 1;
        if (index % 16 === 0) await Promise.resolve();
      }
    };
    const writer = async (): Promise<void> => {
      for (let index = 1; index <= 256; index++) {
        active = generation(index);
        if (index % 4 === 0) await Promise.resolve();
      }
    };

    await Promise.all([writer(), ...Array.from({ length: 8 }, () => reader())]);
    expect(operations).toBe(3_200);
    expect(sourceReads).toBe(operations);
    expect(active.revision).toBe(256n);
  });
});

function streamAt(
  transport: FakeTransport,
  index: number,
): FakeDuplex<SubscribeRequest, SubscribeEvent> {
  const stream = transport.streams[index];
  if (!stream) throw new Error(`missing fake stream ${index}`);
  return stream as FakeDuplex<SubscribeRequest, SubscribeEvent>;
}

function parameterChange(revision: number): SubscribeEvent {
  return {
    event: {
      $case: "change",
      value: {
        ref: { namespace: { env: "prod", app: "api" }, key: "hot" },
        changeType: "put",
        value: `burst-value-${revision}`,
        contentType: "string",
        version: BigInt(revision),
        label: "",
      },
    },
    revision: BigInt(revision),
  };
}

function parameterChangeForKey(
  key: string,
  changeType: "put" | "delete",
  revision: bigint,
): SubscribeEvent {
  return {
    event: {
      $case: "change",
      value: {
        ref: { namespace: { env: "prod", app: "api" }, key },
        changeType,
        value: changeType === "put" ? `value-${revision}` : "",
        contentType: "string",
        version: revision,
        label: "",
      },
    },
    revision,
  };
}

function abortListenerCalls(calls: readonly (readonly unknown[])[]): number {
  return calls.filter(([type]) => type === "abort").length;
}

function generation(index: number): PolicySnapshot<{
  readonly generation: number;
  readonly generationLabel: string;
}> {
  return Object.freeze({
    revision: BigInt(index),
    value: Object.freeze({ generation: index, generationLabel: `generation-${index}` }),
  });
}
