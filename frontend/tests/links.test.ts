import { describe, expect, it } from "vitest";
import { links } from "@/lib/links";

const ns = { env: "prod", app: "billing api" };
const ref = { ...ns, key: "db/password" };

describe("links", () => {
  it("applications", () => {
    expect(links.applications()).toBe("/applications");
    expect(links.application("payments-api")).toBe("/applications?app=payments-api");
    expect(links.application("a b")).toBe("/applications?app=a%20b");
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
  });
});
