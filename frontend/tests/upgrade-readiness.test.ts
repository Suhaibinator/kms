import { describe, expect, it } from "vitest";
import { structuredSchemaDifferences } from "@/lib/schema-diff";
import type { ReleaseValidationError } from "@/lib/types";
import type { UpgradeDraftField } from "@/lib/upgrade-field-changes";
import {
  createReadinessCache,
  fieldReadiness,
  matchProblemPath,
  type ReadinessIssue,
  schemaValueFor,
} from "@/lib/upgrade-readiness";

const target = (properties: Record<string, unknown>) =>
  JSON.stringify({ type: "object", properties, required: Object.keys(properties) });

const field = (patch: Partial<UpgradeDraftField>): UpgradeDraftField => ({
  id: 1,
  alias: "database",
  kind: "parameter",
  content_type: "json",
  key: "database",
  versionText: "",
  loaded: true,
  ...patch,
});

const objectTarget = target({
  database: {
    type: "object",
    required: ["tls"],
    properties: { tls: { type: "boolean" }, port: { type: "integer" } },
  },
});

describe("schemaValueFor", () => {
  it("types drafts the way the server does", () => {
    expect(schemaValueFor('{"a":1}', "json")).toEqual({ ok: true, value: { a: 1 } });
    expect(schemaValueFor("20", "integer")).toEqual({ ok: true, value: 20 });
    expect(schemaValueFor("1.5", "float")).toEqual({ ok: true, value: 1.5 });
    expect(schemaValueFor("t", "boolean")).toEqual({ ok: true, value: true });
    expect(schemaValueFor("False", "boolean")).toEqual({ ok: true, value: false });
    expect(schemaValueFor("abc", "string")).toEqual({ ok: true, value: "abc" });
  });
  it("marks what it cannot represent as unchecked and bad drafts as unparsable", () => {
    expect(schemaValueFor("9007199254740993", "integer")).toEqual({
      ok: false,
      reason: "unchecked",
    });
    expect(schemaValueFor("inf", "float")).toEqual({ ok: false, reason: "unchecked" });
    expect(schemaValueFor("0x1p-2", "float")).toEqual({ ok: false, reason: "unchecked" });
    expect(schemaValueFor("AAAA", "binary")).toEqual({ ok: false, reason: "unchecked" });
    expect(schemaValueFor("{", "json")).toEqual({ ok: false, reason: "unparsable" });
    expect(schemaValueFor("1.5", "integer")).toEqual({ ok: false, reason: "unparsable" });
  });
});

describe("fieldReadiness", () => {
  it("needs a value for a new alias and checks a provided string against the schema", () => {
    const schema = target({ name: { type: "string", minLength: 5 } });
    const draft = field({ alias: "name", content_type: "string", value: "" });
    expect(fieldReadiness(draft, schema, []).status).toBe("needs_value");
    const diff = structuredSchemaDifferences(target({ name: { type: "string" } }), schema);
    const short = fieldReadiness({ ...draft, value: "abc" }, schema, diff);
    expect(short.status).toBe("fails_schema");
    expect(short.issues).toEqual([
      { path: [], message: "must be at least 5 characters", cause: "constraint_changed" },
    ]);
    expect(short.summary).toBe("Value fails the target schema");
    expect(fieldReadiness({ ...draft, value: "abc" }, schema, []).issues[0].cause).toBe(
      "unchanged_rule",
    );
    expect(fieldReadiness({ ...draft, value: "abcdef" }, schema, []).status).toBe("ready");
  });
  it("attributes a missing required field to the schema change that introduced it", () => {
    const preserved = field({ fromAlias: "database", version: 2, versionText: "2", value: "{}" });
    const added = structuredSchemaDifferences(
      target({ database: { type: "object", properties: { port: { type: "integer" } } } }),
      objectTarget,
    );
    const readiness = fieldReadiness(preserved, objectTarget, added);
    expect(readiness.status).toBe("fails_schema");
    expect(readiness.issues).toEqual([
      { path: ["tls"], message: "is required", cause: "new_required" },
    ]);
    expect(readiness.summary).toBe("1 field fails the target schema");
    const flipped = structuredSchemaDifferences(
      target({
        database: {
          type: "object",
          properties: { tls: { type: "boolean" }, port: { type: "integer" } },
        },
      }),
      objectTarget,
    );
    expect(fieldReadiness(preserved, objectTarget, flipped).issues[0].cause).toBe("now_required");
  });
  it("distinguishes removed keys from never-declared ones and type changes", () => {
    const closed = target({
      database: {
        type: "object",
        additionalProperties: false,
        properties: { port: { type: "integer" } },
      },
    });
    const removed = structuredSchemaDifferences(
      target({
        database: {
          type: "object",
          additionalProperties: false,
          properties: { port: { type: "integer" }, legacy: { type: "string" } },
        },
      }),
      closed,
    );
    const value = field({ value: '{"port":80,"legacy":"x","mystery":1}' });
    const causes = fieldReadiness(value, closed, removed).issues.map((i) => [i.path, i.cause]);
    expect(causes).toEqual([
      [["legacy"], "no_longer_allowed"],
      [["mystery"], "undeclared"],
    ]);
    const retyped = structuredSchemaDifferences(
      target({ database: { type: "object", properties: { port: { type: "string" } } } }),
      closed,
    );
    expect(fieldReadiness(field({ value: '{"port":"80"}' }), closed, retyped).issues[0]).toEqual({
      path: ["port"],
      message: "must be integer, got string",
      cause: "type_changed",
    });
  });
  it("checks scalar content types against their alias schema", () => {
    const schema = target({
      limit: { type: "integer", minimum: 100 },
      ratio: { type: "integer" },
      flag: { type: "boolean" },
    });
    const limit = field({ alias: "limit", content_type: "integer", value: "20" });
    expect(fieldReadiness(limit, schema, []).issues[0].message).toBe("must be at least 100");
    expect(
      fieldReadiness(field({ alias: "ratio", content_type: "float", value: "1.5" }), schema, [])
        .issues[0].message,
    ).toBe("must be integer, got number");
    expect(
      fieldReadiness(field({ alias: "flag", content_type: "boolean", value: "t" }), schema, [])
        .status,
    ).toBe("ready");
    expect(fieldReadiness({ ...limit, value: "9007199254740993" }, schema, []).status).toBe(
      "unchecked",
    );
    expect(
      fieldReadiness(field({ alias: "limit", content_type: "binary", value: "AAAA" }), schema, [])
        .status,
    ).toBe("unchecked");
  });
  it("orders loading, version and draft states before schema checks", () => {
    expect(fieldReadiness(field({ loadError: "boom", value: "{}" }), objectTarget, []).status).toBe(
      "load_error",
    );
    expect(
      fieldReadiness(field({ loaded: false, version: 2, versionText: "2" }), objectTarget, [])
        .status,
    ).toBe("loading");
    expect(
      fieldReadiness(field({ versionText: "abc", value: "{}" }), objectTarget, []).status,
    ).toBe("needs_version");
    expect(fieldReadiness(field({ value: '{"tls":true}' }), objectTarget, [], false).status).toBe(
      "invalid_draft",
    );
    expect(fieldReadiness(field({ value: "{" }), objectTarget, []).status).toBe("invalid_draft");
    const secret = field({ alias: "token", kind: "secret", content_type: undefined });
    expect(fieldReadiness(secret, objectTarget, []).status).toBe("needs_version");
    expect(
      fieldReadiness({ ...secret, version: 3, versionText: "3" }, objectTarget, []).status,
    ).toBe("ready");
  });
  it("is unchecked when the target schema cannot describe the alias", () => {
    expect(
      fieldReadiness(field({ alias: "elsewhere", value: "{}" }), objectTarget, []).status,
    ).toBe("unchecked");
    expect(fieldReadiness(field({ value: "{}" }), "{not json", []).status).toBe("unchecked");
    expect(fieldReadiness(field({ value: "{}" }), undefined, []).status).toBe("unchecked");
  });
  it("caches results per field until an input changes", () => {
    const cached = createReadinessCache();
    const none: never[] = [];
    const draft = field({ value: "{}" });
    const first = cached(draft, objectTarget, none);
    expect(cached({ ...draft }, objectTarget, none)).toBe(first);
    expect(cached({ ...draft, value: '{"tls":true}' }, objectTarget, none)).not.toBe(first);
    const other = target({ database: { type: "object" } });
    expect(cached(draft, other, none).status).toBe("ready");
  });
});

