import type { ClientReleaseLoaderOptions } from "../client.js";
import type { ReleaseLoader, ReleaseLoaderOptions } from "../releases/loader.js";
import type {
  PreparedRelease,
  ReleaseLoaderStatus,
  ReleaseRejectionCategory,
  ReleaseSnapshot,
} from "../releases/types.js";
import { cloneConfig } from "./clone.js";
import { type ContractEntry, createManifestValidator, validateContract } from "./contract.js";
import {
  AppliedReport,
  CandidateError,
  CandidateRejectionReport,
  candidateErrorPaths,
  DefaultMismatchReport,
  type FieldChange,
  type FieldDifference,
} from "./errors.js";
import { ReleaseIdentity } from "./snapshot.js";

export interface ManagedPreparedCandidate {
  /** Generated binding's atomic active-generation swap. Must be infallible. */
  readonly publish: () => undefined;
  /** Releases candidate-owned resources. Must be infallible and is called at most once. */
  readonly abort?: () => undefined;
  /** Complete non-secret comparison against source defaults. */
  readonly defaultDifferences?: readonly FieldDifference[];
  /** Generated canonical fields whose effective value/pin changed after startup. */
  readonly restartRequiredFields?: readonly string[];
  /**
   * Fields that differ from the previously applied generation; secret entries
   * must be path-only. Ignored for the initial generation.
   */
  readonly changed?: readonly FieldChange[];
  /** Canonical non-secret parameter group documents keyed by alias. Never secrets. */
  readonly groups?: Readonly<Record<string, string>>;
}

export type PrepareManagedCandidate = (
  snapshot: ReleaseSnapshot,
  signal: AbortSignal,
) => ManagedPreparedCandidate | Promise<ManagedPreparedCandidate>;

/**
 * Application-owned observers of a managed store. `onDefaultMismatch` is
 * required; the others are optional. Every callback runs synchronously on the
 * loader's candidate path and must not block; thrown errors are isolated and
 * returned promises are discarded. `consoleCallbacks` is a ready-made
 * implementation.
 */
export interface Callbacks {
  /**
   * Reports every candidate whose non-secret values differ from source
   * defaults, at startup and on every reload. The candidate is still applied;
   * the report is the signal to reconcile code and KMS. Must be synchronous.
   */
  readonly onDefaultMismatch: (report: DefaultMismatchReport) => void;
  /**
   * Fires after each generation is published, including the initial one,
   * carrying the fields that changed since the previously applied generation.
   */
  readonly onApplied?: (report: AppliedReport) => void;
  /** Optional, value-free local diagnostics. Callback failures are isolated. */
  readonly onCandidateRejected?: (report: CandidateRejectionReport) => void;
}

export interface ManagedConfigOptions
  extends Omit<
      ClientReleaseLoaderOptions,
      "name" | "namespace" | "clientName" | "validateManifest"
    >,
    Callbacks {
  /** Release name within the client's home or explicitly selected namespace. */
  readonly release: string;
  /** Optional env/app override. Omit to use the KmsClient home namespace. */
  readonly namespace?: string;
  /** Optional acknowledgement identity override. */
  readonly clientName?: string;
  readonly contract: readonly ContractEntry[];
}

/** Structural KmsClient subset; transport internals remain private. */
export interface ManagedReleaseClient {
  createReleaseLoader(options: ClientReleaseLoaderOptions): Promise<ReleaseLoader>;
}

export interface ManagedConfigStatus {
  readonly state: ReleaseLoaderStatus["state"];
  readonly ready: boolean;
  readonly observed: ReleaseIdentity;
  readonly applied: ReleaseIdentity;
  readonly defaultDivergent: boolean;
  readonly lastRejectionCategory?: ReleaseRejectionCategory;
  readonly lastFailureAt?: Date;
  readonly reconnects: bigint;
}

export interface ManagedConfigStats {
  readonly candidates: bigint;
  readonly applied: bigint;
  readonly rejected: Readonly<Record<string, bigint>>;
  readonly reconnects: bigint;
  readonly defaultDivergent: boolean;
  readonly appliedReleaseVersion: bigint;
  readonly appliedActivationRevision: bigint;
}

