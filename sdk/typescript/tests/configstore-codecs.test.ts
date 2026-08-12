import { describe, expect, it } from "vitest";

import {
  ConfigDecodeError,
  codecs,
  decodeGroup,
  encodeGroup,
  field,
  group,
} from "../src/configstore/codecs.js";
import { parseStrictJson } from "../src/configstore/strict-json.js";

interface Nested {
  count: number;
  note: string | null;
}

interface Fixture {
  enabled: boolean;
  name: string;
  signed: number;
  unsigned: number;
  exact: bigint;
  ratio: number;
  delay: bigint;
  blob: Uint8Array | null;
  nested: Nested;
  items: Nested[] | null;
  labels: Readonly<Record<string, number>> | null;
  optional: string | null;
}

const nested = codecs.object<Nested>([
  field<Nested, "count">("count", "count", codecs.int({ bits: 16 })),
  field<Nested, "note">("note", "note", codecs.nullable(codecs.string)),
]);

const fixture = group<Fixture>([
  field<Fixture, "enabled">("enabled", "enabled", codecs.boolean),
  field<Fixture, "name">("name", "name", codecs.string),
  field<Fixture, "signed">("signed", "signed", codecs.int({ bits: 8 })),
  field<Fixture, "unsigned">("unsigned", "unsigned", codecs.int({ bits: 16, unsigned: true })),
  field<Fixture, "exact">("exact", "exact", codecs.bigint({ bits: 64, unsigned: true })),
  field<Fixture, "ratio">("ratio", "ratio", codecs.float({ bits: 32 })),
  field<Fixture, "delay">("delay", "delay", codecs.duration),
  field<Fixture, "blob">("blob", "blob", codecs.bytes),
  field<Fixture, "nested">("nested", "nested", nested),
  field<Fixture, "items">("items", "items", codecs.array(nested)),
  field<Fixture, "labels">("labels", "labels", codecs.record(codecs.int({ bits: 32 }))),
  field<Fixture, "optional">("optional", "optional", codecs.nullable(codecs.string)),
]);

