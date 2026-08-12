import "server-only";

import { constants as osConstants } from "node:os";

import {
  type AuthoritativeValidator,
  createPolicyPublisher,
  type DecimalRevision,
  formatPublicConfigEtag,
  normalizePublicConfigWire,
  type PolicyPublisher,
  type PolicyPublisherObserver,
  type PolicySnapshot,
  type PolicyValidationResult,
  type PublicConfigWire,
  type PublicJsonObject,
  type PublicJsonValue,
  type PublicProjection,
  type SnapshotReader,
} from "../publishing.js";

/** Re-export this from a Next Route module to make its runtime requirement explicit. */
export const runtime = "nodejs" as const;

export const MAX_PRIVATE_PUBLIC_CONFIG_AGE_SECONDS = 300;

type Awaitable<T> = PromiseLike<T> | T;

export interface NextKmsResource<TPolicy> {
  readonly source: SnapshotReader<TPolicy>;
  readonly close?: () => Awaitable<void>;
}

export interface CreateNextKmsOptions<
  TPolicy,
  TConfig extends PublicJsonObject,
  TInput,
  TValidationErrors extends PublicJsonValue,
> {
  /** Creates the Node SDK client, release watcher, and active snapshot source. */
  readonly initialize: (signal: AbortSignal) => Awaitable<NextKmsResource<TPolicy>>;
  readonly projection: PublicProjection<TPolicy, TConfig>;
  readonly validate: AuthoritativeValidator<TPolicy, TInput, TValidationErrors>;
  /** Observes redacted publisher events from reads and validation. */
  readonly onPublisherEvent?: PolicyPublisherObserver;
}

export type PublicConfigCachePolicy =
  | "no-store"
  | {
      /** Private browser caching only; shared/CDN caching is never emitted. */
      readonly privateMaxAgeSeconds: number;
    };

export interface PublicConfigRouteOptions {
  readonly cache?: PublicConfigCachePolicy;
  /** Receives frozen, value-free HTTP publication events. */
  readonly onEvent?: PublicConfigRouteObserver;
}

export type PublicConfigRouteEvent =
  | {
      readonly type: "public_config_served";
      readonly revision: DecimalRevision;
      readonly observedAtUnixMs: number;
      readonly durationMs: number;
    }
  | {
      readonly type: "public_config_not_modified";
      readonly revision: DecimalRevision;
      readonly observedAtUnixMs: number;
      readonly durationMs: number;
    }
  | {
      readonly type: "public_config_unavailable";
      readonly observedAtUnixMs: number;
      readonly durationMs: number;
    };

export type PublicConfigRouteObserver = (event: PublicConfigRouteEvent) => unknown;

export interface PublicConfigProvider<TConfig extends PublicJsonObject> {
  readPublicPolicy(): Awaitable<PublicConfigWire<TConfig> | undefined>;
}

export type PublicConfigGET = (request: Request) => Promise<Response>;

export interface ProcessShutdownOptions {
  readonly signals?: readonly NodeJS.Signals[];
  /** Receives lifecycle errors; the adapter never serializes or logs them. */
  readonly onError?: (error: unknown) => void;
  /**
   * Runs after the cleanup attempt. Applications that install signal hooks
   * must use this to enact their explicit termination policy.
   */
  readonly onCleanupComplete?: (signal: NodeJS.Signals) => Awaitable<void>;
}

export interface NextKms<
  TPolicy,
  TConfig extends PublicJsonObject,
  TInput,
  TValidationErrors extends PublicJsonValue,
