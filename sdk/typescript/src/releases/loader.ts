import { randomUUID } from "node:crypto";
import {
  type ConfigurationRelease,
  ConfigurationRelease as ConfigurationReleaseMessage,
  type GetActiveReleaseResponse,
  type NamespaceRef,
  type Parameter,
  type ReleaseAcknowledgement,
  type ReleaseWatchRegistration,
  type ResourceRef,
  type WatchReleaseEvent,
  type WatchReleaseRequest,
} from "../generated/kms.js";
import { releaseDigestMatches, sha256Hex } from "./digest.js";
import {
  classifiedReleaseCategory,
  type PreparedRelease,
  type PrepareRelease,
  ReleaseCandidateError,
  type ReleaseEntryKind,
  ReleaseEntryMetadata,
  type ReleaseLoaderStats,
  type ReleaseLoaderStatus,
  ReleaseManifest,
  ReleaseParameter,
  type ReleaseRejectionCategory,
  ReleaseSecret,
  ReleaseSnapshot,
  type ReleaseState,
} from "./types.js";

const DEFAULT_RECONCILE_INTERVAL_MS = 60_000;
const DEFAULT_MAX_CONCURRENT_FETCHES = 16;
const MAX_CONCURRENT_FETCHES = 256;
const BACKOFF_BASE_MS = 250;
const BACKOFF_CAP_MS = 30_000;
const BACKOFF_MIN_MS = 10;
const MAX_ACK_DIVERGENT_FIELD_COUNT = 65_535;

/** Bounded divergence summary attached to applied acknowledgements. */
interface AckDivergence {
  readonly divergent: boolean;
  readonly fieldCount: number;
}

const NO_DIVERGENCE: AckDivergence = Object.freeze({ divergent: false, fieldCount: 0 });

/** Read an optional divergence reporter; a misbehaving reporter never affects the ack. */
function divergenceOf(prepared: PreparedRelease): AckDivergence {
  if (typeof prepared.releaseDivergence !== "function") return NO_DIVERGENCE;
  try {
    const reported: unknown = prepared.releaseDivergence();
    if (typeof reported !== "object" || reported === null) return NO_DIVERGENCE;
    const { divergent, fieldCount } = reported as Partial<AckDivergence>;
    if (divergent !== true) return NO_DIVERGENCE;
    const count =
      typeof fieldCount === "number" && Number.isFinite(fieldCount) ? Math.trunc(fieldCount) : 0;
    return {
      divergent: true,
      fieldCount: Math.min(Math.max(count, 0), MAX_ACK_DIVERGENT_FIELD_COUNT),
    };
  } catch {
    return NO_DIVERGENCE;
  }
}

/** @internal Transport-only exact secret payload. */
export interface FetchedSecret {
  readonly ref: ResourceRef | undefined;
  readonly version: bigint;
  readonly value: Uint8Array;
  readonly contentType: string;
}

/** @internal Transport-only release stream. */
export interface ReleaseWatchStream extends AsyncIterable<WatchReleaseEvent> {
  send(request: WatchReleaseRequest): Promise<void>;
  closeSend?(): void | Promise<void>;
  cancel?(): void;
}

/** @internal Transport boundary implemented by the main KMS client. */
export interface ReleaseTransport {
  getActiveRelease(
    namespace: NamespaceRef,
    name: string,
    signal?: AbortSignal,
  ): Promise<GetActiveReleaseResponse>;
  fetchParameter(ref: ResourceRef, version: bigint, signal?: AbortSignal): Promise<Parameter>;
  fetchSecret(
    ref: ResourceRef,
    version: bigint,
    secretToken: string,
    signal?: AbortSignal,
  ): Promise<FetchedSecret>;
  watchRelease(
    registration: ReleaseWatchRegistration,
    signal: AbortSignal,
  ): ReleaseWatchStream | Promise<ReleaseWatchStream>;
}

export type SecretTokenProvider = (
  alias: string,
  path: string,
  signal: AbortSignal,
) => string | undefined | Promise<string | undefined>;

export type ValidateReleaseManifest = (
  manifest: ReleaseManifest,
  signal: AbortSignal,
) => void | Promise<void>;

/** @internal Full transport-bound loader options. Use ClientReleaseLoaderOptions publicly. */
export interface ReleaseLoaderOptions {
  readonly namespace: NamespaceRef;
  readonly name: string;
  readonly clientName: string;
  readonly instanceId?: string;
  readonly reconcileIntervalMs?: number;
  readonly maxConcurrentFetches?: number;
  readonly secretTokenProvider?: SecretTokenProvider;
  readonly validateManifest?: ValidateReleaseManifest;
  /** @internal Bound for flushing a terminal startup acknowledgement. */
  readonly acknowledgementTimeoutMs?: number;
  /** Injected only for deterministic tests. */
  readonly now?: () => number;
  /** Injected only for deterministic full-jitter backoff tests. */
  readonly random?: () => number;
}

