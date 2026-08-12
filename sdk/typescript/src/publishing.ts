const UINT64_MAX = (1n << 64n) - 1n;
const CANONICAL_DECIMAL_REVISION = /^(?:0|[1-9][0-9]*)$/;
const FORBIDDEN_PUBLIC_KEYS = new Set(["__proto__", "constructor", "prototype"]);

declare const decimalRevisionBrand: unique symbol;

/** A canonical, base-10 uint64 used at JSON and HTTP boundaries. */
export type DecimalRevision = string & {
  readonly [decimalRevisionBrand]: "DecimalRevision";
};

export type PublicJsonPrimitive = null | boolean | number | string;
export type PublicJsonValue = PublicJsonPrimitive | readonly PublicJsonValue[] | PublicJsonObject;
export interface PublicJsonObject {
  readonly [key: string]: PublicJsonValue;
}

/** The single immutable generation captured for one operation. */
export interface PolicySnapshot<TPolicy> {
  readonly revision: bigint;
  readonly value: Readonly<TPolicy>;
}

/**
 * A synchronous atomic-snapshot boundary. Implementations must return one
 * immutable generation and must not assemble a result from multiple reads.
 */
export interface SnapshotReader<TPolicy> {
  current(): PolicySnapshot<TPolicy> | undefined;
}

export interface PublicConfig<TConfig extends PublicJsonObject> {
  readonly revision: bigint;
  readonly config: Readonly<TConfig>;
}

export interface PublicConfigWire<TConfig extends PublicJsonObject> {
  readonly revision: DecimalRevision;
  readonly config: Readonly<TConfig>;
}

export type PublicFieldSelector<TPolicy> = (policy: Readonly<TPolicy>) => PublicJsonValue;

export type PublicProjectionMap<TPolicy> = Readonly<Record<string, PublicFieldSelector<TPolicy>>>;

type ProjectionResult<TPolicy, TMap extends PublicProjectionMap<TPolicy>> = Readonly<{
  [TKey in keyof TMap]: ReturnType<TMap[TKey]>;
}>;

export interface PublicProjection<TPolicy, TConfig extends PublicJsonObject = PublicJsonObject> {
  /** The complete, immutable allowlist of top-level public field names. */
  readonly keys: readonly (keyof TConfig & string)[];

  /** Derives and defensively freezes a public-only view. */
  project(policy: Readonly<TPolicy>): Readonly<TConfig>;
}

export interface ValidationSuccess {
  readonly valid: true;
}

export interface ValidationFailure<TValidationErrors extends PublicJsonValue> {
  readonly valid: false;
  readonly errors: TValidationErrors;
}

export type ValidationDecision<TValidationErrors extends PublicJsonValue> =
  | ValidationSuccess
  | ValidationFailure<TValidationErrors>;

export type AuthoritativeValidator<TPolicy, TInput, TValidationErrors extends PublicJsonValue> = (
  policy: Readonly<TPolicy>,
  input: TInput,
) => ValidationDecision<TValidationErrors>;

/** Redacted publisher lifecycle events suitable for metrics and tracing. */
export type PolicyPublisherEvent =
  | {
      readonly type: "public_config_published";
      readonly revision: DecimalRevision;
      readonly observedAtUnixMs: number;
    }
  | {
      readonly type: "public_config_unavailable";
      readonly observedAtUnixMs: number;
    }
  | {
      readonly type: "policy_revision_rejected";
      readonly currentRevision: DecimalRevision;
      readonly observedAtUnixMs: number;
    }
  | {
      readonly type: "policy_validation_succeeded";
      readonly revision: DecimalRevision;
      readonly observedAtUnixMs: number;
    }
  | {
      readonly type: "policy_validation_failed";
      readonly revision: DecimalRevision;
      readonly observedAtUnixMs: number;
    };

export type PolicyPublisherObserver = (event: PolicyPublisherEvent) => unknown;

export type PolicyValidationResult<
  TConfig extends PublicJsonObject,
  TValidationErrors extends PublicJsonValue,
> =
  | {
      readonly status: "success";
      readonly revision: DecimalRevision;
    }
  | {
      readonly status: "validation_failed";
      readonly revision: DecimalRevision;
      readonly errors: TValidationErrors;
    }
  | {
      readonly status: "policy_changed";
      readonly current: PublicConfigWire<TConfig>;
    }
  | {
      readonly status: "unavailable";
    };

export interface PolicyPublisher<
  TConfig extends PublicJsonObject,
  TInput,
  TValidationErrors extends PublicJsonValue,