describe("strict managed-configuration codecs", () => {
  it("encodes records in locale-independent canonical key order", () => {
    interface RecordHolder {
      labels: Readonly<Record<string, string>> | null;
    }
    const descriptor = group<RecordHolder>([
      field<RecordHolder, "labels">("labels", "labels", codecs.record(codecs.string)),
    ]);
    const first = Object.assign(Object.create(null) as Record<string, string>, {
      z: "last",
      ä: "unicode",
      A: "first",
    });
    const second = Object.assign(Object.create(null) as Record<string, string>, {
      A: "first",
      ä: "unicode",
      z: "last",
    });

    expect(encodeGroup({ labels: first }, descriptor)).toBe(
      encodeGroup({ labels: second }, descriptor),
    );
    expect(encodeGroup({ labels: first }, descriptor)).toBe(
      '{"labels":{"A":"first","z":"last","ä":"unicode"}}',
    );
  });

  it("preserves duplicate properties and exact numeric lexemes in the syntax tree", () => {
    const root = parseStrictJson('{"value":1.20e1,"value":18446744073709551615}');
    expect(root.kind).toBe("object");
    if (root.kind !== "object") return;
    expect(root.properties).toHaveLength(2);
    expect(root.properties[0]?.value).toEqual({ kind: "number", lexeme: "1.20e1" });
    expect(root.properties[1]?.value).toEqual({
      kind: "number",
      lexeme: "18446744073709551615",
    });
  });

  it("normalizes unmatched JSON surrogates like Go without collapsing valid pairs", () => {
    const unmatched = parseStrictJson('"\\ud800"');
    expect(unmatched).toEqual({ kind: "string", value: "\ufffd" });
    const pair = parseStrictJson('"\\ud83d\\ude00"');
    expect(pair).toEqual({ kind: "string", value: "😀" });

    interface MapHolder {
      labels: Readonly<Record<string, string>> | null;
    }
    const maps = group<MapHolder>([
      field<MapHolder, "labels">("labels", "labels", codecs.record(codecs.string)),
    ]);
    expect(() => decodeGroup('{"labels":{"\\ud800":"a","\\ufffd":"b"}}', maps)).toThrow(
      /duplicate map key/u,
    );
  });

  it("decodes composite documents without losing uint64 precision", () => {
    const decoded = decodeGroup(
      JSON.stringify({
        enabled: true,
        name: "service",
        signed: -8,
        unsigned: 42,
        exact: 0, // replaced below so JSON.stringify never sees a bigint
        ratio: 1.25,
        delay: "1m30s",
        blob: "AAEC/w==",
        nested: { count: 7, note: null },
        items: [{ count: 9, note: "ready" }],
        labels: { one: 1, two: 2 },
        optional: null,
      }).replace('"exact":0', '"exact":18446744073709551615'),
      fixture,
    );

    expect(decoded).toMatchObject({
      enabled: true,
      name: "service",
      signed: -8,
      unsigned: 42,
      exact: 18_446_744_073_709_551_615n,
      ratio: 1.25,
      delay: 90_000_000_000n,
      optional: null,
    });
    expect(decoded.blob).toEqual(Uint8Array.from([0, 1, 2, 255]));
    expect(decoded.nested).toEqual({ count: 7, note: null });
    expect(decoded.items).toEqual([{ count: 9, note: "ready" }]);
    expect(decoded.labels).toEqual({ one: 1, two: 2 });
  });

  it("accepts mathematically integral decimal and exponent spellings exactly", () => {
    interface IntegerHolder {
      value: bigint;
    }
    const signed = group<IntegerHolder>([
      field<IntegerHolder, "value">("value", "value", codecs.bigint({ bits: 64 })),
    ]);
    const unsigned = group<IntegerHolder>([
      field<IntegerHolder, "value">("value", "value", codecs.bigint({ bits: 64, unsigned: true })),
    ]);

    expect(decodeGroup('{"value":-9.223372036854775808e18}', signed).value).toBe(-(1n << 63n));
    expect(decodeGroup('{"value":1.8446744073709551615e19}', unsigned).value).toBe(
      (1n << 64n) - 1n,
    );
    expect(decodeGroup('{"value":1.20e1}', signed).value).toBe(12n);
    expect(decodeGroup('{"value":-0.0}', signed).value).toBe(0n);

    expect(() => decodeGroup('{"value":-9.223372036854775809e18}', signed)).toThrow(
      /out of range/u,
    );
    expect(() => decodeGroup('{"value":1.8446744073709551616e19}', unsigned)).toThrow(
      /out of range/u,
    );
    expect(() => decodeGroup('{"value":1e1000000}', signed)).toThrow(/out of range/u);
  });

  it("rejects incomplete, duplicate, unknown, mistyped, and noncanonical input without values", () => {
    interface Holder {
      value: number;
    }
    const descriptor = group<Holder>([
      field<Holder, "value">("value", "value", codecs.int({ bits: 8 })),
    ]);
    const canary = "TOP-SECRET-CANARY";
    const documents = [
      `{"value":"${canary}"`,
      `{"value":1} "${canary}"`,
      "{}",
      `{"value":1,"${canary}":2}`,
      '{"value":1,"value":2}',
      '{"value":null}',
      `{"value":"${canary}"}`,
      '{"value":128}',
      '{"value":1.5}',
    ];

    for (const document of documents) {
      try {
        decodeGroup(document, descriptor);
        expect.fail("decode unexpectedly succeeded");
      } catch (error) {
        expect(error).toBeInstanceOf(ConfigDecodeError);
        expect(String(error)).not.toContain(canary);
        expect(JSON.stringify(error)).not.toContain(canary);
      }
    }
  });

  it("rejects recursive duplicate keys and enforces exact float boundaries", () => {
    interface MapHolder {
      labels: Readonly<Record<string, number>> | null;
    }
    const maps = group<MapHolder>([
      field<MapHolder, "labels">("labels", "labels", codecs.record(codecs.int())),
    ]);
    expect(() => decodeGroup('{"labels":{"same":1,"same":2}}', maps)).toThrow(/duplicate map key/u);

    interface FloatHolder {
      value: number;
    }
    const floats = group<FloatHolder>([
      field<FloatHolder, "value">("value", "value", codecs.float({ bits: 32 })),
    ]);
    expect(Number.isFinite(decodeGroup('{"value":3.4028234663852886e38}', floats).value)).toBe(
      true,
    );
    expect(decodeGroup('{"value":1e-1000}', floats).value).toBe(0);
    expect(() => decodeGroup('{"value":3.4028234663852887e38}', floats)).toThrow(/out of range/u);
  });

  it("preserves null versus empty collections and round trips canonical encodings", () => {
    const decodedNull = decodeGroup(
      '{"enabled":true,"name":"x","signed":0,"unsigned":0,"exact":0,"ratio":0,"delay":"0","blob":null,"nested":{"count":0,"note":null},"items":null,"labels":null,"optional":null}',
      fixture,
    );
    expect(decodedNull.blob).toBeNull();
    expect(decodedNull.items).toBeNull();
    expect(decodedNull.labels).toBeNull();

    const value: Fixture = {
      ...decodedNull,
      delay: 90_000_000_000n,
      blob: new Uint8Array(),
      items: [],
      labels: {},
    };
    const encoded = encodeGroup(value, fixture);
    expect(encoded).toContain('"delay":"1m30s"');
    expect(encoded).toContain('"blob":""');
    const decoded = decodeGroup(encoded, fixture);
    expect(decoded.blob).toEqual(new Uint8Array());
    expect(decoded.items).toEqual([]);
    expect(decoded.labels).toEqual({});
  });

  it("normalizes signed-zero duration and floats and accepts mutable array fields", () => {
    interface MutableCollections {
      values: string[] | null;
      fixed: string[];
      duration: bigint;
      ratio: number;
    }
    const descriptor = group<MutableCollections>([
      field<MutableCollections, "values">("values", "values", codecs.array(codecs.string)),
      field<MutableCollections, "fixed">("fixed", "fixed", codecs.fixedArray(codecs.string, 2)),
      field<MutableCollections, "duration">("duration", "duration", codecs.duration),
      field<MutableCollections, "ratio">("ratio", "ratio", codecs.float()),
    ]);

    for (const duration of ["0", "+0", "-0"]) {
      const decoded = decodeGroup(
        `{"values":[],"fixed":["a","b"],"duration":"${duration}","ratio":-0}`,
        descriptor,
      );
      expect(decoded.duration).toBe(0n);
      expect(Object.is(decoded.ratio, 0)).toBe(true);
      const encoded = encodeGroup(decoded, descriptor);
      expect(encoded).toContain('"ratio":0');
      expect(encodeGroup({ ...decoded, ratio: -0 }, descriptor)).toBe(encoded);
    }
  });

  it("rejects sparse or accessor-backed collections without invoking getters", () => {
    interface Collections {
      values: string[] | null;
      labels: Readonly<Record<string, string>> | null;
    }
    const descriptor = group<Collections>([
      field<Collections, "values">("values", "values", codecs.array(codecs.string)),
      field<Collections, "labels">("labels", "labels", codecs.record(codecs.string)),
    ]);
    const sparse = new Array<string>(1);
    expect(() => encodeGroup({ values: sparse, labels: {} }, descriptor)).toThrow(/dense/u);

    let getterCalls = 0;
    const values: string[] = [];
    Object.defineProperty(values, 0, {
      enumerable: true,
      get: () => {
        getterCalls += 1;
        return "ARRAY-GETTER-CANARY";
      },
    });
    values.length = 1;
    expect(() => encodeGroup({ values, labels: {} }, descriptor)).toThrow(/accessors/u);

    const labels: Record<string, string> = {};
    Object.defineProperty(labels, "unsafe", {
      enumerable: true,
      get: () => {
        getterCalls += 1;
        return "MAP-GETTER-CANARY";
      },
    });
    expect(() => encodeGroup({ values: [], labels }, descriptor)).toThrow(/accessor/u);
    expect(getterCalls).toBe(0);
  });
});