interface NormalizedOptions {
  readonly namespace: NamespaceRef;
  readonly name: string;
  readonly clientName: string;
  readonly instanceId: string;
  readonly reconcileIntervalMs: number;
  readonly maxConcurrentFetches: number;
  readonly secretTokenProvider?: SecretTokenProvider;
  readonly validateManifest?: ValidateReleaseManifest;
  readonly acknowledgementTimeoutMs: number;
  readonly now: () => number;
  readonly random: () => number;
}

type CandidateSource = "activation" | "reconciliation";

interface Candidate {
  readonly release: ConfigurationRelease;
  readonly revision: bigint;
  readonly source: CandidateSource;
  readonly sequence: bigint;
}

interface CandidateResult {
  readonly candidate: Candidate;
  readonly applied: boolean;
  readonly category?: ReleaseRejectionCategory;
  readonly acknowledgementGeneration?: bigint;
  readonly fatal?: Error;
}

interface MutableStatus {
  state: ReleaseState | "idle";
  observedVersion: bigint;
  observedRevision: bigint;
  appliedVersion: bigint;
  appliedRevision: bigint;
  lastFailureCategory?: ReleaseRejectionCategory;
  lastFailureAt?: Date;
  lastResolutionDurationMs: number;
  reconnects: bigint;
}

interface MutableStats {
  candidates: bigint;
  applied: bigint;
  rejected: Record<string, bigint>;
  reconnects: bigint;
}

interface PendingAcknowledgement {
  acknowledgement: ReleaseAcknowledgement;
  generation: bigint;
  dirty: boolean;
  flushedOn?: ReleaseWatchStream;
}

class ResolutionError extends Error {
  readonly category: ReleaseRejectionCategory;

  constructor(category: ReleaseRejectionCategory) {
    super(category);
    this.category = category;
  }
}

/**
 * Resolves, prepares and atomically commits active configuration releases.
 * One preparation runs at a time; a newer candidate cancels it and replaces
 * the single pending slot. After the first commit, failures preserve LKG.
 */
