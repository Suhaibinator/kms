import { inspect } from "node:util";

import { describe, expect, it } from "vitest";
import { cloneConfig, containsSecret } from "../src/configstore/clone.js";
import { codecs, decodeGroup, field, group } from "../src/configstore/codecs.js";
import { createManifestValidator, validateContract } from "../src/configstore/contract.js";
import {
  AppliedReport,
  CandidateError,
  CandidateRejectionReport,
  DefaultMismatchReport,
  rejectDecode,
} from "../src/configstore/errors.js";
import { immutableSnapshot, ReleaseIdentity } from "../src/configstore/snapshot.js";
import { ReleaseEntryMetadata, ReleaseManifest, ReleaseSecret } from "../src/releases/types.js";
import { Secret } from "../src/secret.js";

describe("configstore defensive values and reports", () => {
  it("deep clones mutable graphs, cycles, aliases, and secret backing bytes", () => {
    const original: {
      pointer: { value: number };
      bytes: Uint8Array;
      map: Map<string, number[]>;
      secret: Secret;
      self?: unknown;
    } = {
      pointer: { value: 7 },
      bytes: Uint8Array.from([1, 2, 3]),
      map: new Map([["numbers", [4, 5]]]),
      secret: new Secret("secret-canary"),
    };
    original.self = original;

    const cloned = cloneConfig(original);
    cloned.pointer.value = 99;
    cloned.bytes[0] = 9;
    const numbers = cloned.map.get("numbers");
    if (numbers) numbers[0] = 8;
    const bytes = cloned.secret.bytes();
    bytes[0] = 88;

    expect(original.pointer.value).toBe(7);
    expect(original.bytes).toEqual(Uint8Array.from([1, 2, 3]));
    expect(original.map.get("numbers")).toEqual([4, 5]);
    expect(original.secret.text()).toBe("secret-canary");
    expect(cloned.self).toBe(cloned);
  });

  it("preserves repeated Secret and ReleaseSecret aliases in the detached clone", () => {
    const secret = new Secret(Uint8Array.of(1, 2, 3));
    const entry = new ReleaseEntryMetadata({
      alias: "token",
      kind: "secret",
      path: "tokens/service",
      version: 7n,
    });
    const releaseSecret = new ReleaseSecret(Uint8Array.of(4, 5, 6), entry);
    const source = {
      secret,
      nestedSecret: { value: secret },
      releaseSecret,
      releaseSecretList: [releaseSecret],
    };

    const cloned = cloneConfig(source);

    expect(cloned.secret).not.toBe(secret);
    expect(cloned.secret).toBe(cloned.nestedSecret.value);
    expect(cloned.releaseSecret).not.toBe(releaseSecret);
    expect(cloned.releaseSecret).toBe(cloned.releaseSecretList[0]);
    const secretBytes = cloned.secret.bytes();
    const releaseBytes = cloned.releaseSecret.bytes();
    secretBytes.fill(0);
    releaseBytes.fill(0);
    expect(secret.bytes()).toEqual(Uint8Array.of(1, 2, 3));
    expect(releaseSecret.bytes()).toEqual(Uint8Array.of(4, 5, 6));
    expect(cloned.secret.bytes()).toEqual(Uint8Array.of(1, 2, 3));
    expect(cloned.releaseSecret.bytes()).toEqual(Uint8Array.of(4, 5, 6));
  });

  it("clones array descriptors without invoking accessors", () => {
    const marker = Symbol("marker");
    const sparse = new Array<{ value: number } | undefined>(3) as Array<
      { value: number } | undefined
    > & { [marker]: { deep: number } };
    sparse[1] = { value: 4 };
    Object.defineProperty(sparse, marker, {
      configurable: true,
      value: { deep: 5 },
      writable: true,
    });

    const cloned = cloneConfig(sparse);
    expect(0 in cloned).toBe(false);
    expect(1 in cloned).toBe(true);
    const clonedItem = cloned[1];
    if (clonedItem) clonedItem.value = 9;
    cloned[marker].deep = 10;
    expect(sparse[1]).toEqual({ value: 4 });
    expect(sparse[marker]).toEqual({ deep: 5 });

    let getterCalls = 0;
    const accessorBacked: string[] = [];
    Object.defineProperty(accessorBacked, 0, {
      configurable: true,
      get: () => {
        getterCalls += 1;
        return "GETTER-CANARY";
      },
    });
    accessorBacked.length = 1;
    expect(() => cloneConfig(accessorBacked)).toThrow(/accessor properties/u);
    expect(containsSecret(accessorBacked)).toBe(false);
    expect(getterCalls).toBe(0);
  });

  it("keeps immutable snapshot state private and returns a fresh clone per view read", () => {
    const source = {
      endpoint: { host: "database.internal" },
      secret: new Secret("plaintext-canary"),
    };
    const identity = new ReleaseIdentity({
      namespace: "prod/app",
      name: "runtime",
      version: 3n,
      activationRevision: 9n,
      digest: "digest",
    });
    const snapshot = immutableSnapshot(source, identity);
    source.endpoint.host = "mutated";

    const first = snapshot.get("endpoint");
    first.host = "consumer-mutation";
    expect(snapshot.get("endpoint")).toEqual({ host: "database.internal" });
    expect(snapshot.get("secret").text()).toBe("plaintext-canary");
    expect(snapshot.config()).not.toBe(snapshot.config());
    expect(snapshot.release).toBe(identity);
  });

  it("requires exact uint64 bigint release identity fields at runtime", () => {
    const maximum = 18_446_744_073_709_551_615n;
    expect(
      new ReleaseIdentity({
        version: maximum,
        activationRevision: maximum,
        schemaVersion: maximum,
      }),
    ).toMatchObject({
      version: maximum,
      activationRevision: maximum,
      schemaVersion: maximum,
    });

    for (const field of ["version", "activationRevision", "schemaVersion"] as const) {
      for (const value of [1, "1", null]) {
        expect(() => new ReleaseIdentity({ [field]: value } as never)).toThrow(TypeError);
      }
      for (const value of [-1n, maximum + 1n]) {
        expect(() => new ReleaseIdentity({ [field]: value })).toThrow(RangeError);
      }
    }
  });

  it("deeply clones mismatch fields and redacts an entire secret-bearing value", () => {
    const expected = { values: [1, 2], exact: 9_007_199_254_740_993n };
    const report = new DefaultMismatchReport(
      "startup",
      "error",
      new ReleaseIdentity({ name: "runtime", version: 2n }),
      [
        {
          path: "group.field",
          expected,
          actual: {
            exact: 18_446_744_073_709_551_615n,
            nested: new Secret("plaintext-canary"),
          },
        },
        { path: "field\nINJECTION", expected: 1, actual: 2 },
      ],
    );
    expected.values[0] = 99;
    const first = report.fields();
    const firstDifference = first[0];
    if (!firstDifference) throw new Error("missing test difference");
    (firstDifference.expected as { values: number[] }).values[0] = 88;

    expect(report.fields()[0]).toEqual({
      path: "group.field",
      expected: { values: [1, 2], exact: 9_007_199_254_740_993n },
      actual: "[REDACTED]",
    });
    expect(report.fields()[1]?.path).toBe("invalid_path");
    const encoded = JSON.stringify(report);
    expect(JSON.parse(encoded)).toMatchObject({
      release: { version: "2" },
      differences: [
        {
          path: "group.field",
          expected: { values: [1, 2], exact: "9007199254740993" },
          actual: "[REDACTED]",
        },
        { path: "invalid_path", expected: 1, actual: 2 },
      ],
    });
    for (const rendered of [String(report), inspect(report), encoded]) {
      expect(rendered).not.toContain("plaintext-canary");
      expect(rendered).not.toContain("INJECTION");
    }
  });

  it("deeply clones applied changes, redacts secret-bearing values, and copies groups", () => {
    const previous = { values: [1, 2], exact: 9_007_199_254_740_993n };
    const groups = { runtime: '{"limit":2}', database: '{"host":"db"}' };
    const release = new ReleaseIdentity({ name: "runtime", version: 2n });
    const report = new AppliedReport(
      "runtime",
      release,
      true,
      [
        { path: "group.field", previous, current: { nested: new Secret("plaintext-canary") } },
        { path: "field\nINJECTION", previous: 1, current: 2 },
        { path: "database_password", previous: null, current: null },
      ],
      groups,
    );
    previous.values[0] = 99;
    groups.runtime = "mutated";
    const firstChange = report.changed()[0];
    if (!firstChange) throw new Error("missing test change");
    (firstChange.previous as { values: number[] }).values[0] = 88;

    expect(report.changed()).toEqual([
      {
        path: "group.field",
        previous: { values: [1, 2], exact: 9_007_199_254_740_993n },
        current: "[REDACTED]",
      },
      { path: "invalid_path", previous: 1, current: 2 },
      { path: "database_password", previous: null, current: null },
    ]);
    expect(report.groups()).toEqual({ database: '{"host":"db"}', runtime: '{"limit":2}' });
    expect(Object.keys(report.groups())).toEqual(["database", "runtime"]);
    expect(Object.isFrozen(report.groups())).toBe(true);
    expect(Object.isFrozen(report)).toBe(true);
    expect(report).toMatchObject({ phase: "runtime", defaultDivergent: true, release });
    expect(String(report)).toBe(
      `configstore: applied (runtime) ${release} divergent=true changed=group.field,invalid_path,database_password`,
    );
    const encoded = JSON.stringify(report);
    expect(JSON.parse(encoded)).toMatchObject({
      phase: "runtime",
      release: { version: "2" },
      defaultDivergent: true,
      changed: [
        {
          path: "group.field",
          previous: { values: [1, 2], exact: "9007199254740993" },
          current: "[REDACTED]",
        },
        { path: "invalid_path", previous: 1, current: 2 },
        { path: "database_password", previous: null, current: null },
      ],
    });
    for (const rendered of [String(report), inspect(report), encoded]) {
      expect(rendered).not.toContain("plaintext-canary");
      expect(rendered).not.toContain("INJECTION");
    }
    expect(inspect(report)).not.toContain("values");
    expect(new AppliedReport("startup", release, false).groups()).toEqual({});
  });

  it("classifies candidate errors without rendering their causes", () => {
    const canary = "SECRET-CANDIDATE-CAUSE";
    const cause = new Error(canary);
    const error = new CandidateError("config_validation_failed", cause);
    expect(error.cause).toBe(cause);
    expect(error.releaseRejectionCategory).toBe("config_validation_failed");
    for (const rendered of [String(error), inspect(error), JSON.stringify(error)]) {
      expect(rendered).not.toContain(canary);
    }
  });

  it("maps strict decoder locations to safe generated rejection paths", () => {
    interface Holder {
      value: number;
    }
    const descriptor = group<Holder>([
      field<Holder, "value">("value", "value", codecs.int({ bits: 8 })),
    ]);
    let decodeError: unknown;
    try {
      decodeGroup("{}", descriptor);
    } catch (error) {
      decodeError = error;
    }
    const rejection = rejectDecode("runtime", decodeError);
    const report = new CandidateRejectionReport(
      rejection.category,
      new ReleaseIdentity({ name: "runtime", version: 2n }),
      ["runtime.value", "unsafe\npath", "runtime.value"],
    );
    expect(report.paths()).toEqual(["runtime.value"]);
    const paths = report.paths() as string[];
    paths[0] = "changed";
    expect(report.paths()).toEqual(["runtime.value"]);
  });
});

