import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
  type ContractEntry,
  checkContractAlignment,
  contentTypeToSchema,
  deriveContractFromSchema,
  deriveSchemaFromContract,
  jsonTypeToContentType,
  parseContractFile,
  SCHEMA_2020_12,
  schemaSha256Hex,
  sha256Hex,
} from "@/lib/contract-derive";

const examples = resolve(process.cwd(), "..", "examples", "managed-config");
const contractFile = readFileSync(resolve(examples, "runtime.contract.json"), "utf8");
const schemaFile = readFileSync(resolve(examples, "runtime.schema.json"), "utf8");

const contract: ContractEntry[] = [
  { alias: "database", kind: "parameter", content_type: "json" },
  { alias: "rate_limits", kind: "parameter", content_type: "integer" },
  { alias: "db_password", kind: "secret" },
];

describe("parseContractFile", () => {
  it("projects the kms-config-contract/v1 envelope to groups ∪ secrets", () => {
    const parsed = parseContractFile(contractFile);
    expect(parsed.source).toBe("envelope");
    expect(parsed.contract).toEqual([
      { alias: "runtime", kind: "parameter", content_type: "json" },
      { alias: "server", kind: "parameter", content_type: "json" },
      { alias: "api_key", kind: "secret" },
    ]);
    // The exact digest is checked against the sibling schema file in the sha256Hex suite.
    expect(parsed.schema_sha256).toMatch(/^[0-9a-f]{64}$/);
  });

  it("accepts a bare array", () => {
    const parsed = parseContractFile(JSON.stringify(contract));
    expect(parsed.source).toBe("array");
    expect(parsed.contract).toEqual(contract);
    expect(parsed.schema_sha256).toBeUndefined();
  });

  it("rejects malformed input with a human message", () => {
    expect(() => parseContractFile("{")).toThrow(/not valid JSON/);
    expect(() => parseContractFile('{"format":"other/v9"}')).toThrow(/Unsupported contract format/);
    expect(() => parseContractFile('{"groups":[]}')).toThrow(/Expected a kms-config-contract\/v1/);
    expect(() => parseContractFile('[{"alias":"a","kind":"thing"}]')).toThrow(/kind must be/);
    expect(() =>
      parseContractFile('[{"alias":"a","kind":"parameter","content_type":"yaml"}]'),
    ).toThrow(/content type/i);
    expect(() =>
      parseContractFile(
        '[{"alias":"a","kind":"parameter","content_type":"json"},{"alias":"a","kind":"secret"}]',
      ),
    ).toThrow(/Duplicate contract alias/);
  });
});

describe("type mapping", () => {
  it("mirrors jsonTypeToContentType", () => {
    expect(jsonTypeToContentType({ type: "object" })).toBe("json");
    expect(jsonTypeToContentType({ type: "array" })).toBe("json");
    expect(jsonTypeToContentType({ type: "string" })).toBe("string");
    expect(jsonTypeToContentType({ type: "string", format: "kms-base64" })).toBe("binary");
    expect(jsonTypeToContentType({ type: "integer" })).toBe("integer");
    expect(jsonTypeToContentType({ type: "number" })).toBe("float");
    expect(jsonTypeToContentType({ type: "boolean" })).toBe("boolean");
    expect(jsonTypeToContentType({ type: ["string", "null"] })).toBe("json");
    expect(jsonTypeToContentType({ type: ["integer"] })).toBe("integer");
    expect(jsonTypeToContentType({})).toBe("json");
    expect(jsonTypeToContentType(true)).toBe("json");
  });

  it("reverses to {} for json and {type} otherwise", () => {
    expect(contentTypeToSchema("json")).toEqual({});
    expect(contentTypeToSchema("string")).toEqual({ type: "string" });
    expect(contentTypeToSchema("binary")).toEqual({ type: "string", format: "kms-base64" });
    expect(contentTypeToSchema("integer")).toEqual({ type: "integer" });
    expect(contentTypeToSchema("float")).toEqual({ type: "number" });
    expect(contentTypeToSchema("boolean")).toEqual({ type: "boolean" });
    expect(contentTypeToSchema(undefined)).toEqual({});
  });
});

