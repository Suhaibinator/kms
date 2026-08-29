import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

import { canonicalParameterValue, parameterHash } from "../src/configstore/canonical.js";

interface CanonicalVector {
  readonly name: string;
  readonly content_type: string;
  readonly input: string;
  readonly canonical?: string;
  readonly sha256?: string;
  readonly error?: boolean;
}

// The Go SDK owns these vectors; both SDKs and the server must hash identically.
const vectorsPath = resolve(
  import.meta.dirname,
  "../../go/configstore/testdata/canonical_vectors.json",
);
const vectors = JSON.parse(readFileSync(vectorsPath, "utf8")) as readonly CanonicalVector[];

describe("canonical parameter values", () => {
  it("loads the shared Go vectors", () => {
    expect(vectors.length).toBeGreaterThan(20);
    expect(vectors.some((vector) => vector.error)).toBe(true);
    expect(vectors.some((vector) => vector.content_type !== "json")).toBe(true);
  });

  it.each(vectors.map((vector) => [vector.name, vector] as const))(
    "matches the Go reference for %s",
    (_name, vector) => {
      const inputs: (string | Uint8Array)[] = [vector.input, Buffer.from(vector.input, "utf8")];
      for (const input of inputs) {
        if (vector.error) {
          expect(() => canonicalParameterValue(vector.content_type, input)).toThrow(
            /configstore: canonical json/u,
          );
          expect(() => parameterHash(vector.content_type, input)).toThrow();
          continue;
        }
        const canonical = canonicalParameterValue(vector.content_type, input);
        expect(Buffer.from(canonical).toString("utf8")).toBe(vector.canonical);
        expect(parameterHash(vector.content_type, input)).toBe(vector.sha256);
      }
    },
  );

  it("returns exact copies for non-JSON content and never shares the input buffer", () => {
    const input = Buffer.from([0xff, 0x00, 0x80]);
    const copied = canonicalParameterValue("bytes", input);
    expect([...copied]).toEqual([0xff, 0x00, 0x80]);
    copied.fill(0);
    expect([...input]).toEqual([0xff, 0x00, 0x80]);
  });

  it("rejects invalid UTF-8 bytes and raw lone surrogates but decodes escaped ones like Go", () => {
    expect(() => canonicalParameterValue("json", Buffer.from([0x22, 0xff, 0x22]))).toThrow(
      /invalid UTF-8/u,
    );
    expect(() => canonicalParameterValue("json", '"\ud800"')).toThrow(/invalid UTF-8/u);
    // Go's encoding/json replaces an escaped unpaired surrogate with U+FFFD.
    expect(Buffer.from(canonicalParameterValue("json", '"\\ud800"')).toString("utf8")).toBe('"�"');
    expect(() => canonicalParameterValue("json", '{"a":1,"a":1}')).toThrow(/duplicate/u);
    expect(() => canonicalParameterValue("json", '{"a":[1,{"b":1,"b":2}]}')).toThrow(/duplicate/u);
    expect(() => canonicalParameterValue("json", "[1,]")).toThrow(/invalid document/u);
    expect(() => canonicalParameterValue(7 as unknown as string, "1")).toThrow(TypeError);
    expect(() => canonicalParameterValue("json", 7 as unknown as string)).toThrow(TypeError);
  });

  it("sorts object keys by UTF-8 bytes rather than UTF-16 code units", () => {
    const canonical = Buffer.from(canonicalParameterValue("json", '{"￿":1,"😀":2}'));
    expect(canonical.toString("utf8")).toBe('{"￿":1,"😀":2}');
    expect(parameterHash("json", '{"b":1,"a":2}')).toBe(parameterHash("json", '{"a":2,"b":1}'));
    expect(parameterHash("string", '{"b":1,"a":2}')).not.toBe(
      parameterHash("string", '{"a":2,"b":1}'),
    );
  });
});
