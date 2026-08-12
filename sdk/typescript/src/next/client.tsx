"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { PublicConfigWire, PublicJsonObject, PublicJsonValue } from "../publishing.js";

const UINT64_MAX = (1n << 64n) - 1n;
const CANONICAL_DECIMAL_REVISION = /^(?:0|[1-9][0-9]*)$/;
const FORBIDDEN_PUBLIC_KEYS = new Set(["__proto__", "constructor", "prototype"]);

export interface UsePublicConfigOptions<TConfig extends PublicJsonObject> {
  /** Route created by createPublicConfigGET. */
  readonly endpoint?: string;
  /** Primarily for tests or instrumented fetch implementations. */
  readonly fetcher?: typeof globalThis.fetch;
  /** Optional application-level check in addition to strict public-JSON checks. */
  readonly validateConfig?: (config: unknown) => config is TConfig;
  readonly refreshOnMount?: boolean;
  readonly refreshOnFocus?: boolean;
  /**
   * Application-owned navigation identity (for example a pathname). When it
   * changes after mount, the hook refreshes without importing Next routing.
   */
  readonly navigationKey?: unknown;
  readonly refreshOnNavigation?: boolean;
}

export interface UsePublicConfigResult<TConfig extends PublicJsonObject> {
  readonly config: Readonly<TConfig>;
  readonly revision: bigint;
  readonly isRefreshing: boolean;
  readonly error: Error | null;

  /** Refreshes from the public endpoint while retaining the last-known-good value. */
  readonly refresh: () => Promise<void>;

  /**
   * Applies a structured server validation result. A policy_changed result is
   * checked and installed without a reload; other valid result kinds return false.
   */
  readonly applyServerResult: (result: unknown) => boolean;
}

interface ClientPublicConfig<TConfig extends PublicJsonObject> {
  readonly revision: bigint;
  readonly config: Readonly<TConfig>;
}

/**
 * Keeps a browser-safe public projection fresh. The hook never talks to KMS;
 * it only consumes the ordinary HTTP/public server-action contracts.
 */
