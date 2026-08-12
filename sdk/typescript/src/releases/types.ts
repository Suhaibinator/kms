import { inspect } from "node:util";

const UINT64_MAX = (1n << 64n) - 1n;

export const RELEASE_STATES = ["received", "prepared", "applied", "rejected"] as const;
export type ReleaseState = (typeof RELEASE_STATES)[number];

export const RELEASE_REJECTION_CATEGORIES = [
  "resolution_failed",
  "token_unavailable",
  "version_mismatch",
  "digest_mismatch",
  "prepare_failed",
  "config_contract_mismatch",
  "config_decode_failed",
  "config_validation_failed",
  "default_mismatch",
  "restart_required",
  "superseded",
  "active_check_failed",
  "internal",
] as const;
export type ReleaseRejectionCategory = (typeof RELEASE_REJECTION_CATEGORIES)[number];

export type ReleaseEntryKind = "parameter" | "secret";

export interface ReleaseEntryMetadataInit {
  readonly alias: string;
  readonly kind: ReleaseEntryKind;
  readonly path: string;
  readonly version: bigint;
  readonly contentType?: string;
  readonly metadataJson?: string;
  readonly parameterDigest?: string;
  readonly clientBound?: boolean;
  readonly hasAccessToken?: boolean;
}

/** Non-sensitive metadata for one immutable release resource pin. */
export class ReleaseEntryMetadata {
  readonly alias: string;
  readonly kind: ReleaseEntryKind;
  readonly path: string;
  readonly version: bigint;
  readonly contentType: string;
  readonly metadataJson: string;
  readonly parameterDigest: string;
  readonly clientBound: boolean;
  readonly hasAccessToken: boolean;

  constructor(init: ReleaseEntryMetadataInit) {
    assertUint64(init.version, "release entry version");
    this.alias = init.alias;
    this.kind = init.kind;
    this.path = init.path;
    this.version = init.version;
    this.contentType = init.contentType ?? "";
    this.metadataJson = init.metadataJson ?? "";
    this.parameterDigest = init.parameterDigest ?? "";
    this.clientBound = init.clientBound ?? false;
    this.hasAccessToken = init.hasAccessToken ?? false;
    Object.freeze(this);
  }

  toJSON(): Record<string, unknown> {
    return {
      alias: this.alias,
      kind: this.kind,
      path: this.path,
      version: this.version.toString(),
      contentType: this.contentType,
      metadataJson: this.metadataJson,
      parameterDigest: this.parameterDigest,
      clientBound: this.clientBound,
      hasAccessToken: this.hasAccessToken,
    };
  }
}

/** A resolved, exact-version non-secret parameter. */
export class ReleaseParameter {
  readonly entry: ReleaseEntryMetadata;
  readonly #value: string;

  constructor(value: string, entry: ReleaseEntryMetadata) {
    this.#value = value;
    this.entry = entry;
    Object.freeze(this);
  }

  value(): string {
    return this.#value;
  }

  stringValue(): string {
    return this.#value;
  }
}

const REDACTED = "[REDACTED]";

/** Secret plaintext with defensive copies and redacted inspection/serialization. */
export class ReleaseSecret {
  readonly entry: ReleaseEntryMetadata;
  readonly #value: Uint8Array;

  constructor(value: Uint8Array, entry: ReleaseEntryMetadata) {
    this.#value = Uint8Array.from(value);
    this.entry = entry;
    Object.freeze(this);
  }

