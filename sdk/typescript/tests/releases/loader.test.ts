import { describe, expect, it } from "vitest";
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
} from "../../src/generated/kms.js";
import { deterministicReleaseDigest, sha256Hex } from "../../src/releases/digest.js";
import {
  type FetchedSecret,
  ReleaseLoader,
  type ReleaseTransport,
  type ReleaseWatchStream,
  releaseReconnectBackoff,
  runTypedRelease,
} from "../../src/releases/loader.js";
import { ClassifiedReleaseError } from "../../src/releases/types.js";

const namespace: NamespaceRef = { env: "prod", app: "api" };

class FakeWatchStream implements ReleaseWatchStream {
  readonly sent: WatchReleaseRequest[] = [];
  readonly #events: WatchReleaseEvent[] = [];
  readonly #waiters: Array<(result: IteratorResult<WatchReleaseEvent>) => void> = [];
  #closed = false;

  constructor(
    signal: AbortSignal,
    readonly sendHook?: (request: WatchReleaseRequest) => void | Promise<void>,
  ) {
    signal.addEventListener("abort", () => this.close(), { once: true });
  }

  async send(request: WatchReleaseRequest): Promise<void> {
    if (this.#closed) return Promise.reject(new Error("closed"));
    await this.sendHook?.(request);
    this.sent.push(request);
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

class FakeTransport implements ReleaseTransport {
  active: GetActiveReleaseResponse;
  stream: FakeWatchStream | undefined;
  registration: ReleaseWatchRegistration | undefined;
  readonly calls: string[] = [];
  readonly tokens: string[] = [];
  readonly bindingKeys: string[] = [];
  readonly parameters = new Map<string, Parameter>();
  readonly secrets = new Map<string, FetchedSecret>();
  readonly secretMetadata = new Map<string, SecretMetadata>();
  getActiveReleaseHook:
    | ((
        namespace: NamespaceRef,
        name: string,
        signal?: AbortSignal,
      ) => Promise<GetActiveReleaseResponse>)
    | undefined;
  fetchParameterHook: (() => Promise<void>) | undefined;
  watchReleaseHook:
    | ((registration: ReleaseWatchRegistration, signal: AbortSignal) => Promise<ReleaseWatchStream>)
    | undefined;

  constructor(release: ConfigurationRelease, revision = 1n) {
    this.active = { release, activationRevision: revision, previousVersion: 0n };
  }

  getActiveRelease(
    requestedNamespace: NamespaceRef,
    name: string,
    signal?: AbortSignal,
  ): Promise<GetActiveReleaseResponse> {
    this.calls.push("active");
    if (this.getActiveReleaseHook) {
      return this.getActiveReleaseHook(requestedNamespace, name, signal);
    }
    return Promise.resolve(this.active);
  }

  async fetchParameter(
    ref: ResourceRef,
    _version: bigint,
    _signal?: AbortSignal,
  ): Promise<Parameter> {
    this.calls.push(`parameter:${pathOf(ref)}`);
    await this.fetchParameterHook?.();
    const parameter = this.parameters.get(pathOf(ref));
    if (!parameter) throw new Error("parameter not found");
    return parameter;
  }

  fetchSecret(
    ref: ResourceRef,
    _version: bigint,
    secretToken: string,
    bindingKey: string,
    _signal?: AbortSignal,
  ): Promise<FetchedSecret> {
    this.calls.push(`secret:${pathOf(ref)}`);
    this.tokens.push(secretToken);
    this.bindingKeys.push(bindingKey);
    const secret = this.secrets.get(pathOf(ref));
    if (!secret) return Promise.reject(new Error("secret not found"));
    return Promise.resolve(secret);
  }

  fetchSecretMetadata(ref: ResourceRef): Promise<SecretMetadata> {
    this.calls.push(`secret-metadata:${pathOf(ref)}`);
    const metadata = this.secretMetadata.get(pathOf(ref));
    if (!metadata) return Promise.reject(new Error("secret metadata not found"));
    return Promise.resolve(metadata);
  }

  watchRelease(
    registration: ReleaseWatchRegistration,
    signal: AbortSignal,
  ): ReleaseWatchStream | Promise<ReleaseWatchStream> {
    this.registration = registration;
    if (this.watchReleaseHook) return this.watchReleaseHook(registration, signal);
    this.stream = new FakeWatchStream(signal);
    return this.stream;
  }
}

describe("ReleaseLoader", () => {
  it("keeps release reconnect full jitter above a positive hot-loop floor", () => {
    expect(releaseReconnectBackoff(0, () => 0)).toBe(10);
    expect(releaseReconnectBackoff(1, () => 0)).toBe(10);
    expect(releaseReconnectBackoff(30, () => 0)).toBe(10);
  });

  it("validates the manifest, resolves exact versions, redacts, commits, and acknowledges", async () => {
    const policy = '{"minLength":14}';
    const release = makeRelease(3n, [
      parameterEntry("policy", "policy/password", 7n, policy, "json"),
      secretEntry("database", "database/password", 11n, "string"),
    ]);
    const transport = new FakeTransport(release, 22n);
    transport.parameters.set(
      "/prod/api/policy/password",
      parameterResource("policy/password", 7n, policy, "json"),
    );
    transport.secrets.set(
      "/prod/api/database/password",
      secretResource("database/password", 11n, "super-secret", "string"),
    );
    transport.secretMetadata.set(
      "/prod/api/database/password",
      secretMetadataResource("database/password", 11n, true, true),
    );
    const order: string[] = [];
    const committed = deferred<void>();
    const controller = new AbortController();
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
      instanceId: "stable-instance",
      secretTokenProvider: (alias, path) => {
        order.push(`token:${alias}:${path}`);
        return "local-token";
      },
      bindingKeys: { database: "local-binding-key" },
      validateManifest: (manifest) => {
        order.push("manifest");
        expect(manifest.entry("database")?.path).toBe("/prod/api/database/password");
        expect(JSON.stringify(manifest)).not.toContain("super-secret");
      },
    });

    const run = loader.run((snapshot) => {
      order.push("prepare");
      expect(snapshot.activationRevision).toBe(22n);
      expect(snapshot.parameter("policy")?.value()).toBe(policy);
      expect(snapshot.secret("database")?.stringValue()).toBe("super-secret");
      expect(JSON.stringify(snapshot)).not.toContain("super-secret");
      return {
        commit: () => {
          committed.resolve();
          return undefined;
        },
        abort: () => {
          throw new Error("applied release must not be aborted");
        },
      };
    }, controller.signal);

    await committed.promise;
    await waitFor(() => acknowledgementStates(transport.stream).includes("applied"));
    controller.abort();
    await expect(run).rejects.toMatchObject({ name: "AbortError" });

    expect(order[0]).toBe("manifest");
    expect(order).toContain("token:database:/prod/api/database/password");
    expect(transport.tokens).toEqual(["local-token"]);
    expect(transport.bindingKeys).toEqual(["local-binding-key"]);
    expect(transport.registration).toMatchObject({
      name: "runtime",
      clientName: "unit-test",
      instanceId: "stable-instance",
      lastSeenRevision: 22n,
    });
    expect(acknowledgementStates(transport.stream)).toEqual(
      expect.arrayContaining(["received", "prepared", "applied"]),
    );
    expect(loader.status()).toMatchObject({
      state: "applied",
      appliedVersion: 3n,
      appliedRevision: 22n,
    });
    expect(loader.stats().applied).toBe(1n);
  });

  it("rejects missing exact-version credentials before fetching plaintext", async () => {
    const release = makeRelease(1n, [secretEntry("database", "database/password", 11n, "string")]);
    const transport = new FakeTransport(release);
    transport.secretMetadata.set(
      "/prod/api/database/password",
      secretMetadataResource("database/password", 11n, true, false),
    );
    transport.secrets.set(
      "/prod/api/database/password",
      secretResource("database/password", 11n, "must-not-fetch", "string"),
    );
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });

    await expect(loader.run(() => invalidPrepared())).rejects.toMatchObject({
      category: "token_unavailable",
    });
    expect(transport.calls.filter((call) => call.startsWith("secret:"))).toEqual([]);
  });

