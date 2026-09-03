import { describe, expect, it } from "vitest";

import type { ClientReleaseLoaderOptions } from "../src/client.js";
import type { AppliedReport, DefaultMismatchReport } from "../src/configstore/errors.js";
import type { ManagedReleaseClient } from "../src/configstore/manager.js";
import { parameterHash, parseDefaultsArtifact } from "../src/configstore/index.js";
import type { ReleaseLoader } from "../src/releases/loader.js";
import type {
  VerifyReleaseDefaultsOptions,
  VerifyReleaseDefaultsResult,
} from "../src/releases/verify.js";
import {
  type PrepareRelease,
  ReleaseEntryMetadata,
  type ReleaseLoaderStats,
  type ReleaseLoaderStatus,
  ReleaseManifest,
  ReleaseParameter,
  ReleaseSecret,
  ReleaseSnapshot,
} from "../src/releases/types.js";
import { Secret } from "../src/secret.js";
import {
  encodeDefaultsArtifact,
  encodeParameterGroups,
  generatedContract,
  groupCodecs,
  schemaSHA256,
  Store,
  verifyReleaseDefaults,
} from "./fixtures/configgen/config.generated.js";
import type { Config } from "./fixtures/configgen/config.js";
import { Store as SecretsOnlyStore } from "./fixtures/configgen/secrets-only.generated.js";

