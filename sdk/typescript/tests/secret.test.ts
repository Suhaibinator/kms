import { inspect } from "node:util";

import { describe, expect, it } from "vitest";

import { REDACTED, Secret } from "../src/secret.js";

describe("Secret", () => {
  it("redacts every implicit rendering surface", () => {
    const plaintext = "never-print-this-value";
    const secret = new Secret(plaintext, {
      path: "/prod/api/key",
      version: 9_007_199_254_740_993n,
      contentType: "text/plain",
    });

    for (const rendered of [
      String(secret),
      `${secret}`,
      inspect(secret),
      JSON.stringify(secret),
      JSON.stringify({ secret }),
    ]) {
      expect(rendered).toContain(REDACTED);
      expect(rendered).not.toContain(plaintext);
    }
    expect(Object.keys(secret)).toEqual([]);
  });

  it("takes and returns defensive byte copies", () => {
    const input = Uint8Array.from([115, 101, 99, 114, 101, 116]);
    const secret = new Secret(input);
    input[0] = 88;
    expect(secret.text()).toBe("secret");

    const output = secret.bytes();
    output[1] = 88;
    expect(secret.text()).toBe("secret");
  });

  it("clones plaintext and preserves bigint metadata", () => {
    const original = new Secret("secret", {
      path: "/prod/api/key",
      version: 18_446_744_073_709_551_615n,
      contentType: "text/plain",
    });
    const clone = original.clone();

    expect(clone).not.toBe(original);
    expect(clone.text()).toBe("secret");
    expect(clone.path).toBe(original.path);
    expect(clone.version).toBe(18_446_744_073_709_551_615n);
    expect(clone.contentType).toBe("text/plain");
    expect(clone.isEmpty).toBe(false);
    expect(clone.length).toBe(6);
  });
});