export function usePublicConfig<TConfig extends PublicJsonObject>(
  initial: PublicConfigWire<TConfig>,
  options: UsePublicConfigOptions<TConfig> = {},
): UsePublicConfigResult<TConfig> {
  const {
    endpoint = "/api/kms/public-config",
    fetcher,
    validateConfig,
    refreshOnMount = true,
    refreshOnFocus = true,
    navigationKey,
    refreshOnNavigation = true,
  } = options;

  const normalizedInitial = useMemo(
    () => normalizeWireConfig<TConfig>(initial, validateConfig),
    [initial, validateConfig],
  );
  const [policy, setPolicy] = useState<ClientPublicConfig<TConfig>>(normalizedInitial);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const policyRef = useRef(policy);
  const requestSequenceRef = useRef(0);
  const requestControllerRef = useRef<AbortController | undefined>(undefined);
  const mountedRef = useRef(false);
  const previousNavigationKeyRef = useRef(navigationKey);

  const installIfNewer = useCallback((candidate: ClientPublicConfig<TConfig>): boolean => {
    if (candidate.revision <= policyRef.current.revision) {
      return false;
    }
    policyRef.current = candidate;
    setPolicy(candidate);
    return true;
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      requestSequenceRef.current += 1;
      requestControllerRef.current?.abort();
      requestControllerRef.current = undefined;
    };
  }, []);

  useEffect(() => {
    installIfNewer(normalizedInitial);
  }, [installIfNewer, normalizedInitial]);

  const refresh = useCallback(async (): Promise<void> => {
    const sequence = requestSequenceRef.current + 1;
    requestSequenceRef.current = sequence;
    requestControllerRef.current?.abort();
    const controller = new AbortController();
    requestControllerRef.current = controller;
    if (mountedRef.current) {
      setIsRefreshing(true);
    }

    try {
      const activeFetch = fetcher ?? globalThis.fetch;
      if (typeof activeFetch !== "function") {
        throw new Error("public config refresh requires fetch");
      }
      const response = await activeFetch(endpoint, {
        method: "GET",
        headers: {
          Accept: "application/json",
          "If-None-Match": formatClientEtag(policyRef.current.revision),
        },
        credentials: "same-origin",
        cache: "no-store",
        signal: controller.signal,
      });

      if (sequence !== requestSequenceRef.current || !mountedRef.current) {
        return;
      }
      if (response.status === 304) {
        setError(null);
        return;
      }
      if (!response.ok) {
        throw new Error(`public config refresh failed with status ${response.status}`);
      }

      const body: unknown = await response.json();
      if (sequence !== requestSequenceRef.current || !mountedRef.current) {
        return;
      }
      const candidate = normalizeWireConfig<TConfig>(body, validateConfig);
      installIfNewer(candidate);
      setError(null);
    } catch (refreshError) {
      if (sequence !== requestSequenceRef.current || !mountedRef.current) {
        return;
      }
      setError(asError(refreshError));
    } finally {
      if (sequence === requestSequenceRef.current && mountedRef.current) {
        requestControllerRef.current = undefined;
        setIsRefreshing(false);
      }
    }
  }, [endpoint, fetcher, installIfNewer, validateConfig]);

  useEffect(() => {
    if (refreshOnMount) {
      void refresh();
    }
  }, [refresh, refreshOnMount]);

  useEffect(() => {
    if (!refreshOnFocus) {
      return;
    }
    const onFocus = (): void => {
      void refresh();
    };
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [refresh, refreshOnFocus]);

  useEffect(() => {
    const previous = previousNavigationKeyRef.current;
    previousNavigationKeyRef.current = navigationKey;
    if (refreshOnNavigation && !Object.is(previous, navigationKey)) {
      void refresh();
    }
  }, [navigationKey, refresh, refreshOnNavigation]);

  const applyServerResult = useCallback(
    (result: unknown): boolean => {
      let policyChanged: ClientPublicConfig<TConfig> | undefined;
      try {
        policyChanged = readPolicyChangedCurrent<TConfig>(result, validateConfig);
      } catch (serverResultError) {
        if (mountedRef.current) {
          setError(asError(serverResultError));
        }
        return false;
      }
      if (policyChanged === undefined) {
        return false;
      }

      if (!mountedRef.current || !installIfNewer(policyChanged)) {
        return false;
      }

      requestSequenceRef.current += 1;
      requestControllerRef.current?.abort();
      requestControllerRef.current = undefined;
      setIsRefreshing(false);
      setError(null);
      return true;
    },
    [installIfNewer, validateConfig],
  );

  return {
    config: policy.config,
    revision: policy.revision,
    isRefreshing,
    error,
    refresh,
    applyServerResult,
  };
}

function normalizeWireConfig<TConfig extends PublicJsonObject>(
  value: unknown,
  validateConfig?: (config: unknown) => config is TConfig,
): ClientPublicConfig<TConfig> {
  const object = requirePlainObject(value, "public config");
  const keys = readDataKeys(object, "public config");
  if (keys.length !== 2 || !keys.includes("revision") || !keys.includes("config")) {
    throw new TypeError("public config must contain exactly revision and config");
  }

  const revision = parseClientRevision(readDataProperty(object, "revision", "revision"));
  const config = clonePublicJson(readDataProperty(object, "config", "config"), new WeakSet());
  if (!isPublicJsonObject(config)) {
    throw new TypeError("public config config must be a JSON object");
  }
  if (validateConfig !== undefined && !validateConfig(config)) {
    throw new TypeError("public config failed application validation");
  }
  return Object.freeze({ revision, config }) as ClientPublicConfig<TConfig>;
}

function readPolicyChangedCurrent<TConfig extends PublicJsonObject>(
  result: unknown,
  validateConfig?: (config: unknown) => config is TConfig,
): ClientPublicConfig<TConfig> | undefined {
  if (!isPlainObject(result)) {
    return undefined;
  }
  const statusDescriptor = Object.getOwnPropertyDescriptor(result, "status");
  if (
    statusDescriptor === undefined ||
    !("value" in statusDescriptor) ||
    statusDescriptor.value !== "policy_changed"
  ) {
    return undefined;
  }

  const keys = readDataKeys(result, "policy_changed result");
  if (keys.length !== 2 || !keys.includes("status") || !keys.includes("current")) {
    throw new TypeError("policy_changed result must contain exactly status and current");
  }
  return normalizeWireConfig<TConfig>(
    readDataProperty(result, "current", "policy_changed current"),
    validateConfig,
  );
}