describe("generated managed configuration binding", () => {
  it("encodes all groups canonically and rejects non-zero secret defaults", () => {
    const defaults = defaultConfig();
    expect(encodeParameterGroups(defaults)).toEqual({
      database:
        '{"endpoint":{"host":"db.internal","ports":null,"zones":["west","east"]},"limit":10}',
      runtime: '{"enabled":false,"epoch":0,"labels":null,"payload":null}',
    });
    expect(Object.keys(groupCodecs)).toEqual(["database", "runtime"]);
    expect(generatedContract).toEqual([
      { alias: "database", kind: "parameter", contentType: "json" },
      { alias: "runtime", kind: "parameter", contentType: "json" },
      { alias: "database_password", kind: "secret" },
    ]);
    expect(parseDefaultsArtifact(encodeDefaultsArtifact("local", defaults))).toMatchObject({
      profile: "local",
      contract: [
        { alias: "database", kind: "parameter", contentType: "json" },
        { alias: "database_password", kind: "secret", contentType: "" },
        { alias: "runtime", kind: "parameter", contentType: "json" },
      ],
      parameters: [
        { alias: "database", contentType: "json" },
        { alias: "runtime", contentType: "json" },
      ],
    });

    expect(
      () => new Store({ ...defaults, password: new Secret("not-allowed") }, () => undefined),
    ).toThrow(/must be the zero Secret/u);
  });

  it("prepares, validates and atomically publishes hot changes while fencing restart identities", async () => {
    const first = releaseSnapshot({
      version: 1n,
      revision: 10n,
      limit: 12,
      enabled: true,
      secretVersion: 7n,
    });
    const loader = new InlineLoader(first);
    const reports: DefaultMismatchReport[] = [];
    const applied: AppliedReport[] = [];
    const validated: Config[] = [];
    const defaults = defaultConfig();
    const store = new Store(defaults, (config) => {
      validated.push(config);
      // Validation canonicalization is retained, but each invocation and the
      // caller-owned defaults remain isolated from subsequent mutation.
      config.endpoint.host = "validator-mutation";
      config.payload = Uint8Array.of(99);
    });
    const controller = new AbortController();
    const manager = await store.start(
      inlineClient(loader),
      {
        release: "runtime",
        onDefaultMismatch: (report) => reports.push(report),
        onApplied: (report) => applied.push(report),
      },
      controller.signal,
    );

    expect(validated).toHaveLength(2);
    expect(reports).toHaveLength(1);
    expect(reports[0]?.fields().map(({ path }) => path)).toEqual([
      "database.limit",
      "runtime.enabled",
      "runtime.epoch",
      "runtime.labels",
    ]);
    expect(applied).toHaveLength(1);
    expect(applied[0]).toMatchObject({ phase: "startup", defaultDivergent: true });
    expect(applied[0]?.changed()).toEqual([]);
    const startupGroups = applied[0]?.groups() ?? {};
    expect(Object.keys(startupGroups)).toEqual(["database", "runtime"]);
    expect(JSON.parse(startupGroups.database ?? "")).toEqual({
      endpoint: { host: "validator-mutation", ports: null, zones: ["west", "east"] },
      limit: 12,
    });
    expect(JSON.parse(startupGroups.runtime ?? "")).toEqual({
      enabled: true,
      epoch: 1,
      labels: { region: "west", zone: "one" },
      payload: "Yw==",
    });
    expect(JSON.stringify(startupGroups)).not.toContain("release-secret");
    expect(defaults.endpoint.host).toBe("db.internal");
    expect(defaults.payload).toBeNull();
    expect(store.current().release.version).toBe(1n);
    expect(store.current().databaseHealth().endpoint.host).toBe("validator-mutation");
    expect(store.current().worker().password.text()).toBe("release-secret");

    const escaped = store.current().config();
    escaped.endpoint.host = "caller-mutation";
    escaped.payload?.fill(0);
    expect(store.current().config().endpoint.host).toBe("validator-mutation");
    expect(store.current().config().payload).toEqual(Uint8Array.of(99));

    await expect(
      loader.apply(
        releaseSnapshot({
          version: 2n,
          revision: 11n,
          limit: 13,
          enabled: true,
          secretVersion: 8n,
        }),
      ),
    ).rejects.toMatchObject({ category: "restart_required" });
    expect(store.current().release.version).toBe(1n);
    expect(store.current().worker().limit).toBe(12);
    expect(applied).toHaveLength(1);

    await loader.apply(
      releaseSnapshot({
        version: 3n,
        revision: 12n,
        limit: 14,
        enabled: true,
        secretVersion: 7n,
      }),
    );
    expect(store.current().release.version).toBe(3n);
    expect(store.current().worker().limit).toBe(14);
    expect(applied).toHaveLength(2);
    expect(applied[1]).toMatchObject({ phase: "runtime", defaultDivergent: true });
    expect(applied[1]?.release.version).toBe(3n);
    expect(applied[1]?.changed()).toEqual([
      { path: "database.limit", previous: 12, current: 14 },
      { path: "runtime.epoch", previous: 1n, current: 3n },
    ]);
    expect(JSON.parse(applied[1]?.groups().database ?? "")).toMatchObject({ limit: 14 });

    controller.abort();
    await expect(manager.wait()).resolves.toBeUndefined();
  });

  it("reports hot secret rotations path-only through onApplied", async () => {
    const snapshot = (version: bigint, secretVersion: bigint, plaintext: string) => {
      const token = entry("worker_token", "secret", secretVersion, "string", "secrets/worker");
      return new ReleaseSnapshot({
        namespace: "prod/api",
        name: "runtime",
        version,
        activationRevision: version,
        digest: `digest-${version}`,
        entries: new Map([[token.alias, token]]),
        parameters: new Map(),
        secrets: new Map([["worker_token", new ReleaseSecret(Buffer.from(plaintext), token)]]),
      });
    };
    const loader = new InlineLoader(snapshot(1n, 7n, "first-secret"));
    const applied: AppliedReport[] = [];
    const store = new SecretsOnlyStore({ token: new Secret() }, () => undefined);
    const controller = new AbortController();
    const manager = await store.start(
      inlineClient(loader),
      {
        release: "runtime",
        onDefaultMismatch: () => undefined,
        onApplied: (report) => applied.push(report),
      },
      controller.signal,
    );
    expect(applied[0]?.groups()).toEqual({});

    await loader.apply(snapshot(2n, 7n, "first-secret"));
    expect(applied[1]?.changed()).toEqual([]);
    await loader.apply(snapshot(3n, 8n, "second-secret"));
    expect(applied).toHaveLength(3);
    expect(applied[2]?.changed()).toEqual([
      { path: "worker_token", previous: null, current: null },
    ]);
    expect(store.current().worker().token.text()).toBe("second-secret");
    for (const report of applied) {
      for (const rendered of [String(report), JSON.stringify(report)]) {
        expect(rendered).not.toContain("first-secret");
        expect(rendered).not.toContain("second-secret");
      }
    }

    controller.abort();
    await expect(manager.wait()).resolves.toBeUndefined();
  });

  it("verifies source defaults through canonical hashes without sending values", async () => {
    const defaults = defaultConfig();
    const groups = encodeParameterGroups(defaults);
    const requests: VerifyReleaseDefaultsOptions[] = [];
    const client = {
      verifyReleaseDefaults: (options: VerifyReleaseDefaultsOptions) => {
        requests.push(options);
        const result: VerifyReleaseDefaultsResult = {
          releaseName: "runtime",
          releaseVersion: 9n,
          activationRevision: 12n,
          schemaMatches: true,
          entries: [
            { alias: "database", verdict: "match" },
            { alias: "runtime", verdict: "differs" },
          ],
          matchCount: 1,
          differsCount: 1,
          missingCount: 0,
          unknownAliasCount: 0,
          secretAliasCount: 0,
          unsupportedCount: 0,
          unverifiedCount: 1,
          passed: () => false,
        };
        return Promise.resolve(result);
      },
    };

    const result = await verifyReleaseDefaults(client, defaults, {
      namespace: "prod/api",
      release: "runtime",
      profile: "local",
    });

    expect(requests).toHaveLength(1);
    expect(requests[0]).toMatchObject({
      namespace: "prod/api",
      release: "runtime",
      profile: "local",
      schemaSha256: schemaSHA256,
      entries: [
        {
          alias: "database",
          contentType: "json",
          sha256: parameterHash("json", groups.database ?? ""),
        },
        {
          alias: "runtime",
          contentType: "json",
          sha256: parameterHash("json", groups.runtime ?? ""),
        },
      ],
    });
    expect(JSON.stringify(requests[0])).not.toContain("db.internal");
    expect(result.passed()).toBe(false);
    expect(result.failures()).toEqual([
      { alias: "runtime", contentType: "json", verdict: "differs" },
    ]);
    expect(result.report()).toContain("differs  runtime   json");
    expect(result.report()).not.toContain("db.internal");
  });

  it("compares record fields canonically instead of treating insertion order as drift", async () => {
    const defaults = defaultConfig();
    defaults.enabled = true;
    defaults.limit = 12;
    defaults.epoch = 1n;
    defaults.payload = Uint8Array.of(1, 2, 3);
    defaults.labels = { region: "west", zone: "one" };
    const loader = new InlineLoader(
      releaseSnapshot({
        version: 1n,
        revision: 10n,
        limit: 12,
        enabled: true,
        secretVersion: 7n,
      }),
    );
    const reports: DefaultMismatchReport[] = [];
    const controller = new AbortController();
    const store = new Store(defaults, () => undefined);
    const manager = await store.start(
      inlineClient(loader),
      {
        release: "runtime",
        onDefaultMismatch: (report) => reports.push(report),
      },
      controller.signal,
    );

    expect(reports).toEqual([]);
    expect(manager.status().defaultDivergent).toBe(false);
    controller.abort();
    await expect(manager.wait()).resolves.toBeUndefined();
  });

  it("rejects a validator that corrupts a statically typed secret field", async () => {
    const loader = new InlineLoader(
      releaseSnapshot({
        version: 1n,
        revision: 10n,
        limit: 12,
        enabled: true,
        secretVersion: 7n,
      }),
    );
    const store = new Store(defaultConfig(), (config) => {
      Object.defineProperty(config, "password", {
        value: "plaintext-is-not-a-secret",
        enumerable: true,
        writable: true,
        configurable: true,
      });
    });

    await expect(
      store.start(inlineClient(loader), {
        release: "runtime",
        onDefaultMismatch: () => undefined,
      }),
    ).rejects.toMatchObject({ category: "config_validation_failed" });
  });
});

