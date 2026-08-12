import { describe, expect, it } from "vitest";

import { KmsClient } from "../src/client.js";
import {
  ConfigurationRelease,
  type ConfigurationReleaseEntry,
  type ResourceRef,
  type WatchReleaseRequest,
} from "../src/generated/kms.js";
import { deterministicReleaseDigest, sha256Hex } from "../src/releases/digest.js";
import { type FakeDuplex, FakeTransport } from "./helpers/fake-transport.js";

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
      clientBound: false,
      hasAccessToken: false,
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
      clientBound: false,
      hasAccessToken: true,
    });
    const transport = new FakeTransport((path, _request, options) => {
      if (path.endsWith("/GetActiveRelease")) {
        return { release, activationRevision: 12n, previousVersion: 0n };
      }
      if (path.endsWith("/GetSecret")) {
        expect(options.metadata?.["x-kms-secret-token"]).toBe("release-token");
        return {
          ref: returnedRef,
          version: 9n,
          value: Buffer.from(plaintext),
          contentType: "text/plain",
          metadataJson: "",
          createdAtUnixMs: 1n,
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
});

function makeRelease(entry: ConfigurationReleaseEntry): ConfigurationRelease {
  const release = ConfigurationRelease.create({
    namespace,
    name: "runtime",
    version: 3n,
    schemaId: "",
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
