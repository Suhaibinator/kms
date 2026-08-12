import {
  InvalidJsonDocumentError,
  type JsonNode,
  parseStrictJson,
  stringifyJsonNode,
} from "./strict-json.js";

export type CodecKind =
  | "boolean"
  | "string"
  | "integer"
  | "bigint"
  | "number"
  | "duration"
  | "bytes"
  | "object"
  | "array"
  | "record"
  | "nullable";

/** Runtime codec emitted by generated managed-configuration bindings. */
export interface ValueCodec<T> {
  readonly kind: CodecKind;
  /** @internal */
  readonly decodeNode: (node: JsonNode, path: string) => T;
  /** @internal */
  readonly encodeNode: (value: T, path: string) => JsonNode;
}

export interface FieldCodec<T extends object = Record<string, unknown>> {
  readonly jsonName: string;
  readonly property: keyof T & string;
  readonly value: ValueCodec<unknown>;
}

export interface GroupCodec<T extends object> extends ValueCodec<T> {
  readonly kind: "object";
  readonly fields: readonly FieldCodec<T>[];
}

const decodePaths = new WeakMap<ConfigDecodeError, string>();

/**
 * A value-free strict-decoding diagnostic. Messages contain only validated,
 * generated canonical paths and fixed error text.
 */
export class ConfigDecodeError extends Error {
  constructor(path: string, message: string, options?: ErrorOptions) {
    super(message, options);
    this.name = "ConfigDecodeError";
    decodePaths.set(this, path);
  }

  toJSON(): Readonly<{ name: string; message: string }> {
    return Object.freeze({ name: this.name, message: this.message });
  }
}

/** @internal Used by the classified-error bridge without exposing input names. */
export function configDecodeErrorPath(error: unknown): string | undefined {
  return error instanceof ConfigDecodeError ? decodePaths.get(error) : undefined;
}

/** Map a required JSON property to a TypeScript property. */
export function field<T extends object, K extends keyof T & string>(
  jsonName: string,
  property: K,
  value: ValueCodec<T[K]>,
): FieldCodec<T> {
  return Object.freeze({ jsonName, property, value: value as ValueCodec<unknown> });
}

/** Define one complete, strict parameter-group document. */
export function group<T extends object>(fields: readonly FieldCodec<T>[]): GroupCodec<T> {
  return objectCodec(fields);
}

export interface IntegerCodecOptions {
  /** Signed portable width. JavaScript number codecs are capped at 53 bits. */
  readonly bits?: number;
  readonly unsigned?: boolean;
}

export interface BigIntCodecOptions {
  readonly bits?: number;
  readonly unsigned?: boolean;
}

export interface FloatCodecOptions {
  readonly bits?: 32 | 64;
}

