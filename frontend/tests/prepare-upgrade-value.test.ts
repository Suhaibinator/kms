import { describe, expect, it } from "vitest";
import { prepareUpgradeValue } from "@/lib/prepare-upgrade-value";

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
    expect(prepareUpgradeValue('[{"old":true},null]', schema)).toEqual({
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
    expect(prepareUpgradeValue("{}", schema)).toEqual({
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