  it("does not mistake inherited object properties for binding keys", async () => {
    const release = makeRelease(1n, [secretEntry("toString", "database/password", 11n, "string")]);
    const transport = new FakeTransport(release);
    transport.secretMetadata.set(
      "/prod/api/database/password",
      secretMetadataResource("database/password", 11n, true, false),
    );
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });

    await expect(loader.run(() => invalidPrepared())).rejects.toMatchObject({
      category: "token_unavailable",
    });
    expect(transport.calls.filter((call) => call.startsWith("secret:"))).toEqual([]);
  });

  it("requires an access token independently of binding-key configuration", async () => {
    const release = makeRelease(1n, [secretEntry("database", "database/password", 11n, "string")]);
    const transport = new FakeTransport(release);
    transport.secretMetadata.set(
      "/prod/api/database/password",
      secretMetadataResource("database/password", 11n, false, true),
    );
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
      bindingKeys: { database: "irrelevant-extra-key" },
    });

    await expect(loader.run(() => invalidPrepared())).rejects.toMatchObject({
      category: "token_unavailable",
    });
    expect(transport.calls.filter((call) => call.startsWith("secret:"))).toEqual([]);
  });

  it("rejects a non-string token-provider result before fetching plaintext", async () => {
    const release = makeRelease(1n, [secretEntry("database", "database/password", 11n, "string")]);
    const transport = new FakeTransport(release);
    transport.secretMetadata.set(
      "/prod/api/database/password",
      secretMetadataResource("database/password", 11n, false, true),
    );
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
      secretTokenProvider: (() => 42) as never,
    });

    await expect(loader.run(() => invalidPrepared())).rejects.toMatchObject({
      category: "token_unavailable",
    });
    expect(transport.calls.filter((call) => call.startsWith("secret:"))).toEqual([]);
  });

  it("classifies a rejected supplied binding key as resolution_failed", async () => {
    const release = makeRelease(1n, [secretEntry("database", "database/password", 11n, "string")]);
    const transport = new FakeTransport(release);
    transport.secretMetadata.set(
      "/prod/api/database/password",
      secretMetadataResource("database/password", 11n, true, false),
    );
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
      bindingKeys: { database: "wrong-key" },
    });

    const error = await loader.run(() => invalidPrepared()).catch((reason: unknown) => reason);
    expect(error).toMatchObject({ category: "resolution_failed" });
    expect(String(error)).not.toContain("wrong-key");
    expect(transport.bindingKeys).toEqual(["wrong-key"]);
  });

  it.each([
    ["missing", undefined],
    ["missing version", secretMetadataResource("database/password", 12n)],
    ["duplicate version", duplicateSecretMetadataResource("database/password", 11n)],
    ["disabled", secretMetadataResource("database/password", 11n, false, false, "disabled")],
    ["destroyed", secretMetadataResource("database/password", 11n, false, false, "destroyed")],
    ["expired", secretMetadataResource("database/password", 11n, false, false, "enabled", 99n)],
  ] as const)(
    "rejects %s exact live secret metadata as resolution_failed",
    async (_name, metadata) => {
      const release = makeRelease(1n, [
        secretEntry("database", "database/password", 11n, "string"),
      ]);
      const transport = new FakeTransport(release);
      if (metadata) transport.secretMetadata.set("/prod/api/database/password", metadata);
      const loader = ReleaseLoader._create(transport, {
        namespace,
        name: "runtime",
        clientName: "unit-test",
        now: () => 100,
      });

      await expect(loader.run(() => invalidPrepared())).rejects.toMatchObject({
        category: "resolution_failed",
      });
      expect(transport.calls.filter((call) => call.startsWith("secret:"))).toEqual([]);
    },
  );

  it("rejects mismatched live metadata identity before credential lookup or plaintext fetch", async () => {
    const release = makeRelease(1n, [secretEntry("database", "database/password", 11n, "string")]);
    const transport = new FakeTransport(release);
    const metadata = secretMetadataResource("other/password", 11n, true, true);
    transport.secretMetadata.set("/prod/api/database/password", metadata);
    let tokenCalls = 0;
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
      bindingKeys: { database: "must-not-use" },
      secretTokenProvider: () => {
        tokenCalls += 1;
        return "must-not-use";
      },
    });

    await expect(loader.run(() => invalidPrepared())).rejects.toMatchObject({
      category: "version_mismatch",
    });
    expect(tokenCalls).toBe(0);
    expect(transport.calls.filter((call) => call.startsWith("secret:"))).toEqual([]);
  });

  it.each([
    ["parameter", "digest_mismatch"],
    ["secret", "version_mismatch"],
  ] as const)(
    "rejects a validly digested %s pin whose empty content type differs from the live version",
    async (kind, category) => {
      const entry =
        kind === "parameter"
          ? parameterEntry("value", "value", 1n, "one", "")
          : secretEntry("value", "value", 1n, "");
      const release = makeRelease(1n, [entry]);
      const transport = new FakeTransport(release);
      transport.parameters.set("/prod/api/value", parameterResource("value", 1n, "one", "string"));
      transport.secretMetadata.set("/prod/api/value", secretMetadataResource("value", 1n));
      transport.secrets.set("/prod/api/value", secretResource("value", 1n, "secret", "string"));
      let preparations = 0;
      const loader = ReleaseLoader._create(transport, {
        namespace,
        name: "runtime",
        clientName: "unit-test",
      });

      await expect(
        loader.run(() => {
          preparations += 1;
          return invalidPrepared();
        }),
      ).rejects.toMatchObject({ category });
      expect(preparations).toBe(0);
      expect(loader.stats().applied).toBe(0n);
    },
  );

  it("defensively copies binding keys and ignores extra aliases", async () => {
    const release = makeRelease(1n, [secretEntry("database", "database/password", 11n, "string")]);
    const transport = new FakeTransport(release);
    transport.secretMetadata.set(
      "/prod/api/database/password",
      secretMetadataResource("database/password", 11n, true),
    );
    transport.secrets.set(
      "/prod/api/database/password",
      secretResource("database/password", 11n, "secret", "string"),
    );
    const source = { database: "original-key", extra: "never-transmit" };
    let tokenCalls = 0;
    const controller = new AbortController();
    const committed = deferred<void>();
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
      bindingKeys: source,
      secretTokenProvider: () => {
        tokenCalls += 1;
        return "unused-token";
      },
    });
    source.database = "mutated-key";
    const run = loader.run(
      () => ({
        commit: () => {
          committed.resolve();
          return undefined;
        },
        abort: () => undefined,
      }),
      controller.signal,
    );
    await committed.promise;
    controller.abort();
    await expect(run).rejects.toMatchObject({ name: "AbortError" });

    expect(tokenCalls).toBe(0);
    expect(transport.tokens).toEqual([""]);
    expect(transport.bindingKeys).toEqual(["original-key"]);
    expect(String(loader.status())).not.toContain("original-key");
  });

  it.each([
    ["parameter", parameterEntry("policy", "policy", 1n, "value")],
    ["secret", secretEntry("database", "database/password", 1n, "string")],
  ] as const)("rejects cross-namespace %s pins before any resource fetch", async (_kind, entry) => {
    entry.ref = { namespace: { env: "prod", app: "other" }, key: "policy" };
    const release = makeRelease(1n, [entry]);
    const transport = new FakeTransport(release);
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });

    await expect(loader.run(() => invalidPrepared())).rejects.toMatchObject({
      category: "resolution_failed",
    });
    expect(
      transport.calls.filter((call) => call.startsWith("parameter:") || call.startsWith("secret")),
    ).toEqual([]);
  });

  it("carries a bounded divergence summary on applied acknowledgements only", async () => {
    const policy = '{"minLength":14}';
    const release = makeRelease(3n, [parameterEntry("policy", "policy", 7n, policy, "json")]);
    const transport = new FakeTransport(release, 22n);
    transport.parameters.set("/prod/api/policy", parameterResource("policy", 7n, policy, "json"));
    const controller = new AbortController();
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });

    const run = loader.run(
      () => ({
        commit: () => undefined,
        abort: () => undefined,
        releaseDivergence: () => ({ divergent: true, fieldCount: 70_000.9 }),
      }),
      controller.signal,
    );
    await waitFor(() => acknowledgementStates(transport.stream).includes("applied"));
    controller.abort();
    await expect(run).rejects.toMatchObject({ name: "AbortError" });

    const acks = acknowledgements(transport.stream);
    expect(acks.find((ack) => ack.state === "applied")).toMatchObject({
      appliedDivergent: true,
      divergentFieldCount: 65_535,
    });
    for (const ack of acks.filter((candidate) => candidate.state !== "applied")) {
      expect(ack).toMatchObject({ appliedDivergent: false, divergentFieldCount: 0 });
    }

    for (const reporter of [
      () => ({ divergent: false, fieldCount: 3 }),
      () => {
        throw new Error("reporter failure must not affect the acknowledgement");
      },
      () => "not-a-report",
    ]) {
      const again = new FakeTransport(release, 22n);
      again.parameters.set("/prod/api/policy", parameterResource("policy", 7n, policy, "json"));
      const stop = new AbortController();
      const secondLoader = ReleaseLoader._create(again, {
        namespace,
        name: "runtime",
        clientName: "unit-test",
      });
      const secondRun = secondLoader.run(
        () => ({
          commit: () => undefined,
          abort: () => undefined,
          releaseDivergence: reporter as () => { divergent: boolean; fieldCount: number },
        }),
        stop.signal,
      );
      await waitFor(() => acknowledgementStates(again.stream).includes("applied"));
      stop.abort();
      await expect(secondRun).rejects.toMatchObject({ name: "AbortError" });
      expect(acknowledgements(again.stream).find((ack) => ack.state === "applied")).toMatchObject({
        appliedDivergent: false,
        divergentFieldCount: 0,
      });
    }
  });

  it("runs manifest validation before fetch or token lookup and redacts its failure", async () => {
    const release = makeRelease(1n, [secretEntry("password", "password", 2n, "string")]);
    const transport = new FakeTransport(release);
    let tokenCalls = 0;
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
      secretTokenProvider: () => {
        tokenCalls += 1;
        return "token";
      },
      validateManifest: () => {
        throw new ClassifiedReleaseError("config_contract_mismatch", "sensitive validation detail");
      },
    });

    await expect(loader.run(() => invalidPrepared())).rejects.toMatchObject({
      category: "config_contract_mismatch",
      message: expect.not.stringContaining("sensitive validation detail"),
    });
    expect(tokenCalls).toBe(0);
    expect(transport.calls.filter((call) => call.startsWith("secret:"))).toHaveLength(0);
    expect(rejectedAcknowledgement(transport.stream)).toMatchObject({
      rejectionCategory: "config_contract_mismatch",
      diagnostic: "",
    });
  });

  it("waits for a delayed watch to flush a rejected startup acknowledgement", async () => {
    const release = makeRelease(1n, [parameterEntry("value", "value", 1n, "one")]);
    const transport = new FakeTransport(release);
    transport.parameters.set("/prod/api/value", parameterResource("value", 1n, "one"));
    const watchGate = deferred<ReleaseWatchStream>();
    let watchSignal: AbortSignal | undefined;
    transport.watchReleaseHook = async (_registration, signal) => {
      watchSignal = signal;
      return watchGate.promise;
    };
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
      acknowledgementTimeoutMs: 500,
    });
    let settled = false;
    const run = loader
      .run(() => {
        throw new Error("sensitive startup rejection");
      })
      .finally(() => {
        settled = true;
      });

    await waitFor(() => loader.status().lastFailureCategory === "prepare_failed");
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(settled).toBe(false);
    if (!watchSignal) throw new Error("watch was not started");
    const stream = new FakeWatchStream(watchSignal);
    transport.stream = stream;
    watchGate.resolve(stream);

    await expect(run).rejects.toMatchObject({
      category: "prepare_failed",
      message: expect.not.stringContaining("sensitive startup rejection"),
    });
    expect(rejectedAcknowledgement(stream)).toMatchObject({
      rejectionCategory: "prepare_failed",
      diagnostic: "",
    });
  });

  it("reconnects to retry a failed rejected startup acknowledgement", async () => {
    const release = makeRelease(1n, [parameterEntry("value", "value", 1n, "one")]);
    const transport = new FakeTransport(release);
    transport.parameters.set("/prod/api/value", parameterResource("value", 1n, "one"));
    const streams: FakeWatchStream[] = [];
    let rejectedAttempts = 0;
    transport.watchReleaseHook = async (_registration, signal) => {
      const stream = new FakeWatchStream(signal, (request) => {
        if (
          request.request?.$case === "acknowledgement" &&
          request.request.value.state === "rejected"
        ) {
          rejectedAttempts += 1;
          if (rejectedAttempts === 1) throw new Error("injected acknowledgement failure");
        }
      });
      streams.push(stream);
      transport.stream = stream;
      return stream;
    };
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
      acknowledgementTimeoutMs: 500,
      random: () => 0,
    });

    await expect(
      loader.run(() => {
        throw new Error("reject initial candidate");
      }),
    ).rejects.toMatchObject({ category: "prepare_failed" });

    expect(streams).toHaveLength(2);
    expect(rejectedAttempts).toBe(2);
    expect(rejectedAcknowledgement(streams[1])).toMatchObject({
      rejectionCategory: "prepare_failed",
      diagnostic: "",
    });
  });

  it("defaults nonpositive release reconciliation intervals like the Go SDK", () => {
    const release = makeRelease(1n, [parameterEntry("value", "value", 1n, "one")]);
    const transport = new FakeTransport(release);
    expect(() =>
      ReleaseLoader._create(transport, {
        namespace,
        name: "runtime",
        clientName: "unit-test",
        reconcileIntervalMs: 0,
      }),
    ).not.toThrow();
    expect(() =>
      ReleaseLoader._create(transport, {
        namespace,
        name: "runtime",
        clientName: "unit-test",
        reconcileIntervalMs: -1,
      }),
    ).not.toThrow();
  });

  it("cancels a superseded preparation, aborts it exactly once, and commits only latest", async () => {
    const value1 = "one";
    const value2 = "two";
    const release1 = makeRelease(1n, [parameterEntry("value", "value", 1n, value1)]);
    const release2 = makeRelease(2n, [parameterEntry("value", "value", 2n, value2)]);
    const transport = new FakeTransport(release1, 1n);
    transport.parameters.set("/prod/api/value", parameterResource("value", 1n, value1));
    const controller = new AbortController();
    const firstStarted = deferred<void>();
    const releaseFirst = deferred<void>();
    const latestCommitted = deferred<void>();
    let firstCommits = 0;
    let firstAborts = 0;
    let latestCommits = 0;
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });

    const run = loader.run(async (snapshot) => {
      if (snapshot.version === 1n) {
        firstStarted.resolve();
        await releaseFirst.promise;
        return {
          commit: () => {
            firstCommits += 1;
          },
          abort: () => {
            firstAborts += 1;
          },
        };
      }
      return {
        commit: () => {
          latestCommits += 1;
          latestCommitted.resolve();
        },
        abort: () => undefined,
      };
    }, controller.signal);

    await firstStarted.promise;
    await waitFor(() => transport.stream !== undefined);
    transport.parameters.set("/prod/api/value", parameterResource("value", 2n, value2));
    transport.active = { release: release2, activationRevision: 2n, previousVersion: 1n };
    transport.stream?.push(activationEvent(release2, 2n));
    releaseFirst.resolve();

    await latestCommitted.promise;
    await waitFor(() => acknowledgementStates(transport.stream).includes("applied"));
    controller.abort();
    await expect(run).rejects.toMatchObject({ name: "AbortError" });
    expect({ firstCommits, firstAborts, latestCommits }).toEqual({
      firstCommits: 0,
      firstAborts: 1,
      latestCommits: 1,
    });
    expect(
      acknowledgements(transport.stream).some(
        (ack) => ack.version === 1n && ack.rejectionCategory === "superseded",
      ),
    ).toBe(true);
  });

  it("keeps a cancelled run exclusive until its owned preparation settles", async () => {
    const release = makeRelease(1n, [parameterEntry("value", "value", 1n, "one")]);
    const transport = new FakeTransport(release);
    transport.parameters.set("/prod/api/value", parameterResource("value", 1n, "one"));
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });
    const firstController = new AbortController();
    const firstPrepareStarted = deferred<void>();
    const releaseFirstPrepare = deferred<void>();
    let firstRunSettled = false;
    let firstAborts = 0;
    let activePreparations = 0;
    let maximumActivePreparations = 0;

    const firstRun = loader.run(async () => {
      activePreparations += 1;
      maximumActivePreparations = Math.max(maximumActivePreparations, activePreparations);
      firstPrepareStarted.resolve();
      await releaseFirstPrepare.promise;
      activePreparations -= 1;
      return {
        commit: () => {
          throw new Error("cancelled preparation must not commit");
        },
        abort: () => {
          firstAborts += 1;
        },
      };
    }, firstController.signal);
    void firstRun.then(
      () => {
        firstRunSettled = true;
      },
      () => {
        firstRunSettled = true;
      },
    );

    await firstPrepareStarted.promise;
    firstController.abort(new DOMException("stop first run", "AbortError"));
    await new Promise<void>((resolve) => setImmediate(resolve));
    expect(firstRunSettled).toBe(false);

    let overlappingPreparations = 0;
    await expect(
      loader.run(() => {
        overlappingPreparations += 1;
        return invalidPrepared();
      }),
    ).rejects.toThrow(/already running/);
    expect(overlappingPreparations).toBe(0);

    releaseFirstPrepare.resolve();
    await expect(firstRun).rejects.toMatchObject({ name: "AbortError" });
    expect({ firstAborts, activePreparations, maximumActivePreparations }).toEqual({
      firstAborts: 1,
      activePreparations: 0,
      maximumActivePreparations: 1,
    });

    const secondController = new AbortController();
    const secondCommitted = deferred<void>();
    let sequentialPreparations = 0;
    const secondRun = loader.run(() => {
      activePreparations += 1;
      maximumActivePreparations = Math.max(maximumActivePreparations, activePreparations);
      sequentialPreparations += 1;
      activePreparations -= 1;
      return {
        commit: () => {
          secondCommitted.resolve();
          return undefined;
        },
        abort: () => undefined,
      };
    }, secondController.signal);

    await secondCommitted.promise;
    secondController.abort(new DOMException("stop second run", "AbortError"));
    await expect(secondRun).rejects.toMatchObject({ name: "AbortError" });
    expect({ sequentialPreparations, maximumActivePreparations }).toEqual({
      sequentialPreparations: 1,
      maximumActivePreparations: 1,
    });
  });

  it("keeps last-known-good after a later preparation rejection", async () => {
    const release1 = makeRelease(1n, [parameterEntry("value", "value", 1n, "one")]);
    const release2 = makeRelease(2n, [parameterEntry("value", "value", 2n, "two")]);
    const transport = new FakeTransport(release1, 1n);
    transport.parameters.set("/prod/api/value", parameterResource("value", 1n, "one"));
    const controller = new AbortController();
    const firstCommitted = deferred<void>();
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });
    const run = loader.run((snapshot) => {
      if (snapshot.version === 2n) {
        throw new ClassifiedReleaseError("default_mismatch", "must never leave process");
      }
      return {
        commit: () => {
          firstCommitted.resolve();
          return undefined;
        },
        abort: () => undefined,
      };
    }, controller.signal);

    await firstCommitted.promise;
    await waitFor(() => loader.status().state === "applied");
    transport.parameters.set("/prod/api/value", parameterResource("value", 2n, "two"));
    transport.active = { release: release2, activationRevision: 2n, previousVersion: 1n };
    transport.stream?.push(activationEvent(release2, 2n));
    await waitFor(() => loader.status().lastFailureCategory === "default_mismatch");

    expect(loader.status()).toMatchObject({
      state: "rejected",
      appliedVersion: 1n,
      appliedRevision: 1n,
      lastFailureCategory: "default_mismatch",
    });
    expect(loader.stats().rejected.default_mismatch).toBe(1n);
    expect(rejectedAcknowledgement(transport.stream)?.diagnostic).toBe("");
    controller.abort();
    await expect(run).rejects.toMatchObject({ name: "AbortError" });
  });

  it("rejects a later bad digest, acknowledges it, and preserves last-known-good", async () => {
    const release1 = makeRelease(1n, [parameterEntry("value", "value", 1n, "one")]);
    const release2 = makeRelease(2n, [parameterEntry("value", "value", 2n, "two")]);
    release2.digest = "0".repeat(64);
    const transport = new FakeTransport(release1, 1n);
    transport.parameters.set("/prod/api/value", parameterResource("value", 1n, "one"));
    const controller = new AbortController();
    const firstCommitted = deferred<void>();
    let preparations = 0;
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });
    const run = loader.run(() => {
      preparations += 1;
      return {
        commit: () => {
          firstCommitted.resolve();
          return undefined;
        },
        abort: () => undefined,
      };
    }, controller.signal);

    await firstCommitted.promise;
    await waitFor(() => loader.status().state === "applied");
    transport.parameters.set("/prod/api/value", parameterResource("value", 2n, "two"));
    transport.active = { release: release2, activationRevision: 2n, previousVersion: 1n };
    transport.stream?.push(activationEvent(release2, 2n));
    await waitFor(() =>
      acknowledgements(transport.stream).some(
        (acknowledgement) =>
          acknowledgement.version === 2n && acknowledgement.rejectionCategory === "digest_mismatch",
      ),
    );

    expect(preparations).toBe(1);
    expect(loader.status()).toMatchObject({
      state: "rejected",
      appliedVersion: 1n,
      appliedRevision: 1n,
      lastFailureCategory: "digest_mismatch",
    });
    expect(loader.stats()).toMatchObject({ applied: 1n });
    expect(loader.stats().rejected.digest_mismatch).toBe(1n);
    expect(rejectedAcknowledgement(transport.stream)).toMatchObject({
      version: 2n,
      activationRevision: 2n,
      rejectionCategory: "digest_mismatch",
      diagnostic: "",
    });

    controller.abort();
    await expect(run).rejects.toMatchObject({ name: "AbortError" });
  });

  it("fails closed when commit throws without attempting an unsafe abort", async () => {
    const release = makeRelease(1n, [parameterEntry("value", "value", 1n, "one")]);
    const transport = new FakeTransport(release);
    transport.parameters.set("/prod/api/value", parameterResource("value", 1n, "one"));
    let aborts = 0;
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });

    const error = await loader
      .run(() => ({
        commit: () => {
          throw new Error("sensitive partial commit detail");
        },
        abort: () => {
          aborts += 1;
        },
      }))
      .catch((reason: unknown) => reason);

    expect(error).toMatchObject({
      message: expect.stringContaining("commit() threw; commit must be infallible"),
    });
    expect(String(error)).not.toContain("sensitive partial commit detail");

    expect(aborts).toBe(0);
    expect(loader.status()).toMatchObject({
      state: "rejected",
      appliedVersion: 0n,
      lastFailureCategory: "internal",
    });
    expect(loader.stats().rejected.internal).toBe(1n);
    expect(acknowledgementStates(transport.stream)).not.toContain("applied");
  });

  it("fails closed and observes a Promise returned by commit", async () => {
    const release = makeRelease(1n, [parameterEntry("value", "value", 1n, "one")]);
    const transport = new FakeTransport(release);
    transport.parameters.set("/prod/api/value", parameterResource("value", 1n, "one"));
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });
    const sensitiveFailure = "sensitive async commit failure";

    const error = await loader
      .run(() => ({
        commit: (() => Promise.reject(new Error(sensitiveFailure))) as unknown as () => undefined,
        abort: () => undefined,
      }))
      .catch((reason: unknown) => reason);
    await Promise.resolve();

    expect(error).toMatchObject({
      message: expect.stringContaining("commit() must return undefined synchronously"),
    });
    expect(String(error)).not.toContain(sensitiveFailure);
    expect(loader.status()).toMatchObject({
      state: "rejected",
      appliedVersion: 0n,
      lastFailureCategory: "internal",
    });
    expect(loader.stats().rejected.internal).toBe(1n);
    expect(acknowledgementStates(transport.stream)).not.toContain("applied");
  });

  it("aborts a prepared candidate exactly once when interrupted before commit", async () => {
    const release = makeRelease(1n, [parameterEntry("value", "value", 1n, "one")]);
    const transport = new FakeTransport(release);
    transport.parameters.set("/prod/api/value", parameterResource("value", 1n, "one"));
    const activeCheckStarted = deferred<void>();
    let activeCalls = 0;
    transport.getActiveReleaseHook = (_requestedNamespace, _name, signal) => {
      activeCalls += 1;
      if (activeCalls === 1) return Promise.resolve(transport.active);
      activeCheckStarted.resolve();
      return rejectOnAbort(signal);
    };
    const controller = new AbortController();
    let commits = 0;
    let aborts = 0;
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });
    const run = loader.run(
      () => ({
        commit: () => {
          commits += 1;
        },
        abort: () => {
          aborts += 1;
        },
      }),
      controller.signal,
    );

    await activeCheckStarted.promise;
    controller.abort(new DOMException("test interruption", "AbortError"));
    await expect(run).rejects.toMatchObject({ name: "AbortError" });
    expect({ commits, aborts }).toEqual({ commits: 0, aborts: 1 });
    expect(loader.status().appliedVersion).toBe(0n);
  });

  it("surfaces an abort contract violation as a redacted fatal error", async () => {
    const release1 = makeRelease(1n, [parameterEntry("value", "value", 1n, "one")]);
    const release2 = makeRelease(2n, [parameterEntry("value", "value", 2n, "two")]);
    const transport = new FakeTransport(release1, 1n);
    transport.parameters.set("/prod/api/value", parameterResource("value", 1n, "one"));
    let activeCalls = 0;
    transport.getActiveReleaseHook = () => {
      activeCalls += 1;
      return Promise.resolve(
        activeCalls === 1
          ? transport.active
          : { release: release2, activationRevision: 2n, previousVersion: 1n },
      );
    };
    let commits = 0;
    let aborts = 0;
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });

    const error = await loader
      .run(() => ({
        commit: () => {
          commits += 1;
        },
        abort: () => {
          aborts += 1;
          throw new Error("sensitive rollback detail");
        },
      }))
      .catch((reason: unknown) => reason);

    expect(error).toMatchObject({
      message: expect.stringContaining("abort() threw; abort must be infallible"),
    });
    expect(String(error)).not.toContain("sensitive rollback detail");
    expect({ commits, aborts }).toEqual({ commits: 0, aborts: 1 });
    expect(loader.status().lastFailureCategory).toBe("internal");
    expect(loader.stats().rejected.internal).toBe(1n);
  });

  it("fails closed and observes a Promise returned by abort", async () => {
    const release1 = makeRelease(1n, [parameterEntry("value", "value", 1n, "one")]);
    const release2 = makeRelease(2n, [parameterEntry("value", "value", 2n, "two")]);
    const transport = new FakeTransport(release1, 1n);
    transport.parameters.set("/prod/api/value", parameterResource("value", 1n, "one"));
    let activeCalls = 0;
    transport.getActiveReleaseHook = () => {
      activeCalls += 1;
      return Promise.resolve(
        activeCalls === 1
          ? transport.active
          : { release: release2, activationRevision: 2n, previousVersion: 1n },
      );
    };
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });
    const sensitiveFailure = "sensitive async abort failure";

    const error = await loader
      .run(() => ({
        commit: () => undefined,
        abort: (() => Promise.reject(new Error(sensitiveFailure))) as unknown as () => undefined,
      }))
      .catch((reason: unknown) => reason);
    await Promise.resolve();

    expect(error).toMatchObject({
      message: expect.stringContaining("abort() must return undefined synchronously"),
    });
    expect(String(error)).not.toContain(sensitiveFailure);
    expect(loader.status().lastFailureCategory).toBe("internal");
    expect(loader.stats().rejected.internal).toBe(1n);
    expect(acknowledgementStates(transport.stream)).not.toContain("applied");
  });

  it("runTypedRelease decodes before typed preparation and commits the result", async () => {
    const release = makeRelease(1n, [parameterEntry("value", "value", 1n, "41")]);
    const transport = new FakeTransport(release);
    transport.parameters.set("/prod/api/value", parameterResource("value", 1n, "41"));
    const controller = new AbortController();
    const committed = deferred<void>();
    const calls: string[] = [];
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
    });
    const run = runTypedRelease(
      loader,
      (snapshot) => {
        calls.push("decode");
        return Number(snapshot.parameter("value")?.value()) + 1;
      },
      (value, signal) => {
        calls.push(`prepare:${value}:${signal.aborted}`);
        return {
          commit: () => {
            calls.push("commit");
            committed.resolve();
          },
          abort: () => undefined,
        };
      },
      controller.signal,
    );

    await committed.promise;
    expect(calls).toEqual(["decode", "prepare:42:false", "commit"]);
    controller.abort();
    await expect(run).rejects.toMatchObject({ name: "AbortError" });
  });

  it("bounds concurrent resource fetches", async () => {
    const entries = Array.from({ length: 8 }, (_, index) =>
      parameterEntry(`p${index}`, `p${index}`, 1n, `value-${index}`),
    );
    const release = makeRelease(1n, entries);
    const transport = new FakeTransport(release);
    for (let index = 0; index < entries.length; index += 1) {
      transport.parameters.set(
        `/prod/api/p${index}`,
        parameterResource(`p${index}`, 1n, `value-${index}`),
      );
    }
    let activeFetches = 0;
    let maximumFetches = 0;
    transport.fetchParameterHook = async () => {
      activeFetches += 1;
      maximumFetches = Math.max(maximumFetches, activeFetches);
      await new Promise((resolve) => setTimeout(resolve, 2));
      activeFetches -= 1;
    };
    const controller = new AbortController();
    const committed = deferred<void>();
    const loader = ReleaseLoader._create(transport, {
      namespace,
      name: "runtime",
      clientName: "unit-test",
      maxConcurrentFetches: 3,
    });
    const run = loader.run(
      () => ({
        commit: () => {
          committed.resolve();
          return undefined;
        },
        abort: () => undefined,
      }),
      controller.signal,
    );
    await committed.promise;
    expect(maximumFetches).toBe(3);
    controller.abort();
    await expect(run).rejects.toMatchObject({ name: "AbortError" });
  });
});