export class ReleaseLoader {
  readonly #transport: ReleaseTransport;
  readonly #options: NormalizedOptions;
  readonly #status: MutableStatus = {
    state: "idle",
    observedVersion: 0n,
    observedRevision: 0n,
    appliedVersion: 0n,
    appliedRevision: 0n,
    lastResolutionDurationMs: 0,
    reconnects: 0n,
  };
  readonly #stats: MutableStats = {
    candidates: 0n,
    applied: 0n,
    rejected: {},
    reconnects: 0n,
  };
  readonly #pendingAcknowledgements = new Map<ReleaseState, PendingAcknowledgement>();
  readonly #acknowledgementWaiters = new Map<bigint, (stream: ReleaseWatchStream) => void>();
  #running = false;
  #stopController: AbortController | undefined;
  #currentStream: ReleaseWatchStream | undefined;
  #lastSeenRevision = 0n;
  #ackGeneration = 0n;
  #flushChain: Promise<void> = Promise.resolve();

  private constructor(transport: ReleaseTransport, options: ReleaseLoaderOptions) {
    this.#transport = transport;
    this.#options = normalizeOptions(options);
  }

  /** @internal Construct through KmsClient.createReleaseLoader in application code. */
  static _create(transport: ReleaseTransport, options: ReleaseLoaderOptions): ReleaseLoader {
    return new ReleaseLoader(transport, options);
  }

  get instanceId(): string {
    return this.#options.instanceId;
  }

  status(): ReleaseLoaderStatus {
    const status = this.#status;
    return Object.freeze({
      state: status.state,
      observedVersion: status.observedVersion,
      observedRevision: status.observedRevision,
      appliedVersion: status.appliedVersion,
      appliedRevision: status.appliedRevision,
      ...(status.lastFailureCategory ? { lastFailureCategory: status.lastFailureCategory } : {}),
      ...(status.lastFailureAt ? { lastFailureAt: new Date(status.lastFailureAt) } : {}),
      lastResolutionDurationMs: status.lastResolutionDurationMs,
      reconnects: status.reconnects,
    });
  }

  stats(): ReleaseLoaderStats {
    return Object.freeze({
      candidates: this.#stats.candidates,
      applied: this.#stats.applied,
      rejected: Object.freeze({ ...this.#stats.rejected }),
      reconnects: this.#stats.reconnects,
    });
  }

  stop(reason: unknown = new DOMException("Release loader stopped", "AbortError")): void {
    this.#stopController?.abort(reason);
  }

  async run(prepare: PrepareRelease, signal?: AbortSignal): Promise<void> {
    if (this.#running) throw new Error("KMS release loader is already running");
    if (typeof prepare !== "function") throw new TypeError("prepare callback is required");
    if (signal?.aborted) throw abortReason(signal);

    this.#running = true;
    const runController = new AbortController();
    this.#stopController = runController;
    const unlinkSignal = linkAbort(signal, runController);
    let watchTask: Promise<void> | undefined;
    let reconcileTask: Promise<void> | undefined;
    let activeController: AbortController | undefined;
    const candidateTasks = new Set<Promise<void>>();

    try {
      const initial = await this.#getActive(runController.signal);
      if (!initial.release) throw new Error("KMS active release response was empty");
      this.#lastSeenRevision = initial.activationRevision;

      let sequence = 0n;
      let latest: Candidate | undefined;
      let retryLatest = false;
      let pending: Candidate | undefined;
      let processing = false;
      let appliedOnce = false;
      let gracefulWatchStop = false;
      const finished = deferred<void>();

      const start = (candidate: Candidate): void => {
        const candidateController = new AbortController();
        const unlinkRun = linkAbort(runController.signal, candidateController);
        activeController = candidateController;
        processing = true;
        const task = this.#processCandidate(candidate, prepare, candidateController.signal)
          .then(async (result) => {
            processing = false;
            activeController = undefined;
            unlinkRun();
            if (result.applied) appliedOnce = true;
            if (result.fatal) {
              finished.reject(result.fatal);
              return;
            }
            if (result.candidate.sequence !== latest?.sequence) {
              if (pending && !runController.signal.aborted) {
                const next = pending;
                pending = undefined;
                start(next);
              }
              return;
            }
            if (result.category && result.category !== "superseded" && !appliedOnce) {
              try {
                const fresh = await this.#getActive(runController.signal);
                if (fresh.release) {
                  const freshCandidate = makeCandidate(
                    fresh.release,
                    fresh.activationRevision,
                    "reconciliation",
                    0n,
                  );
                  if (!sameActiveCandidate(result.candidate, freshCandidate)) {
                    offer(freshCandidate);
                    return;
                  }
                }
              } catch {
                // Preserve the original, redacted startup rejection.
              }
              const acknowledgedStream =
                result.acknowledgementGeneration === undefined
                  ? undefined
                  : await this.#waitForAcknowledgement(
                      result.acknowledgementGeneration,
                      runController.signal,
                    );
              if (acknowledgedStream) {
                gracefulWatchStop = true;
                await settleWithin(
                  closeStream(acknowledgedStream),
                  this.#options.acknowledgementTimeoutMs,
                  runController.signal,
                );
                if (watchTask) {
                  await settleWithin(
                    watchTask,
                    this.#options.acknowledgementTimeoutMs,
                    runController.signal,
                  );
                }
              }
              finished.reject(new ReleaseCandidateError(result.category));
              return;
            }
            if (result.category && result.category !== "superseded") retryLatest = true;
            if (pending && !runController.signal.aborted) {
              const next = pending;
              pending = undefined;
              start(next);
            }
          })
          .catch((error: unknown) => {
            processing = false;
            activeController = undefined;
            unlinkRun();
            finished.reject(error);
          });
        candidateTasks.add(task);
        void task.then(
          () => candidateTasks.delete(task),
          () => candidateTasks.delete(task),
        );
      };

      const offer = (incoming: Candidate): void => {
        if (runController.signal.aborted) return;
        if (latest) {
          if (incoming.revision < latest.revision) return;
          if (sameQueuedCandidate(incoming, latest)) {
            if (!(retryLatest && incoming.source === "reconciliation")) return;
          }
        }
        sequence += 1n;
        const candidate = { ...incoming, sequence };
        latest = candidate;
        retryLatest = false;
        this.#observe(candidate);
        if (processing) {
          activeController?.abort(new DOMException("Release superseded", "AbortError"));
          pending = candidate;
          return;
        }
        start(candidate);
      };

      watchTask = this.#watchLoop(runController.signal, offer, () => gracefulWatchStop);
      reconcileTask = this.#reconcileLoop(runController.signal, offer);
      offer(makeCandidate(initial.release, initial.activationRevision, "reconciliation", 0n));

      await Promise.race([finished.promise, aborted(runController.signal)]);
    } finally {
      activeController?.abort(new DOMException("Release loader stopped", "AbortError"));
      runController.abort(new DOMException("Release loader stopped", "AbortError"));
      const backgroundTasks = Promise.allSettled([watchTask, reconcileTask].filter(isPromise));
      await Promise.allSettled([...candidateTasks]);
      await this.#scheduleAckFlush().catch(() => undefined);
      await closeStream(this.#currentStream);
      this.#currentStream = undefined;
      await backgroundTasks;
      unlinkSignal();
      this.#stopController = undefined;
      this.#running = false;
    }
  }

  async #processCandidate(
    candidate: Candidate,
    prepare: PrepareRelease,
    signal: AbortSignal,
  ): Promise<CandidateResult> {
    const started = this.#options.now();
    let snapshot: ReleaseSnapshot;
    try {
      snapshot = await this.#resolveCandidate(candidate, signal);
    } catch (error) {
      this.#status.lastResolutionDurationMs = Math.max(0, this.#options.now() - started);
      const category = resolutionCategory(error, signal);
      const acknowledgementGeneration = this.#reject(candidate, category);
      return { candidate, applied: false, category, acknowledgementGeneration };
    }
    this.#status.lastResolutionDurationMs = Math.max(0, this.#options.now() - started);
    this.#ack(candidate, "received");
    this.#status.state = "received";

    let prepared: PreparedRelease;
    try {
      prepared = await prepare(snapshot, signal);
      if (
        !prepared ||
        typeof prepared.commit !== "function" ||
        typeof prepared.abort !== "function"
      ) {
        throw new TypeError("prepare callback returned an invalid PreparedRelease");
      }
    } catch (error) {
      const category = signal.aborted
        ? "superseded"
        : (classifiedReleaseCategory(error) ?? "prepare_failed");
      const acknowledgementGeneration = this.#reject(candidate, category);
      return { candidate, applied: false, category, acknowledgementGeneration };
    }

    let abortedPrepared = false;
    const abortPrepared = (): Error | undefined => {
      if (abortedPrepared) return undefined;
      abortedPrepared = true;
      try {
        const returned: unknown = prepared.abort();
        return synchronousCallbackError("PreparedRelease.abort()", returned);
      } catch {
        return new Error("PreparedRelease.abort() threw; abort must be infallible");
      }
    };

    if (signal.aborted) {
      const fatal = abortPrepared();
      const acknowledgementGeneration = this.#reject(candidate, fatal ? "internal" : "superseded");
      return fatal
        ? { candidate, applied: false, category: "internal", acknowledgementGeneration, fatal }
        : { candidate, applied: false, category: "superseded", acknowledgementGeneration };
    }
    this.#ack(candidate, "prepared");
    this.#status.state = "prepared";

    let active: Candidate;
    try {
      const response = await this.#getActive(signal);
      if (!response.release) throw new Error("empty active release");
      active = makeCandidate(response.release, response.activationRevision, "reconciliation", 0n);
    } catch {
      const category: ReleaseRejectionCategory = signal.aborted
        ? "superseded"
        : "active_check_failed";
      const fatal = abortPrepared();
      const acknowledgementGeneration = this.#reject(candidate, fatal ? "internal" : category);
      return fatal
        ? { candidate, applied: false, category: "internal", acknowledgementGeneration, fatal }
        : { candidate, applied: false, category, acknowledgementGeneration };
    }

    if (signal.aborted || !sameActiveCandidate(candidate, active)) {
      const fatal = abortPrepared();
      const acknowledgementGeneration = this.#reject(candidate, fatal ? "internal" : "superseded");
      return fatal
        ? { candidate, applied: false, category: "internal", acknowledgementGeneration, fatal }
        : { candidate, applied: false, category: "superseded", acknowledgementGeneration };
    }

    try {
      const returned: unknown = prepared.commit();
      const contractError = synchronousCallbackError("PreparedRelease.commit()", returned);
      if (contractError) {
        this.#recordRejected("internal");
        return { candidate, applied: false, category: "internal", fatal: contractError };
      }
    } catch {
      const fatal = new Error("PreparedRelease.commit() threw; commit must be infallible");
      this.#recordRejected("internal");
      return { candidate, applied: false, category: "internal", fatal };
    }

    this.#ack(candidate, "applied", "", divergenceOf(prepared));
    this.#status.state = "applied";
    this.#status.appliedVersion = candidate.release.version;
    this.#status.appliedRevision = candidate.revision;
    delete this.#status.lastFailureCategory;
    this.#stats.applied += 1n;
    return { candidate, applied: true };
  }

  async #resolveCandidate(candidate: Candidate, signal: AbortSignal): Promise<ReleaseSnapshot> {
    throwIfAborted(signal);
    const { release } = candidate;
    const namespace = release.namespace;
    if (!namespace) throw new ResolutionError("resolution_failed");
    if (
      release.name !== this.#options.name ||
      namespace.env !== this.#options.namespace.env ||
      namespace.app !== this.#options.namespace.app
    ) {
      throw new ResolutionError("version_mismatch");
    }
    try {
      if (!releaseDigestMatches(release)) throw new ResolutionError("digest_mismatch");
    } catch (error) {
      if (error instanceof ResolutionError) throw error;
      throw new ResolutionError("digest_mismatch");
    }
    if (release.entries.length === 0) throw new ResolutionError("resolution_failed");

    const entries = new Map<string, ReleaseEntryMetadata>();
    for (const entry of release.entries) {
      const metadata = metadataForEntry(entry);
      if (entries.has(metadata.alias)) throw new ResolutionError("resolution_failed");
      entries.set(metadata.alias, metadata);
    }
    const identity = {
      namespace: namespacePath(namespace),
      name: release.name,
      version: release.version,
      activationRevision: candidate.revision,
      schemaId: release.schemaId,
      schemaVersion: release.schemaVersion,
      digest: release.digest,
      metadataJson: release.metadataJson,
      entries,
    };
    const manifest = new ReleaseManifest(identity);
    if (this.#options.validateManifest) {
      try {
        await this.#options.validateManifest(manifest, signal);
      } catch (error) {
        if (signal.aborted) throw new ResolutionError("superseded");
        throw new ResolutionError(classifiedReleaseCategory(error) ?? "prepare_failed");
      }
    }
    throwIfAborted(signal);

    const resolved = await mapConcurrent(
      release.entries,
      this.#options.maxConcurrentFetches,
      signal,
      (entry, workerSignal) =>
        this.#resolveEntry(entry, entries.get(entry.alias) as ReleaseEntryMetadata, workerSignal),
    );
    const parameters = new Map<string, ReleaseParameter>();
    const secrets = new Map<string, ReleaseSecret>();
    for (const item of resolved) {
      if (item.parameter) parameters.set(item.metadata.alias, item.parameter);
      if (item.secret) secrets.set(item.metadata.alias, item.secret);
    }
    return new ReleaseSnapshot({ ...identity, parameters, secrets });
  }

  async #resolveEntry(
    entry: ConfigurationRelease["entries"][number],
    metadata: ReleaseEntryMetadata,
    signal: AbortSignal,
  ): Promise<{
    metadata: ReleaseEntryMetadata;
    parameter?: ReleaseParameter;
    secret?: ReleaseSecret;
  }> {
    const ref = entry.ref;
    if (!ref?.namespace) throw new ResolutionError("resolution_failed");
    if (entry.kind === "parameter") {
      let parameter: Parameter;
      try {
        parameter = await this.#transport.fetchParameter(cloneRef(ref), entry.version, signal);
      } catch {
        throw new ResolutionError(signal.aborted ? "superseded" : "resolution_failed");
      }
      if (!sameResourceRef(parameter.ref, ref) || parameter.version !== entry.version) {
        throw new ResolutionError("version_mismatch");
      }
      if (
        !/^[0-9a-f]{64}$/iu.test(entry.parameterDigest) ||
        sha256Hex(Buffer.from(parameter.value, "utf8")).toLowerCase() !==
          entry.parameterDigest.toLowerCase()
      ) {
        throw new ResolutionError("digest_mismatch");
      }
      if (entry.contentType && parameter.contentType !== entry.contentType) {
        throw new ResolutionError("digest_mismatch");
      }
      return { metadata, parameter: new ReleaseParameter(parameter.value, metadata) };
    }

    if (entry.kind === "secret") {
      let token = "";
      if (entry.clientBound || entry.hasAccessToken) {
        if (!this.#options.secretTokenProvider) {
          throw new ResolutionError("token_unavailable");
        }
        try {
          token =
            (await this.#options.secretTokenProvider(entry.alias, metadata.path, signal)) ?? "";
        } catch {
          throw new ResolutionError(signal.aborted ? "superseded" : "token_unavailable");
        }
        if (!token) throw new ResolutionError("token_unavailable");
      }
      let secret: FetchedSecret;
      try {
        secret = await this.#transport.fetchSecret(cloneRef(ref), entry.version, token, signal);
      } catch {
        throw new ResolutionError(signal.aborted ? "superseded" : "resolution_failed");
      }
      if (!sameResourceRef(secret.ref, ref) || secret.version !== entry.version) {
        throw new ResolutionError("version_mismatch");
      }
      if (entry.contentType && secret.contentType !== entry.contentType) {
        throw new ResolutionError("version_mismatch");
      }
      return { metadata, secret: new ReleaseSecret(secret.value, metadata) };
    }
    throw new ResolutionError("resolution_failed");
  }

  async #getActive(signal: AbortSignal): Promise<GetActiveReleaseResponse> {
    throwIfAborted(signal);
    const response = await this.#transport.getActiveRelease(
      { ...this.#options.namespace },
      this.#options.name,
      signal,
    );
    return {
      release: response.release ? cloneRelease(response.release) : undefined,
      activationRevision: response.activationRevision,
      previousVersion: response.previousVersion,
    };
  }

  async #watchLoop(
    signal: AbortSignal,
    offer: (candidate: Candidate) => void,
    shouldStopGracefully: () => boolean,
  ): Promise<void> {
    let attempt = 0;
    while (!signal.aborted && !shouldStopGracefully()) {
      let stream: ReleaseWatchStream | undefined;
      let receivedEvent = false;
      try {
        const registration: ReleaseWatchRegistration = {
          namespace: { ...this.#options.namespace },
          name: this.#options.name,
          clientName: this.#options.clientName,
          instanceId: this.#options.instanceId,
          lastSeenRevision: this.#lastSeenRevision,
        };
        stream = await this.#transport.watchRelease(registration, signal);
        await this.#flushAcknowledgements(stream, true);
        this.#currentStream = stream;
        await this.#scheduleAckFlush();
        for await (const event of stream) {
          if (signal.aborted) break;
          receivedEvent = true;
          if (event.revision > this.#lastSeenRevision) this.#lastSeenRevision = event.revision;
          const payload = event.event;
          if (payload?.$case === "snapshot" && payload.value.release) {
            offer(makeCandidate(payload.value.release, event.revision, "reconciliation", 0n));
          } else if (payload?.$case === "activation" && payload.value.release) {
            offer(makeCandidate(payload.value.release, event.revision, "activation", 0n));
          }
        }
      } catch {
        // Stream reliability is owned here; candidate failures are separate.
      } finally {
        if (this.#currentStream === stream) this.#currentStream = undefined;
        await closeStream(stream);
      }
      if (signal.aborted || shouldStopGracefully()) return;
      if (receivedEvent) attempt = 0;
      this.#recordReconnect();
      const reconnectDelay = releaseReconnectBackoff(attempt, this.#options.random);
      attempt += 1;
      await delay(reconnectDelay, signal);
    }
  }

  async #reconcileLoop(signal: AbortSignal, offer: (candidate: Candidate) => void): Promise<void> {
    while (!signal.aborted) {
      await delay(this.#options.reconcileIntervalMs, signal);
      if (signal.aborted) return;
      try {
        const active = await this.#getActive(signal);
        if (active.release) {
          offer(makeCandidate(active.release, active.activationRevision, "reconciliation", 0n));
        }
      } catch {
        // A failed safety read never displaces last-known-good state.
      }
    }
  }

  #observe(candidate: Candidate): void {
    this.#status.observedVersion = candidate.release.version;
    this.#status.observedRevision = candidate.revision;
    this.#stats.candidates += 1n;
  }

  #reject(candidate: Candidate, category: ReleaseRejectionCategory): bigint {
    const acknowledgementGeneration = this.#ack(candidate, "rejected", category);
    this.#recordRejected(category);
    return acknowledgementGeneration;
  }

  #recordRejected(category: ReleaseRejectionCategory): void {
    this.#status.state = "rejected";
    this.#status.lastFailureCategory = category;
    this.#status.lastFailureAt = new Date(this.#options.now());
    this.#stats.rejected[category] = (this.#stats.rejected[category] ?? 0n) + 1n;
  }

  #recordReconnect(): void {
    this.#status.reconnects += 1n;
    this.#stats.reconnects += 1n;
  }

  #ack(
    candidate: Candidate,
    state: ReleaseState,
    rejectionCategory: ReleaseRejectionCategory | "" = "",
    divergence: AckDivergence = NO_DIVERGENCE,
  ): bigint {
    this.#ackGeneration += 1n;
    const applied = state === "applied" && divergence.divergent;
    const acknowledgement: ReleaseAcknowledgement = {
      namespace: { ...this.#options.namespace },
      name: this.#options.name,
      version: candidate.release.version,
      activationRevision: candidate.revision,
      clientName: this.#options.clientName,
      instanceId: this.#options.instanceId,
      state,
      rejectionCategory,
      diagnostic: "",
      timestampUnixMs: BigInt(Math.trunc(this.#options.now())),
      appliedDivergent: applied,
      divergentFieldCount: applied ? divergence.fieldCount : 0,
    };
    const current = this.#pendingAcknowledgements.get(state);
    if (!current || current.acknowledgement.activationRevision <= candidate.revision) {
      this.#pendingAcknowledgements.set(state, {
        acknowledgement,
        generation: this.#ackGeneration,
        dirty: true,
      });
    }
    void this.#scheduleAckFlush().catch(() => undefined);
    return this.#ackGeneration;
  }

  #scheduleAckFlush(): Promise<void> {
    const stream = this.#currentStream;
    if (!stream) return this.#flushChain;
    this.#flushChain = this.#flushChain
      .catch(() => undefined)
      .then(() => this.#flushAcknowledgements(stream, false))
      .catch((error: unknown) => {
        stream.cancel?.();
        throw error;
      });
    return this.#flushChain;
  }

  async #flushAcknowledgements(stream: ReleaseWatchStream, replay: boolean): Promise<void> {
    const pending = [...this.#pendingAcknowledgements.entries()]
      .filter(([, item]) => replay || item.dirty)
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([state, item]) => ({ state, ...item }));
    for (const item of pending) {
      await stream.send({
        request: { $case: "acknowledgement", value: { ...item.acknowledgement } },
      });
      const current = this.#pendingAcknowledgements.get(item.state);
      if (current?.generation === item.generation) {
        current.dirty = false;
        current.flushedOn = stream;
        this.#acknowledgementWaiters.get(item.generation)?.(stream);
        this.#acknowledgementWaiters.delete(item.generation);
      }
    }
  }

  #waitForAcknowledgement(
    generation: bigint,
    signal: AbortSignal,
  ): Promise<ReleaseWatchStream | undefined> {
    for (const item of this.#pendingAcknowledgements.values()) {
      if (item.generation === generation && !item.dirty) return Promise.resolve(item.flushedOn);
    }
    return new Promise<ReleaseWatchStream | undefined>((resolve, reject) => {
      let settled = false;
      const finish = (stream: ReleaseWatchStream | undefined): void => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        signal.removeEventListener("abort", abort);
        this.#acknowledgementWaiters.delete(generation);
        resolve(stream);
      };
      const abort = (): void => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        this.#acknowledgementWaiters.delete(generation);
        reject(abortReason(signal));
      };
      const timer = setTimeout(() => finish(undefined), this.#options.acknowledgementTimeoutMs);
      timer.unref?.();
      this.#acknowledgementWaiters.set(generation, finish);
      signal.addEventListener("abort", abort, { once: true });
      if (signal.aborted) {
        abort();
        return;
      }

      for (const item of this.#pendingAcknowledgements.values()) {
        if (item.generation === generation && !item.dirty) {
          finish(item.flushedOn);
          break;
        }
      }
    });
  }
}