describe("deriveContractFromSchema", () => {
  it("derives one parameter per top-level property", () => {
    const { contract: derived, notes } = deriveContractFromSchema(schemaFile);
    expect(derived).toEqual([
      { alias: "runtime", kind: "parameter", content_type: "json" },
      { alias: "server", kind: "parameter", content_type: "json" },
    ]);
    expect(notes).toEqual([]);
  });

  it("keeps existing secrets and existing content types where the schema is silent", () => {
    const schema = JSON.stringify({
      type: "object",
      properties: {
        database: { type: "object" },
        rate_limits: {},
        flag: { type: ["boolean", "null"] },
        limit: { type: "number" },
      },
      required: ["database", "ghost"],
    });
    const existing: ContractEntry[] = [
      { alias: "database", kind: "parameter", content_type: "json" },
      { alias: "rate_limits", kind: "parameter", content_type: "integer" },
      { alias: "limit", kind: "parameter", content_type: "integer" },
      { alias: "legacy", kind: "parameter", content_type: "string" },
      { alias: "db_password", kind: "secret" },
    ];
    const { contract: derived, notes } = deriveContractFromSchema(schema, existing);
    expect(derived).toEqual([
      { alias: "database", kind: "parameter", content_type: "json" },
      { alias: "rate_limits", kind: "parameter", content_type: "integer" },
      { alias: "flag", kind: "parameter", content_type: "json" },
      { alias: "limit", kind: "parameter", content_type: "float" },
      { alias: "db_password", kind: "secret" },
    ]);
    expect(notes).toEqual([
      "`rate_limits` has no single JSON type in the schema; kept its existing content type integer.",
      "`flag` has no single JSON type in the schema; defaulted to json.",
      "`limit` changed from integer to float to match the schema.",
      "`legacy` is not a schema property and was dropped.",
      "The schema requires `ghost` but defines no such property.",
    ]);
  });

  it("keeps a secret that collides with a schema property", () => {
    const { contract: derived, notes } = deriveContractFromSchema(
      JSON.stringify({ properties: { db_password: { type: "string" } } }),
      [{ alias: "db_password", kind: "secret" }],
    );
    expect(derived).toEqual([{ alias: "db_password", kind: "secret" }]);
    expect(notes[0]).toMatch(/kept as a secret/);
  });

  it("returns the existing contract with a note when the schema is unusable", () => {
    expect(deriveContractFromSchema("nope", contract)).toEqual({
      contract,
      notes: [expect.stringMatching(/not valid JSON/)],
    });
    expect(
      deriveContractFromSchema(
        JSON.stringify({ $schema: "http://json-schema.org/draft-07/schema#" }),
      ).notes[0],
    ).toMatch(/Only JSON Schema 2020-12/);
    expect(deriveContractFromSchema("{}").notes).toEqual([
      "The schema defines no top-level properties.",
    ]);
  });
});

describe("deriveSchemaFromContract", () => {
  it("emits a closed 2020-12 object requiring every parameter alias", () => {
    const { schemaJson, notes } = deriveSchemaFromContract(contract);
    expect(JSON.parse(schemaJson)).toEqual({
      $schema: SCHEMA_2020_12,
      type: "object",
      properties: { database: {}, rate_limits: { type: "integer" } },
      required: ["database", "rate_limits"],
      additionalProperties: false,
    });
    expect(notes).toEqual([]);
  });

  it("merges existing property bodies and carries other top-level keywords", () => {
    const existing = JSON.stringify({
      $schema: SCHEMA_2020_12,
      title: "gradethis",
      type: "object",
      properties: {
        database: { type: "object", properties: { host: { type: "string" } } },
        rate_limits: { type: "string", format: "kms-base64", description: "per minute" },
        stale: { type: "string" },
      },
      required: ["database"],
      additionalProperties: true,
    });
    const { schemaJson, notes } = deriveSchemaFromContract(contract, existing);
    expect(JSON.parse(schemaJson)).toEqual({
      $schema: SCHEMA_2020_12,
      title: "gradethis",
      type: "object",
      properties: {
        database: { type: "object", properties: { host: { type: "string" } } },
        rate_limits: { type: "integer", description: "per minute" },
      },
      required: ["database", "rate_limits"],
      additionalProperties: false,
    });
    expect(notes).toEqual(["Schema property `stale` is not a contract parameter and was dropped."]);
  });

  it("round-trips the example artifacts", () => {
    const parsed = parseContractFile(contractFile);
    const { schemaJson } = deriveSchemaFromContract(parsed.contract, schemaFile);
    expect(JSON.parse(schemaJson)).toEqual(JSON.parse(schemaFile));
    expect(checkContractAlignment(parsed.contract, schemaFile).aligned).toBe(true);
  });

  it("notes an unparseable existing schema and starts fresh", () => {
    const { schemaJson, notes } = deriveSchemaFromContract([], "{");
    expect(notes).toEqual([
      expect.stringMatching(/not valid JSON.*Starting from an empty schema/),
      "The contract has no parameter aliases.",
    ]);
    expect(JSON.parse(schemaJson).required).toEqual([]);
  });
});

