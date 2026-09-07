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
