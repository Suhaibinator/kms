import { describe, expect, it } from "vitest";

import { ReadCache } from "../src/cache.js";
import { Secret } from "../src/secret.js";

describe("ReadCache", () => {
  it("is disabled for a non-positive TTL", () => {
    const cache = new ReadCache(0);
    cache.setParameter("/prod/app/key", "value");
    cache.setSecret("/prod/app/secret", new Secret("secret"));
    expect(cache.enabled).toBe(false);
    expect(cache.getParameter("/prod/app/key")).toBeUndefined();
    expect(cache.getSecret("/prod/app/secret")).toBeUndefined();
    expect(cache.parameterSize).toBe(0);
    expect(cache.secretSize).toBe(0);
  });

  it("keys entries independently by bigint version and label", () => {
    const cache = new ReadCache(1_000);
    const path = "/prod/app/key";
    cache.setParameter(path, "current");
    cache.setParameter(path, "previous", { label: "previous" });
    cache.setParameter(path, "exact", { version: 9_007_199_254_740_993n });

    expect(cache.getParameter(path)).toBe("current");
    expect(cache.getParameter(path, { label: "previous" })).toBe("previous");
    expect(cache.getParam(path, 9_007_199_254_740_993n)).toBe("exact");
    expect(cache.parameterSize).toBe(3);
  });

  it("expires entries against a monotonic clock and cleans counters", () => {
    let now = 10;
    const cache = new ReadCache({ ttlMs: 5, now: () => now });
    cache.setParameter("/prod/app/key", "value");
    expect(cache.getParameter("/prod/app/key")).toBe("value");
    now = 15;
    expect(cache.getParameter("/prod/app/key")).toBeUndefined();
    expect(cache.parameterSize).toBe(0);
  });

  it("bounds parameter and secret entries independently", () => {
    const cache = new ReadCache({ ttlMs: 1_000, maxEntries: 8 });
    for (let i = 0; i < 1_000; i++) {
      cache.setParameter(`/prod/app/p${i}`, "value", { version: BigInt(i) });
      cache.setSecret(`/prod/app/s${i}`, new Secret(`secret-${i}`), { version: BigInt(i) });
    }
    expect(cache.parameterSize).toBeLessThanOrEqual(8);
    expect(cache.secretSize).toBeLessThanOrEqual(8);
  });

  it("invalidates all selectors for a path", () => {
    const cache = new ReadCache(1_000);
    cache.setParameter("/prod/app/p", "one", { version: 1n });
    cache.setParameter("/prod/app/p", "two", { version: 2n });
    cache.setSecret("/prod/app/s", new Secret("secret"));
    cache.invalidateParameter("/prod/app/p");
    cache.invalidateSecret("/prod/app/s");
    expect(cache.parameterSize).toBe(0);
    expect(cache.secretSize).toBe(0);
  });

  it("only invalidates secrets in requested namespaces", () => {
    const cache = new ReadCache(1_000);
    cache.setSecret("/prod/app/scoped", new Secret("one"));
    cache.setSecret("/prod/other/kept", new Secret("two"));
    cache.invalidateSecretsInNamespaces([{ env: "prod", app: "app" }]);
    expect(cache.getSecret("/prod/app/scoped")).toBeUndefined();
    expect(cache.getSecret("/prod/other/kept")?.text()).toBe("two");
  });

  it("only invalidates parameters in requested namespaces", () => {
    const cache = new ReadCache(1_000);
    cache.setParameter("/prod/app/scoped", "one");
    cache.setParameter("/prod/other/kept", "two");
    cache.invalidateParametersInNamespaces([{ env: "prod", app: "app" }]);
    expect(cache.getParameter("/prod/app/scoped")).toBeUndefined();
    expect(cache.getParameter("/prod/other/kept")).toBe("two");
  });

  it("returns independent Secret objects", () => {
    const cache = new ReadCache(1_000);
    const original = new Secret("secret", { version: 7n });
    cache.setSecret("/prod/app/s", original);
    const first = cache.getSecret("/prod/app/s");
    const second = cache.getSecret("/prod/app/s");
    expect(first).not.toBe(original);
    expect(second).not.toBe(first);
    expect(second?.text()).toBe("secret");
    expect(second?.version).toBe(7n);
  });

  it("fences every parameter selector when point invalidation races an in-flight read", () => {
    const cache = new ReadCache(1_000);
    const path = "/prod/app/parameter";
    const reads = [
      { generation: cache.beginParameterRead(path), version: 0n, label: "" },
      { generation: cache.beginParameterRead(path), version: 0n, label: "previous" },
      { generation: cache.beginParameterRead(path), version: 7n, label: "" },
    ];

    cache.invalidateParam(path);
    for (const read of reads) {
      if (read.generation === undefined) throw new Error("cache unexpectedly disabled");
      expect(
        cache.cacheParameterIfUnchanged(read.generation, read.version, read.label, "stale"),
      ).toBe(false);
      cache.endRead(read.generation);
    }

    expect(cache.parameterSize).toBe(0);
  });

  it("fences namespace-scoped parameter and secret reads without fencing later reads", () => {
    const cache = new ReadCache(1_000);
    const parameterPath = "/prod/app/parameter";
    const secretPath = "/prod/app/secret";
    const staleParameter = cache.beginParameterRead(parameterPath);
    const staleSecret = cache.beginSecretRead(secretPath);
    if (staleParameter === undefined || staleSecret === undefined) {
      throw new Error("cache unexpectedly disabled");
    }

    cache.invalidateParametersInNamespaces(["prod/app"]);
    cache.invalidateSecretsInNamespaces(["prod/app"]);

    const freshParameter = cache.beginParameterRead(parameterPath);
    const freshSecret = cache.beginSecretRead(secretPath);
    if (freshParameter === undefined || freshSecret === undefined) {
      throw new Error("cache unexpectedly disabled");
    }
    expect(cache.cacheParameterIfUnchanged(staleParameter, 0n, "", "stale")).toBe(false);
    expect(cache.cacheSecretIfUnchanged(staleSecret, 0n, "", new Secret("stale"))).toBe(false);
    expect(cache.cacheParameterIfUnchanged(freshParameter, 0n, "", "fresh")).toBe(true);
    expect(cache.cacheSecretIfUnchanged(freshSecret, 0n, "", new Secret("fresh"))).toBe(true);

    for (const generation of [staleParameter, staleSecret, freshParameter, freshSecret]) {
      cache.endRead(generation);
      cache.endRead(generation);
    }
    expect(cache.getParameter(parameterPath)).toBe("fresh");
    expect(cache.getSecret(secretPath)?.text()).toBe("fresh");
  });

  it("keeps invalidation generations isolated by resource kind and path", () => {
    const cache = new ReadCache(1_000);
    const parameter = cache.beginParameterRead("/prod/app/shared");
    const secret = cache.beginSecretRead("/prod/app/shared");
    const other = cache.beginParameterRead("/prod/app/other");
    if (parameter === undefined || secret === undefined || other === undefined) {
      throw new Error("cache unexpectedly disabled");
    }

    cache.invalidateParam("/prod/app/shared");

    expect(cache.cacheParameterIfUnchanged(parameter, 0n, "", "stale")).toBe(false);
    expect(cache.cacheSecretIfUnchanged(secret, 0n, "", new Secret("secret"))).toBe(true);
    expect(cache.cacheParameterIfUnchanged(other, 0n, "", "other")).toBe(true);
    for (const generation of [parameter, secret, other]) cache.endRead(generation);
  });

  it("fences active parameter and secret fills when cleared", () => {
    const cache = new ReadCache(1_000);
    const parameter = cache.beginParameterRead("/prod/app/parameter");
    const secret = cache.beginSecretRead("/prod/app/secret");
    if (parameter === undefined || secret === undefined) {
      throw new Error("cache unexpectedly disabled");
    }

    cache.clear();

    expect(cache.cacheParameterIfUnchanged(parameter, 0n, "", "stale")).toBe(false);
    expect(cache.cacheSecretIfUnchanged(secret, 0n, "", new Secret("stale"))).toBe(false);
    cache.endRead(parameter);
    cache.endRead(secret);
  });
});
