import {
  type NamespaceRef,
  namespaceKey,
  normalizeVersionRef,
  parseNamespace,
  refOf,
  type VersionRef,
} from "./refs.js";
import type { Secret } from "./secret.js";

export const DEFAULT_MAX_CACHE_ENTRIES = 4_096;

export interface ReadCacheOptions {
  /** Entry lifetime in milliseconds. A non-positive value disables caching. */
  readonly ttlMs: number;
  /** Independent maximum for parameter entries and secret entries. */
  readonly maxEntries?: number;
  /** Monotonic millisecond clock, injectable for deterministic tests. */
  readonly now?: () => number;
}

interface CacheEntry<T> {
  readonly value: T;
  readonly expiresAt: number;
}

type EntryMap<T> = Map<string, Map<string, CacheEntry<T>>>;

type CacheKind = "parameter" | "secret";

interface GenerationState {
  epoch: object;
  readers: number;
}

interface ReadGeneration {
  readonly kind: CacheKind;
  readonly path: string;
  readonly state: GenerationState;
  readonly epoch: object;
  released: boolean;
}

type GenerationMap = Map<string, GenerationState>;

function selectorKey(selector: VersionRef): string {
  const { version, label } = normalizeVersionRef(selector);
  return `${version}\0${label}`;
}

function positionalSelector(version: bigint, label: string): VersionRef {
  return { version, label };
}

/**
 * Bounded in-memory TTL cache used by parameter and secret reads.
 *
 * Parameter and secret caps are independent. All secret writes and reads clone
 * their `Secret`, preventing callers from ever sharing plaintext buffers with
 * cache state. JavaScript execution is run-to-completion, so operations cannot
 * interleave while a map mutation is in progress.
 */
export class ReadCache {
  readonly #ttlMs: number;
  readonly #maxEntries: number;
  readonly #now: () => number;
  readonly #parameters: EntryMap<string> = new Map();
  readonly #secrets: EntryMap<Secret> = new Map();
  readonly #parameterGenerations: GenerationMap = new Map();
  readonly #secretGenerations: GenerationMap = new Map();
  #parameterCount = 0;
  #secretCount = 0;

  constructor(ttlMsOrOptions: number | ReadCacheOptions) {
    const options = typeof ttlMsOrOptions === "number" ? { ttlMs: ttlMsOrOptions } : ttlMsOrOptions;
    if (!Number.isFinite(options.ttlMs)) throw new TypeError("cache ttlMs must be finite");
    const maxEntries = options.maxEntries ?? DEFAULT_MAX_CACHE_ENTRIES;
    if (!Number.isSafeInteger(maxEntries) || maxEntries <= 0) {
      throw new TypeError("cache maxEntries must be a positive safe integer");
    }
    this.#ttlMs = options.ttlMs;
    this.#maxEntries = maxEntries;
    this.#now = options.now ?? (() => performance.now());
  }

  get enabled(): boolean {
    return this.#ttlMs > 0;
  }

  get parameterSize(): number {
    return this.#parameterCount;
  }

  get secretSize(): number {
    return this.#secretCount;
  }

  getParameter(path: string, selector: VersionRef = {}): string | undefined {
    return this.#get(this.#parameters, path, selector, "parameter");
  }

  setParameter(path: string, value: string, selector: VersionRef = {}): void {
    this.#parameterCount = this.#set(this.#parameters, this.#parameterCount, path, selector, value);
    this.#parameterCount = this.#evict(this.#parameters, this.#parameterCount, "parameter");
  }

  /** Positional Go-parity alias for internal client hot paths. */
  getParam(path: string, version: bigint = 0n, label = ""): string | undefined {
    return this.getParameter(path, positionalSelector(version, label));
  }

  /** Positional Go-parity alias for internal client hot paths. */
  putParam(path: string, version: bigint, label: string, value: string): void {
    this.setParameter(path, value, positionalSelector(version, label));
  }

  /** Capture the invalidation generation before starting a parameter RPC. */
  beginParameterRead(path: string): ReadGeneration | undefined {
    return this.#beginRead(this.#parameterGenerations, "parameter", path);
  }

