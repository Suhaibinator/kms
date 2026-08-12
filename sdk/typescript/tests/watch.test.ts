import { describe, expect, it, vi } from "vitest";
import { KmsClient, type WatchEvent } from "../src/client.js";
import type { SubscribeEvent, SubscribeRequest } from "../src/generated/kms.js";
import { resolveRef } from "../src/refs.js";
import { ParameterValue } from "../src/values.js";
import { fullJitterBackoff, revisionAllowsWrite } from "../src/watch.js";
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
