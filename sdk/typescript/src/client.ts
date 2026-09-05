import { basename } from "node:path";
import type { ChannelCredentials, ChannelOptions } from "@grpc/grpc-js";
import { ReadCache } from "./cache.js";
import {
  ConfigError,
  KmsError,
  mapGrpcError,
  mapPurgeSecretGrpcError,
  mapSecretGrpcError,
  RateLimitedError,
  wrapError,
} from "./errors.js";
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
  SecretBindingCohortResult,
  SecretInfo,
  SecretVersion,
  SecretVersionSetResult,
  SecretVersionTransitionResult,
  WhoAmI,
} from "./models.js";
import {
  displayNamespace,
  displayPath,
  type NamespaceRef,
  namespaceEquals,
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
import {
  type VerifyReleaseDefaultsOptions,
  type VerifyReleaseDefaultsResult,
  validLowerHex64,
  verifyResultFromWire,
} from "./releases/verify.js";
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
  type WatchStatus,
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
  readonly bindingKey?: string;
}

export interface PutParameterOptions extends CallOptions {
  readonly contentType?: string;
  readonly metadataJson?: string;
}

export interface PutSecretOptions extends CallOptions {
  readonly contentType?: string;
  readonly metadataJson?: string;
  readonly bindingKey?: string;
  readonly generateAccessToken?: boolean;
  readonly expiresAtUnixMs?: bigint;
}

export interface BindSecretOptions extends CallOptions {
  readonly expectedCurrentVersion: bigint;
  readonly bindingKey: string;
}

export interface PreviewSecretBindingCohortOptions extends CallOptions {
  readonly anchorVersion?: bigint;
  readonly bindingKey: string;
}

export interface SecretBindingCohortGuardOptions {
  readonly expectedRevision: bigint;
  readonly expectedAffectedVersions: readonly bigint[];
}

export interface RotateSecretBindingKeyOptions extends CallOptions {
  readonly expectedCurrentVersion: bigint;
  readonly bindingKey: string;
  readonly newBindingKey: string;
}

export type PurgeSecretBindingCohortOptions = PreviewSecretBindingCohortOptions &
  SecretBindingCohortGuardOptions;

export interface PurgeSecretUnboundVersionsOptions extends CallOptions {
  readonly expectedRevision: bigint;
  readonly expectedAffectedVersions: readonly bigint[];
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
  readonly bindingKeys?: Readonly<Record<string, string>>;
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
    this.logger = isolateLogger(options.logger ?? console);
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

  /** A value-free snapshot of shared watch and reconciliation health. */
  get watchStatus(): WatchStatus {
    return (
      this.#subscriptions?.status ??
      Object.freeze({
        state: this.#closed ? "stopped" : "idle",
        reconciliation: "not_started",
        currentRevision: 0n,
        reconnectCount: 0,
        namespaceCount: 0,
        trackedParameterCount: 0,
        watcherCount: 0,
        parameterHandlerCount: 0,
      })
    );
  }

