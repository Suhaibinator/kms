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
