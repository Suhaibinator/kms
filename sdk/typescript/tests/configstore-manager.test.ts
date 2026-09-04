import { describe, expect, it } from "vitest";
import type { ClientReleaseLoaderOptions } from "../src/client.js";
import { codecs, decodeGroup, field, group } from "../src/configstore/codecs.js";
import type {
  AppliedReport,
  CandidateRejectionReport,
  DefaultMismatchReport,
} from "../src/configstore/errors.js";
import { startManagedConfig } from "../src/configstore/manager.js";
import {
  ConfigurationRelease,
  type ConfigurationReleaseEntry,
  type GetActiveReleaseResponse,
  type NamespaceRef,
  type Parameter,
  type ReleaseWatchRegistration,
  type ResourceRef,
  type SecretMetadata,
  type WatchReleaseEvent,
  type WatchReleaseRequest,
} from "../src/generated/kms.js";
import { deterministicReleaseDigest, sha256Hex } from "../src/releases/digest.js";
import {
  type FetchedSecret,
  ReleaseLoader,
  type ReleaseTransport,
  type ReleaseWatchStream,
} from "../src/releases/loader.js";

const namespace: NamespaceRef = { env: "prod", app: "api" };

interface RuntimeConfig {
  hot: number;
  restart: string;
}

const runtimeCodec = group<RuntimeConfig>([
  field<RuntimeConfig, "hot">("hot", "hot", codecs.int({ bits: 32 })),
  field<RuntimeConfig, "restart">("restart", "restart", codecs.string),
]);