export const codecs = Object.freeze({
  boolean: scalarCodec<boolean>(
    "boolean",
    (node, path) => {
      if (node.kind !== "boolean") throw typeError(path, "boolean");
      return node.value;
    },
    (value, path) => {
      if (typeof value !== "boolean")
        throw descriptorError(path, "boolean codec does not match source");
      return { kind: "boolean", value };
    },
  ),

  string: scalarCodec<string>(
    "string",
    (node, path) => {
      if (node.kind !== "string") throw typeError(path, "string");
      return node.value;
    },
    (value, path) => {
      if (typeof value !== "string")
        throw descriptorError(path, "string codec does not match source");
      return { kind: "string", value };
    },
  ),

  int(options: IntegerCodecOptions = {}): ValueCodec<number> {
    const bits = options.bits ?? 32;
    validateBits(bits, 53, "integer");
    const unsigned = options.unsigned ?? false;
    const minimum = unsigned ? 0n : -(1n << BigInt(bits - 1));
    const maximum = unsigned ? (1n << BigInt(bits)) - 1n : (1n << BigInt(bits - 1)) - 1n;
    const expected = unsigned ? "unsigned integer" : "integer";
    return scalarCodec<number>(
      "integer",
      (node, path) => {
        if (node.kind !== "number") throw typeError(path, expected);
        const value = exactInteger(node.lexeme);
        if (value === undefined || value < minimum || value > maximum) {
          throw rangeError(path, expected);
        }
        return Number(value);
      },
      (value, path) => {
        if (!Number.isSafeInteger(value)) throw rangeError(path, expected);
        const integer = BigInt(value);
        if (integer < minimum || integer > maximum) throw rangeError(path, expected);
        return { kind: "number", lexeme: integer.toString() };
      },
    );
  },

  bigint(options: BigIntCodecOptions = {}): ValueCodec<bigint> {
    const bits = options.bits ?? 64;
    validateBits(bits, 64, "bigint");
    const unsigned = options.unsigned ?? false;
    const minimum = unsigned ? 0n : -(1n << BigInt(bits - 1));
    const maximum = unsigned ? (1n << BigInt(bits)) - 1n : (1n << BigInt(bits - 1)) - 1n;
    const expected = unsigned ? "unsigned integer" : "integer";
    return scalarCodec<bigint>(
      "bigint",
      (node, path) => {
        if (node.kind !== "number") throw typeError(path, expected);
        const value = exactInteger(node.lexeme);
        if (value === undefined || value < minimum || value > maximum) {
          throw rangeError(path, expected);
        }
        return value;
      },
      (value, path) => {
        if (typeof value !== "bigint" || value < minimum || value > maximum) {
          throw rangeError(path, expected);
        }
        return { kind: "number", lexeme: value.toString() };
      },
    );
  },

  float(options: FloatCodecOptions = {}): ValueCodec<number> {
    const bits = options.bits ?? 64;
    if (bits !== 32 && bits !== 64) throw new RangeError("float bits must be 32 or 64");
    const maximum = bits === 32 ? "3.4028234663852886e38" : "1.7976931348623157e308";
    return scalarCodec<number>(
      "number",
      (node, path) => {
        if (node.kind !== "number") throw typeError(path, "number");
        if (compareDecimalMagnitude(node.lexeme, maximum) > 0) throw rangeError(path, "number");
        const value = Number(node.lexeme);
        if (!Number.isFinite(value)) throw rangeError(path, "number");
        return bits === 32 ? Math.fround(value) : value;
      },
      (value, path) => {
        if (typeof value !== "number" || !Number.isFinite(value)) throw rangeError(path, "number");
        const encoded = bits === 32 ? Math.fround(value) : value;
        const lexeme = String(encoded);
        if (compareDecimalMagnitude(lexeme, maximum) > 0) throw rangeError(path, "number");
        return { kind: "number", lexeme };
      },
    );
  },

  /** Go-duration string represented exactly as signed nanoseconds. */
  duration: scalarCodec<bigint>(
    "duration",
    (node, path) => {
      if (node.kind !== "string") throw typeError(path, "duration string");
      const value = parseDuration(node.value);
      if (value === undefined) throw diagnostic(path, `configstore: invalid duration at ${path}`);
      return value;
    },
    (value, path) => {
      if (typeof value !== "bigint" || value < INT64_MIN || value > INT64_MAX) {
        throw rangeError(path, "duration");
      }
      return { kind: "string", value: formatDuration(value) };
    },
  ),

  /** Canonical standard-base64 bytes. JSON null remains distinct from empty. */
  bytes: scalarCodec<Uint8Array | null>(
    "bytes",
    (node, path) => {
      if (node.kind === "null") return null;
      if (node.kind !== "string") throw typeError(path, "base64 string");
      if (!isCanonicalBase64(node.value)) {
        throw diagnostic(path, `configstore: invalid base64 at ${path}`);
      }
      return Uint8Array.from(Buffer.from(node.value, "base64"));
    },
    (value, path) => {
      if (value === null) return { kind: "null" };
      if (!(value instanceof Uint8Array)) {
        throw descriptorError(path, "byte codec does not match source");
      }
      return { kind: "string", value: Buffer.from(value).toString("base64") };
    },
  ),

  object: objectCodec,

  /** Variable-length array. JSON null remains distinct from an empty array. */
  array<T>(element: ValueCodec<T>): ValueCodec<readonly T[] | null> {
    return arrayCodec(element);
  },

  fixedArray<T>(element: ValueCodec<T>, length: number): ValueCodec<readonly T[]> {
    if (!Number.isSafeInteger(length) || length < 0) {
      throw new RangeError("fixed array length must be a non-negative safe integer");
    }
    return scalarCodec<readonly T[]>(
      "array",
      (node, path) => {
        if (node.kind !== "array") throw typeError(path, "array");
        if (node.elements.length !== length) {
          throw diagnostic(path, `configstore: wrong array length at ${path}`);
        }
        return node.elements.map((item) => element.decodeNode(item, `${path}[]`));
      },
      (value, path) => {
        if (!Array.isArray(value)) throw descriptorError(path, "array codec does not match source");
        if (value.length !== length) {
          throw diagnostic(path, `configstore: wrong array length at ${path}`);
        }
        return {
          kind: "array",
          elements: value.map((item) => element.encodeNode(item, `${path}[]`)),
        };
      },
    );
  },

  /** String-keyed object map. JSON null remains distinct from an empty map. */
  record<T>(element: ValueCodec<T>): ValueCodec<Readonly<Record<string, T>> | null> {
    return recordCodec(element);
  },

  nullable<T>(element: ValueCodec<T>): ValueCodec<T | null> {
    return scalarCodec<T | null>(
      "nullable",
      (node, path) => (node.kind === "null" ? null : element.decodeNode(node, path)),
      (value, path) => (value === null ? { kind: "null" } : element.encodeNode(value, path)),
    );
  },
});

