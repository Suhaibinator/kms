import { inspect } from "node:util";
import { describe, expect, it } from "vitest";
import {
  ReleaseEntryMetadata,
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
});
