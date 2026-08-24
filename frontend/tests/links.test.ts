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
  });

  it("namespaces", () => {
    expect(links.namespaces()).toBe("/namespaces");
  });

  it("secrets list", () => {
    expect(links.secrets()).toBe("/secrets");
    expect(links.secrets(ns)).toBe("/secrets?env=prod&app=billing%20api");
    expect(links.secrets(ns, "db/")).toBe("/secrets?env=prod&app=billing%20api&key_prefix=db%2F");
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
    expect(links.parameters(ns, "db/")).toBe(
      "/parameters?env=prod&app=billing%20api&key_prefix=db%2F",
    );
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