describe("ManagedConfigManager", () => {
  it("creates its loader through the public KmsClient-compatible bridge", async () => {
    const release = makeRelease(1n, '{"hot":1,"restart":"a"}');
    const transport = new FakeReleaseTransport(release, 1n);
    let receivedOptions: ClientReleaseLoaderOptions | undefined;
    const client = {
      createReleaseLoader(options: ClientReleaseLoaderOptions): Promise<ReleaseLoader> {
        receivedOptions = options;
        return managedClient(transport).createReleaseLoader(options);
      },
    };
    const controller = new AbortController();
    const manager = await startManagedConfig(
      client,
      {
        release: "runtime",
        instanceId: "stable-test-instance",
        contract: [{ alias: "settings", kind: "parameter", contentType: "json" }],
        onDefaultMismatch: () => undefined,
      },
      () => ({ publish: () => undefined }),
      controller.signal,
    );

    expect(receivedOptions).toMatchObject({ name: "runtime" });
    expect(receivedOptions).not.toHaveProperty("namespace");
    expect(receivedOptions).not.toHaveProperty("clientName");
    controller.abort();
    await manager.wait();
  });

  it("applies and reports startup drift, notifies onApplied, and acknowledges divergence", async () => {
    const release = makeRelease(1n, '{"hot":2,"restart":"a"}');
    const transport = new FakeReleaseTransport(release, 1n);
    let published = 0;
    let aborted = 0;
    const reports: DefaultMismatchReport[] = [];
    const applied: AppliedReport[] = [];
    const controller = new AbortController();

    const manager = await startManagedConfig(
      managedClient(transport),
      {
        ...options((report) => reports.push(report)),
        onApplied: (report) => applied.push(report),
      },
      (snapshot) => {
        const config = decodeGroup(snapshot.parameter("settings")?.value() ?? "", runtimeCodec);
        return {
          publish: () => {
            published += 1;
          },
          abort: () => {
            aborted += 1;
          },
          defaultDifferences: [{ path: "settings.hot", expected: 1, actual: config.hot }],
          // Ignored for the initial generation: there is nothing to compare against.
          changed: [{ path: "settings.hot", previous: 1, current: config.hot }],
          groups: { settings: '{"hot":2,"restart":"a"}' },
        };
      },
      controller.signal,
    );

    expect(published).toBe(1);
    expect(aborted).toBe(0);
    expect(reports).toHaveLength(1);
    expect(reports[0]).toMatchObject({ phase: "startup", severity: "error" });
    expect(manager.status()).toMatchObject({ ready: true, defaultDivergent: true });
    expect(applied).toHaveLength(1);
    expect(applied[0]).toMatchObject({ phase: "startup", defaultDivergent: true });
    expect(applied[0]?.release.version).toBe(1n);
    expect(applied[0]?.changed()).toEqual([]);
    expect(applied[0]?.groups()).toEqual({ settings: '{"hot":2,"restart":"a"}' });
    await waitFor(() => appliedAcknowledgements(transport).length === 1);
    expect(appliedAcknowledgements(transport)[0]).toMatchObject({
      state: "applied",
      appliedDivergent: true,
      divergentFieldCount: 1,
    });
    expect(transport.calls.filter((call) => call.startsWith("parameter:"))).toHaveLength(1);

    controller.abort();
    await expect(manager.wait()).resolves.toBeUndefined();
  });

  it("admits startup drift, hot updates and restoration, but rejects restart changes", async () => {
    const release1 = makeRelease(1n, '{"hot":2,"restart":"a"}');
    const transport = new FakeReleaseTransport(release1, 1n);
    const controller = new AbortController();
    const mismatchReports: DefaultMismatchReport[] = [];
    const rejectionReports: CandidateRejectionReport[] = [];
    const applied: AppliedReport[] = [];
    let active: RuntimeConfig | undefined;
    let preparedAgainst: RuntimeConfig | undefined;
    let aborts = 0;

    const manager = await startManagedConfig(
      managedClient(transport),
      {
        ...options((report) => mismatchReports.push(report)),
        onApplied: (report) => applied.push(report),
        onCandidateRejected: (report) => rejectionReports.push(report),
      },
      (snapshot) => {
        const candidate = decodeGroup(snapshot.parameter("settings")?.value() ?? "", runtimeCodec);
        preparedAgainst = candidate;
        const differences =
          candidate.hot === 1 ? [] : [{ path: "settings.hot", expected: 1, actual: candidate.hot }];
        const restartRequiredFields =
          active && active.restart !== candidate.restart ? ["settings.restart"] : [];
        const changed =
          active && active.hot !== candidate.hot
            ? [{ path: "settings.hot", previous: active.hot, current: candidate.hot }]
            : [];
        return {
          publish: () => {
            active = candidate;
          },
          abort: () => {
            aborts += 1;
          },
          defaultDifferences: differences,
          restartRequiredFields,
          changed,
        };
      },
      controller.signal,
    );

    expect(active).toEqual({ hot: 2, restart: "a" });
    expect(manager.status()).toMatchObject({ ready: true, defaultDivergent: true });
    expect(mismatchReports).toHaveLength(1);
    expect(mismatchReports[0]).toMatchObject({ phase: "startup", severity: "error" });
    expect(applied).toHaveLength(1);
    expect(applied[0]).toMatchObject({ phase: "startup", defaultDivergent: true });
    expect(applied[0]?.groups()).toEqual({});

    transport.activate(makeRelease(2n, '{"hot":3,"restart":"a"}'), 2n);
    await waitFor(() => manager.status().applied.version === 2n);
    expect(active).toEqual({ hot: 3, restart: "a" });
    expect(mismatchReports).toHaveLength(2);
    expect(mismatchReports[1]).toMatchObject({ phase: "runtime", severity: "error" });
    expect(applied).toHaveLength(2);
    expect(applied[1]).toMatchObject({ phase: "runtime", defaultDivergent: true });
    expect(applied[1]?.changed()).toEqual([{ path: "settings.hot", previous: 2, current: 3 }]);

    transport.activate(makeRelease(3n, '{"hot":4,"restart":"b"}'), 3n);
    await waitFor(() => manager.status().lastRejectionCategory === "restart_required");
    expect(preparedAgainst).toEqual({ hot: 4, restart: "b" });
    expect(active).toEqual({ hot: 3, restart: "a" });
    expect(manager.status().applied.version).toBe(2n);
    expect(aborts).toBe(1);
    expect(rejectionReports).toHaveLength(1);
    expect(rejectionReports[0]?.category).toBe("restart_required");
    expect(rejectionReports[0]?.paths()).toEqual(["settings.restart"]);
    expect(applied).toHaveLength(2);

    transport.activate(makeRelease(4n, '{"hot":1,"restart":"a"}'), 4n);
    await waitFor(() => manager.status().applied.version === 4n);
    expect(active).toEqual({ hot: 1, restart: "a" });
    expect(manager.status().defaultDivergent).toBe(false);
    expect(mismatchReports).toHaveLength(2);
    expect(applied).toHaveLength(3);
    expect(applied[2]).toMatchObject({ phase: "runtime", defaultDivergent: false });
    expect(applied[2]?.changed()).toEqual([{ path: "settings.hot", previous: 3, current: 1 }]);
    expect(manager.stats()).toMatchObject({
      applied: 3n,
      defaultDivergent: false,
      appliedReleaseVersion: 4n,
      appliedActivationRevision: 4n,
    });
    await waitFor(() => appliedAcknowledgements(transport).length === 3);
    expect(
      appliedAcknowledgements(transport).map((acknowledgement) => [
        acknowledgement.version,
        acknowledgement.appliedDivergent,
        acknowledgement.divergentFieldCount,
      ]),
    ).toEqual([
      [1n, true, 1],
      [2n, true, 1],
      [4n, false, 0],
    ]);

    controller.abort();
    await expect(manager.wait()).resolves.toBeUndefined();
  });

  it("isolates throwing and asynchronous onApplied callbacks from publication", async () => {
    const transport = new FakeReleaseTransport(makeRelease(1n, '{"hot":1,"restart":"a"}'), 1n);
    const controller = new AbortController();
    let calls = 0;
    let active: RuntimeConfig | undefined;

    const manager = await startManagedConfig(
      managedClient(transport),
      {
        ...options(() => undefined),
        onApplied: (() => {
          calls += 1;
          if (calls === 1) throw new Error("applied-callback-canary");
          return Promise.reject(new Error("async-applied-canary"));
        }) as unknown as (report: AppliedReport) => void,
      },
      (snapshot) => {
        const candidate = decodeGroup(snapshot.parameter("settings")?.value() ?? "", runtimeCodec);
        return {
          publish: () => {
            active = candidate;
          },
        };
      },
      controller.signal,
    );

    expect(calls).toBe(1);
    expect(active).toEqual({ hot: 1, restart: "a" });
    expect(manager.status()).toMatchObject({ ready: true, state: "applied" });

    transport.activate(makeRelease(2n, '{"hot":2,"restart":"a"}'), 2n);
    await waitFor(() => manager.status().applied.version === 2n);
    await Promise.resolve();
    expect(calls).toBe(2);
    expect(active).toEqual({ hot: 2, restart: "a" });
    expect(manager.status().lastRejectionCategory).toBeUndefined();

    controller.abort();
    await expect(manager.wait()).resolves.toBeUndefined();
  });

  it("rejects malformed changed fields and group documents as internal before publication", async () => {
    for (const malformed of [
      { changed: [{ path: 7 as unknown as string, previous: 1, current: 2 }] },
      { groups: { settings: 42 as unknown as string } },
      { groups: ["settings"] as unknown as Record<string, string> },
    ]) {
      const transport = new FakeReleaseTransport(makeRelease(1n, '{"hot":1,"restart":"a"}'), 1n);
      let aborts = 0;
      await expect(
        startManagedConfig(
          managedClient(transport),
          options(() => undefined),
          () => ({
            publish: () => undefined,
            abort: () => {
              aborts += 1;
            },
            ...malformed,
          }),
        ),
      ).rejects.toMatchObject({ category: "internal" });
      expect(aborts).toBe(1);
    }
  });

  it("enforces the manifest contract before parameter fetch and reports once", async () => {
    const valid = makeRelease(1n, '{"hot":1,"restart":"a"}');
    const wrong = ConfigurationRelease.create({
      ...valid,
      entries: [parameterEntry("unexpected", "settings", 1n, '{"hot":1}')],
    });
    wrong.digest = deterministicReleaseDigest(wrong);
    const transport = new FakeReleaseTransport(wrong, 1n);
    const reports: CandidateRejectionReport[] = [];

    await expect(
      startManagedConfig(
        managedClient(transport),
        {
          ...options(() => undefined),
          onCandidateRejected: (report) => reports.push(report),
        },
        () => ({ publish: () => undefined }),
      ),
    ).rejects.toMatchObject({ category: "config_contract_mismatch" });
    expect(transport.calls.filter((call) => call.startsWith("parameter:"))).toHaveLength(0);
    expect(reports).toHaveLength(1);
    expect(reports[0]?.category).toBe("config_contract_mismatch");
    expect(reports[0]?.paths()).toEqual([]);
  });

  it("isolates a throwing default callback, admits the candidate and reports once", async () => {
    const release = makeRelease(1n, '{"hot":2,"restart":"a"}');
    const transport = new FakeReleaseTransport(release, 1n);
    let callbacks = 0;
    let aborts = 0;
    let publishes = 0;
    const rejectionReports: CandidateRejectionReport[] = [];

    const manager = await startManagedConfig(
      managedClient(transport),
      {
        ...options(async () => {
          callbacks += 1;
          throw new Error("callback-canary");
        }),
        onCandidateRejected: (report) => rejectionReports.push(report),
      },
      () => ({
        publish: () => {
          publishes += 1;
        },
        abort: () => {
          aborts += 1;
        },
        defaultDifferences: [{ path: "settings.hot", expected: 1, actual: 2 }],
      }),
    );
    expect(callbacks).toBe(1);
    expect(publishes).toBe(1);
    expect(aborts).toBe(0);
    expect(rejectionReports).toHaveLength(0);
    expect(manager.status().defaultDivergent).toBe(true);
    await manager.stop();
  });

  it("aborts candidate resources when malformed policy metadata cannot be cloned", async () => {
    const release = makeRelease(1n, '{"hot":1,"restart":"a"}');
    const transport = new FakeReleaseTransport(release, 1n);
    let aborts = 0;
    const unsafeExpected = {};
    Object.defineProperty(unsafeExpected, "value", {
      enumerable: true,
      get: () => "GETTER-CANARY",
    });

    await expect(
      startManagedConfig(
        managedClient(transport),
        options(() => undefined),
        () => ({
          publish: () => undefined,
          abort: () => {
            aborts += 1;
          },
          defaultDifferences: [{ path: "settings.hot", expected: unsafeExpected, actual: 1 }],
        }),
      ),
    ).rejects.toMatchObject({ category: "internal" });
    expect(aborts).toBe(1);
  });

  it("fails closed and observes asynchronous publish and abort contract violations", async () => {
    const sensitivePublish = "sensitive async publish failure";
    const publishTransport = new FakeReleaseTransport(
      makeRelease(1n, '{"hot":1,"restart":"a"}'),
      1n,
    );
    const publishError = await startManagedConfig(
      managedClient(publishTransport),
      options(() => undefined),
      () => ({
        publish: (() => Promise.reject(new Error(sensitivePublish))) as unknown as () => undefined,
      }),
    ).catch((reason: unknown) => reason);
    await Promise.resolve();
    expect(publishError).toMatchObject({
      message: expect.stringContaining("commit() threw; commit must be infallible"),
    });
    expect(String(publishError)).not.toContain(sensitivePublish);

    const sensitiveAbort = "sensitive async abort failure";
    const abortTransport = new FakeReleaseTransport(makeRelease(1n, '{"hot":2,"restart":"a"}'), 1n);
    const abortError = await startManagedConfig(
      managedClient(abortTransport),
      options(() => undefined),
      () => ({
        publish: () => undefined,
        abort: (() => Promise.reject(new Error(sensitiveAbort))) as unknown as () => undefined,
        // A malformed restart field forces the manager onto its abort path.
        restartRequiredFields: [7 as unknown as string],
      }),
    ).catch((reason: unknown) => reason);
    await Promise.resolve();
    expect(abortError).toMatchObject({ category: "internal" });
    expect(String(abortError)).not.toContain(sensitiveAbort);
  });
});

