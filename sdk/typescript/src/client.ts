import { basename } from "node:path";
import type { ChannelCredentials, ChannelOptions } from "@grpc/grpc-js";
import { ReadCache } from "./cache.js";
import { ConfigError, KmsError, mapGrpcError, wrapError } from "./errors.js";
import {
  AdminServiceService,
  type ConfigurationRelease,
  ConfigurationReleaseServiceService,
  type GetActiveReleaseResponse,
  ParameterServiceService,
  type SecretMetadata,
  SecretServiceService,
  type Parameter as WireParameter,
  type ResourceRef as WireResourceRef,
} from "./generated/kms.js";
import type {
  Page,
  Parameter,
  PutResult,
  PutSecretResult,
  SecretInfo,
  SecretVersion,
  WhoAmI,
} from "./models.js";
import {
  displayNamespace,
  displayPath,
  type NamespaceRef,
  normalizeVersionRef,
  parseNamespace,
  type ResourceRef,
  resolveRef,
  UINT64_MAX,
  type VersionRef,
} from "./refs.js";
import {
  ReleaseLoader,
  type ReleaseTransport,
  type ReleaseWatchStream,
  type SecretTokenProvider,
  type ValidateReleaseManifest,
} from "./releases/loader.js";
import { Secret } from "./secret.js";
import {
  GrpcTransport,
  insecureCredentials,
  type RpcTransport,
  type TransportCallOptions,
  type UnaryMethod,
} from "./transport.js";
import { resolveValues, type ValueReadOptions } from "./values.js";
import {
  type ParameterUpdateHandler,
  SubscriptionManager,
  type WatchCallback,
  type WatchEvent,
  type WatchOptions,
} from "./watch.js";

const DEFAULT_TIMEOUT_MS = 5_000;
const CALLBACK_QUEUE_SIZE = 1_024;
const INT64_MAX = (1n << 63n) - 1n;

export interface Logger {
  warn(message: string): void;
  error?(message: string): void;
}

export interface KmsClientOptions {
  readonly endpoint?: string;
  readonly namespace?: string;
  readonly token?: string;
  readonly credentials?: ChannelCredentials;
  /** Explicitly allow cleartext. Intended only for trusted local development. */
  readonly insecure?: boolean;
  /** Injectable protocol transport for deterministic tests and advanced integrations. */
  readonly transport?: RpcTransport;
  readonly channelOptions?: ChannelOptions;
  readonly cacheTtlMs?: number;
  readonly fallbackToDefaultsOnError?: boolean;
  readonly timeoutMs?: number;
  readonly clientName?: string;
  readonly logger?: Logger;
  readonly reconcileIntervalMs?: number;
}

export interface CallOptions {
  readonly signal?: AbortSignal;
  readonly deadline?: Date;
}

export interface GetOptions extends CallOptions, VersionRef {
  readonly secretToken?: string;
}

export interface PutParameterOptions extends CallOptions {
  readonly contentType?: string;
  readonly metadataJson?: string;
}

export interface PutSecretOptions extends CallOptions {
  readonly contentType?: string;
  readonly metadataJson?: string;
  readonly clientBound?: boolean;
  readonly generateAccessToken?: boolean;
  readonly expiresAtUnixMs?: bigint;
  readonly secretToken?: string;
}

export interface ListOptions extends CallOptions {
  readonly keyPrefix?: string;
  readonly pageSize?: number;
  readonly pageToken?: string;
}

export interface ParameterMetadata {
  readonly ref: ResourceRef;
  readonly contentType: string;
  readonly metadataJson: string;
  readonly createdAtUnixMs: bigint;
  readonly updatedAtUnixMs: bigint;
  readonly labels: Readonly<Record<string, bigint>>;
  readonly versions: readonly {
    readonly version: bigint;
    readonly contentType: string;
    readonly state: string;
    readonly createdBy: string;
    readonly createdAtUnixMs: bigint;
    readonly metadataJson: string;
  }[];
}