  bytes(): Uint8Array {
    return Uint8Array.from(this.#value);
  }

  stringValue(): string {
    return Buffer.from(this.#value).toString("utf8");
  }

  clone(): ReleaseSecret {
    return new ReleaseSecret(this.#value, this.entry);
  }

  toString(): string {
    return REDACTED;
  }

  toJSON(): string {
    return REDACTED;
  }

  [Symbol.toPrimitive](): string {
    return REDACTED;
  }

  [inspect.custom](): string {
    return REDACTED;
  }
}

interface ReleaseIdentityInit {
  readonly namespace: string;
  readonly name: string;
  readonly version: bigint;
  readonly activationRevision: bigint;
  readonly schemaId?: string;
  readonly schemaVersion?: bigint;
  readonly digest: string;
  readonly metadataJson?: string;
  readonly entries: ReadonlyMap<string, ReleaseEntryMetadata>;
}

interface SafeReleaseJson {
  readonly namespace: string;
  readonly name: string;
  readonly version: string;
  readonly activationRevision: string;
  readonly schemaId: string;
  readonly schemaVersion: string;
  readonly digest: string;
  readonly entries: Readonly<Record<string, ReleaseEntryMetadata>>;
}

abstract class ReleaseIdentity {
  readonly namespace: string;
  readonly name: string;
  readonly version: bigint;
  readonly activationRevision: bigint;
  readonly schemaId: string;
  readonly schemaVersion: bigint;
  readonly digest: string;
  readonly metadataJson: string;
  readonly #entries: ReadonlyMap<string, ReleaseEntryMetadata>;

  protected constructor(init: ReleaseIdentityInit) {
    assertUint64(init.version, "release version");
    assertUint64(init.activationRevision, "release activationRevision");
    const schemaVersion = init.schemaVersion ?? 0n;
    assertUint64(schemaVersion, "release schemaVersion");
    this.namespace = init.namespace;
    this.name = init.name;
    this.version = init.version;
    this.activationRevision = init.activationRevision;
    this.schemaId = init.schemaId ?? "";
    this.schemaVersion = schemaVersion;
    this.digest = init.digest;
    this.metadataJson = init.metadataJson ?? "";
    this.#entries = new Map(init.entries);
  }

  entries(): ReadonlyMap<string, ReleaseEntryMetadata> {
    return new Map(this.#entries);
  }

  entry(alias: string): ReleaseEntryMetadata | undefined {
    return this.#entries.get(alias);
  }

  protected safeJson(): SafeReleaseJson {
    return {
      namespace: this.namespace,
      name: this.name,
      version: this.version.toString(),
      activationRevision: this.activationRevision.toString(),
      schemaId: this.schemaId,
      schemaVersion: this.schemaVersion.toString(),
      digest: this.digest,
      entries: Object.fromEntries(this.#entries),
    };
  }

  protected safeString(type: string): string {
    return `${type}{${this.namespace}/${this.name} version=${this.version} revision=${this.activationRevision} digest=${this.digest} entries=${this.#entries.size}}`;
  }
}

export interface ReleaseManifestInit extends ReleaseIdentityInit {}

/** Immutable unresolved manifest passed to pre-fetch validation. */
export class ReleaseManifest extends ReleaseIdentity {
  constructor(init: ReleaseManifestInit) {
    super(init);
    Object.freeze(this);
  }

  toJSON(): SafeReleaseJson {
    return this.safeJson();
  }

  override toString(): string {
    return this.safeString("ReleaseManifest");
  }

  [inspect.custom](): string {
    return this.toString();
  }
}

export interface ReleaseSnapshotInit extends ReleaseIdentityInit {
  readonly parameters: ReadonlyMap<string, ReleaseParameter>;
  readonly secrets: ReadonlyMap<string, ReleaseSecret>;
}

/** A fully resolved candidate. JSON and inspection deliberately omit all values. */
export class ReleaseSnapshot extends ReleaseIdentity {
  readonly #parameters: ReadonlyMap<string, ReleaseParameter>;
  readonly #secrets: ReadonlyMap<string, ReleaseSecret>;

  constructor(init: ReleaseSnapshotInit) {
    super(init);
    this.#parameters = new Map(init.parameters);
    this.#secrets = new Map(
      [...init.secrets].map(([alias, secret]) => [alias, secret.clone()] as const),
    );
    Object.freeze(this);
  }

  parameters(): ReadonlyMap<string, ReleaseParameter> {
    return new Map(this.#parameters);
  }

  parameter(alias: string): ReleaseParameter | undefined {
    return this.#parameters.get(alias);
  }

  secrets(): ReadonlyMap<string, ReleaseSecret> {
    return new Map([...this.#secrets].map(([alias, secret]) => [alias, secret.clone()] as const));
  }

  secret(alias: string): ReleaseSecret | undefined {
    return this.#secrets.get(alias)?.clone();
  }

  toJSON(): SafeReleaseJson {
    return this.safeJson();
  }

  override toString(): string {
    return this.safeString("ReleaseSnapshot");
  }

  [inspect.custom](): string {
    return this.toString();
  }
}

/** Application-owned resources prepared before an atomic, infallible swap. */
export interface PreparedRelease {
  commit(): void;
  abort(): void;
}

export type PrepareRelease = (
  snapshot: ReleaseSnapshot,
  signal: AbortSignal,
) => PreparedRelease | Promise<PreparedRelease>;

export interface ReleaseLoaderStatus {
  readonly state: ReleaseState | "idle";
  readonly observedVersion: bigint;
  readonly observedRevision: bigint;
  readonly appliedVersion: bigint;
  readonly appliedRevision: bigint;
  readonly lastFailureCategory?: ReleaseRejectionCategory;
  readonly lastFailureAt?: Date;
  readonly lastResolutionDurationMs: number;
  readonly reconnects: bigint;
}

export interface ReleaseLoaderStats {
  readonly candidates: bigint;
  readonly applied: bigint;
  readonly rejected: Readonly<Record<string, bigint>>;
  readonly reconnects: bigint;
}

/** Categorizes preparation failures without forwarding their potentially sensitive text. */
export class ClassifiedReleaseError extends Error {
  readonly releaseRejectionCategory: ReleaseRejectionCategory;

  constructor(category: ReleaseRejectionCategory, message?: string, options?: ErrorOptions) {
    super(message ?? `configuration release rejected (${category})`, options);
    this.name = "ClassifiedReleaseError";
    this.releaseRejectionCategory = category;
  }
}

/** Redacted failure surfaced by the loader for a rejected candidate. */
export class ReleaseCandidateError extends Error {
  readonly category: ReleaseRejectionCategory;

  constructor(category: ReleaseRejectionCategory) {
    super(`KMS configuration release candidate rejected (${category})`);
    this.name = "ReleaseCandidateError";
    this.category = category;
  }

  toJSON(): Record<string, string> {
    return { name: this.name, category: this.category, message: this.message };
  }

  [inspect.custom](): string {
    return `${this.name}: ${this.message}`;
  }
}

export function classifiedReleaseCategory(error: unknown): ReleaseRejectionCategory | undefined {
  if (typeof error !== "object" || error === null) {
    return undefined;
  }
  try {
    const category = Reflect.get(error, "releaseRejectionCategory") as unknown;
    return typeof category === "string" &&
      (RELEASE_REJECTION_CATEGORIES as readonly string[]).includes(category)
      ? (category as ReleaseRejectionCategory)
      : undefined;
  } catch {
    return undefined;
  }
}

function assertUint64(value: unknown, name: string): asserts value is bigint {
  if (typeof value !== "bigint" || value < 0n || value > UINT64_MAX) {
    throw new TypeError(`${name} must be a bigint in the uint64 range`);
  }
}
