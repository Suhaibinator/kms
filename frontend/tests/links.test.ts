import { describe, expect, it } from "vitest";
import { links } from "@/lib/links";

const ns = { env: "prod", app: "billing api" };
const ref = { ...ns, key: "db/password" };

describe("links", () => {
  it("overview", () => {
    expect(links.overview()).toBe("/");
  });

  it("applications", () => {
    expect(links.applications()).toBe("/applications");
    expect(links.application("payments-api")).toBe("/applications?app=payments-api");
    expect(links.application("a b")).toBe("/applications?app=a%20b");
  });

  it("application deep links keep the app, env, ship, tab, rollback order", () => {
    expect(links.application("gradethis", {})).toBe("/applications?app=gradethis");
    expect(links.application("gradethis", { env: "prod" })).toBe(
      "/applications?app=gradethis&env=prod",
    );
    expect(links.application("gradethis", { env: "prod-eu", ship: "rate_limits" })).toBe(
      "/applications?app=gradethis&env=prod-eu&ship=rate_limits",
    );
    expect(links.application("gradethis", { ship: true })).toBe(
      "/applications?app=gradethis&ship=1",
    );
    expect(links.application("gradethis", { ship: false })).toBe("/applications?app=gradethis");
    expect(links.application("gradethis", { ship: "a b" })).toBe(
      "/applications?app=gradethis&ship=a%20b",
    );
    expect(links.application("gradethis", { tab: "matrix" })).toBe(
      "/applications?app=gradethis&tab=matrix",
    );
    expect(links.application("gradethis", { env: "prod", rollback: true })).toBe(
      "/applications?app=gradethis&env=prod&rollback=1",
    );
    expect(links.application("a b", { env: "dev", ship: "x", tab: "matrix", rollback: true })).toBe(
      "/applications?app=a%20b&env=dev&ship=x&tab=matrix&rollback=1",
    );
  });

  it("environment deep links keep the app, env, schema_version, ship, rollback order", () => {
    expect(links.environment("gradethis", "prod")).toBe(
      "/applications/environment?app=gradethis&env=prod",
    );
    expect(links.environment("gradethis", "prod", { schemaVersion: 1 })).toBe(
      "/applications/environment?app=gradethis&env=prod&schema_version=1",
    );
    expect(links.environment("gradethis", "prod", { ship: "rate_limits" })).toBe(
      "/applications/environment?app=gradethis&env=prod&ship=rate_limits",
    );
    expect(links.environment("gradethis", "prod", { ship: true })).toBe(
      "/applications/environment?app=gradethis&env=prod&ship=1",
    );
    expect(links.environment("gradethis", "prod", { ship: false })).toBe(
      "/applications/environment?app=gradethis&env=prod",
    );
    expect(links.environment("gradethis", "prod", { rollback: true })).toBe(
      "/applications/environment?app=gradethis&env=prod&rollback=1",
    );
    expect(
      links.environment("a b", "prod eu", {
        schemaVersion: 2,
        ship: "a b",
        rollback: true,
      }),
    ).toBe(
      "/applications/environment?app=a%20b&env=prod%20eu&schema_version=2&ship=a%20b&rollback=1",
    );
  });

  it("identities", () => {
    expect(links.identities()).toBe("/identities");
    expect(links.identities({})).toBe("/identities");
    expect(links.identities({ env: "prod", app: "billing api" })).toBe(
      "/identities?env=prod&app=billing%20api",
    );
    expect(links.identities({ env: "prod", app: "gradethis", new: true })).toBe(
      "/identities?env=prod&app=gradethis&new=1",
    );
    expect(links.identities({ new: true })).toBe("/identities?new=1");
    expect(links.identities({ name: "billing api" })).toBe("/identities?name=billing%20api");
  });

  it("static pages", () => {
    expect(links.subscribers()).toBe("/subscribers");
    expect(links.health()).toBe("/health");
    expect(links.audit()).toBe("/audit");
  });

  it("audit resources resolve by resource_type", () => {
    const base = { resource_env: "prod", resource_app: "billing api", resource_key: "db/password" };
    expect(links.auditResource({ ...base, resource_type: "secret" })).toBe(links.secretDetail(ref));
    expect(links.auditResource({ ...base, resource_type: "parameter" })).toBe(
      links.parameterDetail(ref),
    );
    expect(
      links.auditResource({ ...base, resource_type: "configuration_release", resource_key: "run" }),
    ).toBe("/releases?app=billing%20api&env=prod&name=run");
    // A namespace-only resource lands on the application focused on that environment.
    expect(
      links.auditResource({ resource_type: "namespace", resource_env: "prod", resource_app: "x" }),
    ).toBe("/applications?app=x&env=prod");
    // Unknown shapes render as text.
    expect(links.auditResource({ ...base, resource_type: "policy" })).toBeNull();
    expect(links.auditResource({ resource_type: "identity", resource_key: "root" })).toBeNull();
  });

  it("namespaces", () => {
    expect(links.namespaces()).toBe("/namespaces");
  });

  it("secrets list", () => {
    expect(links.secrets()).toBe("/secrets");
    expect(links.secrets(ns)).toBe("/secrets?env=prod&app=billing%20api");
    expect(links.secrets(ns, "db/")).toBe("/secrets?env=prod&app=billing%20api&q=db%2F");
  });

  it("secret detail", () => {
    expect(links.secretDetail(ref)).toBe(
      "/secrets/detail?env=prod&app=billing%20api&key=db%2Fpassword",
    );
  });

  it("new secret", () => {
    expect(links.newSecret()).toBe("/secrets/new");
    expect(links.newSecret(ns)).toBe("/secrets/new?env=prod&app=billing%20api");
    expect(links.newSecret(ns, "db/password")).toBe(
      "/secrets/new?env=prod&app=billing%20api&key=db%2Fpassword",
    );
  });

  it("parameters list", () => {
    expect(links.parameters()).toBe("/parameters");
    expect(links.parameters(ns)).toBe("/parameters?env=prod&app=billing%20api");
    expect(links.parameters(ns, "db/")).toBe("/parameters?env=prod&app=billing%20api&q=db%2F");
  });

  it("parameter detail", () => {
    expect(links.parameterDetail(ref)).toBe(
      "/parameters/detail?env=prod&app=billing%20api&key=db%2Fpassword",
    );
  });

  it("releases", () => {
    expect(links.releases()).toBe("/releases");
    expect(links.releases({})).toBe("/releases");
    expect(links.releases({ app: "billing api", env: "prod" })).toBe(
      "/releases?app=billing%20api&env=prod",
    );
    expect(links.releases({ app: "billing", env: "prod", name: "run time", tab: "schemas" })).toBe(
      "/releases?app=billing&env=prod&name=run%20time&tab=schemas",
    );
    expect(links.releases({ tab: "schemas" })).toBe("/releases?tab=schemas");
    expect(
      links.releases({ app: "gradethis", env: "prod", name: "runtime", release: "runtime@12" }),
    ).toBe("/releases?app=gradethis&env=prod&name=runtime&release=runtime%4012");
    expect(links.releases({ release: "run time@1" })).toBe("/releases?release=run%20time%401");
  });
});