export interface ClientReleaseLoaderOptions {
  readonly name: string;
  readonly namespace?: string;
  readonly clientName?: string;
  readonly instanceId?: string;
  readonly reconcileIntervalMs?: number;
  readonly maxConcurrentFetches?: number;
  readonly secretTokenProvider?: SecretTokenProvider;
  readonly validateManifest?: ValidateReleaseManifest;
  /** Injected only for deterministic tests. */
  readonly now?: () => number;
  /** Injected only for deterministic full-jitter backoff tests. */
  readonly random?: () => number;
}

/** Process-lifetime, concurrency-safe Node.js client. */
export class KmsClient {
  readonly #options: KmsClientOptions;
  readonly #transport: RpcTransport;
  readonly #configuredNamespace: NamespaceRef | undefined;
  readonly #cache: ReadCache;
  readonly #rootController = new AbortController();
  readonly #dispatcher: CallbackDispatcher;
  #discoveredNamespace: NamespaceRef | null | undefined;
  #namespacePromise: Promise<NamespaceRef | undefined> | undefined;
  #subscriptions: SubscriptionManager | undefined;
  #closeAttempt: Promise<void> | undefined;
  #closed = false;

  readonly clientName: string;
  readonly timeoutMs: number;
  readonly fallbackToDefaultsOnError: boolean;
  readonly logger: Logger;