> {
  /** Captures the source exactly once and returns an internal bigint revision. */
  read(): PublicConfig<TConfig> | undefined;

  /** Captures the source exactly once and returns a JSON-safe decimal revision. */
  readWire(): PublicConfigWire<TConfig> | undefined;

  /**
   * Returns a strong ETag for a known revision. With no argument, captures the
   * source exactly once and returns the active revision's ETag.
   */
  etag(revision?: bigint | DecimalRevision): string | undefined;

  /**
   * Compares and validates against one captured generation. Invalid or stale
   * client revisions receive the public projection of that same generation.
   */
  validate(
    clientRevision: unknown,
    input: TInput,
  ): PolicyValidationResult<TConfig, TValidationErrors>;
}

export interface CreatePolicyPublisherOptions<
  TPolicy,
  TConfig extends PublicJsonObject,
  TInput,
  TValidationErrors extends PublicJsonValue,
> {
  readonly source: SnapshotReader<TPolicy>;
  readonly projection: PublicProjection<TPolicy, TConfig>;
  readonly validate: AuthoritativeValidator<TPolicy, TInput, TValidationErrors>;
  /** Receives frozen, value-free lifecycle events. Observer failures are ignored. */
  readonly onEvent?: PolicyPublisherObserver;
}

/** Parses a canonical decimal uint64 without losing precision. */
export function parseRevision(value: unknown): bigint {
  if (typeof value !== "string" || !CANONICAL_DECIMAL_REVISION.test(value)) {
    throw new TypeError("revision must be a canonical unsigned decimal string");
  }

  const revision = BigInt(value);
  if (revision > UINT64_MAX) {
    throw new RangeError("revision is outside the uint64 range");
  }
  return revision;
}

/** Formats a bigint revision as a canonical decimal uint64. */
export function formatRevision(revision: bigint): DecimalRevision {
  assertUint64Revision(revision);
  return revision.toString(10) as DecimalRevision;
}

/** Returns the strong validator used by the public-config Route Handler. */
export function formatPublicConfigEtag(revision: bigint | DecimalRevision): string {
  const decimal =
    typeof revision === "bigint"
      ? formatRevision(revision)
      : formatRevision(parseRevision(revision));
  return `"kms-public-config-${decimal}"`;
}

/**
 * Declares a public projection as an explicit key-to-selector allowlist.
 * Merely adding a field to an application policy can therefore never publish
 * it. Selector results are recursively checked, cloned, and frozen.
 */
export function definePublicProjection<TPolicy>(): <
  const TMap extends PublicProjectionMap<TPolicy>,
>(
  allowlist: TMap,
) => PublicProjection<TPolicy, ProjectionResult<TPolicy, TMap>>;
export function definePublicProjection<TPolicy, const TMap extends PublicProjectionMap<TPolicy>>(
  allowlist: TMap,
): PublicProjection<TPolicy, ProjectionResult<TPolicy, TMap>>;
export function definePublicProjection<TPolicy, const TMap extends PublicProjectionMap<TPolicy>>(
  allowlist?: TMap,
):
  | PublicProjection<TPolicy, ProjectionResult<TPolicy, TMap>>
  | (<const TNextMap extends PublicProjectionMap<TPolicy>>(
      nextAllowlist: TNextMap,
    ) => PublicProjection<TPolicy, ProjectionResult<TPolicy, TNextMap>>) {
  if (allowlist === undefined) {
    return (nextAllowlist) => createPublicProjection(nextAllowlist);
  }
  return createPublicProjection(allowlist);
}

function createPublicProjection<TPolicy, const TMap extends PublicProjectionMap<TPolicy>>(
  allowlist: TMap,
): PublicProjection<TPolicy, ProjectionResult<TPolicy, TMap>> {
  const selectors = captureProjectionMap(allowlist);
  type TResult = ProjectionResult<TPolicy, TMap>;

  return Object.freeze({
    keys: Object.freeze(selectors.map(([key]) => key)) as readonly (keyof TResult & string)[],
    project(policy: Readonly<TPolicy>): Readonly<TResult> {
      const projected: Record<string, PublicJsonValue> = {};
      for (const [key, selector] of selectors) {
        const selected = selector(policy);
        Object.defineProperty(projected, key, {
          value: cloneAndFreezePublicJson(selected, `$.${key}`, new WeakSet()),
          enumerable: true,
          configurable: false,
          writable: false,
        });
      }
      return Object.freeze(projected) as TResult;
    },
  });
}

/**
 * Recursively validates, defensively clones, and freezes an untrusted public
 * JSON value. This intentionally rejects values JSON.stringify would silently
 * omit or coerce.
 */