it("release audit links retain the exact recorded track and release even for old or unknown registrations", () => {
  const base = {
    resource_type: "configuration_release",
    resource_env: "prod",
    resource_app: "billing",
    resource_key: "runtime",
    resource_version: 1,
  };
  for (const schemaVersion of [0, 1, 2, 999]) {
    const url = new URL(
      links.auditResource({
        ...base,
        metadata_json: JSON.stringify({ schema_version: String(schemaVersion) }),
      })!,
      "https://kms.example",
    );
    expect(url.searchParams.get("schema_version")).toBe(String(schemaVersion));
    expect(url.searchParams.get("release")).toBe(`runtime@${schemaVersion}:1`);
  }
});

it("release audits with unknown identity never invent a schema or ambiguous workspace selection", () => {
  const base = {
    resource_type: "configuration_release",
    resource_env: "prod",
    resource_app: "billing",
    resource_key: "runtime",
    resource_version: 1,
  };
  for (const metadata_json of [
    undefined,
    "",
    "{",
    "null",
    "[]",
    "{}",
    ...[null, 0, 1, "", "-1", "1.5", "1e2", "9007199254740992"].map((schema_version) =>
      JSON.stringify({ schema_version }),
    ),
  ]) {
    expect(links.auditResource({ ...base, metadata_json })).toBe(
      "/releases?app=billing&env=prod&name=runtime",
    );
  }
  for (const resource_version of [0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
    expect(
      links.auditResource({ ...base, resource_version, metadata_json: '{"schema_version":"0"}' }),
    ).toBe("/releases?app=billing&env=prod&name=runtime&schema_version=0");
  }
});

it("releaseCompare emits the ten params in a fixed order and accepts track labels", () => {
  expect(
    links.releaseCompare({
      app: "gradethis",
      env: "prod",
      name: "runtime",
      schemaVersion: 1,
      from: 7,
      to: 9,
    }),
  ).toBe("/releases/compare?app=gradethis&env=prod&name=runtime&schema_version=1&from=7&to=9");
  expect(
    links.releaseCompare({
      app: "billing api",
      env: "prod",
      name: "run time",
      schemaVersion: 0,
      from: "previous",
      to: "current",
      toEnv: "staging",
      toSchemaVersion: 2,
      view: "all",
      q: "rate limit",
    }),
  ).toBe(
    "/releases/compare?app=billing%20api&env=prod&name=run%20time&schema_version=0&from=previous&to=current&to_env=staging&to_schema_version=2&view=all&q=rate%20limit",
  );
  // The default view is changed-only, so it is never written; an empty q is dropped.
  expect(
    links.releaseCompare({
      app: "a",
      env: "e",
      name: "n",
      schemaVersion: 3,
      from: 1,
      to: 2,
      q: "",
    }),
  ).toBe("/releases/compare?app=a&env=e&name=n&schema_version=3&from=1&to=2");
});
