import { describe, expect, it } from "vitest";

import { KmsClient } from "../src/client.js";
import { parameterHash } from "../src/configstore/canonical.js";
import type { ContractEntry } from "../src/configstore/contract.js";
import { VerifyResult, verifyDefaults } from "../src/configstore/verify.js";
import { ConfigError, RateLimitedError } from "../src/errors.js";
import {
  VerifyReleaseDefaultsRequest,
  VerifyReleaseDefaultsResponse,
} from "../src/generated/kms.js";
import type {
  VerifyReleaseDefaultsOptions,
  VerifyReleaseDefaultsResult,
} from "../src/releases/verify.js";
import { FakeTransport } from "./helpers/fake-transport.js";

const contract: readonly ContractEntry[] = [
  { alias: "runtime", kind: "parameter", contentType: "json" },
  { alias: "database", kind: "parameter", contentType: "json" },
  { alias: "database_password", kind: "secret" },
];

function fakeClient(
  respond: (options: VerifyReleaseDefaultsOptions) => VerifyReleaseDefaultsResult | Error,
) {
  const requests: VerifyReleaseDefaultsOptions[] = [];
  return {
    requests,
    verifyReleaseDefaults(options: VerifyReleaseDefaultsOptions) {
      requests.push(options);
      const result = respond(options);
      return result instanceof Error ? Promise.reject(result) : Promise.resolve(result);
    },
  };
}

function wireResult(
  entries: readonly {
    alias: string;
    verdict: VerifyReleaseDefaultsResult["entries"][number]["verdict"];
  }[],
  overrides: Partial<VerifyReleaseDefaultsResult> = {},
): VerifyReleaseDefaultsResult {
  const count = (verdict: string): number => entries.filter((e) => e.verdict === verdict).length;
  const schemaMatches = overrides.schemaMatches ?? true;
  return {
    releaseName: "runtime",
    releaseVersion: 4n,
    schemaVersion: 2n,
    activationRevision: 9n,
    schemaMatches,
    entries,
    matchCount: count("match"),
    differsCount: count("differs"),
    missingCount: count("missing_in_release"),
    unknownAliasCount: count("unknown_alias"),
    secretAliasCount: count("secret_alias"),
    unsupportedCount: count("unsupported_content_type"),
    unverifiedCount: 0,
    passed: () => schemaMatches && entries.every((e) => e.verdict === "match"),
    ...overrides,
  };
}

