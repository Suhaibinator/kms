import { KmsError } from "./errors.js";
import {
  type ParameterChange,
  type SecretMetadataChange,
  type Snapshot,
  type SubscribeEvent,
  WatchServiceService,
} from "./generated/kms.js";
import type { Page, Parameter } from "./models.js";
import {
  displayNamespace,
  displayPath,
  type NamespaceRef,
  namespaceEquals,
  namespaceKey,
  type ResourceRef,
  refOf,
} from "./refs.js";
import type { RpcTransport } from "./transport.js";

const DEFAULT_RECONCILE_INTERVAL_MS = 5 * 60_000;
const MAX_RECONCILE_PAGES = 100;

export type WatchEvent =
  | {
      readonly type: "put";
      readonly namespace: string;
      readonly key: string;
      readonly path: string;
      readonly value: string;
      readonly version: bigint;
      readonly revision: bigint;
      readonly changeType: string;
    }
  | {
      readonly type: "delete";
      readonly namespace: string;
      readonly key: string;
      readonly path: string;
      readonly version: bigint;
      readonly revision: bigint;
      readonly changeType: "delete";
    }
  | {
      readonly type: "secret_change";
      readonly namespace: string;
      readonly key: string;
      readonly path: string;
      readonly version: bigint;
      readonly revision: bigint;
      readonly changeType: string;
    };

export type WatchCallback = (event: WatchEvent) => unknown;
export type ParameterUpdateHandler = (value: string, present: boolean) => void;

export interface WatchOptions {
  readonly signal?: AbortSignal;
}

export type WatchConnectionState = "idle" | "connecting" | "connected" | "reconnecting" | "stopped";

export type ReconciliationHealth = "not_started" | "healthy" | "degraded";

/**
 * A value-free snapshot suitable for metrics and health logic. Format
 * `currentRevision` as decimal text before placing this object in JSON.
 */
export interface WatchStatus {
  readonly state: WatchConnectionState;
  readonly reconciliation: ReconciliationHealth;
  readonly currentRevision: bigint;
  readonly reconnectCount: number;
  readonly namespaceCount: number;
  readonly trackedParameterCount: number;
  readonly watcherCount: number;
  readonly parameterHandlerCount: number;
  readonly connectedAtUnixMs?: number;
  readonly lastEventAtUnixMs?: number;
  readonly disconnectedAtUnixMs?: number;
  readonly lastReconcileAttemptAtUnixMs?: number;
  readonly lastReconcileSuccessAtUnixMs?: number;
  readonly lastReconcileFailureAtUnixMs?: number;
}

interface WatchHost {
  readonly clientName: string;
  readonly timeoutMs: number;
  readonly logger: { warn(message: string): void };
  _watchTransport(): RpcTransport;
  _metadata(secretToken?: string): Readonly<Record<string, string>>;
  _rootSignal(): AbortSignal;
  _cache(): {
    invalidateParam(path: string): void;
    invalidateParametersInNamespaces(namespaces: Iterable<NamespaceRef>): void;
    invalidateSecret(path: string): void;
    invalidateSecretsInNamespaces(namespaces: Iterable<NamespaceRef>): void;
  };
  _dispatch(path: string, callback: () => unknown): void;
  listParametersInNamespace(
    namespace: NamespaceRef,
    options: {
      pageToken: string;
      pageSize: number;
      signal: AbortSignal;
    },
  ): Promise<Page<Parameter>>;
}

export interface SubscriptionManagerOptions {
  readonly reconcileIntervalMs?: number;
  readonly random?: () => number;
  readonly sleep?: (milliseconds: number, signal: AbortSignal) => Promise<void>;
}

interface KnownValue {
  readonly value: string;
  readonly present: boolean;
  readonly revision: bigint;
}

interface Watcher {
  readonly id: number;
  readonly namespace: NamespaceRef;
  readonly callback: WatchCallback;
}

