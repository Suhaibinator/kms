import { expect, it } from "vitest";
import { structuredSchemaDifferences } from "@/lib/schema-diff";
import {
  upgradeFieldChanges,
  removedUpgradeAliases,
  orderUpgradeChanges,
  type UpgradeDraftField,
} from "@/lib/upgrade-field-changes";
import type { ConfigurationReleaseEntry } from "@/lib/types";

const contract = [
  { alias: "database", kind: "parameter" as const, content_type: "json" },
  { alias: "token", kind: "secret" as const },
  { alias: "obsolete", kind: "parameter" as const, content_type: "string" },
];
const pin = {
  alias: "database",
  content_type: "json",
  metadata_json: "{}",
  parameter_digest: "digest",
  kind: "parameter",
  version: 2,
  ref: { namespace: { env: "dev", app: "app" }, key: "database" },
} as ConfigurationReleaseEntry;
const field: UpgradeDraftField = {
  ...contract[0],
  id: 1,
  fromAlias: "database",
  key: "database",
  version: 2,
  versionText: "2",
  loaded: false,
};

it("keeps pending and exact unchanged values unmarked, including large JSON integers", () => {
  expect(upgradeFieldChanges([field], contract, [pin], [], [])[0].changed).toBe(false);
  const value = '{"id":90071992547409931234}';
  expect(
    upgradeFieldChanges(
      [{ ...field, loaded: true, value, originalValue: value }],
      contract,
      [pin],
      [],
      [],
    )[0].changed,
  ).toBe(false);
  expect(
    upgradeFieldChanges(
      [{ ...field, loaded: true, value: value.replace("234", "235"), originalValue: value }],
      contract,
      [pin],
      [],
      [],
    )[0].labels,
  ).toEqual(["Value edited"]);
});
it("classifies renames, references, missing fields and removals without reading secret contents", () => {
  const renamed = { ...field, alias: "db", key: "different", version: 3 };
  const added: UpgradeDraftField = {
    id: 2,
    alias: "new",
    kind: "secret",
    key: "new",
    versionText: "",
  };
  const changes = upgradeFieldChanges([renamed, added], contract, [pin], [], []);
  expect(changes[0].labels).toEqual(["Renamed", "Reference changed"]);
  expect(changes[1].labels).toEqual(["Needs attention", "Added"]);
  expect(orderUpgradeChanges(changes)).toEqual([2, 1]);
  expect(removedUpgradeAliases([renamed], contract)).toEqual(["token", "obsolete"]);
  expect(
    removedUpgradeAliases([renamed], contract, [{ ...pin, alias: "old_release_only" }]),
  ).toContain("old_release_only");
});
it("maps dotted aliases and nested required constraints using structured paths", () => {
  const before = {
    properties: { "db.config": { type: "object", properties: { timeout: { type: "integer" } } } },
  };
  const after = {
    properties: {
      "db.config": {
        type: "object",
        required: ["timeout"],
        properties: { timeout: { type: "integer", minimum: 1 } },
      },
    },
  };
  const differences = structuredSchemaDifferences(JSON.stringify(before), JSON.stringify(after));
  expect(differences).toContainEqual({
    path: "db.config.timeout",
    segments: ["db.config", "timeout"],
    change: "changed",
  });
  const draft = { ...field, alias: "db.config", fromAlias: "db.config" };
  expect(upgradeFieldChanges([draft], [], [], differences, [])[0].paths).toHaveLength(2);
  const requiredOnly = structuredSchemaDifferences(
    '{"properties":{"x":{"type":"string"}}}',
    '{"properties":{"x":{"type":"string"}},"required":["x"]}',
  );
  expect(requiredOnly.some((d) => d.segments[0] === "x")).toBe(true);
});

it("does not attach root constraints to new aliases and marks content-type value writes", () => {
  const rootChange = { path: "(root)", segments: [], change: "changed" as const };
  expect(
    upgradeFieldChanges([{ ...field, fromAlias: undefined }], contract, [pin], [rootChange], [])[0]
      .paths,
  ).toEqual([]);
  expect(
    upgradeFieldChanges(
      [{ ...field, loaded: true, value: "1", originalValue: "1", originalContentType: "integer" }],
      contract,
      [pin],
      [],
      [],
    )[0].labels,
  ).toContain("Value edited");
});

it("exposes nested array and tuple property changes for alias search", () => {
  const before = {
    properties: {
      workers: {
        type: "array",
        items: { type: "object", properties: { timeout: { type: "integer" } } },
      },
    },
  };
  const after = {
    properties: {
      workers: {
        type: "array",
        items: { type: "object", properties: { timeout: { type: "integer", minimum: 1 } } },
      },
    },
  };
  expect(structuredSchemaDifferences(JSON.stringify(before), JSON.stringify(after))).toContainEqual(
    { path: "workers.[].timeout", segments: ["workers", "[]", "timeout"], change: "changed" },
  );
});
