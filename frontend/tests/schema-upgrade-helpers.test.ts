import { describe, expect, it } from "vitest";
import { schemaDifferences } from "@/lib/schema-diff";
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