class InlineLoader {
  readonly #first: ReleaseSnapshot;
  #prepare: PrepareRelease | undefined;
  #status: ReleaseLoaderStatus = {
    state: "idle",
    observedVersion: 0n,
    observedRevision: 0n,
    appliedVersion: 0n,
    appliedRevision: 0n,
    lastResolutionDurationMs: 0,
    reconnects: 0n,
  };
  #stats: ReleaseLoaderStats = {
    candidates: 0n,
    applied: 0n,
    rejected: {},
    reconnects: 0n,
  };
  options: ClientReleaseLoaderOptions | undefined;

  constructor(first: ReleaseSnapshot) {
    this.#first = first;
  }

  run(prepare: PrepareRelease, signal?: AbortSignal): Promise<void> {
    this.#prepare = prepare;
    const candidateSignal = signal ?? new AbortController().signal;
    return this.#runFirst(candidateSignal);
  }

  async apply(snapshot: ReleaseSnapshot): Promise<void> {
    const prepare = this.#prepare;
    if (!prepare) throw new Error("test loader was not started");
    this.#observe(snapshot);
    try {
      const prepared = await prepare(snapshot, new AbortController().signal);
      prepared.commit();
      this.#applyStatus(snapshot);
    } catch (error) {
      this.#status = { ...this.#status, state: "rejected" };
      this.#stats = { ...this.#stats, candidates: this.#stats.candidates + 1n };
      throw error;
    }
  }

  status(): ReleaseLoaderStatus {
    return { ...this.#status };
  }

  stats(): ReleaseLoaderStats {
    return { ...this.#stats, rejected: { ...this.#stats.rejected } };
  }

  stop(): void {
    // Test lifecycle uses the caller's AbortSignal.
  }

  async #runFirst(signal: AbortSignal): Promise<void> {
    const manifest = manifestFrom(this.#first);
    await this.options?.validateManifest?.(manifest, signal);
    const prepare = this.#prepare;
    if (!prepare) throw new Error("test prepare callback missing");
    this.#observe(this.#first);
    const prepared = await prepare(this.#first, signal);
    prepared.commit();
    this.#applyStatus(this.#first);
    await aborted(signal);
  }

  #observe(snapshot: ReleaseSnapshot): void {
    this.#status = {
      ...this.#status,
      state: "received",
      observedVersion: snapshot.version,
      observedRevision: snapshot.activationRevision,
    };
  }

  #applyStatus(snapshot: ReleaseSnapshot): void {
    this.#status = {
      ...this.#status,
      state: "applied",
      appliedVersion: snapshot.version,
      appliedRevision: snapshot.activationRevision,
    };
    this.#stats = {
      ...this.#stats,
      candidates: this.#stats.candidates + 1n,
      applied: this.#stats.applied + 1n,
    };
  }
}