  constructor(options: KmsClientOptions) {
    this.#options = options;
    this.timeoutMs = positiveFinite(options.timeoutMs ?? DEFAULT_TIMEOUT_MS, "timeoutMs");
    this.fallbackToDefaultsOnError = options.fallbackToDefaultsOnError ?? false;
    this.clientName = options.clientName?.trim() || basename(process.argv[1] ?? "kms-client");
    this.logger = options.logger ?? console;
    this.#configuredNamespace = options.namespace ? parseNamespace(options.namespace) : undefined;
    this.#cache = new ReadCache(options.cacheTtlMs ?? 0);
    this.#dispatcher = new CallbackDispatcher(this.logger);

    if (options.transport) {
      if (options.credentials || options.insecure) {
        throw new ConfigError("transport cannot be combined with credentials or insecure mode");
      }
      this.#transport = options.transport;
    } else {
      const endpoint = options.endpoint?.trim();
      if (!endpoint) throw new ConfigError("KMS endpoint is required");
      if (options.credentials && options.insecure) {
        throw new ConfigError("credentials and insecure mode are mutually exclusive");
      }
      if (!options.credentials && !options.insecure) {
        throw new ConfigError(
          "transport security is required; supply TLS/mTLS credentials or explicitly set insecure for local development",
        );
      }
      this.#transport = new GrpcTransport({
        endpoint,
        credentials: options.credentials ?? insecureCredentials(),
        ...(options.channelOptions ? { channelOptions: options.channelOptions } : {}),
      });
    }
  }

  get closed(): boolean {
    return this.#closed;
  }

  get currentRevision(): bigint {
    return this.#subscriptions?.currentRevision ?? 0n;
  }

  async whoAmI(options: CallOptions = {}): Promise<WhoAmI> {
    this.#assertOpen();
    try {
      const response = await this.#transport.unary(
        AdminServiceService.whoAmI,
        {},
        this.#callOptions("", options),
      );
      return Object.freeze({
        identity: response.name,
        kind: response.kind,
        ...(response.namespace
          ? { namespace: displayNamespace(fromWireNamespace(response.namespace)) }
          : {}),
        authMethod: response.authMethod,
      });
    } catch (error) {
      throwMapped(error);
    }
  }

  async getParameter(key: string, options: GetOptions = {}): Promise<string> {
    const ref = await this.resolveResourceRef(key, options.signal);
    const selector = normalizeVersionRef(options);
    const cached = this.#cache.getParam(displayPath(ref), selector.version, selector.label);
    if (cached !== undefined) return cached;
    const parameter = await this.fetchParameter(ref, selector, options);
    return parameter.value;
  }

  async getParameterInfo(key: string, options: GetOptions = {}): Promise<Parameter> {
    const ref = await this.resolveResourceRef(key, options.signal);
    return this.fetchParameter(ref, normalizeVersionRef(options), options);
  }

  async fetchParameter(
    ref: ResourceRef,
    selector: { readonly version: bigint; readonly label: string },
    options: CallOptions = {},
  ): Promise<Parameter> {
    this.#assertOpen();
    try {
      const response = await this.#transport.unary(
        ParameterServiceService.getParameter,
        { ref: toWireRef(ref), version: selector.version, label: selector.label },
        this.#callOptions("", options),
      );
      if (!response.parameter) throw new KmsError("internal", "KMS parameter response was empty");
      const parameter = parameterFromWire(response.parameter);
      this.#cache.putParam(displayPath(ref), selector.version, selector.label, parameter.value);
      return parameter;
    } catch (error) {
      throwMapped(error);
    }
  }

  async getSecret(key: string, options: GetOptions = {}): Promise<Secret> {
    const ref = await this.resolveResourceRef(key, options.signal);
    const selector = normalizeVersionRef(options);
    const path = displayPath(ref);
    if (!options.secretToken) {
      const cached = this.#cache.getSecretAt(path, selector.version, selector.label);
      if (cached) return cached;
    }

    const secret = await this.fetchSecret(ref, selector, options);
    if (!options.secretToken) this.#cache.putSecret(path, selector.version, selector.label, secret);
    return secret;
  }

  async fetchSecret(
    ref: ResourceRef,
    selector: { readonly version: bigint; readonly label: string },
    options: GetOptions = {},
  ): Promise<Secret> {
    this.#assertOpen();
    try {
      const response = await this.#transport.unary(
        SecretServiceService.getSecret,
        { ref: toWireRef(ref), version: selector.version, label: selector.label },
        this.#callOptions(options.secretToken ?? "", options),
      );
      const returnedRef = response.ref ? fromWireRef(response.ref) : ref;
      return new Secret(response.value, {
        path: displayPath(returnedRef),
        version: response.version,
        contentType: response.contentType,
      });
    } catch (error) {
      throwMapped(error);
    }
  }

  async putParameter(
    key: string,
    value: string,
    options: PutParameterOptions = {},
  ): Promise<PutResult> {
    const ref = await this.resolveResourceRef(key, options.signal);
    try {
      const response = await this.#transport.unary(
        ParameterServiceService.putParameter,
        {
          ref: toWireRef(ref),
          value,
          contentType: options.contentType ?? "",
          metadataJson: options.metadataJson ?? "",
        },
        this.#callOptions("", options),
      );
      this.#cache.invalidateParam(displayPath(ref));
      return Object.freeze({ version: response.version, revision: response.revision });
    } catch (error) {
      throwMapped(error);
    }
  }

  async putSecret(
    key: string,
    value: Uint8Array | string,
    options: PutSecretOptions = {},
  ): Promise<PutSecretResult> {
    if (typeof value !== "string" && !(value instanceof Uint8Array)) {
      throw new ConfigError("secret value must be a string or Uint8Array");
    }
    const expiresAtUnixMs = options.expiresAtUnixMs ?? 0n;
    if (
      typeof expiresAtUnixMs !== "bigint" ||
      expiresAtUnixMs < 0n ||
      expiresAtUnixMs > INT64_MAX
    ) {
      throw new ConfigError("expiresAtUnixMs must be a bigint in the non-negative int64 range");
    }
    const ref = await this.resolveResourceRef(key, options.signal);
    const plaintext = typeof value === "string" ? Buffer.from(value) : Buffer.from(value);
    try {
      const response = await this.#transport.unary(
        SecretServiceService.putSecret,
        {
          ref: toWireRef(ref),
          value: plaintext,
          contentType: options.contentType ?? "",
          metadataJson: options.metadataJson ?? "",
          clientBound: options.clientBound ?? false,
          generateAccessToken: options.generateAccessToken ?? false,
          expiresAtUnixMs,
        },
        this.#callOptions(options.secretToken ?? "", options),
      );
      this.#cache.invalidateSecret(displayPath(ref));
      return Object.freeze({
        version: response.version,
        revision: response.revision,
        accessToken: response.accessToken,
      });
    } catch (error) {
      throwMapped(error);
    } finally {
      plaintext.fill(0);
    }
  }

  async listParameters(namespace?: string, options: ListOptions = {}): Promise<Page<Parameter>> {
    const ns = namespace
      ? parseNamespace(namespace)
      : await this.requireNamespace("listParameters", options.signal);
    return this.listParametersInNamespace(ns, options);
  }

  async listParametersInNamespace(
    namespace: NamespaceRef,
    options: ListOptions = {},
  ): Promise<Page<Parameter>> {
    const pageSize = validPageSize(options.pageSize);
    try {
      const response = await this.#transport.unary(
        ParameterServiceService.listParameters,
        {
          namespace: toWireNamespace(namespace),
          keyPrefix: options.keyPrefix ?? "",
          pageSize,
          pageToken: options.pageToken ?? "",
        },
        this.#callOptions("", options),
      );
      return Object.freeze({
        items: Object.freeze(response.parameters.map(parameterFromWire)),
        nextPageToken: response.nextPageToken,
      });
    } catch (error) {
      throwMapped(error);
    }
  }

  async deleteParameter(key: string, options: CallOptions = {}): Promise<bigint> {
    const ref = await this.resolveResourceRef(key, options.signal);
    try {
      const response = await this.#transport.unary(
        ParameterServiceService.deleteParameter,
        { ref: toWireRef(ref) },
        this.#callOptions("", options),
      );
      this.#cache.invalidateParam(displayPath(ref));
      return response.revision;
    } catch (error) {
      throwMapped(error);
    }
  }

  async getParameterMetadata(key: string, options: CallOptions = {}): Promise<ParameterMetadata> {
    const ref = await this.resolveResourceRef(key, options.signal);
    try {
      const response = await this.#transport.unary(
        ParameterServiceService.getParameterMetadata,
        { ref: toWireRef(ref) },
        this.#callOptions("", options),
      );
      const responseRef = response.ref ? fromWireRef(response.ref) : ref;
      return Object.freeze({
        ref: responseRef,
        contentType: response.contentType,
        metadataJson: response.metadataJson,
        createdAtUnixMs: response.createdAtUnixMs,
        updatedAtUnixMs: response.updatedAtUnixMs,
        labels: frozenRecord(response.labels),
        versions: Object.freeze(
          response.versions.map((version) =>
            Object.freeze({
              version: version.version,
              contentType: version.contentType,
              state: version.state,
              createdBy: version.createdBy,
              createdAtUnixMs: version.createdAtUnixMs,
              metadataJson: version.metadataJson,
            }),
          ),
        ),
      });
    } catch (error) {
      throwMapped(error);
    }
  }

  async listSecrets(namespace?: string, options: ListOptions = {}): Promise<Page<SecretInfo>> {
    const ns = namespace
      ? parseNamespace(namespace)
      : await this.requireNamespace("listSecrets", options.signal);
    try {
      const response = await this.#transport.unary(
        SecretServiceService.listSecrets,
        {
          namespace: toWireNamespace(ns),
          keyPrefix: options.keyPrefix ?? "",
          pageSize: validPageSize(options.pageSize),
          pageToken: options.pageToken ?? "",
        },
        this.#callOptions("", options),
      );
      return Object.freeze({
        items: Object.freeze(response.secrets.map(secretInfoFromWire)),
        nextPageToken: response.nextPageToken,
      });
    } catch (error) {
      throwMapped(error);
    }
  }

  async getSecretMetadata(key: string, options: CallOptions = {}): Promise<SecretInfo> {
    const ref = await this.resolveResourceRef(key, options.signal);
    try {
      const response = await this.#transport.unary(
        SecretServiceService.getSecretMetadata,
        { ref: toWireRef(ref) },
        this.#callOptions("", options),
      );
      if (!response.secret)
        throw new KmsError("internal", "KMS secret metadata response was empty");
      return secretInfoFromWire(response.secret);
    } catch (error) {
      throwMapped(error);
    }
  }

  async deleteSecret(key: string, options: CallOptions = {}): Promise<bigint> {
    const ref = await this.resolveResourceRef(key, options.signal);
    const response = await this.#secretMutation(
      ref,
      SecretServiceService.deleteSecret,
      { ref: toWireRef(ref) },
      "",
      options,
    );
    return response.revision;
  }

  async setSecretEnabled(
    key: string,
    enabled: boolean,
    options: CallOptions & { readonly version?: bigint; readonly secretToken?: string } = {},
  ): Promise<bigint> {
    assertUint64(options.version ?? 0n, "version");
    const ref = await this.resolveResourceRef(key, options.signal);
    const response = await this.#secretMutation(
      ref,
      SecretServiceService.disableSecret,
      { ref: toWireRef(ref), version: options.version ?? 0n, enable: enabled },
      options.secretToken ?? "",
      options,
    );
    return response.revision;
  }

  async destroySecretVersion(
    key: string,
    version: bigint,
    options: CallOptions & { readonly secretToken?: string } = {},
  ): Promise<bigint> {
    assertUint64(version, "destroySecretVersion version", true);
    const ref = await this.resolveResourceRef(key, options.signal);
    const response = await this.#secretMutation(
      ref,
      SecretServiceService.destroySecretVersion,
      { ref: toWireRef(ref), version },
      options.secretToken ?? "",
      options,
    );
    return response.revision;
  }

  async promoteSecretVersion(
    key: string,
    version: bigint,
    options: CallOptions & { readonly secretToken?: string } = {},
  ): Promise<{
    readonly currentVersion: bigint;
    readonly previousVersion: bigint;
    readonly revision: bigint;
  }> {
    assertUint64(version, "promoteSecretVersion version", true);
    const ref = await this.resolveResourceRef(key, options.signal);
    const response = await this.#secretMutation(
      ref,
      SecretServiceService.promoteSecretVersion,
      { ref: toWireRef(ref), version },
      options.secretToken ?? "",
      options,
    );
    return Object.freeze(response);
  }

  async watch(callback: WatchCallback, options: WatchOptions = {}): Promise<() => void> {
    const namespace = await this.requireNamespace("watch", options.signal);
    return this.#subscriptionManager().watch(namespace, callback, options.signal);
  }

  /** Resolve every declarative value reachable through own object fields and arrays. */
  async resolve(config: object, options: ValueReadOptions = {}): Promise<void> {
    await resolveValues(config, this, options);
  }

  /** Create an atomic release loader using this client's shared authenticated transport. */
  async createReleaseLoader(options: ClientReleaseLoaderOptions): Promise<ReleaseLoader> {
    const namespace = options.namespace
      ? parseNamespace(options.namespace)
      : await this.requireNamespace("createReleaseLoader");
    return ReleaseLoader._create(this.#releaseTransport(), {
      ...options,
      namespace: toWireNamespace(namespace),
      clientName: options.clientName?.trim() || this.clientName,
      acknowledgementTimeoutMs: this.timeoutMs,
    });
  }

  watchNamespace(
    namespace: string,
    callback: WatchCallback,
    options: WatchOptions = {},
  ): () => void {
    return this.#subscriptionManager().watch(parseNamespace(namespace), callback, options.signal);
  }

  async resolveResourceRef(key: string, signal?: AbortSignal): Promise<ResourceRef> {
    this.#assertOpen();
    if (key.startsWith("/")) return resolveRef(key, undefined);
    const namespace = await this.discoverNamespace(signal);
    return resolveRef(key, namespace);
  }

  async discoverNamespace(signal?: AbortSignal): Promise<NamespaceRef | undefined> {
    this.#assertOpen();
    if (this.#configuredNamespace) return this.#configuredNamespace;
    if (this.#discoveredNamespace !== undefined) return this.#discoveredNamespace ?? undefined;
    if (!this.#namespacePromise) {
      this.#namespacePromise = this.whoAmI(signal ? { signal } : {})
        .then((identity) => {
          const namespace = identity.namespace ? parseNamespace(identity.namespace) : undefined;
          this.#discoveredNamespace = namespace ?? null;
          return namespace;
        })
        .catch((error) => {
          this.#namespacePromise = undefined;
          throw error;
        });
    }
    return this.#namespacePromise;
  }

  async requireNamespace(operation: string, signal?: AbortSignal): Promise<NamespaceRef> {
    const namespace = await this.discoverNamespace(signal);
    if (!namespace) throw new KmsError("no_namespace", `${operation} requires a bound namespace`);
    return namespace;
  }

  /** @internal Used by the shared watch manager. */
  _watchTransport(): RpcTransport {
    return this.#transport;
  }

  /** @internal Authentication metadata for stream/session work. */
  _metadata(secretToken = ""): Readonly<Record<string, string>> {
    const metadata: Record<string, string> = {};
    if (this.#options.token) metadata.authorization = `Bearer ${this.#options.token}`;
    if (secretToken) metadata["x-kms-secret-token"] = secretToken;
    return metadata;
  }

  /** @internal Abort signal for process-lifetime client work. */
  _rootSignal(): AbortSignal {
    return this.#rootController.signal;
  }

  /** @internal */
  _cache(): ReadCache {
    return this.#cache;
  }

  /** @internal */
  _dispatch(path: string, callback: () => unknown): void {
    this.#dispatcher.enqueue(path, callback);
  }

  /** @internal */
  _registerParameter(ref: ResourceRef, initial: string, handler: ParameterUpdateHandler): void {
    this.#subscriptionManager().registerParameter(ref, initial, handler);
  }

  /** @internal */
  _getActiveRelease(
    namespace: NamespaceRef,
    name: string,
    options: CallOptions = {},
  ): Promise<GetActiveReleaseResponse> {
    return this.#transport
      .unary(
        ConfigurationReleaseServiceService.getActiveRelease,
        { namespace: toWireNamespace(namespace), name },
        this.#callOptions("", options),
      )
      .catch((error: unknown) => {
        throwMapped(error);
      });
  }

  /** @internal */
  _configurationRelease(release: ConfigurationRelease): ConfigurationRelease {
    return release;
  }

  close(): Promise<void> {
    if (this.#closeAttempt !== undefined) return this.#closeAttempt;
    this.#closed = true;
    const attempt = (async (): Promise<void> => {
      this.#rootController.abort(new DOMException("KMS client closed", "AbortError"));
      await this.#subscriptions?.stop();
      this.#dispatcher.close();
      this.#cache.clear();
      await this.#transport.close();
    })();
    this.#closeAttempt = attempt;
    return attempt;
  }

  async [Symbol.asyncDispose](): Promise<void> {
    await this.close();
  }

  #subscriptionManager(): SubscriptionManager {
    this.#assertOpen();
    if (!this.#subscriptions) {
      this.#subscriptions = new SubscriptionManager(
        this,
        this.#options.reconcileIntervalMs === undefined
          ? {}
          : { reconcileIntervalMs: this.#options.reconcileIntervalMs },
      );
    }
    return this.#subscriptions;
  }

  #releaseTransport(): ReleaseTransport {
    return {
      getActiveRelease: async (namespace, name, signal) => {
        if (!namespace) throw new KmsError("invalid_argument", "release namespace is required");
        return this._getActiveRelease(fromWireNamespace(namespace), name, signal ? { signal } : {});
      },
      fetchParameter: async (wireRef, version, signal) => {
        const ref = fromWireRef(wireRef);
        const parameter = await this.fetchParameter(
          ref,
          { version, label: "" },
          signal ? { signal } : {},
        );
        return {
          ref: toWireRef(ref),
          value: parameter.value,
          contentType: parameter.contentType,
          version: parameter.version,
          metadataJson: parameter.metadataJson,
          createdBy: parameter.createdBy,
          createdAtUnixMs: parameter.createdAtUnixMs,
          labels: { ...parameter.labels },
        };
      },
      fetchSecret: async (wireRef, version, secretToken, signal) => {
        const ref = fromWireRef(wireRef);
        const secret = await this.fetchSecret(
          ref,
          { version, label: "" },
          { secretToken, ...(signal ? { signal } : {}) },
        );
        return {
          ref: toWireRef(ref),
          version: secret.version,
          value: secret.bytes(),
          contentType: secret.contentType,
        };
      },
      watchRelease: async (registration, signal): Promise<ReleaseWatchStream> => {
        const stream = this.#transport.bidi(ConfigurationReleaseServiceService.watchRelease, {
          metadata: this._metadata(),
          signal: AbortSignal.any([this.#rootController.signal, signal]),
        });
        await stream.send({ request: { $case: "register", value: registration } });
        return stream;
      },
    };
  }

  #callOptions(secretToken: string, options: CallOptions): TransportCallOptions {
    this.#assertOpen();
    const defaultDeadline = new Date(Date.now() + this.timeoutMs);
    const deadline =
      options.deadline && options.deadline < defaultDeadline ? options.deadline : defaultDeadline;
    return {
      metadata: this._metadata(secretToken),
      deadline,
      signal: combineSignals(this.#rootController.signal, options.signal),
    };
  }

  async #secretMutation<Request, Response>(
    ref: ResourceRef,
    method: UnaryMethod<Request, Response>,
    request: Request,
    secretToken: string,
    options: CallOptions,
  ): Promise<Response> {
    try {
      const response = await this.#transport.unary(
        method,
        request,
        this.#callOptions(secretToken, options),
      );
      this.#cache.invalidateSecret(displayPath(ref));
      return response;
    } catch (error) {
      throwMapped(error);
    }
  }

  #assertOpen(): void {
    if (this.#closed) throw new KmsError("failed_precondition", "KMS client is closed");
  }
}