export async function runTypedRelease<T>(
  loader: ReleaseLoader,
  decode: (snapshot: ReleaseSnapshot) => T | Promise<T>,
  prepare: (value: T, signal: AbortSignal) => PreparedRelease | Promise<PreparedRelease>,
  signal?: AbortSignal,
): Promise<void> {
  if (typeof decode !== "function" || typeof prepare !== "function") {
    throw new TypeError("decode and prepare callbacks are required");
  }
  return loader.run(async (snapshot, candidateSignal) => {
    const value = await decode(snapshot);
    return prepare(value, candidateSignal);
  }, signal);
}

/** @internal Full-jitter reconnect delay with a floor that prevents hot retry loops. */
export function releaseReconnectBackoff(attempt: number, random = Math.random): number {
  const ceiling = Math.min(
    BACKOFF_CAP_MS,
    BACKOFF_BASE_MS * 2 ** Math.min(Math.max(0, attempt), 30),
  );
  return Math.max(BACKOFF_MIN_MS, Math.floor(random() * ceiling));
}

function normalizeOptions(options: ReleaseLoaderOptions): NormalizedOptions {
  if (!options.namespace?.env.trim() || !options.namespace.app.trim()) {
    throw new TypeError("release loader namespace env and app are required");
  }
  const name = options.name.trim();
  if (!name) throw new TypeError("release loader name is required");
  const clientName = options.clientName.trim();
  if (!clientName) throw new TypeError("release loader clientName is required");
  const instanceId = options.instanceId?.trim() || randomUUID();
  const requestedReconcileIntervalMs = options.reconcileIntervalMs ?? 0;
  if (!Number.isFinite(requestedReconcileIntervalMs)) {
    throw new RangeError("reconcileIntervalMs must be finite");
  }
  const reconcileIntervalMs =
    requestedReconcileIntervalMs > 0 ? requestedReconcileIntervalMs : DEFAULT_RECONCILE_INTERVAL_MS;
  const maxConcurrentFetches =
    options.maxConcurrentFetches && options.maxConcurrentFetches > 0
      ? options.maxConcurrentFetches
      : DEFAULT_MAX_CONCURRENT_FETCHES;
  if (!Number.isInteger(maxConcurrentFetches) || maxConcurrentFetches > MAX_CONCURRENT_FETCHES) {
    throw new RangeError(
      `maxConcurrentFetches must be an integer at most ${MAX_CONCURRENT_FETCHES}`,
    );
  }
  return {
    namespace: { env: options.namespace.env, app: options.namespace.app },
    name,
    clientName,
    instanceId,
    reconcileIntervalMs,
    maxConcurrentFetches,
    ...(options.secretTokenProvider ? { secretTokenProvider: options.secretTokenProvider } : {}),
    ...(options.validateManifest ? { validateManifest: options.validateManifest } : {}),
    acknowledgementTimeoutMs: positiveFinite(
      options.acknowledgementTimeoutMs ?? 5_000,
      "acknowledgementTimeoutMs",
    ),
    now: options.now ?? Date.now,
    random: options.random ?? Math.random,
  };
}