function inlineClient(loader: InlineLoader): ManagedReleaseClient {
  return {
    createReleaseLoader(options) {
      loader.options = options;
      return Promise.resolve(loader as unknown as ReleaseLoader);
    },
  };
}

function defaultConfig(): Config {
  return {
    enabled: false,
    limit: 10,
    epoch: 0n,
    payload: null,
    labels: null,
    endpoint: { host: "db.internal", ports: null, zones: ["west", "east"] },
    password: new Secret(),
  };
}

interface SnapshotOptions {
  readonly version: bigint;
  readonly revision: bigint;
  readonly limit: number;
  readonly enabled: boolean;
  readonly secretVersion: bigint;
}

function releaseSnapshot(options: SnapshotOptions): ReleaseSnapshot {
  const database = entry("database", "parameter", 1n, "json", "groups/database");
  const runtime = entry("runtime", "parameter", 1n, "json", "groups/runtime");
  const password = entry(
    "database_password",
    "secret",
    options.secretVersion,
    "string",
    "secrets/database-password",
  );
  const entries = new Map([
    [database.alias, database],
    [runtime.alias, runtime],
    [password.alias, password],
  ]);
  return new ReleaseSnapshot({
    namespace: "prod/api",
    name: "runtime",
    version: options.version,
    activationRevision: options.revision,
    digest: `digest-${options.version}`,
    entries,
    parameters: new Map([
      [
        "database",
        new ReleaseParameter(
          JSON.stringify({
            endpoint: { host: "db.internal", ports: null, zones: ["west", "east"] },
            limit: options.limit,
          }),
          database,
        ),
      ],
      [
        "runtime",
        new ReleaseParameter(
          `{"enabled":${options.enabled},"epoch":${options.version},"labels":{"zone":"one","region":"west"},"payload":"AQID"}`,
          runtime,
        ),
      ],
    ]),
    secrets: new Map([
      ["database_password", new ReleaseSecret(Buffer.from("release-secret"), password)],
    ]),
  });
}

function manifestFrom(snapshot: ReleaseSnapshot): ReleaseManifest {
  return new ReleaseManifest({
    namespace: snapshot.namespace,
    name: snapshot.name,
    version: snapshot.version,
    activationRevision: snapshot.activationRevision,
    schemaVersion: snapshot.schemaVersion,
    digest: snapshot.digest,
    entries: snapshot.entries(),
  });
}

function entry(
  alias: string,
  kind: "parameter" | "secret",
  version: bigint,
  contentType: string,
  path: string,
): ReleaseEntryMetadata {
  return new ReleaseEntryMetadata({ alias, kind, version, contentType, path });
}

function aborted(signal: AbortSignal): Promise<never> {
  if (signal.aborted) return Promise.reject(abortReason(signal));
  return new Promise((_, reject) => {
    signal.addEventListener("abort", () => reject(abortReason(signal)), { once: true });
  });
}

function abortReason(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException("Aborted", "AbortError");
}