export function createClient(options: KmsClientOptions): KmsClient {
  return new KmsClient(options);
}

function positiveFinite(value: number, name: string): number {
  if (!Number.isFinite(value) || value <= 0) throw new ConfigError(`${name} must be positive`);
  return value;
}

function validPageSize(value = 0): number {
  if (!Number.isSafeInteger(value) || value < 0 || value > 1_000) {
    throw new ConfigError("pageSize must be an integer from 0 through 1000");
  }
  return value;
}

function assertUint64(value: bigint, name: string, positive = false): void {
  if (typeof value !== "bigint" || value < (positive ? 1n : 0n) || value > UINT64_MAX) {
    throw new ConfigError(
      `${name} must be a ${positive ? "positive " : ""}bigint in the uint64 range`,
    );
  }
}

/** @internal */
export function toWireNamespace(namespace: NamespaceRef): { env: string; app: string } {
  return { env: namespace.env, app: namespace.app };
}

/** @internal */
export function toWireRef(ref: ResourceRef): WireResourceRef {
  return { namespace: toWireNamespace(ref.namespace), key: ref.key };
}

/** @internal */
export function fromWireNamespace(namespace: { env: string; app: string }): NamespaceRef {
  return Object.freeze({ env: namespace.env, app: namespace.app });
}