/** Strictly decode one complete group. No caller-owned object is mutated. */
export function decodeGroup<T extends object>(document: string, descriptor: GroupCodec<T>): T {
  let root: JsonNode;
  try {
    root = parseStrictJson(document);
  } catch (error) {
    if (error instanceof InvalidJsonDocumentError) {
      throw diagnostic("$", "configstore: invalid JSON document", error);
    }
    throw error;
  }
  return descriptor.decodeNode(root, "$");
}

/** Encode every described group property exactly once in descriptor order. */
export function encodeGroup<T extends object>(value: T, descriptor: GroupCodec<T>): string {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new TypeError("configstore: EncodeGroup source must be a non-null object");
  }
  return stringifyJsonNode(descriptor.encodeNode(value, "$"));
}

function scalarCodec<T>(
  kind: CodecKind,
  decodeNode: ValueCodec<T>["decodeNode"],
  encodeNode: ValueCodec<T>["encodeNode"],
): ValueCodec<T> {
  return Object.freeze({ kind, decodeNode, encodeNode });
}

function objectCodec<T extends object>(fields: readonly FieldCodec<T>[]): GroupCodec<T> {
  const copiedFields = Object.freeze([...fields]);
  const codec: GroupCodec<T> = {
    kind: "object",
    fields: copiedFields,
    decodeNode: (node, path) => decodeObject(node, copiedFields, path),
    encodeNode: (value, path) => encodeObject(value, copiedFields, path),
  };
  return Object.freeze(codec);
}