interface PolicyOptions extends Callbacks {
  readonly name: string;
}

interface Completion {
  readonly error?: unknown;
}

/**
 * Managed drift/restart policy around ReleaseLoader. Construct with
 * startManagedConfig so exact manifest validation runs before any resource
 * fetch and startup does not return before generated state is publishable.
 */
export class ManagedConfigManager {
  readonly #loader: ReleaseLoader;
  readonly #options: PolicyOptions;
  readonly #prepare: PrepareManagedCandidate;
  readonly #readySignal = deferred<void>();
  #completion: Promise<Completion> | undefined;
  #ready = false;
  #observed = new ReleaseIdentity();
  #applied = new ReleaseIdentity();
  #divergent = false;
  #lastReportedKey = "";
  #lastRejectedKey = "";

  /** @internal Use startManagedConfig. */
  constructor(loader: ReleaseLoader, options: PolicyOptions, prepare: PrepareManagedCandidate) {
    this.#loader = loader;
    this.#options = options;
    this.#prepare = prepare;
  }

  /** @internal Records safe unresolved identity before exact contract validation. */
  observeManifest(manifest: Parameters<ReturnType<typeof createManifestValidator>>[0]): void {
    this.#observed = ReleaseIdentity.from(manifest);
  }

  /** @internal Reports classified pre-resolution contract rejection locally. */
  rejectManifest(
    manifest: Parameters<ReturnType<typeof createManifestValidator>>[0],
    error: unknown,
  ): void {
    this.#notifyCandidateRejected(ReleaseIdentity.from(manifest), error);
  }