/** @internal */
export function fromWireRef(ref: WireResourceRef): ResourceRef {
  if (!ref.namespace) throw new KmsError("internal", "KMS resource reference omitted namespace");
  return Object.freeze({ namespace: fromWireNamespace(ref.namespace), key: ref.key });
}

function parameterFromWire(parameter: WireParameter): Parameter {
  if (!parameter.ref) throw new KmsError("internal", "KMS parameter omitted resource reference");
  const ref = fromWireRef(parameter.ref);
  return Object.freeze({
    env: ref.namespace.env,
    app: ref.namespace.app,
    key: ref.key,
    value: parameter.value,
    contentType: parameter.contentType,
    version: parameter.version,
    metadataJson: parameter.metadataJson,
    createdBy: parameter.createdBy,
    createdAtUnixMs: parameter.createdAtUnixMs,
    labels: frozenRecord(parameter.labels),
    namespace: displayNamespace(ref.namespace),
    path: displayPath(ref),
  });
}

function secretInfoFromWire(secret: SecretMetadata): SecretInfo {
  if (!secret.ref) throw new KmsError("internal", "KMS secret metadata omitted resource reference");
  const ref = fromWireRef(secret.ref);
  const versions: readonly SecretVersion[] = Object.freeze(
    secret.versions.map((version) =>
      Object.freeze({
        version: version.version,
        state: version.state,
        createdBy: version.createdBy,
        createdAtUnixMs: version.createdAtUnixMs,
        destroyedAtUnixMs: version.destroyedAtUnixMs,
        expiresAtUnixMs: version.expiresAtUnixMs,
        metadataJson: version.metadataJson,
      }),
    ),
  );
  return Object.freeze({
    env: ref.namespace.env,
    app: ref.namespace.app,
    key: ref.key,
    contentType: secret.contentType,
    clientBound: secret.clientBound,
    hasAccessToken: secret.hasAccessToken,
    metadataJson: secret.metadataJson,
    createdAtUnixMs: secret.createdAtUnixMs,
    updatedAtUnixMs: secret.updatedAtUnixMs,
    labels: frozenRecord(secret.labels),
    versions,
    namespace: displayNamespace(ref.namespace),
    path: displayPath(ref),
  });
}

