/** The only value emitted when a secret-bearing object is rendered. */
export const REDACTED = "[REDACTED]";

const inspectCustom = Symbol.for("nodejs.util.inspect.custom");
const encoder = new TextEncoder();
const UINT64_MAX = (1n << 64n) - 1n;

export interface SecretMetadata {
  readonly path?: string;
  readonly version?: bigint;
  readonly contentType?: string;
}

/**
 * Secret plaintext plus non-sensitive metadata.
 *
 * Construction and every plaintext accessor make defensive byte copies.
 * String coercion, JSON serialization, and Node inspection always redact.
 */
export class Secret {
  readonly #value: Uint8Array;
  readonly #path: string;
  readonly #version: bigint;
  readonly #contentType: string;

  constructor(value: Uint8Array | string = new Uint8Array(), metadata: SecretMetadata = {}) {
    this.#value = typeof value === "string" ? encoder.encode(value) : Uint8Array.from(value);
    this.#path = metadata.path ?? "";
    this.#version = metadata.version ?? 0n;
    if (typeof this.#version !== "bigint" || this.#version < 0n || this.#version > UINT64_MAX) {
      throw new TypeError("secret version must be a bigint in the uint64 range");
    }
    this.#contentType = metadata.contentType ?? "";
    Object.freeze(this);
  }

  /** Return an independent copy of the plaintext bytes. */
  bytes(): Uint8Array {
    return Uint8Array.from(this.#value);
  }

  /** Decode the plaintext explicitly. UTF-8 is used by default. */
  text(encoding: BufferEncoding = "utf8"): string {
    return Buffer.from(this.#value).toString(encoding);
  }

  get path(): string {
    return this.#path;
  }

  get version(): bigint {
    return this.#version;
  }

  get contentType(): string {
    return this.#contentType;
  }

  get length(): number {
    return this.#value.byteLength;
  }

  get isEmpty(): boolean {
    return this.#value.byteLength === 0;
  }

  clone(): Secret {
    return new Secret(this.#value, {
      path: this.#path,
      version: this.#version,
      contentType: this.#contentType,
    });
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

/** Wrap plaintext, mainly for tests and tools. */
export function newSecret(value: Uint8Array | string, metadata: SecretMetadata = {}): Secret {
  return new Secret(value, metadata);
}
