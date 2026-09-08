import { describe, expect, it } from "vitest";
import { prepareUpgradeValue } from "@/lib/prepare-upgrade-value";

const migrationSchema = {
  type: "object",
  additionalProperties: false,
  required: ["urls"],
  properties: { urls: { type: "array", items: { type: "string" } }, count: { type: "integer" } },
};

it("keeps null defaults from nullable wrappers and does not prepare unchecked contains defaults", () => {
  const result = prepareUpgradeValue("{}", {
    type: "object",
    required: ["urls"],
    properties: {
      urls: {
        anyOf: [{ type: "array", items: { type: "string" } }, { type: "null" }],
        default: null,
      },
      requiredMatch: { type: "array", contains: { const: "required" }, default: [] },
    },
  });
  expect(result.value).toBe('{"urls":null}');
  expect(result.appliedDefaults).toEqual(["urls"]);
  expect(result.initializedLists).toEqual([]);
});

it("suggests a singular-to-plural conversion without removing the source or overwriting it with []", () => {
  const raw = '{ "url": "https:\\/\\/example.com/callback", "count": 9007199254740993 }';
  const prepared = prepareUpgradeValue(raw, migrationSchema);
  expect(prepared.value).toBe(raw);
  expect(prepared.removed).toEqual([]);
  expect(prepared.initializedLists).toEqual([]);
  expect(prepared.suggestions).toMatchObject([
    { from: "url", to: "urls", value: '["https:\\/\\/example.com/callback"]' },
  ]);
  const accepted = prepareUpgradeValue(raw, migrationSchema, undefined, [
    prepared.suggestions[0].id,
  ]);
  expect(accepted.value).toBe(
    '{"count":9007199254740993,"urls":["https:\\/\\/example.com/callback"]}',
  );
  expect(accepted.migrations).toHaveLength(1);
  expect(accepted.removed).toEqual(["url"]);
});

it("applies declared sibling mappings in a prepared draft and preserves an existing destination", () => {
  const schema = {
    ...migrationSchema,
    properties: {
      ...migrationSchema.properties,
      urls: { ...migrationSchema.properties.urls, "x-kms-migrate-from": "callback" },
    },
  };
  expect(prepareUpgradeValue('{"callback":"https://example.com"}', schema).value).toBe(
    '{"urls":["https://example.com"]}',
  );
  expect(prepareUpgradeValue('{"callback":"old","urls":["new"]}', schema).value).toBe(
    '{"urls":["new"]}',
  );
});

it("requires a separate choice for an empty old string, even with a declared mapping", () => {
  const schema = {
    ...migrationSchema,
    properties: { urls: { ...migrationSchema.properties.urls, "x-kms-migrate-from": "url" } },
  };
  const prepared = prepareUpgradeValue('{"url":""}', schema);
  expect(prepared.value).toBe('{"url":""}');
  expect(prepared.suggestions[0]).toMatchObject({
    value: "[]",
    reason: expect.stringContaining("empty string"),
  });
  expect(
    prepareUpgradeValue('{"url":""}', schema, undefined, [prepared.suggestions[0].id]).value,
  ).toBe('{"urls":[]}');
});

it("preserves incompatible sources for manual editing and leaves nonempty/complex array requirements unresolved", () => {
  const schema = {
    ...migrationSchema,
    properties: { urls: { ...migrationSchema.properties.urls, minItems: 2 } },
  };
  expect(prepareUpgradeValue("{}", schema).value).toBe("{}");
  for (const source of ['{"url":""}', '{"url":"one"}', '{"url":null}', '{"url":123}']) {
    const result = prepareUpgradeValue(source, schema);
    expect(result.value).toBe(source);
    expect(result.suggestions[0].value).toBeUndefined();
  }
  const contains = {
    ...migrationSchema,
    properties: {
      urls: {
        ...migrationSchema.properties.urls,
        contains: { const: "special" },
        "x-kms-migrate-from": "url",
      },
    },
  };
  expect(prepareUpgradeValue("{}", contains).value).toBe("{}");
  expect(prepareUpgradeValue('{"url":"other"}', contains).value).toBe('{"url":"other"}');
});

it("reports defaults and initializations separately, while preserving explicit empty, omitted and null values", () => {
  const schema = {
    type: "object",
    required: ["newList"],
    properties: {
      newList: { type: ["array", "null"], items: { type: "string" } },
      existing: { type: "array", default: ["default"] },
      nullable: { type: ["array", "null"], default: [] },
      omitted: { type: "array" },
      enabled: { type: "boolean", default: false },
      invalidDefault: { type: "array", minItems: 1, default: [] },
    },
  };
  const result = prepareUpgradeValue('{"existing":[],"nullable":null}', schema);
  expect(JSON.parse(result.value)).toEqual({
    existing: [],
    nullable: null,
    newList: [],
    enabled: false,
  });
  expect(result.initializedLists).toEqual(["newList"]);
  expect(result.appliedDefaults).toEqual(["enabled"]);
});