function decodeObject<T extends object>(
  node: JsonNode,
  fields: readonly FieldCodec<T>[],
  path: string,
): T {
  if (node.kind !== "object") throw typeError(path, "object");
  const { byName } = validateFields(fields, path);
  const values = new Map<string, JsonNode>();
  for (const property of node.properties) {
    const descriptor = byName.get(property.name);
    if (!descriptor) throw diagnostic(path, `configstore: unknown field at ${path}`);
    if (values.has(property.name)) {
      const duplicatePath = childPath(path, descriptor.jsonName);
      throw diagnostic(duplicatePath, `configstore: duplicate field at ${duplicatePath}`);
    }
    values.set(property.name, property.value);
  }
  for (const descriptor of fields) {
    if (!values.has(descriptor.jsonName)) {
      const missingPath = childPath(path, descriptor.jsonName);
      throw diagnostic(missingPath, `configstore: missing required field at ${missingPath}`);
    }
  }

  const result: Record<string, unknown> = Object.create(null) as Record<string, unknown>;
  for (const descriptor of fields) {
    const nodeValue = values.get(descriptor.jsonName);
    if (!nodeValue) throw descriptorError(path, "field lookup failed");
    result[descriptor.property] = descriptor.value.decodeNode(
      nodeValue,
      childPath(path, descriptor.jsonName),
    );
  }
  return result as T;
}

function encodeObject<T extends object>(
  source: T,
  fields: readonly FieldCodec<T>[],
  path: string,
): JsonNode {
  if (typeof source !== "object" || source === null || Array.isArray(source)) {
    throw descriptorError(path, "object codec does not match source");
  }
  validateFields(fields, path);
  return {
    kind: "object",
    properties: fields.map((descriptor) => {
      const propertyDescriptor = Object.getOwnPropertyDescriptor(source, descriptor.property);
      if (!propertyDescriptor) {
        throw descriptorError(path, "source property is missing");
      }
      if (!("value" in propertyDescriptor)) {
        throw descriptorError(path, "source property is an accessor");
      }
      return {
        name: descriptor.jsonName,
        value: descriptor.value.encodeNode(
          propertyDescriptor.value,
          childPath(path, descriptor.jsonName),
        ),
      };
    }),
  };
}

function validateFields<T extends object>(
  fields: readonly FieldCodec<T>[],
  path: string,
): { readonly byName: ReadonlyMap<string, FieldCodec<T>> } {
  if (!Array.isArray(fields)) throw descriptorError(path, "field descriptor list is invalid");
  const byName = new Map<string, FieldCodec<T>>();
  const properties = new Set<string>();
  for (const descriptor of fields) {
    if (
      !descriptor ||
      !validDiagnosticSegment(descriptor.jsonName) ||
      typeof descriptor.property !== "string" ||
      descriptor.property.length === 0 ||
      !descriptor.value ||
      typeof descriptor.value.decodeNode !== "function" ||
      typeof descriptor.value.encodeNode !== "function"
    ) {
      throw descriptorError(path, "field descriptor is incomplete");
    }
    if (byName.has(descriptor.jsonName) || properties.has(descriptor.property)) {
      throw descriptorError(path, "field descriptor is duplicated");
    }
    byName.set(descriptor.jsonName, descriptor);
    properties.add(descriptor.property);
  }
  return { byName };
}

function arrayCodec<T>(element: ValueCodec<T>): ValueCodec<readonly T[] | null> {
  assertElementCodec(element);
  return scalarCodec<readonly T[] | null>(
    "array",
    (node, path) => {
      if (node.kind === "null") return null;
      if (node.kind !== "array") throw typeError(path, "array");
      return node.elements.map((item) => element.decodeNode(item, `${path}[]`));
    },
    (value, path) => {
      if (value === null) return { kind: "null" };
      if (!Array.isArray(value)) throw descriptorError(path, "array codec does not match source");
      return {
        kind: "array",
        elements: value.map((item) => element.encodeNode(item, `${path}[]`)),
      };
    },
  );
}

function recordCodec<T>(element: ValueCodec<T>): ValueCodec<Readonly<Record<string, T>> | null> {
  assertElementCodec(element);
  return scalarCodec<Readonly<Record<string, T>> | null>(
    "record",
    (node, path) => {
      if (node.kind === "null") return null;
      if (node.kind !== "object") throw typeError(path, "object");
      const result: Record<string, T> = Object.create(null) as Record<string, T>;
      for (const property of node.properties) {
        if (Object.hasOwn(result, property.name)) {
          throw diagnostic(path, `configstore: duplicate map key at ${path}`);
        }
        result[property.name] = element.decodeNode(property.value, `${path}[*]`);
      }
      return result;
    },
    (value, path) => {
      if (value === null) return { kind: "null" };
      if (typeof value !== "object" || Array.isArray(value)) {
        throw descriptorError(path, "map codec does not match source");
      }
      return {
        kind: "object",
        properties: Object.keys(value).map((name) => ({
          name,
          value: element.encodeNode(value[name] as T, `${path}[*]`),
        })),
      };
    },
  );
}

