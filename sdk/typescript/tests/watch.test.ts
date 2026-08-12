import { describe, expect, it } from "vitest";
import { KmsClient, type WatchEvent } from "../src/client.js";
import type { SubscribeEvent, SubscribeRequest } from "../src/generated/kms.js";
import { resolveRef } from "../src/refs.js";
import { fullJitterBackoff, revisionAllowsWrite } from "../src/watch.js";
import { type FakeDuplex, FakeTransport, waitFor } from "./helpers/fake-transport.js";

describe("shared watches", () => {
  it("shares a namespace stream, acknowledges heartbeats, fences duplicates, and resumes", async () => {
    const transport = new FakeTransport(() => ({ parameters: [], nextPageToken: "" }));
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const events: WatchEvent[] = [];
    const second: WatchEvent[] = [];
    const stopA = await client.watch((event) => events.push(event));
    const stopB = await client.watch((event) => second.push(event));

    await waitFor(() => transport.streams.length === 1);
    const stream = transport.streams[0] as FakeDuplex<SubscribeRequest, SubscribeEvent>;
    await waitFor(() => stream.sent.length === 1);
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

    stopA();
    stopB();
    await client.close();
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
