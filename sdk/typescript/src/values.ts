import { ConfigError, isKmsError, NotInitializedError, wrapError } from "./errors.js";
import { type ResourceRef, splitDisplayPath, type VersionRef } from "./refs.js";
import { REDACTED, Secret } from "./secret.js";

const inspectCustom = Symbol.for("nodejs.util.inspect.custom");

export interface ValueReadOptions extends VersionRef {
  readonly signal?: AbortSignal;
  /** Absolute deadline (`Date`) or transport-specific millisecond value. */
  readonly deadline?: Date | number;
}

export interface SecretReadOptions extends ValueReadOptions {
  readonly secretToken?: string;
}

export type ParameterUpdateHandler = (value: string, present?: boolean) => void;
export type ChangeCallback = (oldValue: string, newValue: string) => void;

export type SubscriptionHandle =
  | undefined
  | void
  | (() => void | Promise<void>)
  | {
      unsubscribe?: () => void | Promise<void>;
      dispose?: () => void | Promise<void>;
      close?: () => void | Promise<void>;
    };

/**
 * Small client surface required by declarative values. `Client` implements it,
 * while tests and alternative transports can provide a structural fake.
 */
export interface ValueResolver {
  getParameter(key: string, options?: ValueReadOptions): Promise<string>;
  getSecret(key: string, options?: SecretReadOptions): Promise<Secret>;

  /** Opt in to using a default for remote failures other than not-found. */
  readonly fallbackToDefaultsOnError?: boolean;
  defaultAllowedForError?(error: unknown): boolean;
  _defaultAllowedForError?(error: unknown): boolean;

  /** Public alternative for clients that expose subscriptions by key. */
  subscribeParameter?(
    key: string,
    initialValue: string,
    handler: ParameterUpdateHandler,
  ): SubscriptionHandle | Promise<SubscriptionHandle>;

  /** Internal shared-watch integration implemented by the first-party Client. */
  _resolveRef?(key: string): ResourceRef | Promise<ResourceRef>;
  _registerParameter?(
    ref: ResourceRef,
    initialValue: string,
    handler: ParameterUpdateHandler,
  ): SubscriptionHandle | Promise<SubscriptionHandle>;
  _enqueueCallback?(callback: () => void): void;
  _log?(message: string): void;
}

export interface SecretValueOptions {
  readonly key?: string;
  /** Per-secret access token (and key share for client-bound secrets). */
  readonly token?: string;
  /** A non-empty environment value wins and avoids all store access. */
  readonly envVar?: string;
  /** Non-empty development fallback. Used for not-found by default. */
  readonly default?: string;
}

export interface ParameterValueOptions {
  readonly key?: string;
  readonly envVar?: string;
  readonly default?: string;
  /** Resolve once and opt out of the shared hot-reload subscription. */
  readonly static?: boolean;
}

function secretOptions(
  keyOrOptions: string | SecretValueOptions,
  options: Omit<SecretValueOptions, "key">,
): Required<SecretValueOptions> {
  const input = typeof keyOrOptions === "string" ? { ...options, key: keyOrOptions } : keyOrOptions;
  return {
    key: input.key ?? "",
    token: input.token ?? "",
    envVar: input.envVar ?? "",
    default: input.default ?? "",
  };
}

function parameterOptions(
  keyOrOptions: string | ParameterValueOptions,
  options: Omit<ParameterValueOptions, "key">,
): Required<ParameterValueOptions> {
  const input = typeof keyOrOptions === "string" ? { ...options, key: keyOrOptions } : keyOrOptions;
  return {
    key: input.key ?? "",
    envVar: input.envVar ?? "",
    default: input.default ?? "",
    static: input.static ?? false,
  };
}

function readOptions(options: ValueReadOptions): ValueReadOptions {
  return { ...options };
}

function hasDefault(value: string): boolean {
  // Go parity: the empty string means no fallback was configured.
  return value.length > 0;
}