function assertElementCodec<T>(codec: ValueCodec<T>): void {
  if (!codec || typeof codec.decodeNode !== "function" || typeof codec.encodeNode !== "function") {
    throw new TypeError("element codec is required");
  }
}

function validateBits(bits: number, maximum: number, name: string): void {
  if (!Number.isInteger(bits) || bits < 1 || bits > maximum) {
    throw new RangeError(`${name} bits must be an integer from 1 through ${maximum}`);
  }
}

interface DecimalParts {
  readonly negative: boolean;
  readonly digits: string;
  readonly exponent10: bigint;
}

function decimalParts(lexeme: string): DecimalParts | undefined {
  const match = /^(-)?(\d+)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/u.exec(lexeme);
  if (!match) return undefined;
  const integer = match[2];
  if (!integer) return undefined;
  const fraction = match[3] ?? "";
  const exponent = BigInt(match[4] ?? "0");
  const rawDigits = `${integer}${fraction}`;
  const digits = rawDigits.replace(/^0+/u, "") || "0";
  return {
    negative: match[1] === "-",
    digits,
    exponent10: exponent - BigInt(fraction.length),
  };
}

function exactInteger(lexeme: string): bigint | undefined {
  const parts = decimalParts(lexeme);
  if (!parts) return undefined;
  if (parts.digits === "0") return 0n;
  let digits = parts.digits;
  if (parts.exponent10 < 0n) {
    const removed = -parts.exponent10;
    if (removed > BigInt(digits.length)) return undefined;
    const count = Number(removed);
    if (!digits.endsWith("0".repeat(count))) return undefined;
    digits = digits.slice(0, digits.length - count) || "0";
  } else if (parts.exponent10 > 0n) {
    // Managed integer codecs are at most 64 bits. Avoid allocating for hostile
    // exponents that are guaranteed to fail their range check.
    if (parts.exponent10 > 128n) return undefined;
    digits += "0".repeat(Number(parts.exponent10));
  }
  const value = BigInt(digits);
  return parts.negative ? -value : value;
}

function compareDecimalMagnitude(left: string, right: string): number {
  const a = decimalParts(left);
  const b = decimalParts(right);
  if (!a || !b) return 1;
  if (a.digits === "0") return b.digits === "0" ? 0 : -1;
  if (b.digits === "0") return 1;
  const aOrder = BigInt(a.digits.length) + a.exponent10;
  const bOrder = BigInt(b.digits.length) + b.exponent10;
  if (aOrder !== bOrder) return aOrder < bOrder ? -1 : 1;
  const width = Math.max(a.digits.length, b.digits.length);
  const aDigits = a.digits.padEnd(width, "0");
  const bDigits = b.digits.padEnd(width, "0");
  return aDigits === bDigits ? 0 : aDigits < bDigits ? -1 : 1;
}

const DURATION_UNITS: Readonly<Record<string, bigint>> = Object.freeze({
  ns: 1n,
  us: 1_000n,
  µs: 1_000n,
  μs: 1_000n,
  ms: 1_000_000n,
  s: 1_000_000_000n,
  m: 60_000_000_000n,
  h: 3_600_000_000_000n,
});
const INT64_MIN = -(1n << 63n);
const INT64_MAX = (1n << 63n) - 1n;