  /** Populate the parameter cache only when no invalidation raced the RPC. */
  cacheParameterIfUnchanged(
    generation: ReadGeneration,
    version: bigint,
    label: string,
    value: string,
  ): boolean {
    if (!this.#isCurrent(this.#parameterGenerations, generation, "parameter")) return false;
    this.putParam(generation.path, version, label, value);
    return true;
  }

  getSecret(path: string, selector: VersionRef = {}): Secret | undefined {
    return this.#get(this.#secrets, path, selector, "secret")?.clone();
  }

  setSecret(path: string, secret: Secret, selector: VersionRef = {}): void {
    this.#secretCount = this.#set(this.#secrets, this.#secretCount, path, selector, secret.clone());
    this.#secretCount = this.#evict(this.#secrets, this.#secretCount, "secret");
  }

  /** Positional Go-parity alias for internal client hot paths. */
  getSecretAt(path: string, version: bigint = 0n, label = ""): Secret | undefined {
    return this.getSecret(path, positionalSelector(version, label));
  }

  /** Positional Go-parity alias for internal client hot paths. */
  putSecret(path: string, version: bigint, label: string, secret: Secret): void {
    this.setSecret(path, secret, positionalSelector(version, label));
  }

  /** Capture the invalidation generation before starting a secret RPC. */
  beginSecretRead(path: string): ReadGeneration | undefined {
    return this.#beginRead(this.#secretGenerations, "secret", path);
  }

  /** Populate the secret cache only when no invalidation raced the RPC. */
  cacheSecretIfUnchanged(
    generation: ReadGeneration,
    version: bigint,
    label: string,
    secret: Secret,
  ): boolean {
    if (!this.#isCurrent(this.#secretGenerations, generation, "secret")) return false;
    this.putSecret(generation.path, version, label, secret);
    return true;
  }

  /** Release a read generation after its RPC settles, whether or not it cached. */
  endRead(generation: ReadGeneration | undefined): void {
    if (generation === undefined || generation.released) return;
    generation.released = true;
    generation.state.readers--;
    const generations =
      generation.kind === "parameter" ? this.#parameterGenerations : this.#secretGenerations;
    if (generation.state.readers === 0 && generations.get(generation.path) === generation.state) {
      generations.delete(generation.path);
    }
  }

  invalidateParameter(path: string): void {
    this.#invalidateGeneration(this.#parameterGenerations, path);
    const entries = this.#parameters.get(path);
    if (entries === undefined) return;
    this.#parameterCount -= entries.size;
    this.#parameters.delete(path);
  }

  invalidateParam(path: string): void {
    this.invalidateParameter(path);
  }

  /** Invalidate every cached parameter selector in authoritative snapshot scope. */
  invalidateParametersInNamespaces(namespaces: Iterable<NamespaceRef | string>): void {
    this.#parameterCount = this.#invalidateNamespaces(
      this.#parameters,
      this.#parameterGenerations,
      this.#parameterCount,
      namespaces,
    );
  }

  invalidateSecret(path: string): void {
    this.#invalidateGeneration(this.#secretGenerations, path);
    const entries = this.#secrets.get(path);
    if (entries === undefined) return;
    this.#secretCount -= entries.size;
    this.#secrets.delete(path);
  }

  /**
   * Invalidate tokenless secret cache entries in authoritative snapshot scope.
   * A watch snapshot contains parameters only, so this closes the replay-window
   * gap for secrets that changed while a subscriber was disconnected.
   */
  invalidateSecretsInNamespaces(namespaces: Iterable<NamespaceRef | string>): void {
    this.#secretCount = this.#invalidateNamespaces(
      this.#secrets,
      this.#secretGenerations,
      this.#secretCount,
      namespaces,
    );
  }