function positiveFinite(value: number, name: string): number {
  if (!Number.isFinite(value) || value <= 0) {
    throw new RangeError(`${name} must be positive`);
  }
  return value;
}

function metadataForEntry(entry: ConfigurationRelease["entries"][number]): ReleaseEntryMetadata {
  if (!entry.alias || entry.version <= 0n || !entry.ref?.namespace) {
    throw new ResolutionError("resolution_failed");
  }
  if (entry.kind !== "parameter" && entry.kind !== "secret") {
    throw new ResolutionError("resolution_failed");
  }
  return new ReleaseEntryMetadata({
    alias: entry.alias,
    kind: entry.kind as ReleaseEntryKind,
    path: resourcePath(entry.ref),
    version: entry.version,
    contentType: entry.contentType,
    metadataJson: entry.metadataJson,
    parameterDigest: entry.parameterDigest,
    clientBound: entry.clientBound,
    hasAccessToken: entry.hasAccessToken,
  });
}

function makeCandidate(
  release: ConfigurationRelease,
  revision: bigint,
  source: CandidateSource,
  sequence: bigint,
): Candidate {
  return { release: cloneRelease(release), revision, source, sequence };
}

function cloneRelease(release: ConfigurationRelease): ConfigurationRelease {
  return ConfigurationReleaseMessage.decode(ConfigurationReleaseMessage.encode(release).finish());
}