describe("configstore.verifyDefaults", () => {
  const groups = { runtime: '{"b":1,"a":2}', database: '{"host":"db.internal"}' };
  const schemaSha256 = "a".repeat(64);

  it.each([0n, 2n])(
    "retains schema %s in managed results, JSON, and reports",
    async (schemaVersion) => {
      const transport = new FakeTransport(() =>
        VerifyReleaseDefaultsResponse.fromPartial({
          name: "runtime",
          version: 1n,
          schemaVersion,
          activationRevision: 9n,
          schemaMatches: true,
          matchCount: 2,
          entries: [
            { alias: "runtime", verdict: "match" },
            { alias: "database", verdict: "match" },
          ],
        }),
      );
      const client = new KmsClient({ transport });
      try {
        const result = await verifyDefaults(
          client,
          { schemaSha256: schemaVersion === 0n ? "" : schemaSha256, contract, groups },
          { namespace: "prod/api", ...(schemaVersion === 0n ? { schemaVersion } : {}) },
        );
        expect(transport.calls).toHaveLength(1);
        expect(transport.calls[0]?.path).toBe(
          "/kms.v1.ConfigurationReleaseService/VerifyReleaseDefaults",
        );
        const request = transport.calls[0]?.request as VerifyReleaseDefaultsRequest;
        expect(request.schemaVersion).toBe(schemaVersion === 0n ? 0n : undefined);
        expect(request.schemaSha256).toBe(schemaVersion === 0n ? "" : schemaSha256);
        const decoded = VerifyReleaseDefaultsRequest.decode(
          VerifyReleaseDefaultsRequest.encode(request).finish(),
        );
        expect(decoded.schemaVersion).toBe(schemaVersion === 0n ? 0n : undefined);
        expect(decoded.schemaSha256).toBe(schemaVersion === 0n ? "" : schemaSha256);
        expect(result.schemaVersion).toBe(schemaVersion);
        expect(result.passed()).toBe(true);
        expect(JSON.parse(JSON.stringify(result))).toMatchObject({
          releaseVersion: "1",
          schemaVersion: schemaVersion.toString(),
        });
        expect(result.report()).toContain(
          `prod/api runtime@1#9  schema_version: ${schemaVersion}  schema: match`,
        );
      } finally {
        await client.close();
      }
    },
  );

  it.each([
    { label: "missing", schemaSha256: "", options: {} },
    { label: "digest and zero", schemaSha256, options: { schemaVersion: 0n } },
    { label: "digest and registered version", schemaSha256, options: { schemaVersion: 2n } },
  ])("rejects $label selectors before any RPC", async (selection) => {
    const transport = new FakeTransport(() => undefined);
    const client = new KmsClient({ transport });
    try {
      await expect(
        verifyDefaults(
          client,
          { schemaSha256: selection.schemaSha256, contract, groups },
          { namespace: "prod/api", ...selection.options },
        ),
      ).rejects.toThrow(ConfigError);
      expect(transport.calls).toHaveLength(0);
    } finally {
      await client.close();
    }
  });

  it("hashes parameter groups canonically, skips secrets, and never sends values", async () => {
    const client = fakeClient(() =>
      wireResult([
        { alias: "runtime", verdict: "match" },
        { alias: "database", verdict: "match" },
      ]),
    );
    const result = await verifyDefaults(
      client,
      { schemaSha256, contract, groups },
      { namespace: " prod/api ", release: "runtime", profile: "local" },
    );

    expect(client.requests).toHaveLength(1);
    expect(client.requests[0]).toEqual({
      namespace: "prod/api",
      release: "runtime",
      profile: "local",
      schemaSha256,
      entries: [
        { alias: "runtime", contentType: "json", sha256: parameterHash("json", '{"a":2,"b":1}') },
        { alias: "database", contentType: "json", sha256: parameterHash("json", groups.database) },
      ],
    });
    expect(JSON.stringify(client.requests[0])).not.toContain("db.internal");
    expect(result).toBeInstanceOf(VerifyResult);
    expect(result.passed()).toBe(true);
    expect(result.failures()).toEqual([]);
    expect(result).toMatchObject({
      namespace: "prod/api",
      releaseName: "runtime",
      releaseVersion: 4n,
      schemaVersion: 2n,
      activationRevision: 9n,
      schemaMatches: true,
      unverified: 0,
    });
    expect(Object.isFrozen(result)).toBe(true);
    expect(Object.isFrozen(result.entries)).toBe(true);
    expect(result.report()).toBe(
      [
        "prod/api runtime@4#9  schema_version: 2  schema: match",
        "VERDICT  ALIAS     CONTENT_TYPE",
        "match    database  json",
        "match    runtime   json",
        "summary: match=2 differs=0 missing_in_release=0 unknown_alias=0 secret_alias=0 unsupported_content_type=0 unverified=0",
        "result: active release matches source defaults",
        "",
      ].join("\n"),
    );
  });

  it("reports failures sorted by alias with a value-free table and counts", async () => {
    const client = fakeClient(() =>
      wireResult(
        [
          { alias: "runtime", verdict: "differs" },
          { alias: "database", verdict: "missing_in_release" },
        ],
        { schemaMatches: false, unverifiedCount: 3 },
      ),
    );
    const result = await verifyDefaults(
      client,
      { schemaSha256, contract, groups },
      { namespace: "prod/api" },
    );
    expect(client.requests[0]).not.toHaveProperty("release");
    expect(result.passed()).toBe(false);
    expect(result.failures()).toEqual([
      { alias: "database", contentType: "json", verdict: "missing_in_release" },
      { alias: "runtime", contentType: "json", verdict: "differs" },
    ]);
    const report = result.report();
    expect(report).toBe(
      [
        "prod/api runtime@4#9  schema_version: 2  schema: differs",
        "VERDICT             ALIAS     CONTENT_TYPE",
        "missing_in_release  database  json",
        "differs             runtime   json",
        "summary: match=0 differs=1 missing_in_release=1 unknown_alias=0 secret_alias=0 unsupported_content_type=0 unverified=3",
        "result: active release differs from source defaults",
        "",
      ].join("\n"),
    );
    expect(report).not.toContain("db.internal");
    expect(JSON.parse(JSON.stringify(result))).toMatchObject({
      releaseVersion: "4",
      passed: false,
      unverified: 3,
    });
  });

  it("validates its inputs before any RPC", async () => {
    const client = fakeClient(() => wireResult([]));
    await expect(
      verifyDefaults(client, { schemaSha256, contract, groups }, { namespace: " " }),
    ).rejects.toThrow(/options\.namespace/u);
    await expect(
      verifyDefaults(
        client,
        { schemaSha256, contract, groups: { runtime: groups.runtime } },
        { namespace: "prod/api" },
      ),
    ).rejects.toThrow(/missing encoded parameter group database/u);
    await expect(
      verifyDefaults(
        client,
        { schemaSha256, contract, groups: { ...groups, runtime: '{"a":1,"a":2}' } },
        { namespace: "prod/api" },
      ),
    ).rejects.toThrow(/hash parameter group runtime/u);
    await expect(
      verifyDefaults(
        {} as unknown as Parameters<typeof verifyDefaults>[0],
        { schemaSha256, contract, groups },
        { namespace: "prod/api" },
      ),
    ).rejects.toThrow(TypeError);
    expect(client.requests).toHaveLength(0);
  });

  it("propagates rate limiting untouched so callers can back off", async () => {
    const limited = new RateLimitedError("verify budget exhausted");
    const client = fakeClient(() => limited);
    await expect(
      verifyDefaults(client, { schemaSha256, contract, groups }, { namespace: "prod/api" }),
    ).rejects.toBe(limited);
  });
});
