import { ReleaseSecret } from "../releases/types.js";
import { Secret } from "../secret.js";

/**
 * Recursively clone supported managed-configuration values while preserving
 * cycles and shared references. Secret plaintext backing storage is never
 * shared with the source.
 */
export function cloneConfig<T>(value: T): T {
  return cloneValue(value, new Map<object, unknown>());
}

/** Return true when a supported value graph contains secret plaintext. */
export function containsSecret(value: unknown): boolean {
  return valueContainsSecret(value, new Set<object>());
}

function cloneValue<T>(value: T, seen: Map<object, unknown>): T {
  if (value === null || (typeof value !== "object" && typeof value !== "function")) {
    return value;
  }
  if (seen.has(value)) return seen.get(value) as T;
  if (value instanceof Secret || value instanceof ReleaseSecret) {
    const result = value.clone();
    seen.set(value, result);
    return result as T;
  }

  if (Buffer.isBuffer(value)) {
    const result = Buffer.from(value);
    seen.set(value, result);
    return result as T;
  }
  if (value instanceof Uint8Array) {
    const result = Uint8Array.from(value);
    seen.set(value, result);
    return result as T;
  }
  if (value instanceof Date) {
    const result = new Date(value.getTime());
    seen.set(value, result);
    return result as T;
  }
  if (value instanceof Map) {
    const result = new Map<unknown, unknown>();
    seen.set(value, result);
    for (const [key, item] of value) {
      result.set(cloneValue(key, seen), cloneValue(item, seen));
    }
    return result as T;
  }
  if (value instanceof Set) {
    const result = new Set<unknown>();
    seen.set(value, result);
    for (const item of value) result.add(cloneValue(item, seen));
    return result as T;
  }
  if (Array.isArray(value)) {
    const lengthDescriptor = Object.getOwnPropertyDescriptor(value, "length");
    if (!lengthDescriptor || !("value" in lengthDescriptor)) {
      throw new TypeError("configstore: array length is not a cloneable data property");
    }
    const result: unknown[] = new Array(lengthDescriptor.value as number);
    seen.set(value, result);
    cloneDataProperties(value, result, seen, true);
    Object.defineProperty(result, "length", lengthDescriptor);
    return result as T;
  }
  if (typeof value === "function") return value;

  const result = Object.create(Object.getPrototypeOf(value)) as object;
  seen.set(value, result);
  cloneDataProperties(value, result, seen, false);
  return result as T;
}

function cloneDataProperties(
  source: object,
  target: object,
  seen: Map<object, unknown>,
  skipArrayLength: boolean,
): void {
  for (const key of Reflect.ownKeys(source)) {
    if (skipArrayLength && key === "length") continue;
    const descriptor = Object.getOwnPropertyDescriptor(source, key);
    if (!descriptor) continue;
    if (!("value" in descriptor)) {
      throw new TypeError("configstore: accessor properties are not cloneable config values");
    }
    Object.defineProperty(target, key, {
      ...descriptor,
      value: cloneValue(descriptor.value, seen),
    });
  }
}

function valueContainsSecret(value: unknown, seen: Set<object>): boolean {
  if (value instanceof Secret || value instanceof ReleaseSecret) return true;
  if (value === null || (typeof value !== "object" && typeof value !== "function")) return false;
  if (seen.has(value)) return false;
  seen.add(value);

  if (value instanceof Map) {
    for (const [key, item] of value) {
      if (valueContainsSecret(key, seen) || valueContainsSecret(item, seen)) return true;
    }
    return false;
  }
  if (value instanceof Set) {
    for (const item of value) {
      if (valueContainsSecret(item, seen)) return true;
    }
    return false;
  }
  for (const key of Reflect.ownKeys(value)) {
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (descriptor && "value" in descriptor && valueContainsSecret(descriptor.value, seen)) {
      return true;
    }
  }
  return false;
}