export function freezePublicJson(value: unknown): PublicJsonValue {
  return cloneAndFreezePublicJson(value, "$", new WeakSet());
}

/** Validates and freezes an untrusted public-config wire object. */
export function normalizePublicConfigWire<TConfig extends PublicJsonObject = PublicJsonObject>(
  value: unknown,
  validateConfig?: (config: PublicJsonObject) => config is TConfig,
): PublicConfigWire<TConfig> {
  const object = requirePlainObject(value, "$", new WeakSet());
  const keys = ownEnumerableDataKeys(object, "$", new Set(["revision", "config"]));
  if (keys.length !== 2 || !keys.includes("revision") || !keys.includes("config")) {
    throw new TypeError("public config must contain exactly revision and config");
  }

  const revisionValue = readDataProperty(object, "revision", "$.revision");
  const revision = formatRevision(parseRevision(revisionValue));
  const configValue = readDataProperty(object, "config", "$.config");
  const config = cloneAndFreezePublicJson(configValue, "$.config", new WeakSet());
  if (!isPublicJsonObject(config)) {
    throw new TypeError("public config config must be a JSON object");
  }
  if (validateConfig !== undefined && !validateConfig(config)) {
    throw new TypeError("public config failed application validation");
  }

  return Object.freeze({ revision, config }) as PublicConfigWire<TConfig>;
}

export function createPolicyPublisher<
  TPolicy,
  TConfig extends PublicJsonObject,
  TInput,
  TValidationErrors extends PublicJsonValue,
>(
  options: CreatePolicyPublisherOptions<TPolicy, TConfig, TInput, TValidationErrors>,
): PolicyPublisher<TConfig, TInput, TValidationErrors> {
  if (typeof options.source?.current !== "function") {
    throw new TypeError("policy publisher source must implement current()");
  }
  if (typeof options.projection?.project !== "function") {
    throw new TypeError("policy publisher projection must implement project()");
  }
  if (typeof options.validate !== "function") {
    throw new TypeError("policy publisher validate must be a function");
  }
  const allowedProjectionKeys = captureProjectionKeys(options.projection.keys);
  const observe = createSafeObserver(options.onEvent);

  const readSnapshot = (): PolicySnapshot<TPolicy> | undefined => {
    const snapshot = options.source.current();
    if (snapshot === undefined) {
      return undefined;
    }
    assertSnapshot(snapshot);
    return snapshot;
  };

  const projectSnapshot = (snapshot: PolicySnapshot<TPolicy>): PublicConfig<TConfig> => {
    const projected = freezePublicJson(options.projection.project(snapshot.value));
    if (!isPublicJsonObject(projected)) {
      throw new TypeError("public projection must return a JSON object");
    }
    const projectedKeys = Object.keys(projected);
    if (
      projectedKeys.length !== allowedProjectionKeys.length ||
      projectedKeys.some((key) => !allowedProjectionKeys.includes(key))
    ) {
      throw new TypeError("public projection returned fields outside its allowlist");
    }
    return Object.freeze({
      revision: snapshot.revision,
      config: projected as TConfig,
    });
  };

  const toWire = (config: PublicConfig<TConfig>): PublicConfigWire<TConfig> =>
    Object.freeze({
      revision: formatRevision(config.revision),
      config: config.config,
    });

  return Object.freeze({
    read(): PublicConfig<TConfig> | undefined {
      const snapshot = readSnapshot();
      if (snapshot === undefined) {
        observe({ type: "public_config_unavailable", observedAtUnixMs: Date.now() });
        return undefined;
      }
      const config = projectSnapshot(snapshot);
      observe({
        type: "public_config_published",
        revision: formatRevision(snapshot.revision),
        observedAtUnixMs: Date.now(),
      });
      return config;
    },

    readWire(): PublicConfigWire<TConfig> | undefined {
      const snapshot = readSnapshot();
      if (snapshot === undefined) {
        observe({ type: "public_config_unavailable", observedAtUnixMs: Date.now() });
        return undefined;
      }
      const config = toWire(projectSnapshot(snapshot));
      observe({
        type: "public_config_published",
        revision: config.revision,
        observedAtUnixMs: Date.now(),
      });
      return config;
    },

    etag(revision?: bigint | DecimalRevision): string | undefined {
      if (revision !== undefined) {
        return formatPublicConfigEtag(revision);
      }
      const snapshot = readSnapshot();
      return snapshot === undefined ? undefined : formatPublicConfigEtag(snapshot.revision);
    },

    validate(
      clientRevision: unknown,
      input: TInput,
    ): PolicyValidationResult<TConfig, TValidationErrors> {
      const snapshot = readSnapshot();
      if (snapshot === undefined) {
        observe({ type: "public_config_unavailable", observedAtUnixMs: Date.now() });
        return Object.freeze({ status: "unavailable" });
      }

      if (!revisionEquals(clientRevision, snapshot.revision)) {
        observe({
          type: "policy_revision_rejected",
          currentRevision: formatRevision(snapshot.revision),
          observedAtUnixMs: Date.now(),
        });
        return Object.freeze({
          status: "policy_changed",
          current: toWire(projectSnapshot(snapshot)),
        });
      }

      const decision = options.validate(snapshot.value, input);
      if (!isValidationDecision(decision)) {
        throw new TypeError("policy validator returned an invalid decision");
      }
      const revision = formatRevision(snapshot.revision);
      if (decision.valid) {
        observe({
          type: "policy_validation_succeeded",
          revision,
          observedAtUnixMs: Date.now(),
        });
        return Object.freeze({ status: "success", revision });
      }

      observe({
        type: "policy_validation_failed",
        revision,
        observedAtUnixMs: Date.now(),
      });

      return Object.freeze({
        status: "validation_failed",
        revision,
        errors: freezePublicJson(decision.errors) as TValidationErrors,
      });
    },
  });
}