it("scopes mappings to siblings in nested objects and array items", () => {
  const source = '[{"url":"a"},{"url":"b"}]';
  const result = prepareUpgradeValue(source, { type: "array", items: migrationSchema });
  expect(result.value).toBe(source);
  const accepted = prepareUpgradeValue(
    source,
    { type: "array", items: migrationSchema },
    undefined,
    [result.suggestions[1].id],
  );
  expect(accepted.value).toBe('[{"url":"a"},{"urls":["b"]}]');
});

describe("prepareUpgradeValue", () => {
  it("adds nested required lists/defaults, removes forbidden fields, and preserves exact numbers", () => {
    const prepared = prepareUpgradeValue(
      '{"id":9007199254740993,"config":{"old":"value","keep":1e400}}',
      {
        type: "object",
        properties: {
          id: { type: "integer" },
          config: {
            type: "object",
            additionalProperties: false,
            required: ["urls", "name"],
            properties: {
              keep: { type: "number" },
              urls: { type: "array", items: { type: "string" } },
              name: { type: "string" },
              enabled: { type: "boolean", default: false },
            },
          },
        },
      },
    );
    expect(prepared.value).toBe(
      '{"id":9007199254740993,"config":{"keep":1e400,"urls":[],"enabled":false}}',
    );
    expect(prepared.added).toEqual(["config.urls", "config.enabled"]);
    expect(prepared.removed).toEqual(["config.old"]);
  });
  it("preserves allowed extras and does not guess through complex schemas", () => {
    const raw = '{ "old": 1.0 }';
    expect(
      prepareUpgradeValue(raw, { type: "object", properties: { name: { type: "string" } } }).value,
    ).toBe(raw);
    expect(
      prepareUpgradeValue(raw, { type: "object", additionalProperties: false, oneOf: [{}, {}] })
        .value,
    ).toBe(raw);
    expect(
      prepareUpgradeValue(raw, {
        type: "object",
        additionalProperties: false,
        patternProperties: { ".*": {} },
      }).value,
    ).toBe(raw);
  });
  it("handles arrays and nullable objects and leaves invalid JSON untouched", () => {
    const schema = {
      type: "array",
      items: {
        anyOf: [
          {
            type: "object",
            additionalProperties: false,
            properties: { urls: { type: "array" } },
            required: ["urls"],
          },
          { type: "null" },
        ],
      },
    };
    expect(prepareUpgradeValue('[{"old":true},null]', schema)).toMatchObject({
      value: '[{"urls":[]},null]',
      added: ["0.urls"],
      removed: ["0.old"],
    });
    expect(prepareUpgradeValue("{", {}).value).toBe("{");
  });
});

it("uses tuple prefix schemas before applying the trailing item schema", () => {
  const prepared = prepareUpgradeValue('[{"name":"db"},{"old":1,"port":8080}]', {
    type: "array",
    prefixItems: [
      { type: "object", additionalProperties: false, properties: { name: { type: "string" } } },
    ],
    items: {
      type: "object",
      additionalProperties: false,
      properties: { port: { type: "integer" } },
    },
  });
  expect(prepared.value).toBe('[{"name":"db"},{"port":8080}]');
  expect(prepared.removed).toEqual(["1.old"]);
});

it("skips parsed numeric defaults whose original precision cannot be established", () => {
  for (const token of ["0.123456789123456789", "1e-400", "9007199254740993", "1e400"]) {
    const schema = JSON.parse(
      `{"type":"object","properties":{"number":{"default":${token}},"nested":{"default":{"numbers":[${token}]}},"safe":{"default":"retained"}}}`,
    );
    expect(prepareUpgradeValue("{}", schema)).toMatchObject({
      value: '{"safe":"retained"}',
      added: ["safe"],
      removed: [],
    });
  }
});

it("copies exact numeric default tokens from the registered schema, including nested and nullable defaults", () => {
  const source = `{"type":"object","properties":{"config":{"type":"object","properties":{
    "precise":{"default":0.123456789123456789},
    "underflow":{"default":1e-400},
    "nested":{"default":{"numbers":[9007199254740993,1e400,42]}},
    "nullable":{"anyOf":[{"type":"number","default":1.00000000000000001},{"type":"null"}]},
    "wrapped":{"default":2.00000000000000001,"anyOf":[{"type":"number"},{"type":"null"}]},
    "integer":{"default":5}
  }}}}`;
  const prepared = prepareUpgradeValue("{}", source, "config");
  expect(prepared.value).toBe(
    '{"precise":0.123456789123456789,"underflow":1e-400,"nested":{"numbers":[9007199254740993,1e400,42]},"wrapped":2.00000000000000001,"integer":5}',
  );
  expect(
    prepareUpgradeValue(
      "{}",
      '{"type":"object","required":["nullable"],"properties":{"nullable":{"anyOf":[{"type":"number","default":1.00000000000000001},{"type":"null"}]}}}',
    ).value,
  ).toBe('{"nullable":1.00000000000000001}');
});