> extends PublicConfigProvider<TConfig> {
  /** Concurrent calls share one initialization attempt. Failed attempts may retry. */
  start(): Promise<void>;

  /** Permanently closes the adapter. Concurrent calls share one cleanup. */
  close(): Promise<void>;

  /** Captures one raw active policy generation for a Server Component/Action. */
  readPolicy(): Promise<PolicySnapshot<TPolicy> | undefined>;

  /** Captures one active generation and returns only its public wire projection. */
  readPublicPolicy(): Promise<PublicConfigWire<TConfig> | undefined>;

  /** Performs authoritative validation against one active generation. */
  validateAtRevision(
    revision: unknown,
    input: TInput,
  ): Promise<PolicyValidationResult<TConfig, TValidationErrors>>;

  /** Creates a Next-compatible public configuration GET Route Handler. */
  createPublicConfigGET(options?: PublicConfigRouteOptions): PublicConfigGET;

  /** Installs cleanup-only SIGINT/SIGTERM hooks and returns an uninstaller. */
  installProcessShutdown(options?: ProcessShutdownOptions): () => void;
}

export class NextKmsClosedError extends Error {
  constructor() {
    super("Next KMS adapter is closed");
    this.name = "NextKmsClosedError";
  }
}

/**
 * Owns one process-local KMS lifecycle and exposes low-wiring server helpers.
 * Reads lazily initialize the resource; applications may call start eagerly.
 */
export function createNextKms<
  TPolicy,
  TConfig extends PublicJsonObject,
  TInput,
  TValidationErrors extends PublicJsonValue,