function cloneRef(ref: ResourceRef): ResourceRef {
  return {
    namespace: ref.namespace ? { env: ref.namespace.env, app: ref.namespace.app } : undefined,
    key: ref.key,
  };
}

function sameResourceRef(left: ResourceRef | undefined, right: ResourceRef | undefined): boolean {
  return (
    left?.namespace !== undefined &&
    right?.namespace !== undefined &&
    left.namespace.env === right.namespace.env &&
    left.namespace.app === right.namespace.app &&
    left.key === right.key
  );
}

function synchronousCallbackError(name: string, returned: unknown): Error | undefined {
  if (returned === undefined) return undefined;
  // A JavaScript consumer can evade the declaration contract. Attach a
  // terminal observer before failing closed so a rejected Promise/thenable
  // cannot become an unhandled process-level rejection.
  void Promise.resolve(returned).catch(() => undefined);
  return new Error(`${name} must return undefined synchronously`);
}

function sameQueuedCandidate(left: Candidate, right: Candidate): boolean {
  return (
    left.revision === right.revision &&
    left.release.version === right.release.version &&
    left.release.digest === right.release.digest
  );
}

function sameActiveCandidate(left: Candidate, right: Candidate): boolean {
  return (
    left.revision === right.revision &&
    left.release.name === right.release.name &&
    left.release.version === right.release.version &&
    left.release.digest === right.release.digest
  );
}