  #invalidateNamespaces<T>(
    cache: EntryMap<T>,
    generations: GenerationMap,
    count: number,
    namespaces: Iterable<NamespaceRef | string>,
  ): number {
    if (!this.enabled) return count;
    const scope = new Set<string>();
    for (const namespace of namespaces) {
      const ref = typeof namespace === "string" ? parseNamespace(namespace) : namespace;
      scope.add(namespaceKey(ref));
    }
    if (scope.size === 0) return count;

    for (const [path, state] of generations) {
      const ref = refOf(path);
      if (ref !== undefined && scope.has(namespaceKey(ref.namespace))) state.epoch = {};
    }
    for (const [path, entries] of cache) {
      const ref = refOf(path);
      if (ref === undefined || !scope.has(namespaceKey(ref.namespace))) continue;
      count -= entries.size;
      cache.delete(path);
    }
    return count;
  }

  clear(): void {
    for (const state of this.#parameterGenerations.values()) state.epoch = {};
    for (const state of this.#secretGenerations.values()) state.epoch = {};
    this.#parameters.clear();
    this.#secrets.clear();
    this.#parameterCount = 0;
    this.#secretCount = 0;
  }

  #beginRead(
    generations: GenerationMap,
    kind: CacheKind,
    path: string,
  ): ReadGeneration | undefined {
    if (!this.enabled) return undefined;
    let state = generations.get(path);
    if (state === undefined) {
      state = { epoch: {}, readers: 0 };
      generations.set(path, state);
    }
    state.readers++;
    return { kind, path, state, epoch: state.epoch, released: false };
  }

  #isCurrent(generations: GenerationMap, generation: ReadGeneration, kind: CacheKind): boolean {
    return (
      !generation.released &&
      generation.kind === kind &&
      generations.get(generation.path) === generation.state &&
      generation.state.epoch === generation.epoch
    );
  }

  #invalidateGeneration(generations: GenerationMap, path: string): void {
    const state = generations.get(path);
    if (state !== undefined) state.epoch = {};
  }

  #get<T>(
    cache: EntryMap<T>,
    path: string,
    selector: VersionRef,
    kind: "parameter" | "secret",
  ): T | undefined {
    if (!this.enabled) return undefined;
    const entries = cache.get(path);
    if (entries === undefined) return undefined;
    const key = selectorKey(selector);
    const entry = entries.get(key);
    if (entry === undefined) return undefined;
    if (this.#now() < entry.expiresAt) return entry.value;

    entries.delete(key);
    if (kind === "parameter") this.#parameterCount--;
    else this.#secretCount--;
    if (entries.size === 0) cache.delete(path);
    return undefined;
  }

  #set<T>(cache: EntryMap<T>, count: number, path: string, selector: VersionRef, value: T): number {
    if (!this.enabled) return count;
    let entries = cache.get(path);
    if (entries === undefined) {
      entries = new Map();
      cache.set(path, entries);
    }
    const key = selectorKey(selector);
    if (entries.has(key)) {
      // Refresh insertion order so deterministic eviction approximates FIFO.
      entries.delete(key);
    } else {
      count++;
    }
    entries.set(key, { value, expiresAt: this.#now() + this.#ttlMs });
    return count;
  }

  #evict<T>(cache: EntryMap<T>, count: number, kind: "parameter" | "secret"): number {
    if (count <= this.#maxEntries) return count;
    const now = this.#now();

    for (const [path, entries] of cache) {
      for (const [key, entry] of entries) {
        if (now < entry.expiresAt) continue;
        entries.delete(key);
        count--;
      }
      if (entries.size === 0) cache.delete(path);
    }

    while (count > this.#maxEntries) {
      const pathEntry = cache.entries().next();
      if (pathEntry.done) break;
      const [path, entries] = pathEntry.value;
      const keyEntry = entries.keys().next();
      if (keyEntry.done) {
        cache.delete(path);
        continue;
      }
      entries.delete(keyEntry.value);
      count--;
      if (entries.size === 0) cache.delete(path);
    }

    // Keep the dedicated counters honest even if this method is extended.
    if (kind === "parameter") this.#parameterCount = count;
    else this.#secretCount = count;
    return count;
  }
}

export { ReadCache as Cache };