function options(onDefaultMismatch: (report: DefaultMismatchReport) => void) {
  return {
    namespace: "prod/api",
    release: "runtime",
    clientName: "configstore-test",
    instanceId: "stable-test-instance",
    contract: [{ alias: "settings", kind: "parameter" as const, contentType: "json" }],
    onDefaultMismatch,
  };
}

function managedClient(transport: FakeReleaseTransport) {
  return {
    createReleaseLoader(options: ClientReleaseLoaderOptions): Promise<ReleaseLoader> {
      const selectedNamespace = options.namespace?.split("/");
      return Promise.resolve(
        ReleaseLoader._create(transport, {
          ...options,
          namespace: {
            env: selectedNamespace?.[0] ?? namespace.env,
            app: selectedNamespace?.[1] ?? namespace.app,
          },
          clientName: options.clientName ?? "configstore-test",
        }),
      );
    },
  };
}

class FakeReleaseWatchStream implements ReleaseWatchStream {
  readonly sent: WatchReleaseRequest[] = [];
  readonly #events: WatchReleaseEvent[] = [];
  readonly #waiters: Array<(result: IteratorResult<WatchReleaseEvent>) => void> = [];
  #closed = false;

  constructor(signal: AbortSignal) {
    signal.addEventListener("abort", () => this.close(), { once: true });
  }

