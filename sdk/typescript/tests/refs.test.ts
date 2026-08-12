import { describe, expect, it } from "vitest";

import { ConfigError, NoNamespaceError } from "../src/errors.js";
import {
  displayPath,
  normalizeVersionRef,
  parseNamespace,
  resolveRef,
  splitDisplayPath,
} from "../src/refs.js";

describe("namespace and resource references", () => {
  it("parses namespaces structurally without second-guessing the server", () => {
    expect(parseNamespace("Prod/App")).toEqual({ env: "Prod", app: "App" });
    for (const bad of ["noslash", "prod/", "/app", "prod/app/extra"]) {
      expect(() => parseNamespace(bad)).toThrow(ConfigError);
    }
  });

  it("preserves interior key slashes in an absolute display path", () => {
    const ref = splitDisplayPath("/prod/api/billing/stripe-key");
    expect(ref).toEqual({
      namespace: { env: "prod", app: "api" },
      key: "billing/stripe-key",
    });
    expect(displayPath(ref)).toBe("/prod/api/billing/stripe-key");
    expect(Object.isFrozen(ref)).toBe(true);
    expect(Object.isFrozen(ref.namespace)).toBe(true);
  });

  it.each(["prod/app/key", "/prod/app", "/prod//key", "/prod/app/"])(
    "rejects malformed display path %s",
    (path) => expect(() => splitDisplayPath(path)).toThrow(ConfigError),
  );

  it("resolves relative keys and requires a namespace", () => {
    expect(resolveRef("nested/key", "prod/api")).toEqual({
      namespace: { env: "prod", app: "api" },
      key: "nested/key",
    });
    expect(resolveRef("/other/app/key", undefined)).toEqual({
      namespace: { env: "other", app: "app" },
      key: "key",
    });
    expect(() => resolveRef("relative", undefined)).toThrow(NoNamespaceError);
  });
});

describe("version references", () => {
  it("uses bigint and gives exact versions precedence over labels", () => {
    expect(normalizeVersionRef()).toEqual({ version: 0n, label: "" });
    expect(normalizeVersionRef({ label: "previous" })).toEqual({
      version: 0n,
      label: "previous",
    });
    expect(normalizeVersionRef({ version: 9_007_199_254_740_993n, label: "ignored" })).toEqual({
      version: 9_007_199_254_740_993n,
      label: "",
    });
  });

  it("rejects negative and runtime-number versions", () => {
    expect(() => normalizeVersionRef({ version: -1n })).toThrow(ConfigError);
    expect(() => normalizeVersionRef({ version: 1 as unknown as bigint })).toThrow(ConfigError);
  });
});
