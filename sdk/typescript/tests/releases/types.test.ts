import { inspect } from "node:util";
import { describe, expect, it } from "vitest";
import {
  classifiedReleaseCategory,
  ReleaseEntryMetadata,
  ReleaseManifest,
  ReleaseParameter,
  ReleaseSecret,
  ReleaseSnapshot,
} from "../../src/releases/types.js";

describe("release snapshots", () => {
  it("redacts secrets and snapshot serialization while returning defensive copies", () => {
    const parameterEntry = new ReleaseEntryMetadata({
      alias: "policy",
      kind: "parameter",
      path: "/prod/api/policy",
      version: 4n,
    });
    const secretEntry = new ReleaseEntryMetadata({
      alias: "password",
      kind: "secret",
      path: "/prod/api/password",
      version: 8n,
    });
    const source = Buffer.from("sensitive-value");
    const secret = new ReleaseSecret(source, secretEntry);
    source.fill(0);
    const snapshot = new ReleaseSnapshot({
      namespace: "prod/api",
      name: "runtime",
      version: 12n,
      activationRevision: 9_007_199_254_740_993n,
      digest: "digest",
      entries: new Map([
        ["policy", parameterEntry],
        ["password", secretEntry],
      ]),
      parameters: new Map([["policy", new ReleaseParameter('{"min":14}', parameterEntry)]]),
      secrets: new Map([["password", secret]]),
    });

    expect(String(secret)).toBe("[REDACTED]");
    expect(inspect(secret)).toBe("[REDACTED]");
    expect(JSON.stringify(secret)).toBe('"[REDACTED]"');
    expect(secret.stringValue()).toBe("sensitive-value");

    const firstCopy = snapshot.secret("password");
    if (!firstCopy) throw new Error("secret missing");
    firstCopy.bytes().fill(0);
    expect(snapshot.secret("password")?.stringValue()).toBe("sensitive-value");

    const entryCopy = snapshot.entries() as Map<string, ReleaseEntryMetadata>;
    entryCopy.clear();
    expect(snapshot.entry("policy")).toBe(parameterEntry);
    expect(snapshot.parameter("policy")?.value()).toBe('{"min":14}');

    const json = JSON.stringify(snapshot);
    expect(json).not.toContain("sensitive-value");
    expect(json).not.toContain("min");
    expect(json).toContain('"activationRevision":"9007199254740993"');
    expect(inspect(snapshot)).not.toContain("sensitive-value");
  });

  it("accepts every uint64 boundary on public release identity objects", () => {
    const maximum = 18_446_744_073_709_551_615n;
    const entry = new ReleaseEntryMetadata({
      alias: "maximum",
      kind: "parameter",
      path: "/prod/api/maximum",
      version: maximum,
    });
    const identity = {
      namespace: "prod/api",
      name: "runtime",
      version: maximum,
      activationRevision: maximum,
      schemaVersion: maximum,
      digest: "digest",
      entries: new Map([[entry.alias, entry]]),
    };

    expect(entry.version).toBe(maximum);
    expect(new ReleaseManifest(identity)).toMatchObject({
      version: maximum,
      activationRevision: maximum,
      schemaVersion: maximum,
    });
    expect(
      new ReleaseSnapshot({ ...identity, parameters: new Map(), secrets: new Map() }),
    ).toMatchObject({
      version: maximum,
      activationRevision: maximum,
      schemaVersion: maximum,
    });
  });

  it.each([
    ["negative", -1n],
    ["overflow", 18_446_744_073_709_551_616n],
    ["non-bigint", 1 as unknown as bigint],
  ])("rejects a %s public release entry version", (_description, version) => {
    expect(
      () =>
        new ReleaseEntryMetadata({
          alias: "invalid",
          kind: "parameter",
          path: "/prod/api/invalid",
          version,
        }),
    ).toThrow(TypeError);
  });

  it.each(["version", "activationRevision", "schemaVersion"] as const)(
    "rejects invalid %s values in both public release identity constructors",
    (field) => {
      const base = {
        namespace: "prod/api",
        name: "runtime",
        version: 1n,
        activationRevision: 1n,
        schemaVersion: 1n,
        digest: "digest",
        entries: new Map<string, ReleaseEntryMetadata>(),
      };
      for (const invalid of [-1n, 18_446_744_073_709_551_616n, 1 as unknown as bigint]) {
        const identity = { ...base, [field]: invalid };
        expect(() => new ReleaseManifest(identity)).toThrow(TypeError);
        expect(
          () => new ReleaseSnapshot({ ...identity, parameters: new Map(), secrets: new Map() }),
        ).toThrow(TypeError);
      }
    },
  );
});

describe("release rejection classification", () => {
  it("reads an allowed category once and contains hostile reflection", () => {
    let reads = 0;
    const classified = Object.defineProperty({}, "releaseRejectionCategory", {
      get: () => {
        reads += 1;
        return "prepare_failed";
      },
    });
    expect(classifiedReleaseCategory(classified)).toBe("prepare_failed");
    expect(reads).toBe(1);

    const hostile = new Proxy(
      {},
      {
        get: () => {
          throw new Error("sensitive proxy failure");
        },
      },
    );
    expect(classifiedReleaseCategory(hostile)).toBeUndefined();
  });
});