function fallbackAllowed(client: ValueResolver, error: unknown): boolean {
  // Configuration/wiring failures are not store-fetch failures and must never
  // be hidden by the broad fallback switch.
  if (
    isKmsError(error, "no_namespace") ||
    isKmsError(error, "invalid_argument") ||
    isKmsError(error, "not_initialized")
  ) {
    return false;
  }
  if (isKmsError(error, "not_found")) return true;
  if (client.defaultAllowedForError !== undefined) {
    return client.defaultAllowedForError(error);
  }
  if (client._defaultAllowedForError !== undefined) {
    return client._defaultAllowedForError(error);
  }
  return client.fallbackToDefaultsOnError === true;
}

/** A declarative, store-backed secret that always redacts implicit rendering. */
export class SecretValue {
  readonly #key: string;
  readonly #token: string;
  readonly #envVar: string;
  readonly #default: string;
  #resolved: Secret | undefined;
  #initializing: Promise<void> | undefined;

  constructor(
    keyOrOptions: string | SecretValueOptions = {},
    options: Omit<SecretValueOptions, "key"> = {},
  ) {
    const normalized = secretOptions(keyOrOptions, options);
    this.#key = normalized.key;
    this.#token = normalized.token;
    this.#envVar = normalized.envVar;
    this.#default = normalized.default;
  }

  get key(): string {
    return this.#key;
  }

  get token(): string {
    return this.#token;
  }

  get envVar(): string {
    return this.#envVar;
  }

  get default(): string {
    return this.#default;
  }

  get initialized(): boolean {
    return this.#resolved !== undefined;
  }

  isInitialized(): boolean {
    return this.initialized;
  }