function parseClientRevision(value: unknown): bigint {
  if (typeof value !== "string" || !CANONICAL_DECIMAL_REVISION.test(value)) {
    throw new TypeError("public config revision is not a canonical decimal string");
  }
  const revision = BigInt(value);
  if (revision > UINT64_MAX) {
    throw new RangeError("public config revision is outside the uint64 range");
  }
  return revision;
}

function formatClientEtag(revision: bigint): string {
  return `"kms-public-config-${revision.toString(10)}"`;
}

function clonePublicJson(value: unknown, ancestors: WeakSet<object>): PublicJsonValue {
  if (value === null || typeof value === "string" || typeof value === "boolean") {
    return value;
  }
  if (typeof value === "number") {
    if (!Number.isFinite(value)) {
      throw new TypeError("public config contains a non-finite number");
    }
    return value;
  }
  if (typeof value !== "object") {
    throw new TypeError("public config contains a non-JSON value");
  }
  if (ancestors.has(value)) {
    throw new TypeError("public config contains a cycle");
  }

  ancestors.add(value);
  try {
    if (Array.isArray(value)) {
      return clonePublicArray(value, ancestors);
    }
    const object = requirePlainObject(value, "public config value");
    const clone: Record<string, PublicJsonValue> = {};
    for (const key of readDataKeys(object, "public config value")) {
      Object.defineProperty(clone, key, {
        value: clonePublicJson(readDataProperty(object, key, key), ancestors),
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

function clonePublicArray(
  value: readonly unknown[],
  ancestors: WeakSet<object>,
): readonly PublicJsonValue[] {
  for (const key of Reflect.ownKeys(value)) {
    if (key === "length") {
      continue;
    }
    if (typeof key !== "string" || !/^(?:0|[1-9][0-9]*)$/.test(key)) {
      throw new TypeError("public config array contains a non-index property");
    }
    const index = Number(key);
    if (!Number.isSafeInteger(index) || index < 0 || index >= value.length) {
      throw new TypeError("public config array contains an invalid index");
    }
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (descriptor === undefined || !("value" in descriptor) || !descriptor.enumerable) {
      throw new TypeError("public config array indices must be enumerable data properties");
    }
  }

  const clone: PublicJsonValue[] = [];
  for (let index = 0; index < value.length; index += 1) {
    if (!Object.hasOwn(value, index)) {
      throw new TypeError("public config contains a sparse array");
    }
    clone.push(clonePublicJson(value[index], ancestors));
  }
  return Object.freeze(clone);
}

function requirePlainObject(value: unknown, label: string): Record<string, unknown> {
  if (!isPlainObject(value)) {
    throw new TypeError(`${label} must be a plain object`);
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

function readDataKeys(object: object, label: string): string[] {
  const keys: string[] = [];
  for (const key of Reflect.ownKeys(object)) {
    if (typeof key !== "string") {
      throw new TypeError(`${label} contains a symbol property`);
    }
    if (FORBIDDEN_PUBLIC_KEYS.has(key)) {
      throw new TypeError(`${label} contains a forbidden property`);
    }
    const descriptor = Object.getOwnPropertyDescriptor(object, key);
    if (descriptor === undefined || !("value" in descriptor) || !descriptor.enumerable) {
      throw new TypeError(`${label} properties must be enumerable data properties`);
    }
    keys.push(key);
  }
  return keys;
}

function readDataProperty(object: object, key: string, label: string): unknown {
  const descriptor = Object.getOwnPropertyDescriptor(object, key);
  if (descriptor === undefined || !("value" in descriptor)) {
    throw new TypeError(`${label} must be a data property`);
  }
  return descriptor.value;
}

function asError(value: unknown): Error {
  return value instanceof Error ? value : new Error("public config refresh failed");
}
