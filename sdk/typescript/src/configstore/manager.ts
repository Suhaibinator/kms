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
  CandidateError,
  CandidateRejectionReport,
  candidateErrorPaths,
  DefaultMismatchError,
  DefaultMismatchReport,
  type FieldDifference,
} from "./errors.js";
import { ReleaseIdentity } from "./snapshot.js";

export interface ManagedPreparedCandidate {
  /** Generated binding's atomic active-generation swap. Must be infallible. */
  readonly publish: () => void;
  /** Releases candidate-owned resources. Must be infallible and is called at most once. */
  readonly abort?: () => void;
  /** Complete non-secret comparison against source defaults. */
  readonly defaultDifferences?: readonly FieldDifference[];
  /** Generated canonical fields whose effective value/pin changed after startup. */
  readonly restartRequiredFields?: readonly string[];
}

export type PrepareManagedCandidate = (
  snapshot: ReleaseSnapshot,
  signal: AbortSignal,
) => ManagedPreparedCandidate | Promise<ManagedPreparedCandidate>;

export interface ManagedConfigOptions
  extends Omit<
    ClientReleaseLoaderOptions,
    "name" | "namespace" | "clientName" | "validateManifest"
  > {
  /** Release name within the client's home or explicitly selected namespace. */
  readonly release: string;
  /** Optional env/app override. Omit to use the KmsClient home namespace. */
  readonly namespace?: string;
  /** Optional acknowledgement identity override. */
  readonly clientName?: string;
  readonly contract: readonly ContractEntry[];
  readonly allowDefaultMismatch?: boolean;
  /** Mandatory: default drift must never become silent. Must be synchronous. */
  readonly onDefaultMismatch: (report: DefaultMismatchReport) => void;
  /** Optional, value-free local diagnostics. Callback failures are isolated. */
  readonly onCandidateRejected?: (report: CandidateRejectionReport) => void;
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

interface PolicyOptions {
  readonly name: string;
  readonly allowDefaultMismatch: boolean;
  readonly onDefaultMismatch: (report: DefaultMismatchReport) => void;
  readonly onCandidateRejected?: (report: CandidateRejectionReport) => void;
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
  #lastReportError: CandidateError | undefined;
  #lastRejectedKey = "";
  #startupError: DefaultMismatchError | undefined;

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

  /** @internal Wait for the first atomic publication or typed startup failure. */
  async waitUntilReady(): Promise<void> {
    const completion = this.#completion;
    if (!completion) throw new Error("configstore: manager is not started");
    const outcome = await Promise.race([
      this.#readySignal.promise.then(() => ({ ready: true }) as const),
      completion.then((result) => ({ ready: false, result }) as const),
    ]);
    if (outcome.ready) return;
    if (this.#startupError && isRejectionCategory(outcome.result.error, "default_mismatch")) {
      throw this.#startupError;
    }
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
    if (startup) this.#startupError = undefined;

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

    const differences = cloneDifferences(candidate.defaultDifferences ?? []);
    const restartFields = [...(candidate.restartRequiredFields ?? [])];
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

    const divergent = differences.length > 0;
    if (divergent) {
      const phase = startup ? "startup" : "runtime";
      const severity = startup && !this.#options.allowDefaultMismatch ? "fatal" : "error";
      const report = new DefaultMismatchReport(phase, severity, identity, differences);
      const reportError = this.#reportOnce(identity, report);
      if (reportError) {
        this.#abortOrInternal(abort, identity);
        this.#notifyCandidateRejected(identity, reportError);
        throw reportError;
      }
      if (severity === "fatal") {
        const mismatch = new DefaultMismatchError(report);
        this.#startupError = mismatch;
        this.#abortOrInternal(abort, identity);
        const error = new CandidateError(
          "default_mismatch",
          mismatch,
          differences.map(({ path }) => path),
        );
        this.#notifyCandidateRejected(identity, error);
        throw error;
      }
    }

    return {
      commit: () => {
        candidate.publish();
        this.#applied = identity;
        this.#divergent = divergent;
        this.#ready = true;
        this.#readySignal.resolve();
      },
      abort,
    };
  }

  #abortOrInternal(abort: () => void, identity: ReleaseIdentity): void {
    try {
      abort();
    } catch (cause) {
      const error = new CandidateError("internal", cause);
      this.#notifyCandidateRejected(identity, error);
      throw error;
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

  #reportOnce(
    identity: ReleaseIdentity,
    report: DefaultMismatchReport,
  ): CandidateError | undefined {
    const key = identity.dedupeKey();
    if (this.#lastReportedKey === key) return this.#lastReportError;
    this.#lastReportedKey = key;
    try {
      const result = callUnknown(this.#options.onDefaultMismatch, report);
      if (isPromiseLike(result)) {
        discardPromise(result);
        throw new TypeError("configstore: default mismatch callback must be synchronous");
      }
      this.#lastReportError = undefined;
    } catch (cause) {
      this.#lastReportError = new CandidateError("internal", cause);
    }
    return this.#lastReportError;
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
      allowDefaultMismatch: options.allowDefaultMismatch ?? false,
      onDefaultMismatch: options.onDefaultMismatch,
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
  return differences.map((difference) => ({
    path: difference.path,
    expected: cloneConfig(difference.expected),
    actual: cloneConfig(difference.actual),
  }));
}

function once(callback: () => void): () => void {
  let called = false;
  return () => {
    if (called) return;
    called = true;
    callback();
  };
}

function isRejectionCategory(error: unknown, category: ReleaseRejectionCategory): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "category" in error &&
    error.category === category
  );
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
