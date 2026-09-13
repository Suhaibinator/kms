import { describe, expect, it } from "vitest";
import {
  describeSchemaEffect,
  schemaDifferences,
  structuredSchemaDifferences,
} from "@/lib/schema-diff";
import { parseSchema } from "@/lib/schema-form";
import { parseUpgradeDefaults } from "@/lib/upgrade-defaults";

describe("schema upgrade helpers", () => {
  it("compares nested fields and constraints without reporting reordered object keys", () => {
    const before = {
      properties: {
        database: {
          type: "object",
          properties: { host: { type: "string" }, port: { type: "integer" } },
        },
      },
    };
    const after = {
      properties: {
        database: {
          properties: { host: { minLength: 1, type: "string" }, tls: { type: "boolean" } },
          type: "object",
        },
      },
    };
    expect(schemaDifferences(JSON.stringify(before), JSON.stringify(after))).toEqual([
      { path: "database.host", change: "changed" },
      { path: "database.port", change: "removed" },
      { path: "database.tls", change: "added" },
    ]);
    expect(
      schemaDifferences('{"type":"object","properties":{}}', '{"properties":{},"type":"object"}'),
    ).toEqual([]);
  });
  it("compares exact numeric tokens throughout nested constraints", () => {
    const before =
      '{"properties":{"settings":{"properties":{"id":{"maximum":9223372036854775807,"examples":[null,9223372036854775807]}}}}}';
    const after =
      '{"properties":{"settings":{"properties":{"id":{"maximum":9223372036854775806,"examples":[null,9223372036854775806]}}}}}';

    expect(schemaDifferences(before, after)).toEqual([{ path: "settings.id", change: "changed" }]);
    expect(
      schemaDifferences(
        '{"properties":{"settings":{"examples":[null,[9223372036854775807]]}}}',
        '{"properties":{"settings":{"examples":[null,[9223372036854775806]]}}}',
      ),
    ).toEqual([{ path: "settings", change: "changed" }]);
    expect(
      schemaDifferences(
        '{"properties":{"value":{"const":9223372036854775807}},"examples":[null,[1,2]]}',
        '{"examples":[null,[1,2]],"properties":{"value":{"const":9223372036854775807}}}',
      ),
    ).toEqual([]);
  });
  it("distinguishes exact numbers from schema objects resembling internal markers", () => {
    const before = '{"properties":{"value":{"const":1}}}';
    const after = JSON.stringify({
      properties: { value: { const: { "\u0000kms.schema-number": "1" } } },
    });
    expect(schemaDifferences(before, after)).toEqual([{ path: "value", change: "changed" }]);
  });
  const artifact = {
    format: "kms-config-defaults/v1",
    profile: "dev",
    schema_sha256: "hash",
    contract: [
      { alias: "database", kind: "parameter", content_type: "json" },
      { alias: "token", kind: "secret" },
    ],
    parameters: [{ alias: "database", content_type: "json", value: '{"id":90071992547409931234}' }],
  };
  it("preserves exact encoded values and requires the selected schema", () => {
    expect(parseUpgradeDefaults(JSON.stringify(artifact), "hash").parameters[0].value).toBe(
      artifact.parameters[0].value,
    );
    expect(() => parseUpgradeDefaults(JSON.stringify(artifact), "different")).toThrow("digest");
  });
  it("rejects missing, duplicate, or secret parameter payloads", () => {
    for (const parameters of [
      [],
      [...artifact.parameters, ...artifact.parameters],
      [...artifact.parameters, { alias: "token", content_type: "string", value: "secret" }],
    ])
      expect(() =>
        parseUpgradeDefaults(JSON.stringify({ ...artifact, parameters }), "hash"),
      ).toThrow();
  });
});

describe("describeSchemaEffect", () => {
  const effects = (before: unknown, after: unknown) => {
    const a = JSON.stringify(before);
    const b = JSON.stringify(after);
    return Object.fromEntries(
      structuredSchemaDifferences(a, b).map((d) => [
        `${d.path}:${d.change}`,
        describeSchemaEffect(d, parseSchema(a), parseSchema(b)),
      ]),
    );
  };
  it("tells the operator what an added or removed property means for the value", () => {
    const before = {
      properties: {
        database: {
          type: "object",
          additionalProperties: false,
          properties: { legacy: { type: "string" } },
        },
        cache: { type: "object", properties: { old: { type: "string" } } },
      },
    };
    const after = {
      properties: {
        database: {
          type: "object",
          additionalProperties: false,
          required: ["tls"],
          properties: { tls: { type: "boolean" }, pool: { type: "integer", default: 5 } },
        },
        cache: { type: "object", properties: {} },
        region: { type: "string" },
      },
      required: ["region"],
    };
    const result = effects(before, after);
    expect(result["database.tls:added"]).toEqual({ kind: "provide", text: "Provide a value" });
    expect(result["database.pool:added"]).toEqual({
      kind: "optional",
      text: "Optional · default: 5",
    });
    expect(result["database.legacy:removed"]).toEqual({
      kind: "remove",
      text: "Remove it from the value",
    });
    expect(result["cache.old:removed"]).toEqual({
      kind: "ignored",
      text: "Ignored by the target schema",
    });
    expect(result["region:added"]).toEqual({
      kind: "provide",
      text: "New contract field — give it a value in Edit values",
    });
    expect(result["database:changed"]).toEqual({ kind: "review", text: "now requires tls" });
  });
  it("describes changed constraints, required flips, and documentation-only edits", () => {
    const before = {
      properties: {
        settings: {
          type: "object",
          required: ["mode"],
          properties: {
            port: { type: "string", description: "old" },
            mode: { type: "string" },
            note: { type: "string", description: "a" },
            timeout: { type: "integer" },
          },
        },
        workers: {
          type: "array",
          items: { type: "object", properties: { n: { type: "integer" } } },
        },
      },
    };
    const after = {
      properties: {
        settings: {
          type: "object",
          required: ["timeout"],
          properties: {
            port: { type: "integer", minimum: 1, description: "new" },
            mode: { type: "string" },
            note: { type: "string", description: "b" },
            timeout: { type: "integer" },
          },
        },
        workers: {
          type: "array",
          items: { type: "object", properties: { n: { type: "integer", minimum: 1 } } },
        },
      },
    };
    const result = effects(before, after);
    expect(result["settings.port:changed"]).toEqual({
      kind: "review",
      text: "minimum, type changed",
    });
    expect(result["settings.mode:changed"]).toEqual({ kind: "optional", text: "Now optional" });
    expect(result["settings.timeout:changed"]).toEqual({
      kind: "provide",
      text: "Now required — provide a value",
    });
    expect(result["settings.note:changed"]).toEqual({ kind: "none", text: "Documentation only" });
    expect(result["workers.[].n:changed"]).toEqual({ kind: "review", text: "minimum changed" });
  });
});