>(
  options: CreateNextKmsOptions<TPolicy, TConfig, TInput, TValidationErrors>,
): NextKms<TPolicy, TConfig, TInput, TValidationErrors> {
  assertNodeRuntime();
  if (typeof options?.initialize !== "function") {
    throw new TypeError("Next KMS initialize must be a function");
  }

  let resource: NextKmsResource<TPolicy> | undefined;
  let publisher: PolicyPublisher<TConfig, TInput, TValidationErrors> | undefined;
  let initializationController: AbortController | undefined;
  let startAttempt: Promise<void> | undefined;
  let closeAttempt: Promise<void> | undefined;
  let closed = false;
  const shutdownUninstallers = new Set<() => void>();

  const start = (): Promise<void> => {
    assertNodeRuntime();
    if (closed) {
      return Promise.reject(new NextKmsClosedError());
    }
    if (resource !== undefined && publisher !== undefined) {
      return Promise.resolve();
    }
    if (startAttempt !== undefined) {
      return startAttempt;
    }

    const controller = new AbortController();
    initializationController = controller;
    const attempt = Promise.resolve().then(async (): Promise<void> => {
      let initialized: NextKmsResource<TPolicy> | undefined;
      try {
        initialized = await options.initialize(controller.signal);
        assertResource(initialized);
        resource = initialized;
        publisher = createPolicyPublisher({
          source: initialized.source,
          projection: options.projection,
          validate: options.validate,
          ...(options.onPublisherEvent === undefined ? {} : { onEvent: options.onPublisherEvent }),
        });
        if (closed) {
          throw new NextKmsClosedError();
        }
      } catch (error) {
        if (initialized !== undefined && resource === initialized) {
          if (!closed) {
            resource = undefined;
            publisher = undefined;
            await initialized.close?.();
          }
        } else if (initialized !== undefined) {
          // Even a malformed resource is initializer-owned state. If its
          // source validation failed after construction, release any valid
          // cleanup hook instead of leaking a partially initialized client.
          await initialized.close?.();
        }
        throw error;
      } finally {
        if (initializationController === controller) {
          initializationController = undefined;
        }
      }
    });
    startAttempt = attempt;
    const clearAttempt = (): void => {
      if (startAttempt === attempt) {
        startAttempt = undefined;
      }
    };
    // Publish the in-flight promise before initialization can execute, then
    // clear it on either outcome. This preserves retryability even when an
    // initializer throws synchronously before its first await.
    void attempt.then(clearAttempt, clearAttempt);
    return attempt;
  };

  const close = (): Promise<void> => {
    if (closeAttempt !== undefined) {
      return closeAttempt;
    }
    closed = true;
    let resolveAttempt!: () => void;
    let rejectAttempt!: (error: unknown) => void;
    const attempt = new Promise<void>((resolve, reject) => {
      resolveAttempt = resolve;
      rejectAttempt = reject;
    });
    // Publish the close attempt before aborting initialization or removing
    // listeners. Either side effect can synchronously re-enter close().
    closeAttempt = attempt;
    initializationController?.abort();
    for (const uninstall of [...shutdownUninstallers]) {
      uninstall();
    }

    const finish = async (): Promise<void> => {
      try {
        await startAttempt;
      } catch {
        // Initialization errors do not replace a resource cleanup error.
      }

      const initialized = resource;
      resource = undefined;
      publisher = undefined;
      await initialized?.close?.();
    };
    void finish().then(resolveAttempt, rejectAttempt);
    return attempt;
  };

  const activeResource = async (): Promise<NextKmsResource<TPolicy>> => {
    await start();
    if (closed || resource === undefined) {
      throw new NextKmsClosedError();
    }
    return resource;
  };

  const activePublisher = async (): Promise<
    PolicyPublisher<TConfig, TInput, TValidationErrors>
  > => {
    await start();
    if (closed || publisher === undefined) {
      throw new NextKmsClosedError();
    }
    return publisher;
  };

  const adapter: NextKms<TPolicy, TConfig, TInput, TValidationErrors> = {
    start,
    close,

    async readPolicy(): Promise<PolicySnapshot<TPolicy> | undefined> {
      const initialized = await activeResource();
      return initialized.source.current();
    },

    async readPublicPolicy(): Promise<PublicConfigWire<TConfig> | undefined> {
      const active = await activePublisher();
      return active.readWire();
    },

    async validateAtRevision(
      revision: unknown,
      input: TInput,
    ): Promise<PolicyValidationResult<TConfig, TValidationErrors>> {
      const active = await activePublisher();
      return active.validate(revision, input);
    },

    createPublicConfigGET(routeOptions?: PublicConfigRouteOptions): PublicConfigGET {
      return createPublicConfigGET(adapter, routeOptions);
    },

    installProcessShutdown(shutdownOptions: ProcessShutdownOptions = {}): () => void {
      assertNodeRuntime();
      if (closed) {
        throw new NextKmsClosedError();
      }
      const signals = shutdownOptions.signals ?? ["SIGINT", "SIGTERM"];
      const uniqueSignals = [...new Set(signals)];
      for (const signal of uniqueSignals) assertInstallableSignal(signal);
      let installed = true;
      let shutdownStarted = false;

      const reportError = (error: unknown): void => {
        let result: unknown;
        try {
          result = shutdownOptions.onError?.(error);
        } catch {
          // A lifecycle observer must not interfere with cleanup.
          return;
        }
        // TypeScript permits an async function where a void-returning callback
        // is expected. Observe that escaped promise (and hostile thenables) so
        // an error observer cannot create an unhandled rejection during
        // shutdown. Completion remains deliberately independent of telemetry.
        void Promise.resolve(result).catch(() => undefined);
      };

      const handlers = uniqueSignals.map((signal) => {
        const handler = (): void => {
          if (shutdownStarted) return;
          shutdownStarted = true;
          uninstall();
          void close()
            .catch(reportError)
            .then(async () => {
              try {
                await shutdownOptions.onCleanupComplete?.(signal);
              } catch (error) {
                reportError(error);
              }
            });
        };
        return [signal, handler] as const;
      });

      const uninstall = (): void => {
        if (!installed) {
          return;
        }
        installed = false;
        for (const [signal, handler] of handlers) {
          process.removeListener(signal, handler);
        }
        shutdownUninstallers.delete(uninstall);
      };
      try {
        for (const [signal, handler] of handlers) process.once(signal, handler);
      } catch (error) {
        uninstall();
        throw error;
      }
      shutdownUninstallers.add(uninstall);
      return uninstall;
    },
  };

  return Object.freeze(adapter);
}