  /** @internal Starts the one owned loader run. */
  start(signal?: AbortSignal): void {
    if (this.#completion) throw new Error("configstore: manager is already started");
    const run = this.#loader.run((snapshot, candidateSignal) => {
      return this.#prepareSnapshot(snapshot, candidateSignal);
    }, signal);
    this.#completion = run.then(
      () => ({}),
      (error: unknown) => ({ error }),
    );
  }

  /** @internal Wait for the first atomic publication or the loader's startup failure. */
  async waitUntilReady(): Promise<void> {
    const completion = this.#completion;
    if (!completion) throw new Error("configstore: manager is not started");
    const outcome = await Promise.race([
      this.#readySignal.promise.then(() => ({ ready: true }) as const),
      completion.then((result) => ({ ready: false, result }) as const),
    ]);
    if (outcome.ready) return;
    throw (
      outcome.result.error ?? new Error("configstore: release loader stopped before publication")
    );
  }

  /** Request background loader shutdown. */
  stop(reason?: unknown): void {
    this.#loader.stop(reason);
  }

  /** Wait for loader termination. Cancellation after readiness is normal shutdown. */
  async wait(): Promise<void> {
    const completion = this.#completion;
    if (!completion) throw new Error("configstore: manager is not started");
    const { error } = await completion;
    if (error === undefined || (this.#ready && isAbortError(error))) return;
    throw error;
  }

  /** Redacted point-in-time lifecycle status. */
  status(): ManagedConfigStatus {
    const loaderStatus = this.#loader.status();
    let observed = this.#observed;
    if (
      observed.version !== loaderStatus.observedVersion ||
      observed.activationRevision !== loaderStatus.observedRevision
    ) {
      observed = new ReleaseIdentity({
        name: this.#options.name,
        version: loaderStatus.observedVersion,
        activationRevision: loaderStatus.observedRevision,
      });
    }
    return Object.freeze({
      state: loaderStatus.state,
      ready: this.#ready,
      observed,
      applied: this.#applied,
      defaultDivergent: this.#divergent,
      ...(loaderStatus.lastFailureCategory
        ? { lastRejectionCategory: loaderStatus.lastFailureCategory }
        : {}),
      ...(loaderStatus.lastFailureAt
        ? { lastFailureAt: new Date(loaderStatus.lastFailureAt) }
        : {}),
      reconnects: loaderStatus.reconnects,
    });
  }

  /** Fresh bounded metrics counters. */
  stats(): ManagedConfigStats {
    const loaderStats = this.#loader.stats();
    return Object.freeze({
      candidates: loaderStats.candidates,
      applied: loaderStats.applied,
      rejected: Object.freeze({ ...loaderStats.rejected }),
      reconnects: loaderStats.reconnects,
      defaultDivergent: this.#divergent,
      appliedReleaseVersion: this.#applied.version,
      appliedActivationRevision: this.#applied.activationRevision,
    });
  }

  async #prepareSnapshot(snapshot: ReleaseSnapshot, signal: AbortSignal): Promise<PreparedRelease> {
    const identity = ReleaseIdentity.from(snapshot);
    this.#observed = identity;
    const startup = !this.#ready;

    let candidate: ManagedPreparedCandidate;
    try {
      candidate = await this.#prepare(snapshot, signal);
    } catch (error) {
      this.#notifyCandidateRejected(identity, error);
      throw error;
    }
    const abort = once(candidate?.abort ?? (() => undefined));
    if (!candidate || typeof candidate.publish !== "function") {
      this.#abortOrInternal(abort, identity);
      const error = new CandidateError(
        "config_validation_failed",
        new Error("configstore: prepared candidate publish is required"),
      );
      this.#notifyCandidateRejected(identity, error);
      throw error;
    }
    if (signal.aborted) {
      this.#abortOrInternal(abort, identity);
      throw abortReason(signal);
    }

    let differences: FieldDifference[];
    let restartFields: string[];
    let changes: FieldChange[];
    let groups: Record<string, string>;
    try {
      differences = cloneDifferences(candidate.defaultDifferences ?? []);
      restartFields = cloneRestartFields(candidate.restartRequiredFields ?? []);
      // Always validate the binding's contract; the initial generation has no
      // previous generation, so its change list is discarded rather than reported.
      const candidateChanges = cloneChanges(candidate.changed ?? []);
      changes = startup ? [] : candidateChanges;
      groups = cloneGroups(candidate.groups ?? {});
    } catch (cause) {
      this.#abortOrInternal(abort, identity);
      const error = new CandidateError("internal", cause);
      this.#notifyCandidateRejected(identity, error);
      throw error;
    }
    if (!startup && restartFields.length > 0) {
      this.#abortOrInternal(abort, identity);
      const error = new CandidateError(
        "restart_required",
        new Error("configstore: candidate changes restart-required fields"),
        restartFields,
      );
      this.#notifyCandidateRejected(identity, error);
      throw error;
    }

    // Divergence from source defaults is reported, never refused: a process
    // must be able to restart onto whatever release is active. The report is
    // the operator's signal to reconcile code and KMS.
    const phase = startup ? "startup" : "runtime";
    const divergent = differences.length > 0;
    if (divergent) {
      const report = new DefaultMismatchReport(phase, "error", identity, differences);
      this.#reportOnce(identity, report);
    }

    return {
      commit: () => {
        const returned: unknown = candidate.publish();
        const contractError = synchronousCallbackError(
          "configstore: prepared candidate publish",
          returned,
        );
        if (contractError) throw contractError;
        this.#applied = identity;
        this.#divergent = divergent;
        this.#ready = true;
        // A clean generation forgets the last reported identity so rolling back
        // onto a previously reported divergent release is reported again.
        if (!divergent) this.#lastReportedKey = "";
        this.#readySignal.resolve();
        this.#notifyApplied(phase, identity, divergent, changes, groups);
        return undefined;
      },
      abort,
      releaseDivergence: () => ({ divergent, fieldCount: differences.length }),
    };
  }

  #abortOrInternal(abort: () => undefined, identity: ReleaseIdentity): void {
    try {
      const returned: unknown = abort();
      const contractError = synchronousCallbackError(
        "configstore: prepared candidate abort",
        returned,
      );
      if (contractError) throw contractError;
    } catch (cause) {
      const error = new CandidateError("internal", cause);
      this.#notifyCandidateRejected(identity, error);
      throw error;
    }
  }

  #notifyApplied(
    phase: "startup" | "runtime",
    identity: ReleaseIdentity,
    divergent: boolean,
    changes: readonly FieldChange[],
    groups: Readonly<Record<string, string>>,
  ): void {
    const callback = this.#options.onApplied;
    if (!callback) return;
    // commit must stay infallible for the loader; a callback failure must
    // never turn a published generation into a fatal loader failure.
    try {
      const report = new AppliedReport(phase, identity, divergent, changes, groups);
      const result = callUnknown(callback, report);
      discardPromise(result);
    } catch {
      // Diagnostics cannot affect an already published generation.
    }
  }

  #notifyCandidateRejected(identity: ReleaseIdentity, error: unknown): void {
    if (!(error instanceof CandidateError) || !this.#options.onCandidateRejected) return;
    const key = identity.dedupeKey();
    if (this.#lastRejectedKey === key) return;
    this.#lastRejectedKey = key;
    const report = new CandidateRejectionReport(
      error.category,
      identity,
      candidateErrorPaths(error),
    );
    try {
      const result = callUnknown(this.#options.onCandidateRejected, report);
      discardPromise(result);
    } catch {
      // Diagnostics cannot affect candidate admission or reconciliation.
    }
  }

  /**
   * Deliver a mismatch report at most once per release identity. The callback
   * is an observer: a throw or a returned promise is isolated and never
   * influences candidate admission, so a broken logger cannot refuse startup.
   */
  #reportOnce(identity: ReleaseIdentity, report: DefaultMismatchReport): void {
    const key = identity.dedupeKey();
    if (this.#lastReportedKey === key) return;
    this.#lastReportedKey = key;
    try {
      const result = callUnknown(this.#options.onDefaultMismatch, report);
      if (isPromiseLike(result)) discardPromise(result);
    } catch {
      // Isolated: observers never affect admission.
    }
  }
}

/**
 * Validate the generated contract, start ReleaseLoader, and resolve only after
 * the initial complete candidate has been atomically published.
 */
export async function startManagedConfig(
  client: ManagedReleaseClient,
  options: ManagedConfigOptions,
  prepare: PrepareManagedCandidate,
  signal?: AbortSignal,
): Promise<ManagedConfigManager> {
  if (!client || typeof client.createReleaseLoader !== "function") {
    throw new TypeError("configstore: KmsClient-compatible release client is required");
  }
  if (typeof prepare !== "function")
    throw new TypeError("configstore: prepare callback is required");
  if (typeof options?.onDefaultMismatch !== "function") {
    throw new TypeError("configstore: onDefaultMismatch callback is required");
  }
  if (options.onApplied !== undefined && typeof options.onApplied !== "function") {
    throw new TypeError("configstore: onApplied must be a function when provided");
  }
  const release = options.release?.trim();
  if (!release) throw new TypeError("configstore: release is required");
  const contract = validateContract(options.contract);
  let manager: ManagedConfigManager | undefined;
  const baseManifestValidator = createManifestValidator(contract);
  const validateManifest: NonNullable<ReleaseLoaderOptions["validateManifest"]> = (
    manifest,
    candidateSignal,
  ) => {
    manager?.observeManifest(manifest);
    try {
      return baseManifestValidator(manifest, candidateSignal);
    } catch (error) {
      manager?.rejectManifest(manifest, error);
      throw error;
    }
  };
  const loader = await client.createReleaseLoader(
    copyLoaderOptions(options, release, validateManifest),
  );
  manager = new ManagedConfigManager(
    loader,
    {
      name: release,
      onDefaultMismatch: options.onDefaultMismatch,
      ...(options.onApplied ? { onApplied: options.onApplied } : {}),
      ...(options.onCandidateRejected ? { onCandidateRejected: options.onCandidateRejected } : {}),
    },
    prepare,
  );
  manager.start(signal);
  await manager.waitUntilReady();
  return manager;
}

function copyLoaderOptions(
  options: ManagedConfigOptions,
  release: string,
  validateManifest: NonNullable<ReleaseLoaderOptions["validateManifest"]>,
): ClientReleaseLoaderOptions {
  return {
    name: release,
    ...(options.namespace ? { namespace: options.namespace } : {}),
    ...(options.clientName ? { clientName: options.clientName } : {}),
    ...(options.instanceId ? { instanceId: options.instanceId } : {}),
    ...(options.reconcileIntervalMs !== undefined
      ? { reconcileIntervalMs: options.reconcileIntervalMs }
      : {}),
    ...(options.maxConcurrentFetches !== undefined
      ? { maxConcurrentFetches: options.maxConcurrentFetches }
      : {}),
    ...(options.secretTokenProvider ? { secretTokenProvider: options.secretTokenProvider } : {}),
    validateManifest,
    ...(options.now ? { now: options.now } : {}),
    ...(options.random ? { random: options.random } : {}),
  };
}

function cloneDifferences(differences: readonly FieldDifference[]): FieldDifference[] {
  if (!Array.isArray(differences)) {
    throw new TypeError("configstore: default differences must be an array");
  }
  return differences.map((difference) => ({
    path: requireString(difference?.path, "default difference path"),
    expected: cloneConfig(difference.expected),
    actual: cloneConfig(difference.actual),
  }));
}

function cloneChanges(changes: readonly FieldChange[]): FieldChange[] {
  if (!Array.isArray(changes)) {
    throw new TypeError("configstore: changed fields must be an array");
  }
  return changes.map((change) => ({
    path: requireString(change?.path, "changed field path"),
    previous: cloneConfig(change.previous),
    current: cloneConfig(change.current),
  }));
}

function cloneGroups(groups: Readonly<Record<string, string>>): Record<string, string> {
  if (typeof groups !== "object" || groups === null || Array.isArray(groups)) {
    throw new TypeError("configstore: parameter groups must be a record of documents");
  }
  const result: Record<string, string> = Object.create(null) as Record<string, string>;
  for (const alias of Object.keys(groups)) {
    const descriptor = Object.getOwnPropertyDescriptor(groups, alias);
    if (!descriptor || !("value" in descriptor)) {
      throw new TypeError("configstore: parameter group documents must be data properties");
    }
    result[alias] = requireString(descriptor.value, "parameter group document");
  }
  return result;
}

function cloneRestartFields(fields: readonly string[]): string[] {
  if (!Array.isArray(fields)) {
    throw new TypeError("configstore: restart-required fields must be an array");
  }
  return fields.map((path) => requireString(path, "restart-required field path"));
}

function requireString(value: unknown, description: string): string {
  if (typeof value !== "string")
    throw new TypeError(`configstore: ${description} must be a string`);
  return value;
}

function once(callback: () => undefined): () => undefined {
  let called = false;
  return () => {
    if (called) return undefined;
    called = true;
    return callback();
  };
}

function synchronousCallbackError(name: string, returned: unknown): Error | undefined {
  if (returned === undefined) return undefined;
  void Promise.resolve(returned).catch(() => undefined);
  return new TypeError(`${name} must return undefined synchronously`);
}

function isAbortError(error: unknown): boolean {
  return (
    (error instanceof DOMException && error.name === "AbortError") ||
    (error instanceof Error && error.name === "AbortError")
  );
}

function abortReason(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException("Aborted", "AbortError");
}

function callUnknown<T>(callback: (value: T) => void, value: T): unknown {
  return (callback as (item: T) => unknown)(value);
}

function isPromiseLike(value: unknown): value is PromiseLike<unknown> {
  return (
    (typeof value === "object" || typeof value === "function") &&
    value !== null &&
    "then" in value &&
    typeof value.then === "function"
  );
}

function discardPromise(value: unknown): void {
  if (isPromiseLike(value)) void Promise.resolve(value).catch(() => undefined);
}

function deferred<T>(): { readonly promise: Promise<T>; readonly resolve: (value: T) => void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((onResolve) => {
    resolve = onResolve;
  });
  return { promise, resolve };
}