function namespacePath(namespace: NamespaceRef): string {
  return `${namespace.env}/${namespace.app}`;
}

function resourcePath(ref: ResourceRef): string {
  if (!ref.namespace) throw new ResolutionError("resolution_failed");
  return `/${ref.namespace.env}/${ref.namespace.app}/${ref.key}`;
}

function resolutionCategory(error: unknown, signal: AbortSignal): ReleaseRejectionCategory {
  if (signal.aborted) return "superseded";
  return error instanceof ResolutionError ? error.category : "resolution_failed";
}

async function mapConcurrent<T, R>(
  values: readonly T[],
  concurrency: number,
  signal: AbortSignal,
  worker: (value: T, signal: AbortSignal) => Promise<R>,
): Promise<R[]> {
  const controller = new AbortController();
  const unlink = linkAbort(signal, controller);
  const results = new Array<R>(values.length);
  let index = 0;
  let firstError: unknown;
  const runWorker = async (): Promise<void> => {
    while (!controller.signal.aborted) {
      const current = index;
      if (current >= values.length) return;
      index += 1;
      const value = values[current];
      if (value === undefined) return;
      try {
        results[current] = await worker(value, controller.signal);
      } catch (error) {
        if (firstError === undefined) firstError = error;
        controller.abort(error);
        return;
      }
    }
  };
  try {
    await Promise.all(
      Array.from({ length: Math.min(concurrency, values.length) }, () => runWorker()),
    );
  } finally {
    unlink();
  }
  if (firstError !== undefined) throw firstError;
  throwIfAborted(signal);
  return results;
}