describe("checkContractAlignment", () => {
  it("is aligned with no schema or a matching one", () => {
    expect(checkContractAlignment(contract, null)).toEqual({ aligned: true, issues: [] });
    const { schemaJson } = deriveSchemaFromContract(contract);
    expect(checkContractAlignment(contract, schemaJson)).toEqual({ aligned: true, issues: [] });
  });

  it("grades a missing parameter by additionalProperties and ignores secrets", () => {
    const closed = JSON.stringify({
      additionalProperties: false,
      properties: { database: {} },
      required: ["database"],
    });
    expect(checkContractAlignment(contract, closed)).toEqual({
      aligned: false,
      issues: [
        {
          code: "missing_in_schema",
          alias: "rate_limits",
          severity: "error",
          detail: expect.stringMatching(/additionalProperties is false/),
        },
      ],
    });
    const open = JSON.stringify({ properties: { database: {} }, required: ["database"] });
    expect(checkContractAlignment(contract, open).issues).toEqual([
      expect.objectContaining({
        code: "missing_in_schema",
        alias: "rate_limits",
        severity: "warning",
      }),
    ]);
  });

  it("reports required, type and extra-property issues", () => {
    const schema = JSON.stringify({
      properties: {
        database: { type: "string" },
        rate_limits: { type: "integer" },
        db_password: { type: "string" },
        extra: {},
        must: {},
      },
      required: ["database", "must"],
    });
    const result = checkContractAlignment(contract, schema);
    expect(result.aligned).toBe(false);
    expect(result.issues).toEqual([
      expect.objectContaining({
        code: "content_type_mismatch",
        alias: "database",
        severity: "warning",
      }),
      expect.objectContaining({
        code: "required_missing",
        alias: "rate_limits",
        severity: "warning",
      }),
      expect.objectContaining({ code: "missing_in_contract", alias: "extra", severity: "warning" }),
      expect.objectContaining({ code: "missing_in_contract", alias: "must", severity: "error" }),
    ]);
  });

  it("flags an unparseable schema or a foreign dialect", () => {
    expect(checkContractAlignment(contract, "{").issues).toEqual([
      expect.objectContaining({ code: "schema_unparseable", severity: "error" }),
    ]);
    expect(checkContractAlignment(contract, "[]").issues).toEqual([
      expect.objectContaining({ code: "schema_unparseable" }),
    ]);
    expect(
      checkContractAlignment(contract, '{"$schema":"http://json-schema.org/draft-07/schema#"}')
        .issues,
    ).toEqual([expect.objectContaining({ code: "unsupported_dialect", severity: "error" })]);
    expect(
      checkContractAlignment([], `{"$schema":"${SCHEMA_2020_12}#","properties":{}}`).aligned,
    ).toBe(true);
  });
});

describe("sha256Hex", () => {
  it("matches the artifact's schema_sha256 for the sibling schema file", async () => {
    await expect(schemaSha256Hex(schemaFile)).resolves.toBe(
      parseContractFile(contractFile).schema_sha256,
    );
    await expect(schemaSha256Hex(` { "maximum" : 18446744073709551615 } `)).resolves.toBe(
      await schemaSha256Hex('{"maximum":18446744073709551615}'),
    );
    await expect(sha256Hex("")).resolves.toBe(
      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    );
  });
});