  /** Resolve env override, store value, or an allowed default. Idempotent. */
  async init(client: ValueResolver, options: ValueReadOptions = {}): Promise<void> {
    if (this.initialized) return;
    if (this.#initializing !== undefined) return this.#initializing;
    const pending = this.#initialize(client, options);
    this.#initializing = pending;
    try {
      await pending;
    } finally {
      if (this.#initializing === pending) this.#initializing = undefined;
    }
  }

  async initialize(client: ValueResolver, options: ValueReadOptions = {}): Promise<void> {
    return this.init(client, options);
  }

  async #initialize(client: ValueResolver, options: ValueReadOptions): Promise<void> {
    if (this.#envVar.length > 0) {
      const value = process.env[this.#envVar];
      if (value !== undefined && value.length > 0) {
        this.#resolved = new Secret(value, { path: this.#key });
        client._log?.(
          `secret ${JSON.stringify(this.#key)} resolved from env ${this.#envVar} (store fetch skipped)`,
        );
        return;
      }
    }

    if (this.#key.length > 0) {
      try {
        const secret = await client.getSecret(this.#key, {
          ...readOptions(options),
          ...(this.#token.length === 0 ? {} : { secretToken: this.#token }),
        });
        if (!(secret instanceof Secret)) {
          throw new TypeError("ValueResolver.getSecret() must return a Secret");
        }
        this.#resolved = secret.clone();
        return;
      } catch (error) {
        if (hasDefault(this.#default) && fallbackAllowed(client, error)) {
          client._log?.(
            `secret ${JSON.stringify(this.#key)} fetch failed; using configured default`,
          );
          this.#resolved = new Secret(this.#default, { path: this.#key });
          return;
        }
        throw wrapError(`resolve secret ${JSON.stringify(this.#key)}`, error);
      }
    }

    if (hasDefault(this.#default)) {
      this.#resolved = new Secret(this.#default);
      return;
    }
    throw new ConfigError("SecretValue has no key, envVar, or non-empty default configured");
  }

  /** Explicit plaintext access; throws before successful initialization. */
  value(): string {
    return this.#requireSecret().text();
  }

  text(): string {
    return this.value();
  }

  stringValue(): string {
    return this.value();
  }

  bytes(): Uint8Array {
    return this.#requireSecret().bytes();
  }

  /** Return an independent redacting wrapper around the resolved plaintext. */
  secret(): Secret {
    return this.#requireSecret().clone();
  }

  #requireSecret(): Secret {
    if (this.#resolved === undefined) throw new NotInitializedError("SecretValue", this.#key);
    return this.#resolved;
  }

  toString(): string {
    return REDACTED;
  }

  toJSON(): string {
    return REDACTED;
  }

  valueOf(): string {
    return REDACTED;
  }

  [Symbol.toPrimitive](): string {
    return REDACTED;
  }

  [inspectCustom](): string {
    return REDACTED;
  }
}

/** A declarative non-secret parameter, hot-reloaded unless `static` is set. */
export class ParameterValue {
  readonly #key: string;
  readonly #envVar: string;
  readonly #default: string;
  readonly #static: boolean;
  readonly #callbacks = new Set<ChangeCallback>();
  #value = "";
  #initialized = false;
  #pinned = false;
  #client: ValueResolver | undefined;
  #initializing: Promise<void> | undefined;
  #subscription: SubscriptionHandle;

  constructor(
    keyOrOptions: string | ParameterValueOptions = {},
    options: Omit<ParameterValueOptions, "key"> = {},
  ) {
    const normalized = parameterOptions(keyOrOptions, options);
    this.#key = normalized.key;
    this.#envVar = normalized.envVar;
    this.#default = normalized.default;
    this.#static = normalized.static;
  }

  get key(): string {
    return this.#key;
  }

  get envVar(): string {
    return this.#envVar;
  }

  get default(): string {
    return this.#default;
  }

  get static(): boolean {
    return this.#static;
  }

  get initialized(): boolean {
    return this.#initialized;
  }

  isInitialized(): boolean {
    return this.initialized;
  }

  /** Return the latest value, or the empty string before initialization. */
  get(): string {
    return this.#value;
  }

  /** Resolve and, by default, register on the client's shared subscription. */
  async init(client: ValueResolver, options: ValueReadOptions = {}): Promise<void> {
    if (this.#initialized) return;
    if (this.#initializing !== undefined) return this.#initializing;
    const pending = this.#initialize(client, options);
    this.#initializing = pending;
    try {
      await pending;
    } finally {
      if (this.#initializing === pending) this.#initializing = undefined;
    }
  }

  async initialize(client: ValueResolver, options: ValueReadOptions = {}): Promise<void> {
    return this.init(client, options);
  }

  async #initialize(client: ValueResolver, options: ValueReadOptions): Promise<void> {
    this.#client = client;
    if (this.#envVar.length > 0) {
      const value = process.env[this.#envVar];
      if (value !== undefined && value.length > 0) {
        this.#value = value;
        this.#pinned = true;
        this.#initialized = true;
        if (!this.#static) {
          client._log?.(
            `parameter ${JSON.stringify(this.#key)} pinned to env ${this.#envVar}; hot reload disabled`,
          );
        }
        return;
      }
    }

    let value: string;
    let hasStoreRef = false;
    if (this.#key.length > 0) {
      hasStoreRef = true;
      try {
        value = await client.getParameter(this.#key, readOptions(options));
        if (typeof value !== "string") {
          throw new TypeError("ValueResolver.getParameter() must return a string");
        }
      } catch (error) {
        if (!hasDefault(this.#default) || !fallbackAllowed(client, error)) {
          throw wrapError(`resolve parameter ${JSON.stringify(this.#key)}`, error);
        }
        client._log?.(
          `parameter ${JSON.stringify(this.#key)} fetch failed; using configured default`,
        );
        value = this.#default;
      }
    } else if (hasDefault(this.#default)) {
      value = this.#default;
    } else {
      throw new ConfigError("ParameterValue has no key, envVar, or non-empty default configured");
    }

    this.#value = value;
    this.#initialized = true;
    if (!this.#static && hasStoreRef) {
      try {
        this.#subscription = await this.#register(client, value);
      } catch (error) {
        this.#initialized = false;
        this.#value = "";
        throw wrapError(`subscribe parameter ${JSON.stringify(this.#key)}`, error);
      }
    }
  }

  async #register(client: ValueResolver, initialValue: string): Promise<SubscriptionHandle> {
    if (client._registerParameter !== undefined) {
      let ref: ResourceRef | undefined;
      if (client._resolveRef !== undefined) ref = await client._resolveRef(this.#key);
      else if (this.#key.startsWith("/")) ref = splitDisplayPath(this.#key);
      if (ref !== undefined) {
        return client._registerParameter(ref, initialValue, (value, present = true) => {
          this.applyUpdate(value, present);
        });
      }
    }
    if (client.subscribeParameter !== undefined) {
      return client.subscribeParameter(this.#key, initialValue, (value, present = true) => {
        this.applyUpdate(value, present);
      });
    }
    return undefined;
  }

  /**
   * Apply a shared-watch update. Deletion reverts to a configured default;
   * without one, last-known-good is retained.
   */
  applyUpdate(newValue: string, present = true): void {
    if (!this.#initialized || this.#static || this.#pinned) return;
    if (!present) {
      if (!hasDefault(this.#default)) return;
      newValue = this.#default;
    }
    const oldValue = this.#value;
    if (oldValue === newValue) return;
    this.#value = newValue;
    for (const callback of [...this.#callbacks]) {
      const invoke = () => callback(oldValue, newValue);
      if (this.#client?._enqueueCallback !== undefined) this.#client._enqueueCallback(invoke);
      else queueMicrotask(invoke);
    }
  }

  /** Register a callback and return a local callback-unsubscribe function. */
  onChange(callback: ChangeCallback): () => void {
    this.#callbacks.add(callback);
    return () => this.#callbacks.delete(callback);
  }

  /** Stop this field's subscription when it owns an explicit handle. */
  async dispose(): Promise<void> {
    const subscription = this.#subscription;
    this.#subscription = undefined;
    if (typeof subscription === "function") {
      await subscription();
    } else if (subscription?.unsubscribe !== undefined) {
      await subscription.unsubscribe();
    } else if (subscription?.dispose !== undefined) {
      await subscription.dispose();
    } else if (subscription?.close !== undefined) {
      await subscription.close();
    }
  }

  toString(): string {
    return this.get();
  }

  valueOf(): string {
    return this.get();
  }

  [Symbol.toPrimitive](): string {
    return this.get();
  }
}

export type DeclarativeValue = SecretValue | ParameterValue;

function collectValues(value: unknown, targets: Set<DeclarativeValue>, visited: Set<object>): void {
  if (value instanceof SecretValue || value instanceof ParameterValue) {
    targets.add(value);
    return;
  }
  if (value === null || typeof value !== "object" || visited.has(value)) return;
  if (
    value instanceof Secret ||
    value instanceof Date ||
    value instanceof RegExp ||
    value instanceof Map ||
    value instanceof Set ||
    ArrayBuffer.isView(value) ||
    value instanceof ArrayBuffer
  ) {
    return;
  }
  visited.add(value);

  for (const descriptor of Object.values(Object.getOwnPropertyDescriptors(value))) {
    // Reading accessors while discovering configuration can execute arbitrary
    // application code, so only ordinary enumerable data fields are walked.
    if (descriptor.enumerable && "value" in descriptor) {
      collectValues(descriptor.value, targets, visited);
    }
  }
}

/** Discover declarative values in nested objects and arrays without getters. */
export function collectDeclarativeValues(config: object): readonly DeclarativeValue[] {
  const targets = new Set<DeclarativeValue>();
  collectValues(config, targets, new Set());
  return Object.freeze([...targets]);
}

/** All field failures from a concurrent object-resolution pass. */
export class ResolutionError extends AggregateError {
  constructor(errors: readonly Error[]) {
    super(errors, `failed to resolve ${errors.length} declarative value(s)`);
    this.name = "ResolutionError";
  }
}

/**
 * Resolve every declarative value reachable through object fields and arrays.
 * Fetches run concurrently; successful siblings stay initialized on failure.
 */
export async function resolveValues(
  config: object,
  client: ValueResolver,
  options: ValueReadOptions = {},
): Promise<void> {
  if (config === null || typeof config !== "object") {
    throw new ConfigError("resolveValues requires a non-null configuration object");
  }
  const targets = collectDeclarativeValues(config);
  const results = await Promise.allSettled(targets.map((value) => value.init(client, options)));
  const errors: Error[] = [];
  for (const result of results) {
    if (result.status === "rejected") {
      errors.push(
        result.reason instanceof Error ? result.reason : new Error("value resolution failed"),
      );
    }
  }
  if (errors.length > 0) throw new ResolutionError(errors);
}
