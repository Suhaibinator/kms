import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

import {
  DefaultsArtifactError,
  encodeDefaultsArtifact,
  MAX_DEFAULT_PARAMETER_VALUE_BYTES,
  MAX_DEFAULTS_ARTIFACT_BYTES,
  parseDefaultsArtifact,
} from "../src/configgen/index.js";

const fixturePath = resolve(import.meta.dirname, "fixtures/configgen/defaults-artifact.json");
const schemaSHA256 = "0123456789abcdef".repeat(4);

describe("defaults artifact", () => {
  it("matches the canonical cross-language fixture including Go JSON escaping", async () => {
    const encodedValue = '{"text":"<>&\u2028\u2029"}';
    const document = encodeDefaultsArtifact({
      profile: "development<>&\u2028inner",
      schemaSHA256,
      contract: [
        { alias: "runtime", kind: "parameter", contentType: "application/json" },
        { alias: "secret-token", kind: "secret" },
      ],
      parameters: { runtime: encodedValue },
    });
    const fixture = await readFile(fixturePath, "utf8");

    expect(document).toBe(fixture);
    expect(document.endsWith("\n")).toBe(true);
    expect(document).toContain("<>&");
    expect(document).toContain("\\u2028");
    expect(document).toContain("\\u2029");
    expect(document).not.toContain("\\u003c");
    expect(document).not.toContain("\\u003e");
    expect(document).not.toContain("\\u0026");

    const parsed = parseDefaultsArtifact(Buffer.from(fixture, "utf8"));
    expect(parsed.profile).toBe("development<>&\u2028inner");
    expect(parsed.parameters[0]?.value).toBe(encodedValue);
    expect(parsed.contract).toEqual([
      { alias: "runtime", kind: "parameter", contentType: "application/json" },
      { alias: "secret-token", kind: "secret", contentType: "" },
    ]);
  });

  it("rejects unknown, duplicate, missing, unsorted, and secret-value structures", async () => {
    const fixture = (await readFile(fixturePath, "utf8")).trimEnd();
    expect(() => parseDefaultsArtifact(fixture.replace("{", '{"unknown":true,'))).toThrow(
      /unknown property/u,
    );
    expect(() =>
      parseDefaultsArtifact(fixture.replace('"profile":', '"profile":"duplicate","profile":')),
    ).toThrow(/duplicate property/u);
    const missing = JSON.parse(fixture) as Record<string, unknown>;
    delete missing.parameters;
    expect(() => parseDefaultsArtifact(JSON.stringify(missing))).toThrow(/missing/u);

    const parsed = JSON.parse(fixture) as {
      contract: unknown[];
      parameters: Array<Record<string, unknown>>;
    };
    parsed.contract.reverse();
    expect(() => parseDefaultsArtifact(JSON.stringify(parsed))).toThrow(/contract is not sorted/u);
    parsed.contract.reverse();
    parsed.parameters[0] = {
      alias: "secret-token",
      content_type: "",
      value: "must-never-be-representable",
    };
    expect(() => parseDefaultsArtifact(JSON.stringify(parsed))).toThrow(
      /parameters do not exactly match/u,
    );
    (parsed.contract[0] as Record<string, unknown>).value = "secret";
    expect(() => parseDefaultsArtifact(JSON.stringify(parsed))).toThrow(/unknown property/u);
  });

  it("requires a complete exact parameter contract and rejects duplicate aliases", () => {
    const base = {
      profile: "test",
      schemaSHA256,
      contract: [
        { alias: "runtime", kind: "parameter" as const, contentType: "json" },
        { alias: "token", kind: "secret" as const },
      ],
    };
    expect(() => encodeDefaultsArtifact({ ...base, parameters: {} })).toThrow(/exactly match/u);
    expect(() =>
      encodeDefaultsArtifact({
        ...base,
        contract: [
          { alias: "runtime", kind: "parameter", contentType: "json" },
          { alias: "runtime", kind: "parameter", contentType: "json" },
        ],
        parameters: { runtime: "{}" },
      }),
    ).toThrow(/duplicate alias/u);
    expect(() =>
      encodeDefaultsArtifact({
        ...base,
        contract: [],
        parameters: {},
      }),
    ).toThrow(/must not be empty/u);
  });

  it("enforces canonical profile, content types, digest, aliases, and UTF-8", () => {
    const input = {
      profile: "test",
      schemaSHA256,
      contract: [{ alias: "runtime", kind: "parameter" as const, contentType: "json" }],
      parameters: { runtime: "{}" },
    };
    expect(() => encodeDefaultsArtifact({ ...input, profile: " test" })).toThrow(/profile/u);
    expect(() =>
      encodeDefaultsArtifact({
        ...input,
        contract: [{ alias: "runtime", kind: "parameter", contentType: "json " }],
      }),
    ).toThrow(/content type/u);
    expect(() =>
      encodeDefaultsArtifact({ ...input, schemaSHA256: schemaSHA256.toUpperCase() }),
    ).toThrow(/SHA-256/u);
    expect(() =>
      encodeDefaultsArtifact({
        ...input,
        contract: [{ alias: "not/valid", kind: "parameter", contentType: "json" }],
      }),
    ).toThrow(/alias/u);
    expect(() => encodeDefaultsArtifact({ ...input, profile: "bad\ud800" })).toThrow(/profile/u);
    expect(() => parseDefaultsArtifact(Uint8Array.of(0xc3, 0x28))).toThrow(/UTF-8/u);
  });

  it("rejects a leading UTF-8 BOM in byte input", () => {
    const artifact = encodeDefaultsArtifact({
      profile: "test",
      schemaSHA256,
      contract: [{ alias: "runtime", kind: "parameter", contentType: "json" }],
      parameters: { runtime: "{}" },
    });
    const bytes = Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from(artifact)]);
    expect(() => parseDefaultsArtifact(new Uint8Array(bytes))).toThrow(/not valid JSON/u);
  });

  it("enforces parameter and artifact byte limits without echoing values", () => {
    const secretMarker = "do-not-print-this";
    const oversized = `${secretMarker}${"x".repeat(MAX_DEFAULT_PARAMETER_VALUE_BYTES)}`;
    let error: unknown;
    try {
      encodeDefaultsArtifact({
        profile: "test",
        schemaSHA256,
        contract: [{ alias: "runtime", kind: "parameter", contentType: "json" }],
        parameters: { runtime: oversized },
      });
    } catch (cause) {
      error = cause;
    }
    expect(error).toBeInstanceOf(DefaultsArtifactError);
    expect(String(error)).not.toContain(secretMarker);
    expect(() => parseDefaultsArtifact(" ".repeat(MAX_DEFAULTS_ARTIFACT_BYTES + 1))).toThrow(
      /maximum size/u,
    );
  });
});