  send(request: WatchReleaseRequest): Promise<void> {
    if (this.#closed) return Promise.reject(new Error("closed"));
    this.sent.push(request);
    return Promise.resolve();
  }

  push(event: WatchReleaseEvent): void {
    if (this.#closed) return;
    const waiter = this.#waiters.shift();
    if (waiter) waiter({ value: event, done: false });
    else this.#events.push(event);
  }

  closeSend(): void {
    this.close();
  }

  cancel(): void {
    this.close();
  }

  close(): void {
    if (this.#closed) return;
    this.#closed = true;
    for (const waiter of this.#waiters.splice(0)) waiter({ value: undefined, done: true });
  }

  [Symbol.asyncIterator](): AsyncIterator<WatchReleaseEvent> {
    return {
      next: () => {
        const event = this.#events.shift();
        if (event) return Promise.resolve({ value: event, done: false });
        if (this.#closed) return Promise.resolve({ value: undefined, done: true });
        return new Promise((resolve) => this.#waiters.push(resolve));
      },
    };
  }
}

class FakeReleaseTransport implements ReleaseTransport {
  active: GetActiveReleaseResponse;
  stream: FakeReleaseWatchStream | undefined;
  registration: ReleaseWatchRegistration | undefined;
  readonly calls: string[] = [];
  readonly parameters = new Map<string, Parameter>();

  constructor(release: ConfigurationRelease, revision: bigint) {
    this.active = { release, activationRevision: revision, previousVersion: 0n };
    this.install(release);
  }

  getActiveRelease(): Promise<GetActiveReleaseResponse> {
    this.calls.push("active");
    return Promise.resolve(this.active);
  }

  fetchParameter(ref: ResourceRef): Promise<Parameter> {
    this.calls.push(`parameter:${pathOf(ref)}`);
    const parameter = this.parameters.get(pathOf(ref));
    if (!parameter) return Promise.reject(new Error("not found"));
    return Promise.resolve(parameter);
  }

  fetchSecret(): Promise<FetchedSecret> {
    return Promise.reject(new Error("unexpected secret fetch"));
  }

  fetchSecretMetadata(): Promise<SecretMetadata> {
    return Promise.reject(new Error("unexpected secret metadata fetch"));
  }

  watchRelease(registration: ReleaseWatchRegistration, signal: AbortSignal): ReleaseWatchStream {
    this.registration = registration;
    this.stream = new FakeReleaseWatchStream(signal);
    return this.stream;
  }

  activate(release: ConfigurationRelease, revision: bigint): void {
    this.install(release);
    this.active = {
      release,
      activationRevision: revision,
      previousVersion: this.active.release?.version ?? 0n,
    };
    this.stream?.push({
      event: { $case: "activation", value: { release } },
      revision,
    });
  }

  install(release: ConfigurationRelease): void {
    for (const entry of release.entries) {
      if (entry.kind !== "parameter" || !entry.ref) continue;
      const value = entry.metadataJson.match(/^value:(.*)$/su)?.[1] ?? "";
      this.parameters.set(pathOf(entry.ref), {
        ref: entry.ref,
        value,
        contentType: entry.contentType,
        version: entry.version,
        metadataJson: "",
        createdBy: "test",
        createdAtUnixMs: 1n,
        labels: {},
      });
    }
  }
}

function makeRelease(version: bigint, value: string): ConfigurationRelease {
  const release = ConfigurationRelease.create({
    namespace,
    name: "runtime",
    version,
    schemaVersion: 0n,
    entries: [parameterEntry("settings", "settings", version, value)],
    metadataJson: "{}",
  });
  release.digest = deterministicReleaseDigest(release);
  return release;
}

function parameterEntry(
  alias: string,
  key: string,
  version: bigint,
  value: string,
): ConfigurationReleaseEntry {
  return {
    alias,
    kind: "parameter",
    ref: { namespace, key },
    version,
    contentType: "json",
    metadataJson: `value:${value}`,
    parameterDigest: sha256Hex(value),
  };
}

function pathOf(ref: ResourceRef): string {
  if (!ref.namespace) return "";
  return `/${ref.namespace.env}/${ref.namespace.app}/${ref.key}`;
}

function appliedAcknowledgements(transport: FakeReleaseTransport) {
  return (transport.stream?.sent ?? [])
    .flatMap((request) =>
      request.request?.$case === "acknowledgement" ? [request.request.value] : [],
    )
    .filter((acknowledgement) => acknowledgement.state === "applied");
}

async function waitFor(predicate: () => boolean, timeoutMs = 2_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error("condition was not met before timeout");
    await new Promise((resolve) => setTimeout(resolve, 1));
  }
}