/** Owns the client's one shared, resumable namespace watch stream. */
export class SubscriptionManager {
  readonly #host: WatchHost;
  readonly #reconcileIntervalMs: number;
  readonly #random: () => number;
  readonly #sleep: (milliseconds: number, signal: AbortSignal) => Promise<void>;
  readonly #controller = new AbortController();
  readonly #namespaces = new Map<string, NamespaceRef>();
  readonly #namespaceRefCounts = new Map<string, number>();
  readonly #parameterHandlers = new Map<string, Set<ParameterUpdateHandler>>();
  readonly #known = new Map<string, KnownValue>();
  readonly #watchers = new Map<number, Watcher>();
  readonly #watcherStops = new Map<number, () => void>();
  #streamNamespaces: readonly NamespaceRef[] = [];
  #nextWatcherId = 1;
  #lastRevision = 0n;
  #state: WatchConnectionState = "idle";
  #reconciliation: ReconciliationHealth = "not_started";
  #reconnectCount = 0;
  #connectedAtUnixMs: number | undefined;
  #lastEventAtUnixMs: number | undefined;
  #disconnectedAtUnixMs: number | undefined;
  #lastReconcileAttemptAtUnixMs: number | undefined;
  #lastReconcileSuccessAtUnixMs: number | undefined;
  #lastReconcileFailureAtUnixMs: number | undefined;
  #started = false;
  #restartRequested = false;
  #namespaceGeneration = 0;
  #snapshotGeneration = 0;
  #sessionController: AbortController | undefined;
  #backoffController: AbortController | undefined;
  #scopeController = new AbortController();
  readonly #scopeWaiters = new Set<() => void>();
  #runTask: Promise<void> | undefined;
  #reconcileTask: Promise<void> | undefined;
  #stopped = false;

