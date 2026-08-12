import { describe, expect, it } from "vitest";
import {
  createPolicyPublisher,
  definePublicProjection,
  formatRevision,
  freezePublicJson,
  parseRevision,
} from "../src/publishing.js";
import { Secret } from "../src/secret.js";

describe("public configuration publishing", () => {
  it("round-trips the entire uint64 range as decimal strings", () => {
    const max = (1n << 64n) - 1n;
    expect(parseRevision(formatRevision(max))).toBe(max);
    for (const invalid of ["", "01", "-1", "+1", "1.0", "18446744073709551616"]) {
      expect(() => parseRevision(invalid)).toThrow();
    }
  });

  it("publishes only declared allowlist selectors and defensively freezes results", () => {
    const privatePolicy = {
      minLength: 14,
      internalEndpoint: "private",
      credentials: new Secret("do-not-publish"),
    };
    const projection = definePublicProjection<typeof privatePolicy>()({
      minLength: (value) => value.minLength,
    });
    const publicPolicy = projection.project(privatePolicy);
    expect(publicPolicy).toEqual({ minLength: 14 });
    expect(publicPolicy).not.toHaveProperty("credentials");
    expect(Object.isFrozen(publicPolicy)).toBe(true);
  });

  it("reserves the publisher-owned revision field", () => {
    expect(() =>
      definePublicProjection<{ internalRevision: string }>()({
        revision: (value) => value.internalRevision,
      }),
    ).toThrow(/reserved/);
  });

  it("rejects non-JSON, secret, getter, cycle, and prototype-bearing values", () => {
    expect(() => freezePublicJson(new Secret("hidden"))).toThrow();
    expect(() => freezePublicJson({ version: 1n })).toThrow();
    expect(() => freezePublicJson({ date: new Date() })).toThrow();
    const cycle: Record<string, unknown> = {};
    cycle.self = cycle;
    expect(() => freezePublicJson(cycle)).toThrow();
    const getter = Object.defineProperty({}, "value", { enumerable: true, get: () => 1 });
    expect(() => freezePublicJson(getter)).toThrow();
  });

  it("captures the source once for reads and authoritative validation", () => {
    let calls = 0;
    let generation = { revision: 9n, value: { minLength: 12 } };
    const source = {
      current: () => {
        calls++;
        return generation;
      },
    };
    const projection = definePublicProjection<
      { minLength: number },
      {
        minLength: (value: Readonly<{ minLength: number }>) => number;
      }
    >({ minLength: (value) => value.minLength });
    const publisher = createPolicyPublisher({
      source,
      projection,
      validate: (policy, password: string) =>
        password.length >= policy.minLength
          ? { valid: true as const }
          : { valid: false as const, errors: ["too_short"] },
    });

    expect(publisher.readWire()).toEqual({ revision: "9", config: { minLength: 12 } });
    expect(calls).toBe(1);
    generation = { revision: 10n, value: { minLength: 14 } };
    expect(publisher.validate("9", "long-enough-at-old-policy")).toEqual({
      status: "policy_changed",
      current: { revision: "10", config: { minLength: 14 } },
    });
    expect(calls).toBe(2);
    expect(publisher.validate("10", "short")).toEqual({
      status: "validation_failed",
      revision: "10",
      errors: ["too_short"],
    });
  });
});