function createSafeObserver(observer: PolicyPublisherObserver | undefined) {
  return (event: PolicyPublisherEvent): void => {
    if (observer === undefined) return;
    try {
      Promise.resolve(observer(Object.freeze(event))).catch(() => undefined);
    } catch {
      // Observability must never change publication or validation behavior.
    }
  };
}

function assertUint64Revision(revision: bigint): void {
  if (typeof revision !== "bigint") {
    throw new TypeError("revision must be a bigint");
  }
  if (revision < 0n || revision > UINT64_MAX) {
    throw new RangeError("revision is outside the uint64 range");
  }
}

function revisionEquals(candidate: unknown, expected: bigint): boolean {
  if (typeof candidate === "bigint") {
    return candidate >= 0n && candidate <= UINT64_MAX && candidate === expected;
  }
  if (typeof candidate !== "string") {
    return false;
  }
  try {
    return parseRevision(candidate) === expected;
  } catch {
    return false;
  }
}

function assertSnapshot<TPolicy>(
  snapshot: PolicySnapshot<TPolicy>,
): asserts snapshot is PolicySnapshot<TPolicy> {
  if (snapshot === null || typeof snapshot !== "object") {
    throw new TypeError("snapshot reader returned an invalid snapshot");
  }
  assertUint64Revision(snapshot.revision);
  if (!("value" in snapshot)) {
    throw new TypeError("snapshot reader returned a snapshot without a value");
  }
}

function isValidationDecision<TValidationErrors extends PublicJsonValue>(
  decision: ValidationDecision<TValidationErrors>,
): decision is ValidationDecision<TValidationErrors> {
  if (decision === null || typeof decision !== "object") {
    return false;
  }
  if (decision.valid === true) {
    return true;
  }
  return decision.valid === false && "errors" in decision;
}

function captureProjectionMap<TPolicy>(
  allowlist: PublicProjectionMap<TPolicy>,
): readonly (readonly [string, PublicFieldSelector<TPolicy>])[] {
  if (!isPlainObject(allowlist)) {
    throw new TypeError("public projection allowlist must be a plain object");
  }

  const keys = ownEnumerableDataKeys(allowlist, "$allowlist");
  const entries: [string, PublicFieldSelector<TPolicy>][] = [];
  for (const key of keys) {
    assertSafePublicKey(key, `$allowlist.${key}`);
    assertProjectionKey(key);
    const selector = readDataProperty(allowlist, key, `$allowlist.${key}`);
    if (typeof selector !== "function") {
      throw new TypeError(`public projection selector $.${key} must be a function`);
    }
    entries.push([key, selector as PublicFieldSelector<TPolicy>]);
  }
  return Object.freeze(entries.map((entry) => Object.freeze(entry)));
}

function captureProjectionKeys(keys: readonly string[]): readonly string[] {
  if (!Array.isArray(keys)) {
    throw new TypeError("public projection keys must be an array");
  }
  const captured: string[] = [];
  const seen = new Set<string>();
  for (const key of keys) {
    if (typeof key !== "string") {
      throw new TypeError("public projection keys must contain only strings");
    }
    assertSafePublicKey(key, `public projection key ${key}`);
    assertProjectionKey(key);
    if (seen.has(key)) {
      throw new TypeError(`public projection key ${key} is duplicated`);
    }
    seen.add(key);
    captured.push(key);
  }
  return Object.freeze(captured);
}