  async whoAmI(options: CallOptions = {}): Promise<WhoAmI> {
    this.#assertOpen();
    try {
      const response = await this.#transport.unary(
        AdminServiceService.whoAmI,
        {},
        this.#callOptions(options),
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
    const ref = await this.#resolveResourceRefForCall(key, options);
    const selector = normalizeVersionRef(options);
    const cached = options.secretToken
      ? undefined
      : this.#cache.getParam(displayPath(ref), selector.version, selector.label);
    if (cached !== undefined) return cached;
    const parameter = await this.fetchParameter(ref, selector, options);
    return parameter.value;
  }

  async getParameterInfo(key: string, options: GetOptions = {}): Promise<Parameter> {
    const ref = await this.#resolveResourceRefForCall(key, options);
    return this.fetchParameter(ref, normalizeVersionRef(options), options);
  }

  /** @internal Exact-ref fetch used by the release/value runtime. */
  async fetchParameter(
    ref: ResourceRef,
    selector: { readonly version: bigint; readonly label: string },
    options: GetOptions = {},
  ): Promise<Parameter> {
    this.#assertOpen();
    const path = displayPath(ref);
    const generation = options.secretToken ? undefined : this.#cache.beginParameterRead(path);
    try {
      const response = await this.#transport.unary(
        ParameterServiceService.getParameter,
        { ref: toWireRef(ref), version: selector.version, label: selector.label },
        this.#callOptions(options),
      );
      if (!response.parameter) throw new KmsError("internal", "KMS parameter response was empty");
      const parameter = parameterFromWire(response.parameter);
      assertReadIdentity("parameter", ref, response.parameter.ref, parameter.version, selector);
      if (generation !== undefined) {
        this.#cache.cacheParameterIfUnchanged(
          generation,
          selector.version,
          selector.label,
          parameter.value,
        );
      }
      return parameter;
    } catch (error) {
      throwMapped(error);
    } finally {
      this.#cache.endRead(generation);
    }
  }

  async getSecret(key: string, options: GetOptions = {}): Promise<Secret> {
    const secretToken = optionalCredential(options.secretToken, "getSecret secretToken");
    const bindingKey = optionalCredential(options.bindingKey, "getSecret bindingKey");
    const selector = normalizeVersionRef(options);
    const ref = await this.#resolveResourceRefForCall(key, options);
    // Secret protection is live metadata. Never serve plaintext from cache:
    // doing so could bypass binding/token protection added after a prior read.
    return this.fetchSecret(ref, selector, { ...options, secretToken, bindingKey });
  }

  /** @internal Exact-ref fetch used by the release runtime. */
  async fetchSecret(
    ref: ResourceRef,
    selector: { readonly version: bigint; readonly label: string },
    options: GetOptions = {},
  ): Promise<Secret> {
    this.#assertOpen();
    const secretToken = optionalCredential(options.secretToken, "fetchSecret secretToken");
    const bindingKey = optionalCredential(options.bindingKey, "fetchSecret bindingKey");
    try {
      const response = await this.#transport.unary(
        SecretServiceService.getSecret,
        {
          ref: toWireRef(ref),
          version: selector.version,
          label: selector.label,
          secretToken,
          bindingKey,
        },
        this.#callOptions(options),
      );
      if (!response.ref)
        throw new KmsError("internal", "KMS secret response omitted resource reference");
      const returnedRef = fromWireRef(response.ref);
      assertReadIdentity("secret", ref, response.ref, response.version, selector);
      return new Secret(response.value, {
        path: displayPath(returnedRef),
        version: response.version,
        contentType: response.contentType,
      });
    } catch (error) {
      throwSecretMapped(error);
    }
  }

  async putParameter(
    key: string,
    value: string,
    options: PutParameterOptions = {},
  ): Promise<PutResult> {
    const ref = await this.#resolveResourceRefForCall(key, options);
    try {
      const response = await this.#transport.unary(
        ParameterServiceService.putParameter,
        {
          ref: toWireRef(ref),
          value,
          contentType: options.contentType ?? "",
          metadataJson: options.metadataJson ?? "",
        },
        this.#callOptions(options),
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
    const bindingKey = optionalCredential(options.bindingKey, "putSecret bindingKey");
    const plaintext = typeof value === "string" ? Buffer.from(value) : Buffer.from(value);
    try {
      const ref = await this.#resolveResourceRefForCall(key, options);
      try {
        const response = await this.#transport.unary(
          SecretServiceService.putSecretV03,
          {
            ref: toWireRef(ref),
            value: plaintext,
            contentType: options.contentType ?? "",
            metadataJson: options.metadataJson ?? "",
            bindingKey,
            generateAccessToken: options.generateAccessToken ?? false,
            expiresAtUnixMs,
          },
          this.#callOptions(options),
        );
        this.#cache.invalidateSecret(displayPath(ref));
        return Object.freeze({
          version: response.version,
          revision: response.revision,
          accessToken: response.accessToken,
        });
      } catch (error) {
        throwSecretMapped(error);
      }
    } finally {
      plaintext.fill(0);
    }
  }

  async listParameters(namespace?: string, options: ListOptions = {}): Promise<Page<Parameter>> {
    const ns = namespace
      ? parseNamespace(namespace)
      : await this.requireNamespace("listParameters", options);
    return this.listParametersInNamespace(ns, options);
  }

  /** @internal Namespace-ref listing used by reconciliation. */
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
        this.#callOptions(options),
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
    const ref = await this.#resolveResourceRefForCall(key, options);
    try {
      const response = await this.#transport.unary(
        ParameterServiceService.deleteParameter,
        { ref: toWireRef(ref) },
        this.#callOptions(options),
      );
      this.#cache.invalidateParam(displayPath(ref));
      return response.revision;
    } catch (error) {
      throwMapped(error);
    }
  }

  async getParameterMetadata(key: string, options: CallOptions = {}): Promise<ParameterMetadata> {
    const ref = await this.#resolveResourceRefForCall(key, options);
    try {
      const response = await this.#transport.unary(
        ParameterServiceService.getParameterMetadata,
        { ref: toWireRef(ref) },
        this.#callOptions(options),
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
      : await this.requireNamespace("listSecrets", options);
    try {
      const response = await this.#transport.unary(
        SecretServiceService.listSecrets,
        {
          namespace: toWireNamespace(ns),
          keyPrefix: options.keyPrefix ?? "",
          pageSize: validPageSize(options.pageSize),
          pageToken: options.pageToken ?? "",
        },
        this.#callOptions(options),
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
    const ref = await this.#resolveResourceRefForCall(key, options);
    try {
      const response = await this.#transport.unary(
        SecretServiceService.getSecretMetadata,
        { ref: toWireRef(ref), version: 0n, label: "" },
        this.#callOptions(options),
      );
      if (!response.secret)
        throw new KmsError("internal", "KMS secret metadata response was empty");
      assertResourceIdentity("secret metadata", ref, response.secret.ref);
      return secretInfoFromWire(response.secret);
    } catch (error) {
      throwMapped(error);
    }
  }

  async deleteSecret(key: string, options: CallOptions = {}): Promise<bigint> {
    const ref = await this.#resolveResourceRefForCall(key, options);
    const response = await this.#secretMutation(
      ref,
      SecretServiceService.deleteSecret,
      { ref: toWireRef(ref) },
      options,
    );
    return response.revision;
  }

  async setSecretEnabled(
    key: string,
    enabled: boolean,
    options: CallOptions & { readonly version?: bigint } = {},
  ): Promise<bigint> {
    assertUint64(options.version ?? 0n, "version");
    const ref = await this.#resolveResourceRefForCall(key, options);
    const response = await this.#secretMutation(
      ref,
      SecretServiceService.disableSecret,
      { ref: toWireRef(ref), version: options.version ?? 0n, enable: enabled },
      options,
    );
    return response.revision;
  }

  async destroySecretVersion(
    key: string,
    version: bigint,
    options: CallOptions = {},
  ): Promise<bigint> {
    assertUint64(version, "destroySecretVersion version", true);
    const ref = await this.#resolveResourceRefForCall(key, options);
    const response = await this.#secretMutation(
      ref,
      SecretServiceService.destroySecretVersion,
      { ref: toWireRef(ref), version },
      options,
    );
    return response.revision;
  }

  async promoteSecretVersion(
    key: string,
    version: bigint,
    options: CallOptions = {},
  ): Promise<{
    readonly currentVersion: bigint;
    readonly previousVersion: bigint;
    readonly revision: bigint;
  }> {
    assertUint64(version, "promoteSecretVersion version", true);
    const ref = await this.#resolveResourceRefForCall(key, options);
    const response = await this.#secretMutation(
      ref,
      SecretServiceService.promoteSecretVersion,
      { ref: toWireRef(ref), version },
      options,
    );
    return Object.freeze(response);
  }

  async bindSecret(
    key: string,
    options: BindSecretOptions,
  ): Promise<SecretVersionTransitionResult> {
    const bindingKey = requiredCredential(options?.bindingKey, "bindSecret bindingKey");
    return this.#bindingTransition(key, options?.expectedCurrentVersion, bindingKey, options, true);
  }

  async unbindSecret(
    key: string,
    options: BindSecretOptions,
  ): Promise<SecretVersionTransitionResult> {
    const bindingKey = requiredCredential(options?.bindingKey, "unbindSecret bindingKey");
    return this.#bindingTransition(
      key,
      options?.expectedCurrentVersion,
      bindingKey,
      options,
      false,
    );
  }

  async previewSecretBindingCohort(
    key: string,
    options: PreviewSecretBindingCohortOptions,
  ): Promise<SecretBindingCohortResult> {
    const bindingKey = requiredCredential(
      options?.bindingKey,
      "previewSecretBindingCohort bindingKey",
    );
    const anchorVersion = options?.anchorVersion ?? 0n;
    assertUint64(anchorVersion, "previewSecretBindingCohort anchorVersion");
    const ref = await this.#resolveResourceRefForCall(key, options);
    try {
      const response = await this.#transport.unary(
        SecretServiceService.previewSecretBindingCohort,
        { ref: toWireRef(ref), anchorVersion, bindingKey },
        this.#callOptions(options),
      );
      return frozenBindingResult(response);
    } catch (error) {
      throwSecretMapped(error);
    }
  }

  async rotateSecretBindingKey(
    key: string,
    options: RotateSecretBindingKeyOptions,
  ): Promise<SecretVersionTransitionResult> {
    const bindingKey = requiredCredential(options?.bindingKey, "rotateSecretBindingKey bindingKey");
    const newBindingKey = requiredCredential(
      options?.newBindingKey,
      "rotateSecretBindingKey newBindingKey",
    );
    const expectedCurrentVersion = options?.expectedCurrentVersion;
    assertUint64(expectedCurrentVersion, "rotateSecretBindingKey expectedCurrentVersion", true);
    const ref = await this.#resolveResourceRefForCall(key, options);
    const response = await this.#secretMutation(
      ref,
      SecretServiceService.rotateSecretBindingKey,
      {
        ref: toWireRef(ref),
        expectedCurrentVersion,
        bindingKey,
        newBindingKey,
      },
      options,
    );
    return frozenTransitionResult(response);
  }

  async previewSecretUnboundVersions(
    key: string,
    options: CallOptions = {},
  ): Promise<SecretVersionSetResult> {
    const ref = await this.#resolveResourceRefForCall(key, options);
    try {
      const response = await this.#transport.unary(
        SecretServiceService.previewSecretUnboundVersions,
        { ref: toWireRef(ref) },
        this.#callOptions(options),
      );
      return frozenVersionSetResult(response);
    } catch (error) {
      throwSecretMapped(error);
    }
  }

  async purgeSecretUnboundVersions(
    key: string,
    options: PurgeSecretUnboundVersionsOptions,
  ): Promise<SecretVersionSetResult> {
    const guards = requiredVersionSetGuards(options, "purgeSecretUnboundVersions");
    const ref = await this.#resolveResourceRefForCall(key, options);
    const response = await this.#secretMutation(
      ref,
      SecretServiceService.purgeSecretUnboundVersions,
      { ref: toWireRef(ref), ...guards },
      options,
      mapPurgeSecretGrpcError,
    );
    return frozenVersionSetResult(response);
  }

  async purgeSecretBindingCohort(
    key: string,
    options: PurgeSecretBindingCohortOptions,
  ): Promise<SecretBindingCohortResult> {
    const bindingKey = requiredCredential(
      options?.bindingKey,
      "purgeSecretBindingCohort bindingKey",
    );
    const anchorVersion = options?.anchorVersion ?? 0n;
    assertUint64(anchorVersion, "purgeSecretBindingCohort anchorVersion");
    const guards = requiredVersionSetGuards(options, "purgeSecretBindingCohort");
    const ref = await this.#resolveResourceRefForCall(key, options);
    const response = await this.#secretMutation(
      ref,
      SecretServiceService.purgeSecretBindingCohort,
      {
        ref: toWireRef(ref),
        anchorVersion,
        bindingKey,
        ...guards,
      },
      options,
      mapPurgeSecretGrpcError,
    );
    return frozenBindingResult(response);
  }

  async watch(callback: WatchCallback, options: WatchOptions = {}): Promise<() => void> {
    const namespace = await this.requireNamespace("watch", options.signal ?? undefined);
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

  /**
   * Ask the server which of the supplied alias hashes differ from the
   * parameters pinned by the active release. Neither direction carries values.
   * Requires the configuration-release:verify-defaults operation and is rate
   * limited per identity; `RateLimitedError` is thrown when a budget is spent.
   */
  async verifyReleaseDefaults(
    options: VerifyReleaseDefaultsOptions,
  ): Promise<VerifyReleaseDefaultsResult> {
    this.#assertOpen();
    const namespace = parseNamespace(options.namespace);
    const entries: { alias: string; contentType: string; sha256: string }[] = [];
    const seen = new Set<string>();
    if (!Array.isArray(options.entries)) {
      throw new ConfigError("verify entries must be an array");
    }
    for (const [index, entry] of options.entries.entries()) {
      const alias = typeof entry?.alias === "string" ? entry.alias.trim() : "";
      if (!alias) throw new ConfigError(`verify entry ${index} has an empty alias`);
      if (seen.has(alias))
        throw new ConfigError(`verify entry ${JSON.stringify(alias)} is duplicated`);
      seen.add(alias);
      if (typeof entry.sha256 !== "string" || !validLowerHex64(entry.sha256)) {
        throw new ConfigError(`verify entry ${JSON.stringify(alias)} has an invalid sha256`);
      }
      entries.push({
        alias,
        contentType: typeof entry.contentType === "string" ? entry.contentType : "",
        sha256: entry.sha256,
      });
    }
    const schemaSha256 = options.schemaSha256 ?? "";
    if (schemaSha256 !== "" && !validLowerHex64(schemaSha256)) {
      throw new ConfigError("invalid schema sha256");
    }
    try {
      const response = await this.#transport.unary(
        ConfigurationReleaseServiceService.verifyReleaseDefaults,
        {
          namespace: toWireNamespace(namespace),
          name: options.release ?? "",
          profile: options.profile ?? "",
          schemaSha256,
          entries,
        },
        this.#callOptions(options),
      );
      if (!response) throw new KmsError("internal", "KMS verify response was empty");
      return verifyResultFromWire(response, seen, (message) => {
        throw new KmsError("internal", message);
      });
    } catch (error) {
      const mapped = mapGrpcError(error);
      if (mapped instanceof KmsError && mapped.code === "resource_exhausted") {
        throw new RateLimitedError(
          `${mapped.message} (the per-identity budget is spent; wait for the window to reset instead of retrying)`,
          {
            cause: error,
            ...(mapped.grpcCode === undefined ? {} : { grpcCode: mapped.grpcCode }),
          },
        );
      }
      throwMapped(error);
    }
  }

  watchNamespace(
    namespace: string,
    callback: WatchCallback,
    options: WatchOptions = {},
  ): () => void {
    return this.#subscriptionManager().watch(parseNamespace(namespace), callback, options.signal);
  }

  /** @internal Resolve a key for declarative value/watch integration. */
  async resolveResourceRef(key: string, signal?: AbortSignal): Promise<ResourceRef> {
    this.#assertOpen();
    if (key.startsWith("/")) return resolveRef(key, undefined);
    const namespace = await this.discoverNamespace(signal === undefined ? {} : { signal });
    return resolveRef(key, namespace);
  }

  /** @internal Lazy namespace discovery shared by public operations. */
  async discoverNamespace(
    options: CallOptions | AbortSignal = {},
  ): Promise<NamespaceRef | undefined> {
    this.#assertOpen();
    const normalized = isAbortSignal(options) ? { signal: options } : options;
    // Do not create client-owned discovery work for a caller that can no
    // longer observe it. In particular, a rejected WhoAmI promise would have
    // no terminal observer when awaitCaller rejects an already-aborted caller.
    assertCallerActive(normalized);
    if (this.#configuredNamespace) return this.#configuredNamespace;
    if (this.#discoveredNamespace !== undefined) return this.#discoveredNamespace ?? undefined;
    if (!this.#namespacePromise) {
      // The coalesced discovery is client-owned. Individual callers race their
      // own cancellation/deadline below, so one impatient caller cannot abort
      // namespace discovery for unrelated joiners.
      this.#namespacePromise = this.whoAmI()
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
    return awaitCaller(this.#namespacePromise, normalized);
  }

  /** @internal Require a resolved namespace for a public operation. */
  async requireNamespace(
    operation: string,
    options: CallOptions | AbortSignal = {},
  ): Promise<NamespaceRef> {
    const namespace = await this.discoverNamespace(options);
    if (!namespace) throw new KmsError("no_namespace", `${operation} requires a bound namespace`);
    return namespace;
  }

  /** @internal Used by the shared watch manager. */
  _watchTransport(): RpcTransport {
    return this.#transport;
  }

  /** @internal Authentication metadata for stream/session work. */
  _metadata(): Readonly<Record<string, string>> {
    const metadata: Record<string, string> = {};
    if (this.#options.token) metadata.authorization = `Bearer ${this.#options.token}`;
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
  _registerParameter(
    ref: ResourceRef,
    initial: string,
    handler: ParameterUpdateHandler,
  ): () => void {
    return this.#subscriptionManager().registerParameter(ref, initial, handler);
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
        this.#callOptions(options),
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
    let resolveAttempt!: () => void;
    let rejectAttempt!: (error: unknown) => void;
    const attempt = new Promise<void>((resolve, reject) => {
      resolveAttempt = resolve;
      rejectAttempt = reject;
    });
    this.#closeAttempt = attempt;
    void this.#finishClose().then(resolveAttempt, rejectAttempt);
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

  async #resolveResourceRefForCall(key: string, options: CallOptions): Promise<ResourceRef> {
    this.#assertOpen();
    if (key.startsWith("/")) return resolveRef(key, undefined);
    const namespace = await this.discoverNamespace(options);
    return resolveRef(key, namespace);
  }

  #releaseTransport(): ReleaseTransport {
    return {
      getActiveRelease: async (namespace, name, signal) => {
        if (!namespace) throw new KmsError("invalid_argument", "release namespace is required");
        return this._getActiveRelease(fromWireNamespace(namespace), name, signal ? { signal } : {});
      },
      fetchParameter: async (wireRef, version, signal) => {
        try {
          const response = await this.#transport.unary(
            ParameterServiceService.getParameter,
            { ref: wireRef, version, label: "" },
            this.#callOptions(signal ? { signal } : {}),
          );
          if (!response.parameter) {
            throw new KmsError("internal", "KMS parameter response was empty");
          }
          // Preserve the server's authoritative ref. The release loader must
          // compare it with the manifest ref before accepting any bytes, and a
          // rejected mismatch must never pollute the ordinary read cache.
          return response.parameter;
        } catch (error) {
          throwMapped(error);
        }
      },
      fetchSecret: async (wireRef, version, secretToken, bindingKey, signal) => {
        try {
          const response = await this.#transport.unary(
            SecretServiceService.getSecret,
            { ref: wireRef, version, label: "", secretToken, bindingKey },
            this.#callOptions(signal ? { signal } : {}),
          );
          // Do not synthesize a missing ref from the request. Absence and
          // mismatch are both fail-closed identity violations for a release.
          return {
            ref: response.ref,
            version: response.version,
            value: Uint8Array.from(response.value),
            contentType: response.contentType,
          };
        } catch (error) {
          throwSecretMapped(error);
        }
      },
      fetchSecretMetadata: async (wireRef, version, signal) => {
        try {
          const response = await this.#transport.unary(
            SecretServiceService.getSecretMetadata,
            { ref: wireRef, version, label: "" },
            this.#callOptions(signal ? { signal } : {}),
          );
          if (!response.secret) {
            throw new KmsError("internal", "KMS secret metadata response was empty");
          }
          return response.secret;
        } catch (error) {
          throwMapped(error);
        }
      },
      watchRelease: async (registration, signal): Promise<ReleaseWatchStream> => {
        const stream = this.#transport.bidi(ConfigurationReleaseServiceService.watchRelease, {
          metadata: this._metadata(),
          signal: AbortSignal.any([this.#rootController.signal, signal]),
        });
        try {
          await stream.send({ request: { $case: "register", value: registration } });
          return stream;
        } catch (error) {
          try {
            stream.cancel();
          } catch {
            // Preserve the registration failure after best-effort cleanup.
          }
          throw error;
        }
      },
    };
  }

  async #finishClose(): Promise<void> {
    this.#rootController.abort(new DOMException("KMS client closed", "AbortError"));
    await this.#subscriptions?.stop();
    this.#dispatcher.close();
    this.#cache.clear();
    await this.#transport.close();
  }

  #callOptions(options: CallOptions): TransportCallOptions {
    this.#assertOpen();
    const defaultDeadline = new Date(Date.now() + this.timeoutMs);
    const deadline =
      options.deadline && options.deadline < defaultDeadline ? options.deadline : defaultDeadline;
    return {
      metadata: this._metadata(),
      deadline,
      signal: combineSignals(this.#rootController.signal, options.signal),
    };
  }

  async #secretMutation<Request, Response>(
    ref: ResourceRef,
    method: UnaryMethod<Request, Response>,
    request: Request,
    options: CallOptions,
    errorMapper: typeof mapSecretGrpcError = mapSecretGrpcError,
  ): Promise<Response> {
    try {
      const response = await this.#transport.unary(method, request, this.#callOptions(options));
      this.#cache.invalidateSecret(displayPath(ref));
      return response;
    } catch (error) {
      throwSecretMapped(error, errorMapper);
    }
  }

  async #bindingTransition(
    key: string,
    expectedCurrentVersion: bigint,
    bindingKey: string,
    options: CallOptions,
    bind: boolean,
  ): Promise<SecretVersionTransitionResult> {
    assertUint64(
      expectedCurrentVersion,
      `${bind ? "bindSecret" : "unbindSecret"} expectedCurrentVersion`,
      true,
    );
    const ref = await this.#resolveResourceRefForCall(key, options);
    const request = { ref: toWireRef(ref), expectedCurrentVersion, bindingKey };
    const response = bind
      ? await this.#secretMutation(ref, SecretServiceService.bindSecret, request, options)
      : await this.#secretMutation(ref, SecretServiceService.unbindSecret, request, options);
    return frozenTransitionResult(response);
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

function isolateLogger(logger: Logger): Logger {
  return {
    warn: (message) => observeLoggerCall(() => logger.warn(message)),
    ...(logger.error === undefined
      ? {}
      : {
          error: (message: string) => observeLoggerCall(() => logger.error?.(message)),
        }),
  };
}

function observeLoggerCall(call: () => unknown): void {
  try {
    const returned: unknown = call();
    void Promise.resolve(returned).catch(() => undefined);
  } catch {
    // Logging is observational and must not affect client lifecycle.
  }
}

function validPageSize(value = 0): number {
  if (!Number.isSafeInteger(value) || value < 0 || value > 1_000) {
    throw new ConfigError("pageSize must be an integer from 0 through 1000");
  }
  return value;
}

function assertUint64(value: unknown, name: string, positive = false): asserts value is bigint {
  if (typeof value !== "bigint" || value < (positive ? 1n : 0n) || value > UINT64_MAX) {
    throw new ConfigError(
      `${name} must be a ${positive ? "positive " : ""}bigint in the uint64 range`,
    );
  }
}

function optionalCredential(value: unknown, name: string): string {
  if (value === undefined) return "";
  return requiredCredential(value, name);
}

function requiredCredential(value: unknown, name: string): string {
  if (typeof value !== "string") {
    throw new ConfigError(`${name} must be a string`);
  }
  return value;
}

function requiredVersionSetGuards(
  options: SecretBindingCohortGuardOptions | undefined,
  operation: string,
): { readonly expectedRevision: bigint; readonly expectedAffectedVersions: bigint[] } {
  const revision = options?.expectedRevision;
  const versions = options?.expectedAffectedVersions;
  if (revision === undefined || versions === undefined) {
    throw new ConfigError(
      `${operation} expectedRevision and expectedAffectedVersions are required`,
    );
  }
  assertUint64(revision, `${operation} expectedRevision`, true);
  if (!Array.isArray(versions) || versions.length === 0) {
    throw new ConfigError(`${operation} expectedAffectedVersions must be a non-empty array`);
  }
  const copied: bigint[] = [];
  let previous = 0n;
  for (const [index, version] of versions.entries()) {
    assertUint64(version, `${operation} expectedAffectedVersions[${index}]`, true);
    if (index > 0 && version <= previous) {
      throw new ConfigError(
        `${operation} expectedAffectedVersions must be sorted and contain unique versions`,
      );
    }
    copied.push(version);
    previous = version;
  }
  return { expectedRevision: revision, expectedAffectedVersions: copied };
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

function assertReadIdentity(
  kind: "parameter" | "secret",
  requestedRef: ResourceRef,
  returnedWireRef: WireResourceRef | undefined,
  returnedVersion: bigint,
  selector: { readonly version: bigint; readonly label: string },
): void {
  assertResourceIdentity(kind, requestedRef, returnedWireRef);
  if (
    typeof returnedVersion !== "bigint" ||
    returnedVersion <= 0n ||
    returnedVersion > UINT64_MAX
  ) {
    throw new KmsError("internal", `KMS ${kind} response contained an invalid version`);
  }
  if (selector.version > 0n && returnedVersion !== selector.version) {
    throw new KmsError("internal", `KMS ${kind} response version did not match request`);
  }
}

function assertResourceIdentity(
  kind: string,
  requestedRef: ResourceRef,
  returnedWireRef: WireResourceRef | undefined,
): void {
  if (returnedWireRef === undefined) {
    throw new KmsError("internal", `KMS ${kind} response omitted resource reference`);
  }
  const returnedRef = fromWireRef(returnedWireRef);
  if (
    returnedRef.key !== requestedRef.key ||
    !namespaceEquals(returnedRef.namespace, requestedRef.namespace)
  ) {
    throw new KmsError("internal", `KMS ${kind} response resource reference did not match request`);
  }
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
        bound: version.bound,
        hasAccessToken: version.hasAccessToken,
      }),
    ),
  );
  return Object.freeze({
    env: ref.namespace.env,
    app: ref.namespace.app,
    key: ref.key,
    contentType: secret.contentType,
    bound: secret.bound,
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

function frozenBindingResult(response: {
  readonly anchorVersion: bigint;
  readonly affectedVersions: readonly bigint[];
  readonly revision: bigint;
}): SecretBindingCohortResult {
  return Object.freeze({
    anchorVersion: response.anchorVersion,
    affectedVersions: Object.freeze([...response.affectedVersions]),
    revision: response.revision,
  });
}

function frozenTransitionResult(response: {
  readonly currentVersion: bigint;
  readonly previousVersion: bigint;
  readonly revision: bigint;
}): SecretVersionTransitionResult {
  return Object.freeze({
    currentVersion: response.currentVersion,
    previousVersion: response.previousVersion,
    revision: response.revision,
  });
}

function frozenVersionSetResult(response: {
  readonly affectedVersions: readonly bigint[];
  readonly revision: bigint;
}): SecretVersionSetResult {
  return Object.freeze({
    affectedVersions: Object.freeze([...response.affectedVersions]),
    revision: response.revision,
  });
}

function frozenRecord(values: Readonly<Record<string, bigint>>): Readonly<Record<string, bigint>> {
  return Object.freeze({ ...values });
}

function combineSignals(root: AbortSignal, call?: AbortSignal): AbortSignal {
  return call ? AbortSignal.any([root, call]) : root;
}

function isAbortSignal(value: CallOptions | AbortSignal): value is AbortSignal {
  return value instanceof AbortSignal;
}

function awaitCaller<T>(promise: Promise<T>, options: CallOptions): Promise<T> {
  try {
    assertCallerActive(options);
  } catch (error) {
    return Promise.reject(error);
  }
  const signal = options.signal;
  const deadlineMs = options.deadline?.getTime();
  const hasDeadline = deadlineMs !== undefined && Number.isFinite(deadlineMs);
  if (signal === undefined && !hasDeadline) return promise;

  return new Promise<T>((resolve, reject) => {
    let settled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const cleanup = (): void => {
      if (timer !== undefined) clearTimeout(timer);
      signal?.removeEventListener("abort", abort);
    };
    const succeed = (value: T): void => {
      if (settled) return;
      settled = true;
      cleanup();
      resolve(value);
    };
    const fail = (error: unknown): void => {
      if (settled) return;
      settled = true;
      cleanup();
      reject(error);
    };
    const abort = (): void => {
      fail(signal?.reason ?? new DOMException("Aborted", "AbortError"));
    };

    signal?.addEventListener("abort", abort, { once: true });
    if (hasDeadline) {
      timer = setTimeout(
        () => fail(new KmsError("deadline_exceeded", "KMS namespace discovery deadline exceeded")),
        Math.max(0, (deadlineMs as number) - Date.now()),
      );
    }
    void promise.then(succeed, fail);
    if (signal?.aborted) abort();
  });
}

function assertCallerActive(options: CallOptions): void {
  if (options.signal?.aborted) {
    throw options.signal.reason ?? new DOMException("Aborted", "AbortError");
  }
  const deadlineMs = options.deadline?.getTime();
  if (deadlineMs !== undefined && Number.isFinite(deadlineMs) && deadlineMs <= Date.now()) {
    throw new KmsError("deadline_exceeded", "KMS namespace discovery deadline exceeded");
  }
}

function throwMapped(error: unknown): never {
  const mapped = mapGrpcError(error);
  throw mapped ?? wrapError("unexpected successful gRPC status", error);
}

function throwSecretMapped(
  error: unknown,
  mapper: typeof mapSecretGrpcError = mapSecretGrpcError,
): never {
  const mapped = mapper(error);
  throw mapped ?? new KmsError("internal", "KMS secret operation failed");
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
      this.#warn(`KMS callback queue full; dropped notification for ${path}`);
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
    if (this.#closed) {
      this.#scheduled = false;
      return;
    }
    const item = this.#queue.shift();
    if (item === undefined) {
      this.#scheduled = false;
      return;
    }

    let result: unknown;
    try {
      result = item.callback();
    } catch {
      this.#warn(`KMS change callback threw for ${item.path}`);
      this.#scheduleNext();
      return;
    }
    void Promise.resolve(result).then(
      () => this.#scheduleNext(),
      () => {
        this.#warn(`KMS change callback rejected for ${item.path}`);
        this.#scheduleNext();
      },
    );
  }

  #scheduleNext(): void {
    if (this.#closed || this.#queue.length === 0) {
      this.#scheduled = false;
      return;
    }
    setImmediate(() => this.#drain());
  }

  #warn(message: string): void {
    try {
      this.#logger.warn(message);
    } catch {
      // Logging is observational and must never break callback isolation.
    }
  }
}

export type { WatchCallback, WatchEvent, WatchOptions, WatchStatus };
