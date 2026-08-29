import { Metadata, status } from "@grpc/grpc-js";
import { describe, expect, it } from "vitest";

import { KmsClient } from "../src/client.js";
import { ConfigError, KmsError, RateLimitedError } from "../src/errors.js";
import type { VerifyReleaseDefaultsResponse } from "../src/generated/kms.js";
import { FakeTransport } from "./helpers/fake-transport.js";

const sha = (fill: string): string => fill.repeat(64);

function response(
  overrides: Partial<VerifyReleaseDefaultsResponse> = {},
): VerifyReleaseDefaultsResponse {
  return {
    name: "runtime",
    version: 5n,
    activationRevision: 11n,
    schemaMatches: true,
    entries: [
      { alias: "runtime", verdict: "match" },
      { alias: "database", verdict: "differs" },
    ],
    matchCount: 1,
    differsCount: 1,
    missingInReleaseCount: 0,
    unknownAliasCount: 0,
    secretAliasCount: 0,
    unsupportedContentTypeCount: 0,
    unverifiedCount: 2,
    ...overrides,
  };
}

function grpcError(code: status, details: string): Error {
  return Object.assign(new Error(`${code} ${details}`), {
    code,
    details,
    metadata: new Metadata(),
  });
}

describe("KmsClient.verifyReleaseDefaults", () => {
  const entries = [
    { alias: " runtime ", contentType: "json", sha256: sha("a") },
    { alias: "database", contentType: "json", sha256: sha("b") },
  ];

  it("sends a value-free request and validates the verdicts", async () => {
    const transport = new FakeTransport((path, request) => {
      expect(path).toBe("/kms.v1.ConfigurationReleaseService/VerifyReleaseDefaults");
      expect(request).toEqual({
        namespace: { env: "prod", app: "api" },
        name: "runtime",
        profile: "local",
        schemaSha256: sha("c"),
        entries: [
          { alias: "runtime", contentType: "json", sha256: sha("a") },
          { alias: "database", contentType: "json", sha256: sha("b") },
        ],
      });
      return response();
    });
    const client = new KmsClient({ transport, token: "bearer" });
    const result = await client.verifyReleaseDefaults({
      namespace: "prod/api",
      release: "runtime",
      profile: "local",
      schemaSha256: sha("c"),
      entries,
    });
    expect(transport.calls[0]?.options.metadata?.authorization).toBe("Bearer bearer");
    expect(result).toMatchObject({
      releaseName: "runtime",
      releaseVersion: 5n,
      activationRevision: 11n,
      schemaMatches: true,
      matchCount: 1,
      differsCount: 1,
      missingCount: 0,
      unknownAliasCount: 0,
      secretAliasCount: 0,
      unsupportedCount: 0,
      unverifiedCount: 2,
    });
    expect(result.entries).toEqual([
      { alias: "runtime", verdict: "match" },
      { alias: "database", verdict: "differs" },
    ]);
    expect(result.passed()).toBe(false);
    expect(Object.isFrozen(result)).toBe(true);
    await client.close();
  });

  it("passes only when the schema and every entry match", async () => {
    const transport = new FakeTransport(() =>
      response({
        entries: [{ alias: "runtime", verdict: "match" }],
        matchCount: 1,
        differsCount: 0,
      }),
    );
    const client = new KmsClient({ transport });
    const result = await client.verifyReleaseDefaults({
      namespace: "prod/api",
      entries: [entries[0] as (typeof entries)[number]],
    });
    expect(result.passed()).toBe(true);
    expect(transport.calls[0]?.request).toMatchObject({ name: "", profile: "", schemaSha256: "" });
    await client.close();
  });

  it("rejects malformed requests before any RPC", async () => {
    const transport = new FakeTransport(() => response());
    const client = new KmsClient({ transport });
    const base = { namespace: "prod/api" };
    for (const [options, pattern] of [
      [
        { ...base, entries: [{ alias: " ", contentType: "json", sha256: sha("a") }] },
        /empty alias/u,
      ],
      [{ ...base, entries: [entries[0], entries[0]] }, /duplicated/u],
      [
        { ...base, entries: [{ alias: "x", contentType: "json", sha256: sha("A") }] },
        /invalid sha256/u,
      ],
      [
        { ...base, entries: [{ alias: "x", contentType: "json", sha256: "abc" }] },
        /invalid sha256/u,
      ],
      [{ ...base, entries, schemaSha256: "not-hex" }, /invalid schema sha256/u],
      [{ namespace: "prod", entries }, /namespace/u],
    ] as const) {
      await expect(
        client.verifyReleaseDefaults(options as Parameters<typeof client.verifyReleaseDefaults>[0]),
      ).rejects.toThrow(pattern);
    }
    await expect(
      client.verifyReleaseDefaults({
        ...base,
        entries: [entries[0] as never, entries[0] as never],
      }),
    ).rejects.toBeInstanceOf(ConfigError);
    expect(transport.calls).toHaveLength(0);
    await client.close();
  });

  it("fails closed on inconsistent or unknown server verdicts", async () => {
    const cases: [Partial<VerifyReleaseDefaultsResponse>, RegExp][] = [
      [{ entries: [{ alias: "runtime", verdict: "match" }] }, /verdicts for 2 entries/u],
      [
        {
          entries: [
            { alias: "runtime", verdict: "match" },
            { alias: "other", verdict: "match" },
          ],
        },
        /unknown or duplicated alias/u,
      ],
      [
        {
          entries: [
            { alias: "runtime", verdict: "match" },
            { alias: "runtime", verdict: "match" },
          ],
        },
        /unknown or duplicated alias/u,
      ],
      [
        {
          entries: [
            { alias: "runtime", verdict: "match" },
            { alias: "database", verdict: "exploded" },
          ],
        },
        /invalid verdict/u,
      ],
      [{ matchCount: 2 }, /match count disagrees/u],
      [{ unverifiedCount: -1 }, /unverified count/u],
    ];
    for (const [overrides, pattern] of cases) {
      const transport = new FakeTransport(() => response(overrides));
      const client = new KmsClient({ transport });
      const error = await client
        .verifyReleaseDefaults({ namespace: "prod/api", entries })
        .catch((reason: unknown) => reason);
      expect(error).toBeInstanceOf(KmsError);
      expect(error).toMatchObject({ code: "internal", message: expect.stringMatching(pattern) });
      await client.close();
    }
  });

  it("maps resource exhaustion to RateLimitedError and other statuses to KmsError", async () => {
    const limited = new KmsClient({
      transport: new FakeTransport(() => {
        throw grpcError(status.RESOURCE_EXHAUSTED, "verify budget spent");
      }),
    });
    const error = await limited
      .verifyReleaseDefaults({ namespace: "prod/api", entries })
      .catch((reason: unknown) => reason);
    expect(error).toBeInstanceOf(RateLimitedError);
    expect(error).toMatchObject({
      code: "resource_exhausted",
      grpcCode: status.RESOURCE_EXHAUSTED,
      message: expect.stringContaining("verify budget spent"),
    });
    expect((error as Error).message).toContain("wait for the window to reset");
    await limited.close();

    const denied = new KmsClient({
      transport: new FakeTransport(() => {
        throw grpcError(status.PERMISSION_DENIED, "missing operation");
      }),
    });
    await expect(
      denied.verifyReleaseDefaults({ namespace: "prod/api", entries }),
    ).rejects.toMatchObject({ code: "permission_denied" });
    await denied.close();
    await expect(denied.verifyReleaseDefaults({ namespace: "prod/api", entries })).rejects.toThrow(
      /closed/u,
    );
  });
});
