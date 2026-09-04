import { describe, expect, it } from "vitest";

import { KmsClient } from "../src/client.js";
import {
  ConfigurationRelease,
  type ConfigurationReleaseEntry,
  type ResourceRef,
  type WatchReleaseRequest,
} from "../src/generated/kms.js";
import { deterministicReleaseDigest, sha256Hex } from "../src/releases/digest.js";
import type { BidiMethod, DuplexRpc, TransportCallOptions } from "../src/transport.js";
import { type FakeDuplex, FakeTransport, waitFor } from "./helpers/fake-transport.js";

const namespace = { env: "prod", app: "api" } as const;
const expectedRef: ResourceRef = { namespace, key: "settings" };
const wrongRef: ResourceRef = { namespace, key: "other" };

describe("KmsClient release transport boundary", () => {
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
    const transport = new FakeTransport((path) => {
      if (path.endsWith("/GetActiveRelease")) {
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
      throw new Error(`unexpected ${path}`);
    });
    const client = new KmsClient({ transport, namespace: "prod/api", cacheTtlMs: 60_000 });
    const loader = await client.createReleaseLoader({ name: "runtime" });

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
        expect((_request as { secretToken?: string }).secretToken).toBe("release-token");
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
            hasAccessToken: true,
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
                hasAccessToken: true,
              },
            ],
          },
        };
      }
      throw new Error(`unexpected ${path}`);
    });
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const loader = await client.createReleaseLoader({
      name: "runtime",
      secretTokenProvider: () => "release-token",
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
      throw new Error(`unexpected ${path}`);
    });
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const loader = await client.createReleaseLoader({ name: "runtime" });

    const run = loader.run(() => ({ commit() {}, abort() {} }));
    await waitFor(() => transport.cancelCount === 1);
    loader.stop();

    await expect(run).rejects.toMatchObject({ name: "AbortError" });
    expect(transport.streamCount).toBe(1);
    expect(transport.cancelCount).toBe(1);
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