const INSTALLABLE_SIGNALS = new Set(
  Object.keys(osConstants.signals).filter((signal) => signal !== "SIGKILL" && signal !== "SIGSTOP"),
);

function assertInstallableSignal(signal: unknown): asserts signal is NodeJS.Signals {
  if (typeof signal !== "string" || !INSTALLABLE_SIGNALS.has(signal)) {
    throw new TypeError(`process signal ${JSON.stringify(signal)} cannot install a cleanup hook`);
  }
}

/** Creates a Next Route Handler from an adapter or another safe provider. */
export function createPublicConfigGET<TConfig extends PublicJsonObject>(
  provider:
    | PublicConfigProvider<TConfig>
    | (() => Awaitable<PublicConfigWire<TConfig> | undefined>),
  options: PublicConfigRouteOptions = {},
): PublicConfigGET {
  assertNodeRuntime();
  const read =
    typeof provider === "function"
      ? provider
      : provider !== null && typeof provider.readPublicPolicy === "function"
        ? () => provider.readPublicPolicy()
        : undefined;
  if (read === undefined) {
    throw new TypeError("public config provider must be a function or adapter");
  }
  const cacheControl = formatCacheControl(options.cache ?? "no-store");
  if (options.onEvent !== undefined && typeof options.onEvent !== "function") {
    throw new TypeError("public config route onEvent must be a function");
  }
  const observe = createSafeRouteObserver(options.onEvent);

  return async (request: Request): Promise<Response> => {
    const startedAtUnixMs = Date.now();
    try {
      const candidate = await read();
      if (candidate === undefined) {
        observeRouteEvent(observe, startedAtUnixMs, { type: "public_config_unavailable" });
        return unavailableResponse();
      }

      const current = normalizePublicConfigWire<TConfig>(candidate);
      const etag = formatPublicConfigEtag(current.revision);
      const commonHeaders = new Headers({
        "Cache-Control": cacheControl,
        ETag: etag,
        "X-Content-Type-Options": "nosniff",
      });

      if (ifNoneMatchMatches(request.headers.get("If-None-Match"), etag)) {
        observeRouteEvent(observe, startedAtUnixMs, {
          type: "public_config_not_modified",
          revision: current.revision,
        });
        return new Response(null, {
          status: 304,
          headers: commonHeaders,
        });
      }

      commonHeaders.set("Content-Type", "application/json; charset=utf-8");
      observeRouteEvent(observe, startedAtUnixMs, {
        type: "public_config_served",
        revision: current.revision,
      });
      return new Response(JSON.stringify(current), {
        status: 200,
        headers: commonHeaders,
      });
    } catch {
      observeRouteEvent(observe, startedAtUnixMs, { type: "public_config_unavailable" });
      return unavailableResponse();
    }
  };
}

type UnstampedRouteEvent =
  | { readonly type: "public_config_served"; readonly revision: DecimalRevision }
  | { readonly type: "public_config_not_modified"; readonly revision: DecimalRevision }
  | { readonly type: "public_config_unavailable" };

function observeRouteEvent(
  observer: (event: PublicConfigRouteEvent) => void,
  startedAtUnixMs: number,
  event: UnstampedRouteEvent,
): void {
  const observedAtUnixMs = Date.now();
  observer(
    Object.freeze({
      ...event,
      observedAtUnixMs,
      durationMs: Math.max(0, observedAtUnixMs - startedAtUnixMs),
    }),
  );
}

function createSafeRouteObserver(
  observer: PublicConfigRouteObserver | undefined,
): (event: PublicConfigRouteEvent) => void {
  return (event): void => {
    if (observer === undefined) return;
    try {
      Promise.resolve(observer(event)).catch(() => undefined);
    } catch {
      // Telemetry must never change the HTTP publication contract.
    }
  };
}