function parseDuration(text: string): bigint | undefined {
  if (text === "0") return 0n;
  const sign = text.startsWith("-") ? -1n : 1n;
  let remaining = text.startsWith("-") || text.startsWith("+") ? text.slice(1) : text;
  if (!remaining) return undefined;
  let total = 0n;
  let consumed = false;
  while (remaining) {
    const match = /^(?:(\d+)(?:\.(\d*))?|\.(\d+))(ns|us|µs|μs|ms|s|m|h)/u.exec(remaining);
    if (!match) return undefined;
    const whole = match[1] ?? "0";
    const fraction = match[2] ?? match[3] ?? "";
    const unitName = match[4];
    if (!unitName) return undefined;
    const unit = DURATION_UNITS[unitName];
    if (unit === undefined) return undefined;
    let component = BigInt(whole) * unit;
    if (fraction) {
      const numerator = BigInt(fraction) * unit;
      component += numerator / 10n ** BigInt(fraction.length);
    }
    total += component;
    consumed = true;
    remaining = remaining.slice(match[0].length);
    if (total > 1n << 63n) return undefined;
  }
  if (!consumed) return undefined;
  total *= sign;
  return total < INT64_MIN || total > INT64_MAX ? undefined : total;
}

function formatDuration(value: bigint): string {
  if (value === 0n) return "0s";
  const sign = value < 0n ? "-" : "";
  let magnitude = value < 0n ? -value : value;
  if (magnitude < 1_000n) return `${sign}${magnitude}ns`;
  if (magnitude < 1_000_000n) return `${sign}${formatFraction(magnitude, 1_000n, "µs", 3)}`;
  if (magnitude < 1_000_000_000n) {
    return `${sign}${formatFraction(magnitude, 1_000_000n, "ms", 6)}`;
  }

  const hours = magnitude / 3_600_000_000_000n;
  magnitude %= 3_600_000_000_000n;
  const minutes = magnitude / 60_000_000_000n;
  magnitude %= 60_000_000_000n;
  const seconds = magnitude / 1_000_000_000n;
  const nanoseconds = magnitude % 1_000_000_000n;
  let result = sign;
  if (hours) result += `${hours}h`;
  if (minutes || hours) result += `${minutes}m`;
  result += seconds.toString();
  if (nanoseconds) result += `.${nanoseconds.toString().padStart(9, "0").replace(/0+$/u, "")}`;
  return `${result}s`;
}

function formatFraction(
  nanoseconds: bigint,
  unit: bigint,
  suffix: string,
  precision: number,
): string {
  const whole = nanoseconds / unit;
  const remainder = nanoseconds % unit;
  if (!remainder) return `${whole}${suffix}`;
  const fraction = remainder.toString().padStart(precision, "0").replace(/0+$/u, "");
  return `${whole}.${fraction}${suffix}`;
}

function isCanonicalBase64(value: string): boolean {
  if (value === "") return true;
  if (value.length % 4 !== 0 || !/^[A-Za-z0-9+/]*={0,2}$/u.test(value)) return false;
  const firstPadding = value.indexOf("=");
  if (firstPadding >= 0 && firstPadding < value.length - 2) return false;
  try {
    return Buffer.from(value, "base64").toString("base64") === value;
  } catch {
    return false;
  }
}

function childPath(parent: string, child: string): string {
  return parent === "$" ? `$.${child}` : `${parent}.${child}`;
}

function validDiagnosticSegment(segment: string): boolean {
  return /^[A-Za-z][A-Za-z0-9_-]{0,63}$/u.test(segment);
}

function diagnostic(path: string, message: string, cause?: unknown): ConfigDecodeError {
  return new ConfigDecodeError(path, message, cause === undefined ? undefined : { cause });
}

function typeError(path: string, expected: string): ConfigDecodeError {
  return diagnostic(path, `configstore: expected ${expected} at ${path}`);
}

function rangeError(path: string, expected: string): ConfigDecodeError {
  return diagnostic(path, `configstore: ${expected} out of range at ${path}`);
}

function descriptorError(path: string, problem: string): ConfigDecodeError {
  return diagnostic(path, `configstore: invalid codec descriptor at ${path} (${problem})`);
}