function linkAbort(source: AbortSignal | undefined, target: AbortController): () => void {
  if (!source) return () => undefined;
  const abort = () => target.abort(source.reason);
  if (source.aborted) abort();
  else source.addEventListener("abort", abort, { once: true });
  return () => source.removeEventListener("abort", abort);
}

function throwIfAborted(signal: AbortSignal): void {
  if (signal.aborted) throw abortReason(signal);
}

function abortReason(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException("Aborted", "AbortError");
}

function aborted(signal: AbortSignal): Promise<never> {
  return new Promise((_, reject) => {
    if (signal.aborted) {
      reject(abortReason(signal));
      return;
    }
    signal.addEventListener("abort", () => reject(abortReason(signal)), { once: true });
  });
}

function delay(milliseconds: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(abortReason(signal));
      return;
    }
    const timer = setTimeout(done, milliseconds);
    timer.unref?.();
    const abort = () => {
      clearTimeout(timer);
      signal.removeEventListener("abort", abort);
      reject(abortReason(signal));
    };
    function done(): void {
      signal.removeEventListener("abort", abort);
      resolve();
    }
    signal.addEventListener("abort", abort, { once: true });
  });
}

function settleWithin(
  task: Promise<unknown>,
  milliseconds: number,
  signal: AbortSignal,
): Promise<void> {
  if (signal.aborted) return Promise.reject(abortReason(signal));
  return new Promise<void>((resolve, reject) => {
    let settled = false;
    const finish = (error?: unknown): void => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      signal.removeEventListener("abort", abort);
      if (error !== undefined) reject(error);
      else resolve();
    };
    const abort = (): void => finish(abortReason(signal));
    const timer = setTimeout(() => finish(), milliseconds);
    timer.unref?.();
    signal.addEventListener("abort", abort, { once: true });
    task.then(
      () => finish(),
      () => finish(),
    );
    if (signal.aborted) abort();
  });
}

async function closeStream(stream: ReleaseWatchStream | undefined): Promise<void> {
  if (!stream) return;
  try {
    await stream.closeSend?.();
  } catch {
    stream.cancel?.();
  }
}

function deferred<T>(): {
  readonly promise: Promise<T>;
  resolve(value: T | PromiseLike<T>): void;
  reject(reason?: unknown): void;
} {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((onResolve, onReject) => {
    resolve = onResolve;
    reject = onReject;
  });
  return { promise, resolve, reject };
}

function isPromise<T>(value: Promise<T> | undefined): value is Promise<T> {
  return value !== undefined;
}
