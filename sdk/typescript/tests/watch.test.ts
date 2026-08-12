import { describe, expect, it, vi } from "vitest";
import { KmsClient, type WatchEvent } from "../src/client.js";
import type { SubscribeEvent, SubscribeRequest } from "../src/generated/kms.js";
import { parseNamespace, resolveRef } from "../src/refs.js";
import { ParameterValue } from "../src/values.js";
import { fullJitterBackoff, revisionAllowsWrite, SubscriptionManager } from "../src/watch.js";
import { type FakeDuplex, FakeTransport, waitFor } from "./helpers/fake-transport.js";

describe("shared watches", () => {
  it("shares a namespace stream, acknowledges heartbeats, fences duplicates, and resumes", async () => {
    const random = vi.spyOn(Math, "random").mockReturnValue(0);
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({ transport, namespace: "prod/api" });
    expect(client.watchStatus).toEqual({
      state: "idle",
      reconciliation: "not_started",
      currentRevision: 0n,
      reconnectCount: 0,
      namespaceCount: 0,
      trackedParameterCount: 0,
      watcherCount: 0,
      parameterHandlerCount: 0,
    });
    const events: WatchEvent[] = [];
    const second: WatchEvent[] = [];
    const stopA = await client.watch((event) => events.push(event));
    const stopB = await client.watch((event) => second.push(event));

    await waitFor(() => transport.streams.length === 1);
    const stream = transport.streams[0] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    await waitFor(() => stream.sent.length === 1);
    expect(client.watchStatus).toMatchObject({
      state: "connected",
      reconciliation: "not_started",
      namespaceCount: 1,
      watcherCount: 2,
      reconnectCount: 0,
      connectedAtUnixMs: expect.any(Number),
    });
    expect(Object.isFrozen(client.watchStatus)).toBe(true);
    expect(stream.sent[0]).toMatchObject({
      clientName: expect.any(String),
      namespaces: [{ env: "prod", app: "api" }],
      lastSeenRevision: 0n,
    });

    stream.emit({
      event: { $case: "snapshot", value: { parameters: [] } },
      revision: 0n,
    });

    stream.emit({
      event: {
        $case: "change",
        value: {
          ref: { namespace: { env: "prod", app: "api" }, key: "flag" },
          changeType: "put",
          value: "on",
          contentType: "string",
          version: 1n,
          label: "",
        },
      },
      revision: 4n,
    });
    await waitFor(() => events.length === 1 && second.length === 1);

    // Same revision is at-least-once delivery and must not re-apply.
    stream.emit({
      event: {
        $case: "change",
        value: {
          ref: { namespace: { env: "prod", app: "api" }, key: "flag" },
          changeType: "put",
          value: "off",
          contentType: "string",
          version: 2n,
          label: "",
        },
      },
      revision: 4n,
    });
    await new Promise((resolve) => setTimeout(resolve, 5));
    expect(events).toHaveLength(1);

    stream.emit({
      event: { $case: "heartbeat", value: { serverTimeUnixMs: 1n } },
      revision: 5n,
    });
    await waitFor(() => stream.sent.length === 2);
    expect(stream.sent[1]).toMatchObject({ ackedRevision: 5n });
    expect(client.currentRevision).toBe(5n);
    expect(client.watchStatus).toMatchObject({
      currentRevision: 5n,
      lastEventAtUnixMs: expect.any(Number),
    });

    stream.cancel();
    await waitFor(() => transport.streams.length === 2);
    const resumed = transport.streams[1] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    await waitFor(() => resumed.sent.length === 1);
    expect(resumed.sent[0]).toMatchObject({
      namespaces: [{ env: "prod", app: "api" }],
      lastSeenRevision: 5n,
    });
    expect(client.watchStatus).toMatchObject({
      state: "connected",
      reconnectCount: 1,
      disconnectedAtUnixMs: expect.any(Number),
    });

    stopA();
    stopB();
    await client.close();
    expect(client.watchStatus).toMatchObject({
      state: "stopped",
      watcherCount: 0,
      currentRevision: 5n,
    });
    random.mockRestore();
  });

  it("restarts only when the add-only namespace union grows", async () => {
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const stopA = await client.watch(() => undefined);
    await waitFor(() => transport.streams.length === 1);
    const stopSame = client.watchNamespace("prod/api", () => undefined);
    await new Promise((resolve) => setTimeout(resolve, 5));
    expect(transport.streams).toHaveLength(1);

    const stopOther = client.watchNamespace("prod/worker", () => undefined);
    await waitFor(() => transport.streams.length === 2);
    const second = transport.streams[1] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    await waitFor(() => second.sent.length === 1);
    expect(second.sent[0]?.namespaces).toEqual([
      { env: "prod", app: "api" },
      { env: "prod", app: "worker" },
    ]);

    stopA();
    stopSame();
    stopOther();
    await client.close();
  });

  it("requests one full snapshot for a larger namespace union without lowering its fence", async () => {
    const random = vi.spyOn(Math, "random").mockReturnValue(0);
    let secretReads = 0;
    const transport = new FakeTransport((path) => {
      if (path.endsWith("/GetSecret")) {
        secretReads += 1;
        return {
          ref: { namespace: { env: "prod", app: "worker" }, key: "secret" },
          version: BigInt(secretReads),
          value: Buffer.from(`secret-${secretReads}`),
          contentType: "text/plain",
          metadataJson: "{}",
          createdAtUnixMs: 0n,
        };
      }
      return { parameters: [], nextPageToken: "" };
    });
    const client = new KmsClient({ transport, namespace: "prod/api", cacheTtlMs: 60_000 });
    expect((await client.getSecret("/prod/worker/secret")).text()).toBe("secret-1");

    const apiEvents: WatchEvent[] = [];
    const workerEvents: WatchEvent[] = [];
    const stopApi = await client.watch((event) => apiEvents.push(event));
    await waitFor(() => transport.streams.length === 1);
    const first = transport.streams[0] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    await waitFor(() => first.sent.length === 1);
    first.emit({
      event: {
        $case: "change",
        value: {
          ref: { namespace: { env: "prod", app: "api" }, key: "flag" },
          changeType: "put",
          value: "newer",
          contentType: "string",
          version: 5n,
          label: "",
        },
      },
      revision: 5n,
    });
    await waitFor(() => apiEvents.length === 1);

    const stopWorker = client.watchNamespace("prod/worker", (event) => workerEvents.push(event));
    // Namespace growth invalidates secrets before the snapshot arrives.
    expect((await client.getSecret("/prod/worker/secret")).text()).toBe("secret-2");
    await waitFor(() => transport.streams.length === 2);
    const expanded = transport.streams[1] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    await waitFor(() => expanded.sent.length === 1);
    expect(expanded.sent[0]).toMatchObject({
      namespaces: [
        { env: "prod", app: "api" },
        { env: "prod", app: "worker" },
      ],
      lastSeenRevision: 0n,
    });

    // Merely sending registration is not enough: until the requested snapshot
    // is delivered, reconnects must continue requesting the full union.
    expanded.cancel();
    await waitFor(() => transport.streams.length === 3);
    const retriedSnapshot = transport.streams[2] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    await waitFor(() => retriedSnapshot.sent.length === 1);
    expect(retriedSnapshot.sent[0]).toMatchObject({ lastSeenRevision: 0n });

    retriedSnapshot.emit({
      event: {
        $case: "snapshot",
        value: {
          parameters: [
            {
              ref: { namespace: { env: "prod", app: "api" }, key: "flag" },
              value: "stale",
              contentType: "string",
              version: 3n,
              metadataJson: "{}",
              createdBy: "test",
              createdAtUnixMs: 0n,
              labels: {},
            },
            {
              ref: { namespace: { env: "prod", app: "worker" }, key: "flag" },
              value: "worker",
              contentType: "string",
              version: 3n,
              metadataJson: "{}",
              createdBy: "test",
              createdAtUnixMs: 0n,
              labels: {},
            },
          ],
        },
      },
      revision: 3n,
    });
    await waitFor(() => workerEvents.length === 1);
    expect(apiEvents).toHaveLength(1);
    expect(apiEvents[0]).toMatchObject({ value: "newer", revision: 5n });
    expect(workerEvents[0]).toMatchObject({ value: "worker", revision: 3n });
    expect(client.currentRevision).toBe(5n);

    retriedSnapshot.cancel();
    await waitFor(() => transport.streams.length === 4);
    const resumed = transport.streams[3] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    await waitFor(() => resumed.sent.length === 1);
    expect(resumed.sent[0]).toMatchObject({ lastSeenRevision: 5n });

    stopApi();
    stopWorker();
    await client.close();
    random.mockRestore();
  });

  it("seeds a late ParameterValue from newer shared-watch state", async () => {
    const transport = new FakeTransport((path) => {
      if (path.endsWith("/GetParameter")) {
        return {
          parameter: {
            ref: { namespace: { env: "prod", app: "api" }, key: "flag" },
            value: "stale-fetch",
            contentType: "string",
            version: 1n,
            metadataJson: "{}",
            createdBy: "test",
            createdAtUnixMs: 0n,
            labels: {},
          },
        };
      }
      return { parameters: [], nextPageToken: "" };
    });
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const events: WatchEvent[] = [];
    const stop = await client.watch((event) => events.push(event));
    await waitFor(() => transport.streams.length === 1);
    const stream = transport.streams[0] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    stream.emit({
      event: {
        $case: "change",
        value: {
          ref: { namespace: { env: "prod", app: "api" }, key: "flag" },
          changeType: "put",
          value: "live-watch",
          contentType: "string",
          version: 9n,
          label: "",
        },
      },
      revision: 9n,
    });
    await waitFor(() => events.length === 1);

    const value = new ParameterValue("flag");
    await value.init(client);
    expect(value.get()).toBe("live-watch");
    expect(client.currentRevision).toBe(9n);

    await value.dispose();
    stop();
    await client.close();
  });

  it("does not restart the first stream when a live ParameterValue registers", async () => {
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({ transport, namespace: "prod/api" });

    client._registerParameter(resolveRef("flag", "prod/api"), "off", () => undefined);
    await waitFor(() => transport.streams.length === 1);
    await new Promise((resolve) => setTimeout(resolve, 5));

    expect(transport.streams).toHaveLength(1);
    await client.close();
  });

  it("stops real-client ParameterValue updates after disposal", async () => {
    const transport = new FakeTransport((path) => {
      if (path.endsWith("/GetParameter")) {
        return {
          parameter: {
            ref: { namespace: { env: "prod", app: "api" }, key: "flag" },
            value: "off",
            contentType: "string",
            version: 1n,
            metadataJson: "{}",
            createdBy: "test",
            createdAtUnixMs: 0n,
            labels: {},
          },
        };
      }
      return { parameters: [], nextPageToken: "" };
    });
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const value = new ParameterValue("flag");
    const callback = vi.fn();
    value.onChange(callback);
    await value.init(client);
    await waitFor(() => transport.streams.length === 1);
    const stream = transport.streams[0] as FakeDuplex<SubscribeRequest, SubscribeEvent>;

    stream.emit({
      event: {
        $case: "change",
        value: {
          ref: { namespace: { env: "prod", app: "api" }, key: "flag" },
          changeType: "put",
          value: "on",
          contentType: "string",
          version: 2n,
          label: "",
        },
      },
      revision: 1n,
    });
    await waitFor(() => value.get() === "on");
    await value.dispose();
    await value.dispose();

    stream.emit({
      event: {
        $case: "change",
        value: {
          ref: { namespace: { env: "prod", app: "api" }, key: "flag" },
          changeType: "put",
          value: "after-dispose",
          contentType: "string",
          version: 3n,
          label: "",
        },
      },
      revision: 2n,
    });
    await new Promise((resolve) => setTimeout(resolve, 5));

    expect(value.get()).toBe("on");
    expect(callback).toHaveBeenCalledTimes(1);
    await client.close();
  });

  it("fences a watch callback already queued when its watcher stops", async () => {
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const callback = vi.fn();
    const stop = await client.watch(callback);
    await waitFor(() => transport.streams.length === 1);
    const stream = transport.streams[0] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    stream.emit({
      event: {
        $case: "change",
        value: {
          ref: { namespace: { env: "prod", app: "api" }, key: "queued" },
          changeType: "put",
          value: "new",
          contentType: "string",
          version: 1n,
          label: "",
        },
      },
      revision: 1n,
    });
    for (let index = 0; index < 10 && client.currentRevision !== 1n; index++) {
      await Promise.resolve();
    }
    expect(client.currentRevision).toBe(1n);
    stop();
    await new Promise<void>((resolve) => setImmediate(resolve));
    expect(callback).not.toHaveBeenCalled();
    await client.close();
  });

  it("interrupts reconnect backoff when the namespace union grows", async () => {
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const sleeps: number[] = [];
    const manager = new SubscriptionManager(client, {
      random: () => 1,
      reconcileIntervalMs: 60_000,
      sleep: (milliseconds, signal) => {
        sleeps.push(milliseconds);
        return new Promise<void>((_resolve, reject) => {
          if (signal.aborted) reject(signal.reason);
          else signal.addEventListener("abort", () => reject(signal.reason), { once: true });
        });
      },
    });
    const stopFirst = manager.watch(parseNamespace("prod/api"), () => undefined);
    await waitFor(() => transport.streams.length === 1);
    const first = transport.streams[0] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    first.cancel();
    await waitFor(() => sleeps.includes(1_000));

    const stopSecond = manager.watch(parseNamespace("prod/worker"), () => undefined);
    await waitFor(() => transport.streams.length === 2, 100);
    const expanded = transport.streams[1] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    await waitFor(() => expanded.sent.length === 1);
    expect(expanded.sent[0]).toMatchObject({
      namespaces: [
        { env: "prod", app: "api" },
        { env: "prod", app: "worker" },
      ],
      lastSeenRevision: 0n,
    });

    stopFirst();
    stopSecond();
    await manager.stop();
    await client.close();
  });

  it("discards an in-flight reconciliation page after its last scope owner leaves", async () => {
    let finishList!: (value: {
      items: readonly {
        env: string;
        app: string;
        key: string;
        value: string;
        contentType: string;
        version: bigint;
        metadataJson: string;
        createdBy: string;
        createdAtUnixMs: bigint;
        labels: Readonly<Record<string, bigint>>;
        namespace: string;
        path: string;
      }[];
      nextPageToken: string;
    }) => void;
    const listPending = new Promise<Parameters<typeof finishList>[0]>((resolve) => {
      finishList = resolve;
    });
    const transport = new FakeTransport(() => listPending);
    const client = new KmsClient({ transport, logger: { warn: vi.fn() } });
    const sleeps: Array<() => void> = [];
    const manager = new SubscriptionManager(client, {
      reconcileIntervalMs: 60_000,
      sleep: (_milliseconds, signal) =>
        new Promise<void>((resolve, reject) => {
          if (signal.aborted) reject(signal.reason);
          else {
            sleeps.push(resolve);
            signal.addEventListener("abort", () => reject(signal.reason), { once: true });
          }
        }),
    });
    const callback = vi.fn();
    const stop = manager.watch(parseNamespace("prod/api"), callback);
    await waitFor(() => sleeps.length >= 1);
    sleeps.shift()?.();
    await waitFor(() => transport.calls.some((call) => call.path.endsWith("/ListParameters")));

    stop();
    finishList({
      items: [
        Object.freeze({
          env: "prod",
          app: "api",
          key: "late",
          value: "stale",
          contentType: "string",
          version: 1n,
          metadataJson: "{}",
          createdBy: "test",
          createdAtUnixMs: 0n,
          labels: Object.freeze({}),
          namespace: "prod/api",
          path: "/prod/api/late",
        }),
      ],
      nextPageToken: "",
    });
    await new Promise<void>((resolve) => setImmediate(resolve));

    expect(manager.status).toMatchObject({
      state: "idle",
      reconciliation: "not_started",
      namespaceCount: 0,
      trackedParameterCount: 0,
      watcherCount: 0,
    });
    expect(callback).not.toHaveBeenCalled();
    await manager.stop();
    await client.close();
  });

  it("removes external abort listeners on unwatch and client close", async () => {
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const unwatchController = new AbortController();
    const closeController = new AbortController();
    const unwatchRemove = vi.spyOn(unwatchController.signal, "removeEventListener");
    const closeRemove = vi.spyOn(closeController.signal, "removeEventListener");

    const unwatch = client.watchNamespace("prod/api", () => undefined, {
      signal: unwatchController.signal,
    });
    client.watchNamespace("prod/api", () => undefined, { signal: closeController.signal });
    await waitFor(() => transport.streams.length === 1);

    unwatch();
    unwatch();
    expect(unwatchRemove).toHaveBeenCalledWith("abort", expect.any(Function));
    await client.close();
    expect(closeRemove).toHaveBeenCalledWith("abort", expect.any(Function));
  });

  it("does not create background work for an already-aborted watcher", async () => {
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const controller = new AbortController();
    controller.abort();

    const stop = client.watchNamespace("prod/api", () => undefined, {
      signal: controller.signal,
    });
    await Promise.resolve();
    expect(transport.streams).toHaveLength(0);

    stop();
    await client.close();
  });

  it("reconciles missed deletions for namespace watchers without a ParameterValue", async () => {
    let parameters = [
      {
        ref: { namespace: { env: "prod", app: "api" }, key: "flag" },
        value: "on",
        contentType: "string",
        version: 1n,
        metadataJson: "{}",
        createdBy: "test",
        createdAtUnixMs: 0n,
        labels: {},
      },
    ];
    const transport = new FakeTransport(() => ({ parameters, nextPageToken: "" }));
    const client = new KmsClient({
      transport,
      namespace: "prod/api",
      reconcileIntervalMs: 5,
    });
    const events: WatchEvent[] = [];
    const stop = await client.watch((event) => events.push(event));

    await waitFor(() => events.some((event) => event.type === "put"));
    parameters = [];
    await waitFor(() => events.some((event) => event.type === "delete"));

    stop();
    await client.close();
  });

  it("does not infer deletions from an incomplete paginated reconciliation", async () => {
    const parameter = (key: string) => ({
      ref: { namespace: { env: "prod", app: "api" }, key },
      value: key,
      contentType: "string",
      version: 1n,
      metadataJson: "{}",
      createdBy: "test",
      createdAtUnixMs: 0n,
      labels: {},
    });
    let phase: "initial" | "incomplete" | "deleted" = "initial";
    const transport = new FakeTransport((_path, request) => {
      const pageToken = (request as { pageToken?: string }).pageToken ?? "";
      if (phase === "incomplete" && pageToken === "second") {
        throw new Error("page failed");
      }
      if (pageToken === "second") {
        return {
          parameters: phase === "initial" ? [parameter("second")] : [],
          nextPageToken: "",
        };
      }
      return {
        parameters: [parameter("first")],
        nextPageToken: phase === "deleted" ? "" : "second",
      };
    });
    const client = new KmsClient({
      transport,
      namespace: "prod/api",
      reconcileIntervalMs: 5,
    });
    const events: WatchEvent[] = [];
    const stop = await client.watch((event) => events.push(event));

    await waitFor(
      () =>
        events.some((event) => event.type === "put" && event.key === "first") &&
        events.some((event) => event.type === "put" && event.key === "second"),
    );
    phase = "incomplete";
    await waitFor(() => client.watchStatus.reconciliation === "degraded");
    expect(client.watchStatus).toMatchObject({
      lastReconcileAttemptAtUnixMs: expect.any(Number),
      lastReconcileFailureAtUnixMs: expect.any(Number),
    });
    expect(events).not.toContainEqual(expect.objectContaining({ type: "delete", key: "second" }));

    phase = "deleted";
    await waitFor(() => events.some((event) => event.type === "delete" && event.key === "second"));
    await waitFor(() => client.watchStatus.reconciliation === "healthy");
    expect(client.watchStatus.lastReconcileSuccessAtUnixMs).toEqual(expect.any(Number));

    stop();
    await client.close();
  });

  it("bounds pagination without inferring a deletion from a capped listing", async () => {
    let listCalls = 0;
    const transport = new FakeTransport((_path, request) => {
      listCalls += 1;
      const pageToken = (request as { pageToken?: string }).pageToken ?? "";
      const page = pageToken === "" ? 0 : Number(pageToken);
      return { parameters: [], nextPageToken: String(page + 1) };
    });
    const client = new KmsClient({
      transport,
      namespace: "prod/api",
      reconcileIntervalMs: 5,
    });
    const events: WatchEvent[] = [];
    const stop = await client.watch((event) => events.push(event));
    await waitFor(() => transport.streams.length === 1);
    const stream = transport.streams[0] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    stream.emit({
      event: {
        $case: "change",
        value: {
          ref: { namespace: { env: "prod", app: "api" }, key: "retained" },
          changeType: "put",
          value: "present",
          contentType: "string",
          version: 1n,
          label: "",
        },
      },
      revision: 1n,
    });
    await waitFor(() => events.some((event) => event.type === "put"));

    await waitFor(() => listCalls >= 100);
    stop();
    await client.close();

    expect(listCalls).toBe(100);
    expect(events).not.toContainEqual(expect.objectContaining({ type: "delete", key: "retained" }));
  });

  it("fences a stale reconciliation page behind a live tombstone", async () => {
    let resolveFirstList:
      | ((response: { parameters: ReturnType<typeof parameter>[]; nextPageToken: string }) => void)
      | undefined;
    const parameter = (value: string) => ({
      ref: { namespace: { env: "prod", app: "api" }, key: "flag" },
      value,
      contentType: "string",
      version: 1n,
      metadataJson: "{}",
      createdBy: "test",
      createdAtUnixMs: 0n,
      labels: {},
    });
    let firstList = true;
    const transport = new FakeTransport(() => {
      if (!firstList) return { parameters: [], nextPageToken: "" };
      firstList = false;
      return new Promise<{ parameters: ReturnType<typeof parameter>[]; nextPageToken: string }>(
        (resolve) => {
          resolveFirstList = resolve;
        },
      );
    });
    const client = new KmsClient({
      transport,
      namespace: "prod/api",
      reconcileIntervalMs: 5,
    });
    const events: WatchEvent[] = [];
    const stop = await client.watch((event) => events.push(event));
    await waitFor(() => transport.streams.length === 1);
    const stream = transport.streams[0] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    stream.emit({
      event: {
        $case: "change",
        value: {
          ref: { namespace: { env: "prod", app: "api" }, key: "flag" },
          changeType: "put",
          value: "current",
          contentType: "string",
          version: 1n,
          label: "",
        },
      },
      revision: 5n,
    });
    await waitFor(() => events.some((event) => event.type === "put"));
    await waitFor(() => resolveFirstList !== undefined);

    stream.emit({
      event: {
        $case: "change",
        value: {
          ref: { namespace: { env: "prod", app: "api" }, key: "flag" },
          changeType: "delete",
          value: "",
          contentType: "",
          version: 2n,
          label: "",
        },
      },
      revision: 6n,
    });
    await waitFor(() => events.some((event) => event.type === "delete"));
    resolveFirstList?.({ parameters: [parameter("stale")], nextPageToken: "" });
    await new Promise((resolve) => setTimeout(resolve, 20));

    expect(events.filter((event) => event.type === "put")).toHaveLength(1);
    expect(events.at(-1)).toMatchObject({ type: "delete", revision: 6n });
    stop();
    await client.close();
  });

  it("invalidates cached secrets on metadata changes without streaming plaintext", async () => {
    let secretReads = 0;
    const transport = new FakeTransport((path) => {
      if (path.endsWith("/GetSecret")) {
        secretReads++;
        return {
          ref: { namespace: { env: "prod", app: "api" }, key: "secret" },
          version: BigInt(secretReads),
          value: Buffer.from(`value-${secretReads}`),
          contentType: "text/plain",
          metadataJson: "{}",
          createdAtUnixMs: 0n,
        };
      }
      return { parameters: [], nextPageToken: "" };
    });
    const client = new KmsClient({ transport, namespace: "prod/api", cacheTtlMs: 60_000 });
    expect((await client.getSecret("secret")).text()).toBe("value-1");
    const events: WatchEvent[] = [];
    await client.watch((event) => events.push(event));
    await waitFor(() => transport.streams.length === 1);
    const stream = transport.streams[0] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    stream.emit({
      event: {
        $case: "secretChange",
        value: {
          ref: { namespace: { env: "prod", app: "api" }, key: "secret" },
          changeType: "promote",
          version: 2n,
          label: "current",
        },
      },
      revision: 9n,
    });
    await waitFor(() => events.length === 1);
    expect(events[0]).not.toHaveProperty("value");
    expect((await client.getSecret("secret")).text()).toBe("value-2");
    await client.close();
  });

  it("invalidates an ordinary cached parameter omitted by a full snapshot", async () => {
    let reads = 0;
    const transport = new FakeTransport((path) => {
      if (path.endsWith("/GetParameter")) {
        reads++;
        return {
          parameter: {
            ref: { namespace: { env: "prod", app: "api" }, key: "deleted" },
            value: reads === 1 ? "cached-before-delete" : "current-after-snapshot",
            contentType: "string",
            version: BigInt(reads),
            metadataJson: "{}",
            createdBy: "test",
            createdAtUnixMs: 0n,
            labels: {},
          },
        };
      }
      return { parameters: [], nextPageToken: "" };
    });
    const client = new KmsClient({ transport, namespace: "prod/api", cacheTtlMs: 60_000 });
    expect(await client.getParameter("deleted")).toBe("cached-before-delete");
    const stop = await client.watch(() => undefined);
    await waitFor(() => transport.streams.length === 1);
    const stream = transport.streams[0] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    stream.emit({
      event: { $case: "snapshot", value: { parameters: [] } },
      revision: 3n,
    });
    await waitFor(() => client.currentRevision === 3n);

    expect(await client.getParameter("deleted")).toBe("current-after-snapshot");
    expect(reads).toBe(2);
    stop();
    await client.close();
  });

  it("invalidates an unknown live tombstone without inventing a value-change event", async () => {
    let reads = 0;
    const transport = new FakeTransport((path) => {
      if (path.endsWith("/GetParameter")) {
        reads++;
        return {
          parameter: {
            ref: { namespace: { env: "prod", app: "api" }, key: "deleted" },
            value: reads === 1 ? "cached-before-delete" : "after-delete",
            contentType: "string",
            version: BigInt(reads),
            metadataJson: "{}",
            createdBy: "test",
            createdAtUnixMs: 0n,
            labels: {},
          },
        };
      }
      return { parameters: [], nextPageToken: "" };
    });
    const client = new KmsClient({ transport, namespace: "prod/api", cacheTtlMs: 60_000 });
    expect(await client.getParameter("deleted")).toBe("cached-before-delete");
    const events: WatchEvent[] = [];
    const stop = await client.watch((event) => events.push(event));
    await waitFor(() => transport.streams.length === 1);
    const stream = transport.streams[0] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    stream.emit({
      event: {
        $case: "change",
        value: {
          ref: { namespace: { env: "prod", app: "api" }, key: "deleted" },
          changeType: "delete",
          value: "",
          contentType: "",
          version: 9n,
          label: "",
        },
      },
      revision: 4n,
    });
    await waitFor(() => client.currentRevision === 4n);
    stream.emit({
      event: {
        $case: "change",
        value: {
          ref: { namespace: { env: "prod", app: "api" }, key: "deleted" },
          changeType: "delete",
          value: "",
          contentType: "",
          version: 10n,
          label: "",
        },
      },
      revision: 5n,
    });
    await waitFor(() => client.currentRevision === 5n);
    expect(events).toEqual([]);
    expect(await client.getParameter("deleted")).toBe("after-delete");
    expect(reads).toBe(2);
    stop();
    await client.close();
  });
});

describe("watch reliability helpers", () => {
  it("fences reconciliation behind newer live revisions", () => {
    expect(revisionAllowsWrite(8n, 7n, true)).toBe(false);
    expect(revisionAllowsWrite(7n, 7n, true)).toBe(true);
    expect(revisionAllowsWrite(7n, 7n, false)).toBe(false);
    expect(revisionAllowsWrite(7n, 0n, false)).toBe(true);
  });

  it("uses bounded full-jitter reconnect delays", () => {
    expect(fullJitterBackoff(0, () => 0)).toBe(10);
    expect(fullJitterBackoff(20, () => 0.999)).toBeLessThanOrEqual(60_000);
  });
});