function makeRelease(version: bigint, entries: ConfigurationReleaseEntry[]): ConfigurationRelease {
  const release = ConfigurationRelease.create({
    namespace,
    name: "runtime",
    version,
    schemaVersion: 0n,
    entries,
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
  contentType = "string",
): ConfigurationReleaseEntry {
  return {
    alias,
    kind: "parameter",
    ref: { namespace, key },
    version,
    contentType,
    metadataJson: "",
    parameterDigest: sha256Hex(value),
  };
}

function secretEntry(
  alias: string,
  key: string,
  version: bigint,
  contentType: string,
): ConfigurationReleaseEntry {
  return {
    alias,
    kind: "secret",
    ref: { namespace, key },
    version,
    contentType,
    metadataJson: "",
    parameterDigest: "",
  };
}

function secretMetadataResource(
  key: string,
  version: bigint,
  bound = false,
  hasAccessToken = false,
  state = "enabled",
  expiresAtUnixMs = 0n,
): SecretMetadata {
  return {
    ref: { namespace, key },
    contentType: "string",
    bound,
    hasAccessToken,
    metadataJson: "",
    createdAtUnixMs: 1n,
    updatedAtUnixMs: 1n,
    labels: { current: version },
    versions: [
      {
        version,
        state,
        createdBy: "test",
        createdAtUnixMs: 1n,
        destroyedAtUnixMs: 0n,
        expiresAtUnixMs,
        metadataJson: "",
        bound,
        hasAccessToken,
      },
    ],
  };
}

function duplicateSecretMetadataResource(key: string, version: bigint): SecretMetadata {
  const metadata = secretMetadataResource(key, version);
  const first = metadata.versions[0];
  if (!first) throw new Error("secret metadata fixture omitted its version");
  metadata.versions.push({ ...first });
  return metadata;
}

function parameterResource(
  key: string,
  version: bigint,
  value: string,
  contentType = "string",
): Parameter {
  return {
    ref: { namespace, key },
    value,
    contentType,
    version,
    metadataJson: "",
    createdBy: "test",
    createdAtUnixMs: 1n,
    labels: {},
  };
}

function secretResource(
  key: string,
  version: bigint,
  value: string,
  contentType: string,
): FetchedSecret {
  return { ref: { namespace, key }, version, value: Buffer.from(value), contentType };
}

function activationEvent(release: ConfigurationRelease, revision: bigint): WatchReleaseEvent {
  return {
    event: { $case: "activation", value: { release } },
    revision,
  };
}

function pathOf(ref: ResourceRef): string {
  if (!ref.namespace) return "";
  return `/${ref.namespace.env}/${ref.namespace.app}/${ref.key}`;
}

function acknowledgements(stream: FakeWatchStream | undefined) {
  return (stream?.sent ?? []).flatMap((request) =>
    request.request?.$case === "acknowledgement" ? [request.request.value] : [],
  );
}

function acknowledgementStates(stream: FakeWatchStream | undefined): string[] {
  return acknowledgements(stream).map((acknowledgement) => acknowledgement.state);
}

function rejectedAcknowledgement(stream: FakeWatchStream | undefined) {
  return acknowledgements(stream).find((acknowledgement) => acknowledgement.state === "rejected");
}

function invalidPrepared() {
  return { commit: () => undefined, abort: () => undefined };
}

function deferred<T = void>(): {
  readonly promise: Promise<T>;
  resolve(value: T | PromiseLike<T>): void;
} {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((onResolve) => {
    resolve = onResolve;
  });
  return { promise, resolve };
}

function rejectOnAbort(signal: AbortSignal | undefined): Promise<never> {
  return new Promise((_, reject) => {
    if (!signal) {
      reject(new Error("abort signal is required"));
      return;
    }
    const abort = (): void => reject(signal.reason);
    if (signal.aborted) abort();
    else signal.addEventListener("abort", abort, { once: true });
  });
}

async function waitFor(predicate: () => boolean, timeoutMs = 2_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error("condition was not met before timeout");
    await new Promise((resolve) => setTimeout(resolve, 1));
  }
}
