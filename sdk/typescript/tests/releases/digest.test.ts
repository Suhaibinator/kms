import { describe, expect, it } from "vitest";
import { ConfigurationRelease } from "../../src/generated/kms.js";
import {
  deterministicReleaseDigest,
  releaseDigestMatches,
  sha256Hex,
} from "../../src/releases/digest.js";

function fixture() {
  return ConfigurationRelease.create({
    namespace: { env: "prod", app: "api" },
    name: "runtime",
    version: 42n,
    schemaId: "runtime-v1",
    schemaVersion: 7n,
    entries: [
      {
        alias: "z-secret",
        kind: "secret",
        ref: { namespace: { env: "prod", app: "api" }, key: "db/password" },
        version: 9n,
        contentType: "string",
        metadataJson: "{}",
        parameterDigest: "",
        clientBound: true,
        hasAccessToken: true,
      },
      {
        alias: "a-policy",
        kind: "parameter",
        ref: { namespace: { env: "prod", app: "api" }, key: "policy" },
        version: 3n,
        contentType: "json",
        metadataJson: '{"owner":"platform"}',
        parameterDigest: sha256Hex('{"min":14}'),
        clientBound: false,
        hasAccessToken: false,
      },
    ],
    digest: "ignored",
    metadataJson: '{"rollout":"all"}',
    createdBy: "ignored-creator",
    createdAtUnixMs: 1_725_000_000_000n,
  });
}

describe("deterministicReleaseDigest", () => {
  it("matches the Go deterministic protobuf golden", () => {
    expect(deterministicReleaseDigest(fixture())).toBe(
      "c6a612894cd56d475d4c31d94007a3432389c347e1af2dfcba46e8490e35c535",
    );
  });

  it("sorts aliases and excludes server-allocated and audit fields", () => {
    const original = fixture();
    const changed = ConfigurationRelease.create({
      ...original,
      entries: [...original.entries].reverse(),
      version: 999n,
      digest: "a".repeat(64),
      createdBy: "somebody-else",
      createdAtUnixMs: 99n,
    });
    expect(deterministicReleaseDigest(changed)).toBe(deterministicReleaseDigest(original));
  });

  it("validates only a canonical SHA-256 claim", () => {
    const release = fixture();
    release.digest = deterministicReleaseDigest(release).toUpperCase();
    expect(releaseDigestMatches(release)).toBe(true);
    release.digest = "not-a-digest";
    expect(releaseDigestMatches(release)).toBe(false);
  });

  it("rejects missing resource namespaces instead of hashing a different projection", () => {
    const release = fixture();
    const first = release.entries[0];
    if (!first) throw new Error("fixture entry missing");
    first.ref = { namespace: undefined, key: "bad" };
    expect(() => deterministicReleaseDigest(release)).toThrow(/resource namespace/u);
  });
});
