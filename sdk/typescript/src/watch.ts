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

interface WatchHost {
  readonly clientName: string;
  readonly timeoutMs: number;
  readonly logger: { warn(message: string): void };
  _watchTransport(): RpcTransport;
  _metadata(secretToken?: string): Readonly<Record<string, string>>;
  _rootSignal(): AbortSignal;
  _cache(): {
    invalidateParam(path: string): void;
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
  readonly #parameterHandlers = new Map<string, Set<ParameterUpdateHandler>>();
  readonly #known = new Map<string, KnownValue>();
  readonly #watchers = new Map<number, Watcher>();
  #streamNamespaces: readonly NamespaceRef[] = [];
  #nextWatcherId = 1;
  #lastRevision = 0n;
  #started = false;
  #restartRequested = false;
  #sessionController: AbortController | undefined;
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

  registerParameter(ref: ResourceRef, initial: string, handler: ParameterUpdateHandler): void {
    const path = displayPath(ref);
    this.#known.set(path, { value: initial, present: true, revision: 0n });
    let handlers = this.#parameterHandlers.get(path);
    if (!handlers) {
      handlers = new Set();
      this.#parameterHandlers.set(path, handlers);
    }
    handlers.add(handler);
    const changed = this.#addNamespace(ref.namespace);
    this.#ensureStarted();
    if (changed && this.#started) this.#restart();
  }

  watch(namespace: NamespaceRef, callback: WatchCallback, signal?: AbortSignal): () => void {
    if (typeof callback !== "function") throw new TypeError("watch callback is required");
    if (this.#stopped) throw new KmsError("failed_precondition", "KMS watch manager is stopped");
    const watcher: Watcher = { id: this.#nextWatcherId++, namespace, callback };
    this.#watchers.set(watcher.id, watcher);
    const wasStarted = this.#started;
    const changed = this.#addNamespace(namespace);
    this.#ensureStarted();
    if (wasStarted && changed) this.#restart();

    let active = true;
    const stop = () => {
      if (!active) return;
      active = false;
      this.#watchers.delete(watcher.id);
      signal?.removeEventListener("abort", stop);
    };
    if (signal) {
      if (signal.aborted) stop();
      else signal.addEventListener("abort", stop, { once: true });
    }
    return stop;
  }

  async stop(): Promise<void> {
    if (this.#stopped) return;
    this.#stopped = true;
    this.#controller.abort(new DOMException("KMS watches stopped", "AbortError"));
    this.#sessionController?.abort();
    await Promise.allSettled([this.#runTask, this.#reconcileTask].filter(Boolean));
    this.#watchers.clear();
    this.#parameterHandlers.clear();
  }

  #addNamespace(namespace: NamespaceRef): boolean {
    const key = namespaceKey(namespace);
    if (this.#namespaces.has(key)) return false;
    this.#namespaces.set(key, Object.freeze({ ...namespace }));
    return true;
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
  }

  async #run(): Promise<void> {
    let attempt = 0;
    while (!this.#controller.signal.aborted) {
      this.#restartRequested = false;
      try {
        await this.#runSession();
      } catch (error) {
        if (this.#controller.signal.aborted) return;
        if (!this.#restartRequested) {
          const delay = fullJitterBackoff(attempt++, this.#random);
          this.#host.logger.warn(
            `KMS watch stream ended (${safeError(error)}); reconnecting in ${delay}ms`,
          );
          await this.#sleep(delay, this.#controller.signal).catch(() => undefined);
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
      await stream.send({
        clientName: this.#host.clientName,
        namespaces: namespaces.map((namespace) => ({ ...namespace })),
        lastSeenRevision: this.#lastRevision,
        ackedRevision: 0n,
      });
      for await (const event of stream) await this.#handleEvent(event, stream.send.bind(stream));
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
  ): Promise<void> {
    switch (event.event?.$case) {
      case "snapshot":
        this.#applySnapshot(event.event.value, event.revision);
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
      this.#setValue(displayPath(ref), "", false, change.version, revision, false);
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
    if (previous && !revisionAllowsWrite(previous.revision, revision, reconcile)) return;
    const nextRevision = previous && previous.revision > revision ? previous.revision : revision;
    const changed = present
      ? !previous?.present || previous.value !== value
      : Boolean(previous?.present);
    this.#known.set(path, { value: present ? value : "", present, revision: nextRevision });
    if (!changed) return;

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
  }

  #fireWatchers(namespace: NamespaceRef, event: WatchEvent): void {
    for (const watcher of this.#watchers.values()) {
      if (!namespaceEquals(watcher.namespace, namespace)) continue;
      this.#host._dispatch(event.path, () => watcher.callback(event));
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
      await this.#sleep(this.#reconcileIntervalMs, this.#controller.signal);
      if (this.#controller.signal.aborted) return;
      await this.#reconcile().catch((error: unknown) => {
        this.#host.logger.warn(`KMS reconciliation failed: ${safeError(error)}`);
      });
    }
  }

  async #reconcile(): Promise<void> {
    const namespaces = [...this.#namespaces.values()];
    const parameterPaths = [...this.#parameterHandlers.keys()];
    const snapshotRevision = this.#lastRevision;
    const present = new Set<string>();
    const completelyListed = new Set<string>();

    for (const namespace of namespaces) {
      if (await this.#reconcileNamespace(namespace, snapshotRevision, present)) {
        completelyListed.add(namespaceKey(namespace));
      }
    }
    for (const path of parameterPaths) {
      if (present.has(path)) continue;
      const ref = refOf(path);
      if (!ref || !completelyListed.has(namespaceKey(ref.namespace))) continue;
      this.#setValue(path, "", false, 0n, snapshotRevision, true);
    }
  }

  async #reconcileNamespace(
    namespace: NamespaceRef,
    snapshotRevision: bigint,
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
