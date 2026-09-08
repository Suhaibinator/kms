import { describe, expect, it } from "vitest";
import { parseReleaseDefinition, releaseDefinitionError } from "@/components/releases/utils";
import { parseSchemaVersion } from "@/lib/schema";

const definition = {
  name: "runtime",
  entries: [
    {
      alias: "settings",
      kind: "parameter",
      ref: { namespace: { env: "dev", app: "app" }, key: "settings" },
    },
  ],
};

describe("schema selectors", () => {
  it.each(["-1", "1.5", "1e3", "9007199254740993", "NaN", "Infinity", " 1"])(
    "rejects unsafe URL selector %s",
    (value) => {
      expect(parseSchemaVersion(value)).toBeUndefined();
    },
  );
  it("preserves explicit zero", () => {
    expect(parseSchemaVersion("0")).toBe(0);
    expect(
      parseReleaseDefinition(JSON.stringify({ ...definition, schema_version: 0 })).schema_version,
    ).toBe(0);
  });
  it.each([-1, 1.5, Number.MAX_SAFE_INTEGER + 1, "1", null, true])(
    "rejects invalid JSON schema selector %s",
    (value) => {
      const json = JSON.stringify({ ...definition, schema_version: value });
      expect(releaseDefinitionError(json)).toMatch(/safe integer/);
      expect(() => parseReleaseDefinition(json)).toThrow(/safe integer/);
    },
  );
});
