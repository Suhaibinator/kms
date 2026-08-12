import { inspect } from "node:util";

import { describe, expect, it } from "vitest";
import { cloneConfig } from "../src/configstore/clone.js";
import { codecs, decodeGroup, field, group } from "../src/configstore/codecs.js";
import { createManifestValidator, validateContract } from "../src/configstore/contract.js";
import {
  CandidateError,
  CandidateRejectionReport,
  DefaultMismatchError,
  DefaultMismatchReport,
  rejectDecode,
} from "../src/configstore/errors.js";
import { immutableSnapshot, ReleaseIdentity } from "../src/configstore/snapshot.js";
import { ReleaseEntryMetadata, ReleaseManifest } from "../src/releases/types.js";
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

  it("deeply clones mismatch fields and redacts an entire secret-bearing value", () => {
    const expected = { values: [1, 2] };
    const report = new DefaultMismatchReport(
      "startup",
      "fatal",
      new ReleaseIdentity({ name: "runtime", version: 2n }),
      [
        { path: "group.field", expected, actual: { nested: new Secret("plaintext-canary") } },
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
      expected: { values: [1, 2] },
      actual: "[REDACTED]",
    });
    expect(report.fields()[1]?.path).toBe("invalid_path");
    for (const rendered of [String(report), inspect(report), JSON.stringify(report)]) {
      expect(rendered).not.toContain("plaintext-canary");
      expect(rendered).not.toContain("INJECTION");
    }

    const error = new DefaultMismatchError(report);
    expect(error.phase).toBe("startup");
    expect(error.severity).toBe("fatal");
    expect(error.fields()[0]?.expected).toEqual({ values: [1, 2] });
    expect(inspect(error)).not.toContain("values");
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
