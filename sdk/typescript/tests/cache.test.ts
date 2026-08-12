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
});
