import type { ReleaseManifest, ReleaseSnapshot } from "../releases/types.js";
import { cloneConfig } from "./clone.js";

const UINT64_MAX = (1n << 64n) - 1n;

export interface ReleaseIdentityInit {
  readonly namespace?: string;
  readonly name?: string;
  readonly version?: bigint;
  readonly activationRevision?: bigint;
  readonly schemaId?: string;
  readonly schemaVersion?: bigint;
  readonly digest?: string;
}

/** Immutable, value-free identity copied from a release candidate. */
export class ReleaseIdentity {
  readonly namespace: string;
  readonly name: string;
  readonly version: bigint;
  readonly activationRevision: bigint;
  readonly schemaId: string;
  readonly schemaVersion: bigint;
  readonly digest: string;

  constructor(init: ReleaseIdentityInit = {}) {
    this.namespace = init.namespace ?? "";
    this.name = init.name ?? "";
    this.version = init.version ?? 0n;
    this.activationRevision = init.activationRevision ?? 0n;
    this.schemaId = init.schemaId ?? "";
    this.schemaVersion = init.schemaVersion ?? 0n;
    this.digest = init.digest ?? "";
    if (
      this.version < 0n ||
      this.version > UINT64_MAX ||
      this.activationRevision < 0n ||
      this.activationRevision > UINT64_MAX ||
      this.schemaVersion < 0n ||
      this.schemaVersion > UINT64_MAX
    ) {
      throw new RangeError("configstore: release identity fields must be in the uint64 range");
    }
    Object.freeze(this);
  }

  static from(candidate: ReleaseSnapshot | ReleaseManifest): ReleaseIdentity {
    return new ReleaseIdentity({
      namespace: candidate.namespace,
      name: candidate.name,
      version: candidate.version,
      activationRevision: candidate.activationRevision,
      schemaId: candidate.schemaId,
      schemaVersion: candidate.schemaVersion,
      digest: candidate.digest,
    });
  }

  get isZero(): boolean {
    return (
      this.namespace === "" &&
      this.name === "" &&
      this.version === 0n &&
      this.activationRevision === 0n &&
      this.digest === ""
    );
  }

  toString(): string {
    if (!this.namespace && !this.name) {
      return `release@${this.version}#${this.activationRevision}`;
    }
    return `${this.namespace}/${this.name}@${this.version}#${this.activationRevision}`;
  }

  toJSON(): Readonly<Record<string, string>> {
    return Object.freeze({
      namespace: this.namespace,
      name: this.name,
      version: this.version.toString(),
      activationRevision: this.activationRevision.toString(),
      schemaId: this.schemaId,
      schemaVersion: this.schemaVersion.toString(),
      digest: this.digest,
    });
  }

  /** @internal Stable candidate-report deduplication key. */
  dedupeKey(): string {
    return `${this.namespace}\0${this.name}\0${this.version}\0${this.activationRevision}\0${this.digest}`;
  }
}

/**
 * Immutable generation holder. The stored root is never exposed; every
 * composite/secret access returns an independent defensive clone.
 */
export class ConfigSnapshot<T extends object> {
  readonly release: ReleaseIdentity;
  readonly #config: T;

  constructor(config: T, release: ReleaseIdentity = new ReleaseIdentity()) {
    this.#config = cloneConfig(config);
    this.release = release;
    Object.freeze(this);
  }

  /** Defensive full-root clone, intended for restart-bound startup wiring. */
  config(): T {
    return cloneConfig(this.#config);
  }

  /** Defensive field read used by generated typed views. */
  get<K extends keyof T>(key: K): T[K] {
    return cloneConfig(this.#config[key]);
  }
}

export function immutableSnapshot<T extends object>(
  config: T,
  release: ReleaseIdentity = new ReleaseIdentity(),
): ConfigSnapshot<T> {
  return new ConfigSnapshot(config, release);
}