function cloneAndFreezePublicJson(
  value: unknown,
  path: string,
  ancestors: WeakSet<object>,
): PublicJsonValue {
  if (value === null || typeof value === "string" || typeof value === "boolean") {
    return value;
  }
  if (typeof value === "number") {
    if (!Number.isFinite(value)) {
      throw new TypeError(`${path} contains a non-finite number`);
    }
    return value;
  }
  if (typeof value !== "object") {
    throw new TypeError(`${path} is not a public JSON value`);
  }
  if (ancestors.has(value)) {
    throw new TypeError(`${path} contains a cycle`);
  }

  ancestors.add(value);
  try {
    if (Array.isArray(value)) {
      return cloneAndFreezeArray(value, path, ancestors);
    }
    const object = requirePlainObject(value, path, ancestors);
    const keys = ownEnumerableDataKeys(object, path);
    const clone: Record<string, PublicJsonValue> = {};
    for (const key of keys) {
      assertSafePublicKey(key, `${path}.${key}`);
      Object.defineProperty(clone, key, {
        value: cloneAndFreezePublicJson(
          readDataProperty(object, key, `${path}.${key}`),
          `${path}.${key}`,
          ancestors,
        ),
        enumerable: true,
        configurable: false,
        writable: false,
      });
    }
    return Object.freeze(clone);
  } finally {
    ancestors.delete(value);
  }
}

function cloneAndFreezeArray(
  array: readonly unknown[],
  path: string,
  ancestors: WeakSet<object>,
): readonly PublicJsonValue[] {
  const ownKeys = Reflect.ownKeys(array);
  for (const key of ownKeys) {
    if (key === "length") {
      continue;
    }
    if (typeof key !== "string" || !/^(?:0|[1-9][0-9]*)$/.test(key)) {
      throw new TypeError(`${path} contains a non-index array property`);
    }
    const index = Number(key);
    if (!Number.isSafeInteger(index) || index < 0 || index >= array.length) {
      throw new TypeError(`${path} contains an invalid array index`);
    }
    const descriptor = Object.getOwnPropertyDescriptor(array, key);
    if (descriptor === undefined || !("value" in descriptor) || !descriptor.enumerable) {
      throw new TypeError(`${path}[${key}] must be an enumerable data property`);
    }
  }

  const clone: PublicJsonValue[] = [];
  for (let index = 0; index < array.length; index += 1) {
    if (!Object.hasOwn(array, index)) {
      throw new TypeError(`${path} contains a sparse array element`);
    }
    clone.push(cloneAndFreezePublicJson(array[index], `${path}[${index}]`, ancestors));
  }
  return Object.freeze(clone);
}

function requirePlainObject(
  value: unknown,
  path: string,
  _ancestors: WeakSet<object>,
): Record<string, unknown> {
  if (!isPlainObject(value)) {
    throw new TypeError(`${path} must be a plain JSON object`);
  }
  return value;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function isPublicJsonObject(value: PublicJsonValue): value is PublicJsonObject {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function ownEnumerableDataKeys(
  object: object,
  path: string,
  allowedKeys?: ReadonlySet<string>,
): string[] {
  const keys: string[] = [];
  for (const key of Reflect.ownKeys(object)) {
    if (typeof key !== "string") {
      throw new TypeError(`${path} contains a symbol property`);
    }
    assertSafePublicKey(key, `${path}.${key}`);
    if (allowedKeys !== undefined && !allowedKeys.has(key)) {
      throw new TypeError(`${path} contains unexpected property ${key}`);
    }
    const descriptor = Object.getOwnPropertyDescriptor(object, key);
    if (descriptor === undefined || !("value" in descriptor) || !descriptor.enumerable) {
      throw new TypeError(`${path}.${key} must be an enumerable data property`);
    }
    keys.push(key);
  }
  return keys;
}

function readDataProperty(object: object, key: string, path: string): unknown {
  const descriptor = Object.getOwnPropertyDescriptor(object, key);
  if (descriptor === undefined || !("value" in descriptor)) {
    throw new TypeError(`${path} must be a data property`);
  }
  return descriptor.value;
}

function assertSafePublicKey(key: string, path: string): void {
  if (FORBIDDEN_PUBLIC_KEYS.has(key)) {
    throw new TypeError(`${path} uses a forbidden public JSON key`);
  }
}

function assertProjectionKey(key: string): void {
  if (key === "revision") {
    throw new TypeError("public projection key revision is reserved for the publisher");
  }
}