  constructor(host: WatchHost, options: SubscriptionManagerOptions = {}) {
    this.#host = host;
    const interval = options.reconcileIntervalMs ?? DEFAULT_RECONCILE_INTERVAL_MS;
    if (!Number.isFinite(interval) || interval <= 0) {
      throw new TypeError("reconcileIntervalMs must be positive");
    }
    this.#reconcileIntervalMs = interval;
    this.#random = options.random ?? Math.random;
    this.#sleep = options.sleep ?? abortableDelay;
    const root = host._rootSignal();
    if (root.aborted) this.#controller.abort(root.reason);
    else root.addEventListener("abort", () => this.#controller.abort(root.reason), { once: true });
  }

  get currentRevision(): bigint {
    return this.#lastRevision;
  }

  get status(): WatchStatus {
    let parameterHandlerCount = 0;
    for (const handlers of this.#parameterHandlers.values()) {
      parameterHandlerCount += handlers.size;
    }
    return Object.freeze({
      state: this.#state,
      reconciliation: this.#reconciliation,
      currentRevision: this.#lastRevision,
      reconnectCount: this.#reconnectCount,
      namespaceCount: this.#namespaces.size,
      trackedParameterCount: this.#known.size,
      watcherCount: this.#watchers.size,
      parameterHandlerCount,
      ...(this.#connectedAtUnixMs === undefined
        ? {}
        : { connectedAtUnixMs: this.#connectedAtUnixMs }),
      ...(this.#lastEventAtUnixMs === undefined
        ? {}
        : { lastEventAtUnixMs: this.#lastEventAtUnixMs }),
      ...(this.#disconnectedAtUnixMs === undefined
        ? {}
        : { disconnectedAtUnixMs: this.#disconnectedAtUnixMs }),
      ...(this.#lastReconcileAttemptAtUnixMs === undefined
        ? {}
        : { lastReconcileAttemptAtUnixMs: this.#lastReconcileAttemptAtUnixMs }),
      ...(this.#lastReconcileSuccessAtUnixMs === undefined
        ? {}
        : { lastReconcileSuccessAtUnixMs: this.#lastReconcileSuccessAtUnixMs }),
      ...(this.#lastReconcileFailureAtUnixMs === undefined
        ? {}
        : { lastReconcileFailureAtUnixMs: this.#lastReconcileFailureAtUnixMs }),
    });
  }

  registerParameter(
    ref: ResourceRef,
    initial: string,
    handler: ParameterUpdateHandler,
  ): () => void {
    const path = displayPath(ref);
    const known = this.#known.get(path);
    if (known === undefined) {
      this.#known.set(path, { value: initial, present: true, revision: 0n });
    }
    if (known !== undefined) {
      handler(known.value, known.present);
    }
    let handlers = this.#parameterHandlers.get(path);
    if (!handlers) {
      handlers = new Set();
      this.#parameterHandlers.set(path, handlers);
    }
    handlers.add(handler);
    const wasStarted = this.#started;
    const changed = this.#retainNamespace(ref.namespace);
    this.#ensureStarted();
    if (wasStarted && changed) this.#restart();

    let active = true;
    return () => {
      if (!active) return;
      active = false;
      handlers.delete(handler);
      if (handlers.size === 0) {
        this.#parameterHandlers.delete(path);
        if (this.#known.get(path)?.present === false) this.#known.delete(path);
      }
      if (this.#releaseNamespace(ref.namespace)) this.#restart();
    };
  }

  watch(namespace: NamespaceRef, callback: WatchCallback, signal?: AbortSignal): () => void {
    if (typeof callback !== "function") throw new TypeError("watch callback is required");
    if (this.#stopped) throw new KmsError("failed_precondition", "KMS watch manager is stopped");
    if (signal?.aborted) return () => undefined;
    const watcher: Watcher = { id: this.#nextWatcherId++, namespace, callback };
    this.#watchers.set(watcher.id, watcher);
    const wasStarted = this.#started;
    const changed = this.#retainNamespace(namespace);
    this.#ensureStarted();
    if (wasStarted && changed) this.#restart();

    let active = true;
    const stop = () => {
      if (!active) return;
      active = false;
      this.#watchers.delete(watcher.id);
      this.#watcherStops.delete(watcher.id);
      signal?.removeEventListener("abort", stop);
      if (this.#releaseNamespace(namespace)) this.#restart();
    };
    this.#watcherStops.set(watcher.id, stop);
    if (signal) {
      if (signal.aborted) stop();
      else signal.addEventListener("abort", stop, { once: true });
    }
    return stop;
  }

  async stop(): Promise<void> {
    if (this.#stopped) return;
    this.#stopped = true;
    this.#state = "stopped";
    for (const stop of [...this.#watcherStops.values()]) stop();
    this.#controller.abort(new DOMException("KMS watches stopped", "AbortError"));
    this.#sessionController?.abort();
    this.#backoffController?.abort();
    await Promise.allSettled([this.#runTask, this.#reconcileTask].filter(Boolean));
    // A session can resume between its awaited registration send and the
    // abort becoming observable. Reassert the terminal state after every
    // owned task has settled so shutdown cannot report a stale connection.
    this.#state = "stopped";
    this.#watchers.clear();
    this.#watcherStops.clear();
    this.#parameterHandlers.clear();
    this.#known.clear();
    this.#namespaces.clear();
    this.#namespaceRefCounts.clear();
  }

  #retainNamespace(namespace: NamespaceRef): boolean {
    const key = namespaceKey(namespace);
    const count = this.#namespaceRefCounts.get(key) ?? 0;
    this.#namespaceRefCounts.set(key, count + 1);
    if (count > 0) return false;
    const captured = Object.freeze({ ...namespace });
    this.#namespaces.set(key, captured);
    this.#scopeChanged();
    // Cached secrets can predate this namespace's first watch. Invalidate them
    // immediately rather than waiting for the requested snapshot to arrive.
    this.#host._cache().invalidateSecretsInNamespaces([captured]);
    return true;
  }

  #releaseNamespace(namespace: NamespaceRef): boolean {
    const key = namespaceKey(namespace);
    const count = this.#namespaceRefCounts.get(key);
    if (count === undefined) return false;
    if (count > 1) {
      this.#namespaceRefCounts.set(key, count - 1);
      return false;
    }
    this.#namespaceRefCounts.delete(key);
    this.#namespaces.delete(key);
    for (const path of this.#known.keys()) {
      const ref = refOf(path);
      if (ref !== undefined && namespaceKey(ref.namespace) === key) this.#known.delete(path);
    }
    this.#scopeChanged();
    return true;
  }

  #scopeChanged(): void {
    this.#namespaceGeneration += 1;
    const previous = this.#scopeController;
    this.#scopeController = new AbortController();
    previous.abort(new DOMException("KMS namespace set changed", "AbortError"));
    for (const wake of [...this.#scopeWaiters]) wake();
  }

  #waitForNamespace(): Promise<void> {
    if (this.#namespaces.size > 0 || this.#controller.signal.aborted) return Promise.resolve();
    return new Promise<void>((resolve) => {
      const wake = (): void => {
        this.#scopeWaiters.delete(wake);
        this.#controller.signal.removeEventListener("abort", wake);
        resolve();
      };
      this.#scopeWaiters.add(wake);
      this.#controller.signal.addEventListener("abort", wake, { once: true });
      if (this.#namespaces.size > 0 || this.#controller.signal.aborted) wake();
    });
  }

  #ensureStarted(): void {
    if (this.#started || this.#stopped) return;
    this.#started = true;
    this.#runTask = this.#run().catch((error: unknown) => {
      if (!this.#controller.signal.aborted)
        this.#host.logger.warn(`KMS watch stopped: ${safeError(error)}`);
    });
    this.#reconcileTask = this.#reconcileLoop().catch((error: unknown) => {
      if (!this.#controller.signal.aborted) {
        this.#host.logger.warn(`KMS reconciliation stopped: ${safeError(error)}`);
      }
    });
  }

  #restart(): void {
    this.#restartRequested = true;
    this.#sessionController?.abort(new DOMException("KMS namespace set changed", "AbortError"));
    this.#backoffController?.abort(new DOMException("KMS namespace set changed", "AbortError"));
  }

  async #run(): Promise<void> {
    let attempt = 0;
    while (!this.#controller.signal.aborted) {
      if (this.#namespaces.size === 0) {
        this.#state = "idle";
        await this.#waitForNamespace();
        continue;
      }
      this.#restartRequested = false;
      this.#state = "connecting";
      try {
        await this.#runSession();
      } catch (error) {
        if (this.#controller.signal.aborted) return;
        if (!this.#restartRequested) {
          this.#state = "reconnecting";
          this.#reconnectCount += 1;
          this.#disconnectedAtUnixMs = Date.now();
          const delay = fullJitterBackoff(attempt++, this.#random);
          this.#host.logger.warn(
            `KMS watch stream ended (${safeError(error)}); reconnecting in ${delay}ms`,
          );
          const backoffController = new AbortController();
          this.#backoffController = backoffController;
          try {
            await this.#sleep(
              delay,
              AbortSignal.any([this.#controller.signal, backoffController.signal]),
            ).catch(() => undefined);
          } finally {
            if (this.#backoffController === backoffController) {
              this.#backoffController = undefined;
            }
          }
        }
      }
      if (this.#restartRequested) attempt = 0;
    }
  }

  async #runSession(): Promise<void> {
    const namespaces = [...this.#namespaces.values()].sort((a, b) =>
      displayNamespace(a).localeCompare(displayNamespace(b)),
    );
    this.#streamNamespaces = namespaces;
    const controller = new AbortController();
    this.#sessionController = controller;
    const signal = AbortSignal.any([this.#controller.signal, controller.signal]);
    const stream = this.#host._watchTransport().bidi(WatchServiceService.subscribe, {
      metadata: this.#host._metadata(),
      signal,
    });
    try {
      const namespaceGeneration = this.#namespaceGeneration;
      const requestFullSnapshot = namespaceGeneration > this.#snapshotGeneration;
      await stream.send({
        clientName: this.#host.clientName,
        namespaces: namespaces.map((namespace) => ({ ...namespace })),
        lastSeenRevision: requestFullSnapshot ? 0n : this.#lastRevision,
        ackedRevision: 0n,
      });
      this.#state = "connected";
      this.#connectedAtUnixMs = Date.now();
      for await (const event of stream) {
        if (namespaceGeneration !== this.#namespaceGeneration) break;
        await this.#handleEvent(
          event,
          stream.send.bind(stream),
          requestFullSnapshot ? namespaceGeneration : undefined,
        );
      }
      throw new Error("watch stream ended");
    } finally {
      stream.cancel();
      if (this.#sessionController === controller) this.#sessionController = undefined;
    }
  }

  async #handleEvent(
    event: SubscribeEvent,
    send: (request: {
      clientName: string;
      namespaces: { env: string; app: string }[];
      lastSeenRevision: bigint;
      ackedRevision: bigint;
    }) => Promise<void>,
    requestedSnapshotGeneration?: number,
  ): Promise<void> {
    this.#lastEventAtUnixMs = Date.now();
    switch (event.event?.$case) {
      case "snapshot":
        this.#applySnapshot(event.event.value, event.revision);
        if (requestedSnapshotGeneration !== undefined) {
          this.#snapshotGeneration = Math.max(
            this.#snapshotGeneration,
            requestedSnapshotGeneration,
          );
        }
        this.#advanceRevision(event.revision);
        return;
      case "change":
        if (this.#shouldApply(event.revision)) this.#applyChange(event.event.value, event.revision);
        this.#advanceRevision(event.revision);
        return;
      case "secretChange":
        if (this.#shouldApply(event.revision)) {
          this.#applySecretChange(event.event.value, event.revision);
        }
        this.#advanceRevision(event.revision);
        return;
      case "heartbeat":
        this.#advanceRevision(event.revision);
        await send({
          clientName: "",
          namespaces: [],
          lastSeenRevision: 0n,
          ackedRevision: this.#lastRevision,
        });
        return;
      case undefined:
        return;
    }
  }

  #applySnapshot(snapshot: Snapshot, revision: bigint): void {
    this.#host._cache().invalidateParametersInNamespaces(this.#streamNamespaces);
    this.#host._cache().invalidateSecretsInNamespaces(this.#streamNamespaces);
    const present = new Set<string>();
    for (const parameter of snapshot.parameters) {
      if (!parameter.ref?.namespace) continue;
      const ref: ResourceRef = {
        namespace: { env: parameter.ref.namespace.env, app: parameter.ref.namespace.app },
        key: parameter.ref.key,
      };
      const path = displayPath(ref);
      present.add(path);
      this.#setValue(path, parameter.value, true, parameter.version, revision, false);
    }
    for (const [path, known] of this.#known) {
      if (!known.present || present.has(path) || !this.#pathInStreamScope(path)) continue;
      this.#setValue(path, "", false, 0n, revision, false);
    }
  }

  #pathInStreamScope(path: string): boolean {
    const ref = refOf(path);
    return Boolean(
      ref && this.#streamNamespaces.some((namespace) => namespaceEquals(namespace, ref.namespace)),
    );
  }

  #applyChange(change: ParameterChange, revision: bigint): void {
    const ref = wireChangeRef(change.ref);
    if (!ref) return;
    if (change.changeType === "delete") {
      const path = displayPath(ref);
      // A normal point read can populate the cache without registering this
      // path in #known. Every explicit tombstone still invalidates that cache,
      // while #setValue preserves value-change-only watch delivery parity.
      this.#host._cache().invalidateParam(path);
      this.#setValue(path, "", false, change.version, revision, false);
    } else {
      this.#setValue(displayPath(ref), change.value, true, change.version, revision, false);
    }
  }

  #applySecretChange(change: SecretMetadataChange, revision: bigint): void {
    const ref = wireChangeRef(change.ref);
    if (!ref) return;
    const path = displayPath(ref);
    this.#host._cache().invalidateSecret(path);
    const event: WatchEvent = Object.freeze({
      type: "secret_change",
      namespace: displayNamespace(ref.namespace),
      key: ref.key,
      path,
      version: change.version,
      revision,
      changeType: change.changeType,
    });
    this.#fireWatchers(ref.namespace, event);
  }

  #setValue(
    path: string,
    value: string,
    present: boolean,
    version: bigint,
    revision: bigint,
    reconcile: boolean,
  ): void {
    const previous = this.#known.get(path);
    // Reconciliation captures a point-in-time global fence before issuing
    // list RPCs. If any live event advanced the stream while those RPCs were
    // in flight, none of the older reconciliation values may be installed,
    // including for a tombstone that has already been compacted.
    if (reconcile && revision < this.#lastRevision) return;
    if (previous && !revisionAllowsWrite(previous.revision, revision, reconcile)) return;
    const nextRevision = previous && previous.revision > revision ? previous.revision : revision;
    const changed = present
      ? !previous?.present || previous.value !== value
      : Boolean(previous?.present);
    this.#known.set(path, { value: present ? value : "", present, revision: nextRevision });
    if (!changed) {
      // An unknown live tombstone still advances the global revision and
      // invalidates the ordinary read cache in #applyChange, but it must not
      // create permanent per-path history or invent a value-change callback.
      if (!present && !this.#parameterHandlers.has(path)) this.#known.delete(path);
      return;
    }

    this.#host._cache().invalidateParam(path);
    for (const handler of this.#parameterHandlers.get(path) ?? []) handler(value, present);
    const ref = refOf(path);
    if (!ref) return;
    const event: WatchEvent = present
      ? Object.freeze({
          type: "put",
          namespace: displayNamespace(ref.namespace),
          key: ref.key,
          path,
          value,
          version,
          revision,
          changeType: "put",
        })
      : Object.freeze({
          type: "delete",
          namespace: displayNamespace(ref.namespace),
          key: ref.key,
          path,
          version,
          revision,
          changeType: "delete",
        });
    this.#fireWatchers(ref.namespace, event);
    if (!present && !this.#parameterHandlers.has(path)) this.#known.delete(path);
  }

  #fireWatchers(namespace: NamespaceRef, event: WatchEvent): void {
    for (const watcher of this.#watchers.values()) {
      if (!namespaceEquals(watcher.namespace, namespace)) continue;
      this.#host._dispatch(event.path, () => {
        if (this.#watchers.get(watcher.id) === watcher) return watcher.callback(event);
      });
    }
  }

  #shouldApply(revision: bigint): boolean {
    return revision === 0n || revision > this.#lastRevision;
  }

  #advanceRevision(revision: bigint): void {
    if (revision > this.#lastRevision) this.#lastRevision = revision;
  }

  async #reconcileLoop(): Promise<void> {
    while (!this.#controller.signal.aborted) {
      if (this.#namespaces.size === 0) {
        this.#reconciliation = "not_started";
        await this.#waitForNamespace();
        continue;
      }
      const scopeController = this.#scopeController;
      await this.#sleep(
        this.#reconcileIntervalMs,
        AbortSignal.any([this.#controller.signal, scopeController.signal]),
      ).catch(() => undefined);
      if (this.#controller.signal.aborted) return;
      if (scopeController.signal.aborted) continue;
      this.#lastReconcileAttemptAtUnixMs = Date.now();
      try {
        if (await this.#reconcile()) {
          this.#reconciliation = "healthy";
          this.#lastReconcileSuccessAtUnixMs = Date.now();
        } else {
          this.#reconciliation = "degraded";
          this.#lastReconcileFailureAtUnixMs = Date.now();
        }
      } catch (error) {
        this.#reconciliation = "degraded";
        this.#lastReconcileFailureAtUnixMs = Date.now();
        this.#host.logger.warn(`KMS reconciliation failed: ${safeError(error)}`);
      }
    }
  }

  async #reconcile(): Promise<boolean> {
    const namespaceGeneration = this.#namespaceGeneration;
    const namespaces = [...this.#namespaces.values()];
    // Capture every previously-present path, not only declarative
    // ParameterValue registrations. Namespace watchers must also receive a
    // tombstone when reconciliation discovers a deletion that was missed
    // while the stream was disconnected.
    const knownPaths = [...this.#known].filter(([, value]) => value.present).map(([path]) => path);
    const snapshotRevision = this.#lastRevision;
    const present = new Set<string>();
    const completelyListed = new Set<string>();

    for (const namespace of namespaces) {
      if (
        await this.#reconcileNamespace(namespace, snapshotRevision, namespaceGeneration, present)
      ) {
        completelyListed.add(namespaceKey(namespace));
      }
    }
    for (const path of knownPaths) {
      if (present.has(path)) continue;
      const ref = refOf(path);
      if (!ref || !completelyListed.has(namespaceKey(ref.namespace))) continue;
      this.#setValue(path, "", false, 0n, snapshotRevision, true);
    }
    return (
      completelyListed.size === namespaces.length &&
      namespaceGeneration === this.#namespaceGeneration &&
      snapshotRevision === this.#lastRevision
    );
  }

  async #reconcileNamespace(
    namespace: NamespaceRef,
    snapshotRevision: bigint,
    namespaceGeneration: number,
    present: Set<string>,
  ): Promise<boolean> {
    let pageToken = "";
    for (let page = 0; page < MAX_RECONCILE_PAGES; page++) {
      let response: Page<Parameter>;
      try {
        response = await this.#host.listParametersInNamespace(namespace, {
          pageToken,
          pageSize: 0,
          signal: this.#controller.signal,
        });
      } catch {
        return false;
      }
      if (
        namespaceGeneration !== this.#namespaceGeneration ||
        !this.#namespaces.has(namespaceKey(namespace))
      ) {
        return false;
      }
      for (const parameter of response.items) {
        present.add(parameter.path);
        this.#setValue(
          parameter.path,
          parameter.value,
          true,
          parameter.version,
          snapshotRevision,
          true,
        );
      }
      pageToken = response.nextPageToken;
      if (!pageToken) return true;
    }
    return false;
  }
}

export function revisionAllowsWrite(
  previousRevision: bigint,
  revision: bigint,
  reconcile: boolean,
): boolean {
  return reconcile ? previousRevision <= revision : revision === 0n || revision > previousRevision;
}

export function fullJitterBackoff(attempt: number, random = Math.random): number {
  const ceiling = Math.min(60_000, 1_000 * 2 ** Math.min(Math.max(0, attempt), 6));
  return Math.max(10, Math.floor(random() * ceiling));
}

function wireChangeRef(
  value: { namespace: { env: string; app: string } | undefined; key: string } | undefined,
): ResourceRef | undefined {
  if (!value?.namespace) return undefined;
  return {
    namespace: { env: value.namespace.env, app: value.namespace.app },
    key: value.key,
  };
}

function abortableDelay(milliseconds: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) return Promise.reject(signal.reason);
  return new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", abort);
      resolve();
    }, milliseconds);
    const abort = () => {
      clearTimeout(timer);
      reject(signal.reason);
    };
    signal.addEventListener("abort", abort, { once: true });
  });
}

function safeError(error: unknown): string {
  if (error instanceof KmsError) return error.code;
  if (error instanceof Error) return error.name;
  return "unknown";
}