function assertNodeRuntime(): void {
  const globalWithEdgeRuntime = globalThis as typeof globalThis & {
    readonly EdgeRuntime?: unknown;
  };
  if (
    typeof process === "undefined" ||
    process.versions?.node === undefined ||
    process.env.NEXT_RUNTIME === "edge" ||
    globalWithEdgeRuntime.EdgeRuntime !== undefined
  ) {
    throw new Error("@suhaibinator/kms/next/server requires the Next.js Node runtime");
  }
}

function assertResource<TPolicy>(
  resource: NextKmsResource<TPolicy>,
): asserts resource is NextKmsResource<TPolicy> {
  if (
    resource === null ||
    typeof resource !== "object" ||
    resource.source === null ||
    typeof resource.source !== "object" ||
    typeof resource.source.current !== "function" ||
    (resource.close !== undefined && typeof resource.close !== "function")
  ) {
    throw new TypeError("Next KMS initialize returned an invalid resource");
  }
}

function formatCacheControl(cache: PublicConfigCachePolicy): string {
  if (cache === "no-store") {
    return "no-store";
  }
  if (cache === null || typeof cache !== "object") {
    throw new TypeError("public config cache policy is invalid");
  }
  const seconds = cache.privateMaxAgeSeconds;
  if (
    !Number.isInteger(seconds) ||
    seconds < 0 ||
    seconds > MAX_PRIVATE_PUBLIC_CONFIG_AGE_SECONDS
  ) {
    throw new RangeError(
      `private public config max age must be between 0 and ${MAX_PRIVATE_PUBLIC_CONFIG_AGE_SECONDS} seconds`,
    );
  }
  return `private, max-age=${seconds}, must-revalidate`;
}

function unavailableResponse(): Response {
  return new Response('{"status":"unavailable"}', {
    status: 503,
    headers: {
      "Cache-Control": "no-store",
      "Content-Type": "application/json; charset=utf-8",
      "Retry-After": "1",
      "X-Content-Type-Options": "nosniff",
    },
  });
}

/** GET If-None-Match uses weak comparison, including for a strong current ETag. */
function ifNoneMatchMatches(header: string | null, current: string): boolean {
  if (header === null) {
    return false;
  }
  const target = stripWeakPrefix(current);
  let offset = 0;

  while (offset < header.length) {
    while (offset < header.length && /[\t ,]/.test(header[offset] ?? "")) {
      offset += 1;
    }
    if (offset >= header.length) {
      break;
    }
    if (header[offset] === "*") {
      const wildcardEnd = offset + 1;
      let next = wildcardEnd;
      while (next < header.length && /[\t ]/.test(header[next] ?? "")) {
        next += 1;
      }
      if (next === header.length || header[next] === ",") {
        return true;
      }
      offset = skipToNextListMember(header, wildcardEnd);
      continue;
    }

    const tokenStart = offset;
    if (header.slice(offset, offset + 2) === "W/") {
      offset += 2;
    }
    if (header[offset] !== '"') {
      offset = skipToNextListMember(header, offset);
      continue;
    }
    offset += 1;
    while (offset < header.length && header[offset] !== '"') {
      offset += 1;
    }
    if (offset >= header.length) {
      return false;
    }
    offset += 1;
    const tokenEnd = offset;
    while (offset < header.length && /[\t ]/.test(header[offset] ?? "")) {
      offset += 1;
    }
    if (offset < header.length && header[offset] !== ",") {
      offset = skipToNextListMember(header, offset);
      continue;
    }

    const token = header.slice(tokenStart, tokenEnd);
    if (stripWeakPrefix(token) === target) {
      return true;
    }
    if (header[offset] === ",") {
      offset += 1;
    }
  }
  return false;
}

function skipToNextListMember(header: string, offset: number): number {
  const comma = header.indexOf(",", offset);
  return comma === -1 ? header.length : comma + 1;
}

function stripWeakPrefix(etag: string): string {
  return etag.startsWith("W/") ? etag.slice(2) : etag;
}

// Keep this type reachable from generated declarations without a runtime import.
export type { DecimalRevision };
