import { describe, expect, it } from "vitest";

import type { ClientReleaseLoaderOptions } from "../src/client.js";
import type { DefaultMismatchReport } from "../src/configstore/errors.js";
import type { ManagedReleaseClient } from "../src/configstore/manager.js";
import type { ReleaseLoader } from "../src/releases/loader.js";
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
  encodeParameterGroups,
  generatedContract,
  groupCodecs,
  Store,
} from "./fixtures/configgen/config.generated.js";
import type { Config } from "./fixtures/configgen/config.js";

describe("generated managed configuration binding", () => {
  it("encodes all groups canonically and rejects non-zero secret defaults", () => {
    const defaults = defaultConfig();
    expect(encodeParameterGroups(defaults)).toEqual({
      database: '{"endpoint":{"host":"db.internal","ports":null},"limit":10}',
      runtime: '{"enabled":false,"epoch":0,"labels":null,"payload":null}',
    });
    expect(Object.keys(groupCodecs)).toEqual(["database", "runtime"]);
    expect(generatedContract).toEqual([
      { alias: "database", kind: "parameter", contentType: "json" },
      { alias: "runtime", kind: "parameter", contentType: "json" },
      { alias: "database_password", kind: "secret" },
    ]);

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
    const validated: Config[] = [];
    const store = new Store(defaultConfig(), (config) => {
      validated.push(config);
      // A mutating validator is isolated from defaults, candidate and publication.
      config.endpoint.host = "validator-mutation";
      config.payload = Uint8Array.of(99);
    });
    const controller = new AbortController();
    const manager = await store.start(
      inlineClient(loader),
      {
        release: "runtime",
        allowDefaultMismatch: true,
        onDefaultMismatch: (report) => reports.push(report),
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
      "runtime.payload",
    ]);
    expect(store.current().release.version).toBe(1n);
    expect(store.current().databaseHealth().endpoint.host).toBe("db.internal");
    expect(store.current().worker().password.text()).toBe("release-secret");

    const escaped = store.current().config();
    escaped.endpoint.host = "caller-mutation";
    escaped.payload?.fill(0);
    expect(store.current().config().endpoint.host).toBe("db.internal");
    expect(store.current().config().payload).toEqual(Uint8Array.of(1, 2, 3));

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

    controller.abort();
    await expect(manager.wait()).resolves.toBeUndefined();
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
    endpoint: { host: "db.internal", ports: null },
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
          JSON.stringify({ endpoint: { host: "db.internal", ports: null }, limit: options.limit }),
          database,
        ),
      ],
      [
        "runtime",
        new ReleaseParameter(
          `{"enabled":${options.enabled},"epoch":${options.version},"labels":{"region":"west"},"payload":"AQID"}`,
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
    schemaId: snapshot.schemaId,
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