function frozenRecord(values: Readonly<Record<string, bigint>>): Readonly<Record<string, bigint>> {
  return Object.freeze({ ...values });
}

function combineSignals(root: AbortSignal, call?: AbortSignal): AbortSignal {
  return call ? AbortSignal.any([root, call]) : root;
}

function throwMapped(error: unknown): never {
  const mapped = mapGrpcError(error);
  throw mapped ?? wrapError("unexpected successful gRPC status", error);
}

class CallbackDispatcher {
  readonly #logger: Logger;
  readonly #queue: { readonly path: string; readonly callback: () => unknown }[] = [];
  #scheduled = false;
  #closed = false;

  constructor(logger: Logger) {
    this.#logger = logger;
  }

  enqueue(path: string, callback: () => unknown): void {
    if (this.#closed) return;
    if (this.#queue.length >= CALLBACK_QUEUE_SIZE) {
      this.#logger.warn(`KMS callback queue full; dropped notification for ${path}`);
      return;
    }
    this.#queue.push({ path, callback });
    if (!this.#scheduled) {
      this.#scheduled = true;
      setImmediate(() => this.#drain());
    }
  }

  close(): void {
    this.#closed = true;
    this.#queue.length = 0;
  }

  #drain(): void {
    this.#scheduled = false;
    if (this.#closed) return;
    const item = this.#queue.shift();
    if (item) {
      try {
        Promise.resolve(item.callback()).catch(() => {
          this.#logger.warn(`KMS change callback rejected for ${item.path}`);
        });
      } catch {
        this.#logger.warn(`KMS change callback threw for ${item.path}`);
      }
    }
    if (this.#queue.length > 0) {
      this.#scheduled = true;
      setImmediate(() => this.#drain());
    }
  }
}

export type { WatchCallback, WatchEvent, WatchOptions };
