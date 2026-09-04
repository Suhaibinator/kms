import { format, inspect } from "node:util";

import { describe, expect, it } from "vitest";

import { newSecret, REDACTED, Secret } from "../src/secret.js";

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
      bindKey: "operator-binding-key-never-render",
    });
    const clone = original.clone();

    expect(clone).not.toBe(original);
    expect(clone.text()).toBe("secret");
    expect(clone.path).toBe(original.path);
    expect(clone.version).toBe(18_446_744_073_709_551_615n);
    expect(clone.contentType).toBe("text/plain");
    expect(clone.bindKey).toBe("operator-binding-key-never-render");
    expect(clone.isEmpty).toBe(false);
    expect(clone.length).toBe(6);
  });

  it("preserves a declaration binding key without exposing it through serialization", () => {
    const bindingKey = "declaration-binding-key-never-render";
    const declaration = new Secret("", { bindKey: bindingKey });
    const clone = declaration.clone();

    expect(declaration.isEmpty).toBe(true);
    expect(declaration.bindKey).toBe(bindingKey);
    expect(clone.bindKey).toBe(bindingKey);
    expect(Object.keys(declaration)).toEqual([]);
    expect(Reflect.ownKeys(declaration)).toEqual([]);
    expect({ ...declaration }).toEqual({});
    for (const rendered of [
      String(declaration),
      `${declaration}`,
      declaration.toString(),
      declaration.valueOf(),
      inspect(declaration),
      format("%s", declaration),
      format("%o", declaration),
      format("%j", declaration),
      JSON.stringify(declaration),
      JSON.stringify({ declaration }),
    ]) {
      expect(rendered).toContain(REDACTED);
      expect(rendered).not.toContain(bindingKey);
    }
  });

  it("rejects values outside uint64 version range", () => {
    expect(() => new Secret("secret", { version: -1n })).toThrow(TypeError);
    expect(() => new Secret("secret", { version: 18_446_744_073_709_551_616n })).toThrow(TypeError);
    expect(() => new Secret("secret", { version: 1 as unknown as bigint })).toThrow(TypeError);
  });

  it("rejects a non-string declaration binding key without rendering it", () => {
    const canary = "non-string-binding-key-canary";
    const hostile = Object.freeze({
      toString() {
        throw new Error(canary);
      },
    });
    let error: unknown;
    try {
      new Secret("", { bindKey: hostile as unknown as string });
    } catch (caught) {
      error = caught;
    }
    expect(error).toBeInstanceOf(TypeError);
    expect(String(error)).toBe("TypeError: secret bindKey must be a string");
    expect(String(error)).not.toContain(canary);
  });

  it("newSecret wraps plaintext with defensive copies and exact metadata", () => {
    const input = Uint8Array.from([115, 101, 99, 114, 101, 116]);
    const secret = newSecret(input, {
      path: "/prod/api/helper",
      version: 9_007_199_254_740_993n,
      contentType: "application/octet-stream",
    });
    input.fill(0);

    expect(secret).toBeInstanceOf(Secret);
    expect(secret.text()).toBe("secret");
    expect(secret).toMatchObject({
      path: "/prod/api/helper",
      version: 9_007_199_254_740_993n,
      contentType: "application/octet-stream",
    });
    expect(JSON.stringify(secret)).toBe(`"${REDACTED}"`);
  });
});
