import { describe, expect, it } from "vitest";

import {
  AppliedReport,
  CandidateRejectionReport,
  DefaultMismatchReport,
} from "../src/configstore/errors.js";
import { consoleCallbacks } from "../src/configstore/logging.js";
import { ReleaseIdentity } from "../src/configstore/snapshot.js";
import { Secret } from "../src/secret.js";

interface Record_ {
  readonly level: "info" | "error";
  readonly message: string;
  readonly attrs: Record<string, unknown> | undefined;
}

function recorder() {
  const records: Record_[] = [];
  return {
    records,
    logger: {
      info: (message: string, attrs?: Record<string, unknown>) => {
        records.push({ level: "info", message, attrs });
      },
      error: (message: string, attrs?: Record<string, unknown>) => {
        records.push({ level: "error", message, attrs });
      },
    },
  };
}

const release = new ReleaseIdentity({
  namespace: "prod/api",
  name: "runtime",
  version: 4n,
  activationRevision: 9n,
});

describe("consoleCallbacks", () => {
  it("logs mismatches as value-free errors", () => {
    const { records, logger } = recorder();
    const callbacks = consoleCallbacks(logger, { component: "api" });
    callbacks.onDefaultMismatch(
      new DefaultMismatchReport("startup", "error", release, [
        { path: "runtime.limit", expected: 1, actual: 2 },
        { path: "database.password", expected: new Secret("plain-canary"), actual: 3 },
      ]),
    );
    expect(records).toEqual([
      {
        level: "error",
        message: "kms config diverges from source defaults",
        attrs: {
          component: "api",
          phase: "startup",
          release: "prod/api/runtime@4#9",
          fields: [
            { path: "runtime.limit", expected: 1, actual: 2 },
            { path: "database.password", expected: "[REDACTED]", actual: 3 },
          ],
        },
      },
    ]);
    expect(JSON.stringify(records)).not.toContain("plain-canary");
  });

  it("logs the startup generation and one record per sorted parameter group", () => {
    const { records, logger } = recorder();
    const callbacks = consoleCallbacks(logger);
    callbacks.onApplied?.(
      new AppliedReport("startup", release, true, [], {
        runtime: '{"limit":2,"big":18446744073709551615}',
        database: '{"host":"db"}',
      }),
    );
    expect(records.map((record) => [record.level, record.message])).toEqual([
      ["info", "kms config applied"],
      ["info", "kms config group"],
      ["info", "kms config group"],
    ]);
    expect(records[0]?.attrs).toEqual({
      phase: "startup",
      release: "prod/api/runtime@4#9",
      release_version: "4",
      activation_revision: "9",
      default_divergent: true,
    });
    expect(records[1]?.attrs).toEqual({
      alias: "database",
      values: { host: "db" },
      release_version: "4",
      activation_revision: "9",
    });
    expect(records[2]?.attrs).toMatchObject({ alias: "runtime", values: { limit: 2 } });
    expect(JSON.stringify(records)).not.toMatch(/"component"/u);
  });

  it("logs reloads with a change count and one record per changed field", () => {
    const { records, logger } = recorder();
    const callbacks = consoleCallbacks(logger, { component: "worker" });
    callbacks.onApplied?.(
      new AppliedReport(
        "runtime",
        release,
        false,
        [
          { path: "runtime.limit", previous: 2, current: 3 },
          { path: "database_password", previous: null, current: null },
        ],
        { runtime: '{"limit":3}' },
      ),
    );
    expect(records).toEqual([
      {
        level: "info",
        message: "kms config reloaded",
        attrs: {
          component: "worker",
          phase: "runtime",
          release: "prod/api/runtime@4#9",
          release_version: "4",
          activation_revision: "9",
          default_divergent: false,
          changed_count: 2,
        },
      },
      {
        level: "info",
        message: "kms config field changed",
        attrs: { component: "worker", path: "runtime.limit", previous: 2, current: 3 },
      },
      {
        level: "info",
        message: "kms config field changed",
        attrs: { component: "worker", path: "database_password", previous: null, current: null },
      },
      {
        level: "info",
        message: "kms config group",
        attrs: {
          component: "worker",
          alias: "runtime",
          values: { limit: 3 },
          release_version: "4",
          activation_revision: "9",
        },
      },
    ]);
  });

  it("honours the snapshot and change toggles and logs rejections", () => {
    const { records, logger } = recorder();
    const callbacks = consoleCallbacks(logger, {
      startupSnapshot: false,
      reloadChanges: false,
      reloadSnapshot: false,
    });
    callbacks.onApplied?.(new AppliedReport("startup", release, false, [], { runtime: "{}" }));
    callbacks.onApplied?.(
      new AppliedReport("runtime", release, false, [{ path: "a.b", previous: 1, current: 2 }], {
        runtime: '{"a":{"b":2}}',
      }),
    );
    callbacks.onCandidateRejected?.(
      new CandidateRejectionReport("restart_required", release, ["runtime.restart"]),
    );
    expect(records.map((record) => [record.level, record.message])).toEqual([
      ["info", "kms config applied"],
      ["info", "kms config reloaded"],
      ["error", "kms config candidate rejected"],
    ]);
    expect(records[2]?.attrs).toEqual({
      category: "restart_required",
      release: "prod/api/runtime@4#9",
      paths: ["runtime.restart"],
    });
  });

  it("never emits attribute keys that look like credentials and isolates logger failures", () => {
    const { records, logger } = recorder();
    const callbacks = consoleCallbacks(logger, { component: "api" });
    callbacks.onDefaultMismatch(new DefaultMismatchReport("runtime", "error", release, []));
    callbacks.onApplied?.(new AppliedReport("startup", release, false, [], { runtime: "{}" }));
    callbacks.onApplied?.(
      new AppliedReport("runtime", release, false, [{ path: "a.b", previous: 1, current: 2 }]),
    );
    callbacks.onCandidateRejected?.(new CandidateRejectionReport("internal", release));
    for (const record of records) {
      for (const key of Object.keys(record.attrs ?? {})) {
        expect(key).not.toMatch(/secret|token|password|api_key/iu);
      }
    }

    const throwing = consoleCallbacks({
      info: () => {
        throw new Error("logger-failure");
      },
      error: () => {
        throw new Error("logger-failure");
      },
    });
    expect(() =>
      throwing.onDefaultMismatch(new DefaultMismatchReport("runtime", "error", release, [])),
    ).not.toThrow();
    expect(() => throwing.onApplied?.(new AppliedReport("startup", release, false))).not.toThrow();
    expect(() => consoleCallbacks({} as never)).toThrow(TypeError);
  });
});
