import { describe, expect, it } from "vitest";

import { KmsClient } from "../src/client.js";
import { KmsError } from "../src/errors.js";
import {
  ConfigurationRelease,
  type ConfigurationReleaseEntry,
  type ResourceRef,
  type WatchReleaseEvent,
  type WatchReleaseRequest,
} from "../src/generated/kms.js";
import { deterministicReleaseDigest, sha256Hex } from "../src/releases/digest.js";
import type { BidiMethod, DuplexRpc, TransportCallOptions } from "../src/transport.js";
import { type FakeDuplex, FakeTransport, waitFor } from "./helpers/fake-transport.js";

const namespace = { env: "prod", app: "api" } as const;
const expectedRef: ResourceRef = { namespace, key: "settings" };
const wrongRef: ResourceRef = { namespace, key: "other" };

describe("KmsClient release transport boundary", () => {
  it("requires one schema selector and resolves a digest exactly once", async () => {
    const digest = "a".repeat(64);
    const transport = new FakeTransport((path, request) => {
      expect(path).toBe("/kms.v1.ConfigurationReleaseService/ResolveReleaseSchema");
      expect(request).toEqual({ namespace, name: "runtime", schemaSha256: digest });
      return { schemaVersion: 7n };
    });
    const client = new KmsClient({ transport, namespace: "prod/api" });

    await expect(client.createReleaseLoader({ name: "runtime" })).rejects.toThrow(/exactly one/u);
    await expect(
      client.createReleaseLoader({ name: "runtime", schemaVersion: 0n, schemaSHA256: digest }),
    ).rejects.toThrow(/exactly one/u);
    await client.createReleaseLoader({ name: "runtime", schemaSHA256: digest });
    expect(transport.calls).toHaveLength(1);
    await client.close();
  });

  it("rejects schema version zero returned for a generated digest", async () => {
    const transport = new FakeTransport(() => ({ schemaVersion: 0n }));
    const client = new KmsClient({ transport, namespace: "prod/api" });
    await expect(
      client.createReleaseLoader({ name: "runtime", schemaSHA256: "a".repeat(64) }),
    ).rejects.toThrow(/positive/u);
    await client.close();
  });

  it("rejects a returned parameter ref mismatch without polluting the read cache", async () => {
    const expectedValue = "expected-value";
    const release = makeRelease({
      alias: "settings",
      kind: "parameter",
      ref: expectedRef,
      version: 7n,
      contentType: "text/plain",
      metadataJson: "",
      parameterDigest: sha256Hex(expectedValue),
    });
    let parameterReads = 0;
    const transport = new FakeTransport((path, request) => {
      if (path.endsWith("/GetActiveRelease")) {
        expect(request).toMatchObject({ schemaVersion: 0n });
        return { release, activationRevision: 11n, previousVersion: 0n };
      }
      if (path.endsWith("/GetParameter")) {
        parameterReads += 1;
        return {
          parameter: {
            ref: parameterReads === 1 ? wrongRef : expectedRef,
            value: parameterReads === 1 ? expectedValue : "fresh-value",
            contentType: "text/plain",
            version: 7n,
            metadataJson: "",
            createdBy: "test",
            createdAtUnixMs: 1n,
            labels: {},
          },
        };
      }
      if (path.endsWith("/RegisterReleaseSession"))
        throw new KmsError("unimplemented", "legacy server");
      throw new Error(`unexpected ${path}`);
    });
    const client = new KmsClient({ transport, namespace: "prod/api", cacheTtlMs: 60_000 });
    const loader = await client.createReleaseLoader({ name: "runtime", schemaVersion: 0n });

    const error = await loader
      .run(() => {
        throw new Error("prepare must not run for a returned-ref mismatch");
      })
      .catch((reason: unknown) => reason);

    expect(error).toMatchObject({ category: "version_mismatch" });
    expect(String(error)).not.toContain(expectedValue);
    expect(rejectedAcknowledgement(transport)).toMatchObject({
      state: "rejected",
      rejectionCategory: "version_mismatch",
      diagnostic: "",
      schemaVersion: 0n,
    });
    await expect(client.getParameter("settings", { version: 7n })).resolves.toBe("fresh-value");
    expect(parameterReads).toBe(2);
    await client.close();
  });

  it.each([
    ["different", wrongRef],
    ["missing", undefined],
  ] as const)("rejects a %s returned secret ref", async (_label, returnedRef) => {
    const plaintext = "highly-sensitive";
    const release = makeRelease({
      alias: "settings",
      kind: "secret",
      ref: expectedRef,
      version: 9n,
      contentType: "text/plain",
      metadataJson: "",
      parameterDigest: "",
    });
    const transport = new FakeTransport((path, _request, options) => {
      if (path.endsWith("/GetActiveRelease")) {
        return { release, activationRevision: 12n, previousVersion: 0n };
      }
      if (path.endsWith("/GetSecret")) {
        expect(options.metadata?.["x-kms-secret-token"]).toBeUndefined();
        return {
          ref: returnedRef,
          version: 9n,
          value: Buffer.from(plaintext),
          contentType: "text/plain",
          metadataJson: "",
          createdAtUnixMs: 1n,
        };
      }
      if (path.endsWith("/GetSecretMetadata")) {
        return {
          secret: {
            ref: expectedRef,
            contentType: "text/plain",
            bound: false,

            metadataJson: "",
            createdAtUnixMs: 1n,
            updatedAtUnixMs: 1n,
            labels: { current: 9n },
            versions: [
              {
                version: 9n,
                state: "enabled",
                createdBy: "test",
                createdAtUnixMs: 1n,
                destroyedAtUnixMs: 0n,
                expiresAtUnixMs: 0n,
                metadataJson: "",
                bound: false,
              },
            ],
          },
        };
      }
      if (path.endsWith("/RegisterReleaseSession"))
        throw new KmsError("unimplemented", "legacy server");
      throw new Error(`unexpected ${path}`);
    });
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const loader = await client.createReleaseLoader({
      name: "runtime",
      schemaVersion: 0n,
    });

    const error = await loader
      .run(() => {
        throw new Error("prepare must not run for a returned-ref mismatch");
      })
      .catch((reason: unknown) => reason);

    expect(error).toMatchObject({ category: "version_mismatch" });
    expect(String(error)).not.toContain(plaintext);
    const acknowledgement = rejectedAcknowledgement(transport);
    expect(acknowledgement?.diagnostic).not.toContain(plaintext);
    expect(acknowledgement).toMatchObject({
      state: "rejected",
      rejectionCategory: "version_mismatch",
      diagnostic: "",
    });
    await client.close();
  });

  it("cancels a release stream whose initial registration send fails", async () => {
    const value = "expected-value";
    const release = makeRelease({
      alias: "settings",
      kind: "parameter",
      ref: expectedRef,
      version: 7n,
      contentType: "text/plain",
      metadataJson: "",
      parameterDigest: sha256Hex(value),
    });
    const transport = new RejectingRegistrationTransport((path) => {
      if (path.endsWith("/GetActiveRelease")) {
        return { release, activationRevision: 11n, previousVersion: 0n };
      }
      if (path.endsWith("/GetParameter")) {
        return {
          parameter: {
            ref: expectedRef,
            value,
            contentType: "text/plain",
            version: 7n,
            metadataJson: "",
            createdBy: "test",
            createdAtUnixMs: 1n,
            labels: {},
          },
        };
      }
      if (path.endsWith("/RegisterReleaseSession"))
        throw new KmsError("unimplemented", "legacy server");
      throw new Error(`unexpected ${path}`);
    });
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const loader = await client.createReleaseLoader({ name: "runtime", schemaVersion: 0n });

    const run = loader.run(() => ({ commit() {}, abort() {} }));
    await waitFor(() => transport.cancelCount === 1);
    loader.stop();

    await expect(run).rejects.toMatchObject({ name: "AbortError" });
    expect(transport.streamCount).toBe(1);
    expect(transport.cancelCount).toBe(1);
    await client.close();
  });

  it("keeps an inactive public loader on its exact track across malformed replay", async () => {
    const value = "expected-value";
    const release = makeRelease({
      alias: "settings",
      kind: "parameter",
      ref: expectedRef,
      version: 7n,
      contentType: "text/plain",
      metadataJson: "",
      parameterDigest: sha256Hex(value),
    });
    let active = false;
    const transport = new FakeTransport((path) => {
      if (path.endsWith("/GetActiveRelease")) {
        if (!active) throw new KmsError("not_found", "no active release");
        return { release, activationRevision: 2n, previousVersion: 0n };
      }
      if (path.endsWith("/GetParameter")) {
        return {
          parameter: {
            ref: expectedRef,
            value,
            contentType: "text/plain",
            version: 7n,
            metadataJson: "",
            createdBy: "test",
            createdAtUnixMs: 1n,
            labels: {},
          },
        };
      }
      if (path.endsWith("/RegisterReleaseSession"))
        throw new KmsError("unimplemented", "legacy server");
      throw new Error(`unexpected ${path}`);
    });
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const loader = await client.createReleaseLoader({ name: "runtime", schemaVersion: 0n });
    const controller = new AbortController();
    const committed = deferred<void>();
    const run = loader.run(
      () => ({
        commit: () => {
          committed.resolve();
          return undefined;
        },
        abort() {},
      }),
      controller.signal,
    );

    await waitFor(() => transport.streams.length === 1);
    const first = transport.streams[0] as FakeDuplex<WatchReleaseRequest, WatchReleaseEvent>;
    const foreign = ConfigurationRelease.create({ ...release, schemaVersion: 9n });
    foreign.digest = deterministicReleaseDigest(foreign);
    first.emit({ event: { $case: "activation", value: { release: foreign } }, revision: 99n });
    first.emit({ event: { $case: "snapshot", value: { release: undefined } }, revision: 100n });
    first.emit({ event: undefined, revision: 101n });
    first.emit({
      event: { $case: "futureEvent", value: {} },
      revision: 102n,
    } as unknown as WatchReleaseEvent);
    first.cancel();

    await waitFor(() => transport.streams.length === 2);
    const second = transport.streams[1] as FakeDuplex<WatchReleaseRequest, WatchReleaseEvent>;
    expect(releaseRegistration(second)?.lastSeenRevision).toBe(0n);

    active = true;
    second.emit({ event: { $case: "activation", value: { release } }, revision: 2n });
    await committed.promise;
    await waitFor(() => loader.status().appliedVersion === release.version);

    controller.abort();
    await expect(run).rejects.toMatchObject({ name: "AbortError" });
    await client.close();
  });
});