describe("matchProblemPath", () => {
  const problem = (patch: Partial<ReleaseValidationError>): ReleaseValidationError => ({
    alias: "database",
    code: "schema_violation",
    schema_pointer: "",
    message: "",
    ...patch,
  });
  const issues: ReadinessIssue[] = [
    { path: ["tls"], message: "is required", cause: "new_required" },
    { path: ["auth", "client_id"], message: "is required", cause: "new_required" },
    { path: ["pool", "min"], message: "must be at least 1", cause: "constraint_changed" },
    { path: ["name"], message: "must be at least 3 characters", cause: "unchanged_rule" },
  ];
  it("prefers the server's instance pointer, unescaping segments", () => {
    expect(matchProblemPath(problem({ instance_pointer: "/database/pool/min" }), [])).toEqual([
      "pool",
      "min",
    ]);
    expect(matchProblemPath(problem({ instance_pointer: "/database/a~1b/c~0d" }), [])).toEqual([
      "a/b",
      "c~d",
    ]);
    expect(matchProblemPath(problem({ instance_pointer: "/database" }), [])).toEqual([]);
  });
  it("matches required problems by the quoted field name", () => {
    expect(
      matchProblemPath(
        problem({
          schema_pointer: "/required",
          message: 'Add the missing required field "client_id".',
        }),
        issues,
      ),
    ).toEqual(["auth", "client_id"]);
    expect(
      matchProblemPath(
        problem({
          schema_pointer: "/required",
          message: 'Add the missing required fields: "x", "tls".',
        }),
        issues,
      ),
    ).toEqual(["tls"]);
    expect(
      matchProblemPath(
        problem({ schema_pointer: "/required", message: 'Add the missing required field "zip".' }),
        issues,
      ),
    ).toEqual(["zip"]);
  });
  it("matches constraint keywords and uses legacy property pointers as hints", () => {
    expect(matchProblemPath(problem({ schema_pointer: "/minimum" }), issues)).toEqual([
      "pool",
      "min",
    ]);
    expect(matchProblemPath(problem({ schema_pointer: "/minLength" }), issues)).toEqual(["name"]);
    expect(
      matchProblemPath(
        problem({ schema_pointer: "/properties/database/properties/pool/minimum" }),
        issues,
      ),
    ).toEqual(["pool", "min"]);
    expect(
      matchProblemPath(
        problem({ schema_pointer: "/properties/database/properties/pool/format" }),
        [],
      ),
    ).toEqual(["pool"]);
    expect(matchProblemPath(problem({ schema_pointer: "/format" }), issues)).toBeNull();
    expect(matchProblemPath(problem({ schema_pointer: "" }), issues)).toBeNull();
  });
});