describe("configstore exact manifest contract", () => {
  const contract = [
    { alias: "database", kind: "parameter" as const, contentType: "json" },
    { alias: "password", kind: "secret" as const },
  ];

  it("validates contract shape before any release work", () => {
    expect(validateContract(contract)).toEqual([
      { alias: "database", kind: "parameter", contentType: "json" },
      { alias: "password", kind: "secret", contentType: "" },
    ]);
    expect(() => validateContract([])).toThrow(/at least one/u);
    expect(() =>
      validateContract([
        { alias: "same", kind: "secret" },
        { alias: "same", kind: "secret" },
      ]),
    ).toThrow(/duplicate/u);
    expect(() => validateContract([{ alias: "database", kind: "parameter" }])).toThrow(
      /content type/u,
    );
  });

  it("requires the exact alias, kind, and parameter content type before fetch", () => {
    const entries = new Map([
      [
        "database",
        new ReleaseEntryMetadata({
          alias: "database",
          kind: "parameter",
          path: "/prod/app/database",
          version: 1n,
          contentType: "json",
        }),
      ],
      [
        "password",
        new ReleaseEntryMetadata({
          alias: "password",
          kind: "secret",
          path: "/prod/app/password",
          version: 1n,
          contentType: "text/plain",
        }),
      ],
    ]);
    const manifest = new ReleaseManifest({
      namespace: "prod/app",
      name: "runtime",
      version: 1n,
      activationRevision: 2n,
      digest: "digest",
      entries,
    });
    const validator = createManifestValidator(contract);
    expect(() => validator(manifest, new AbortController().signal)).not.toThrow();

    const wrong = new ReleaseManifest({
      namespace: "prod/app",
      name: "runtime",
      version: 1n,
      activationRevision: 2n,
      digest: "digest",
      entries: new Map([
        ...entries,
        [
          "extra",
          new ReleaseEntryMetadata({
            alias: "extra",
            kind: "secret",
            path: "/prod/app/extra",
            version: 1n,
          }),
        ],
      ]),
    });
    try {
      validator(wrong, new AbortController().signal);
      expect.fail("manifest unexpectedly accepted");
    } catch (error) {
      expect(error).toBeInstanceOf(CandidateError);
      expect((error as CandidateError).category).toBe("config_contract_mismatch");
    }
  });
});