function makeRelease(entry: ConfigurationReleaseEntry): ConfigurationRelease {
  const release = ConfigurationRelease.create({
    namespace,
    name: "runtime",
    version: 3n,
    schemaVersion: 0n,
    entries: [entry],
    metadataJson: "{}",
  });
  release.digest = deterministicReleaseDigest(release);
  return release;
}

function rejectedAcknowledgement(transport: FakeTransport) {
  const stream = transport.streams[0] as FakeDuplex<WatchReleaseRequest, unknown> | undefined;
  return stream?.sent
    .flatMap((request) =>
      request.request?.$case === "acknowledgement" ? [request.request.value] : [],
    )
    .find((acknowledgement) => acknowledgement.state === "rejected");
}

function releaseRegistration<Response>(stream: FakeDuplex<WatchReleaseRequest, Response>) {
  return stream.sent.flatMap((request) =>
    request.request?.$case === "register" ? [request.request.value] : [],
  )[0];
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

class RejectingRegistrationTransport extends FakeTransport {
  streamCount = 0;
  cancelCount = 0;

  override bidi<Request, Response>(
    _method: BidiMethod<Request, Response>,
    _options: TransportCallOptions = {},
  ): DuplexRpc<Request, Response> {
    this.streamCount++;
    let closed = false;
    return {
      send: async () => {
        throw new Error("registration failed");
      },
      closeSend: () => {
        closed = true;
      },
      cancel: () => {
        if (closed) return;
        closed = true;
        this.cancelCount++;
      },
      [Symbol.asyncIterator]: () => ({
        next: () => Promise.resolve({ done: true, value: undefined }),
      }),
    };
  }
}
