import { type ServiceError, status } from "@grpc/grpc-js";

/** Stable, programmatic error codes exposed by the SDK. */
export type KmsErrorCode =
  | "not_found"
  | "permission_denied"
  | "unauthenticated"
  | "failed_precondition"
  | "not_initialized"
  | "no_namespace"
  | "invalid_argument"
  | "cancelled"
  | "deadline_exceeded"
  | "already_exists"
  | "resource_exhausted"
  | "aborted"
  | "out_of_range"
  | "unimplemented"
  | "internal"
  | "unavailable"
  | "data_loss"
  | "purge_cleanup_pending"
  | "unknown";

/** Fixed wire-safe text for a purge that committed before artifact cleanup failed. */
export const PURGE_CLEANUP_PENDING_MESSAGE =
  "secret purge committed; database artifact cleanup is pending";

export interface KmsErrorOptions extends ErrorOptions {
  /** The original gRPC status code, when the error came from an RPC. */
  grpcCode?: status;
}

/**
 * The base error returned for failures normalized by the SDK.
 *
 * `code` is stable across transports. `grpcCode` retains the wire status when
 * one exists. Messages may contain server-supplied, non-secret diagnostics;
 * SDK call sites must never interpolate secret plaintext or access tokens.
 */
export class KmsError extends Error {
  readonly code: KmsErrorCode;
  readonly grpcCode: status | undefined;

  constructor(code: KmsErrorCode, message: string, options: KmsErrorOptions = {}) {
    super(message, options);
    this.name = "KmsError";
    this.code = code;
    this.grpcCode = options.grpcCode;
  }
}

/** An SDK configuration error rather than a remote service failure. */
export class ConfigError extends KmsError {
  constructor(message: string, options: ErrorOptions = {}) {
    super("invalid_argument", message, options);
    this.name = "ConfigError";
  }
}

/** A relative key could not be resolved because the client is unbound. */
export class NoNamespaceError extends KmsError {
  constructor(key: string, options: ErrorOptions = {}) {
    super(
      "no_namespace",
      `relative key ${JSON.stringify(key)} needs a namespace ` +
        "(configure a namespace, bind the identity, or use /env/app/key)",
      options,
    );
    this.name = "NoNamespaceError";
  }
}

/**
 * The server exhausted a per-identity budget for the requested operation
 * (for example `verifyReleaseDefaults`). Wait for the window to reset instead
 * of retrying in a tight loop. `code` is `resource_exhausted`.
 */
export class RateLimitedError extends KmsError {
  constructor(message: string, options: KmsErrorOptions = {}) {
    super("resource_exhausted", message, options);
    this.name = "RateLimitedError";
  }
}

/**
 * A secret purge committed, but live SQLite artifacts still require cleanup.
 * No mutation result accompanies this error. Do not retry the purge: its
 * exact preview guard is now stale, and a retired binding key may already
 * have been discarded.
 */
export class PurgeCleanupPendingError extends KmsError {
  constructor() {
    super("purge_cleanup_pending", PURGE_CLEANUP_PENDING_MESSAGE, {
      grpcCode: status.UNAVAILABLE,
    });
    this.name = "PurgeCleanupPendingError";
  }
}

/** A declarative secret was accessed before it had been resolved. */
export class NotInitializedError extends KmsError {
  constructor(kind: "SecretValue" | "ParameterValue", key: string, options: ErrorOptions = {}) {
    super(
      "not_initialized",
      `${kind}(${JSON.stringify(key)}) was read before initialization`,
      options,
    );
    this.name = "NotInitializedError";
  }
}

const GRPC_CODES: Readonly<Partial<Record<status, KmsErrorCode>>> = Object.freeze({
  [status.CANCELLED]: "cancelled",
  [status.UNKNOWN]: "unknown",
  [status.INVALID_ARGUMENT]: "invalid_argument",
  [status.DEADLINE_EXCEEDED]: "deadline_exceeded",
  [status.NOT_FOUND]: "not_found",
  [status.ALREADY_EXISTS]: "already_exists",
  [status.PERMISSION_DENIED]: "permission_denied",
  [status.RESOURCE_EXHAUSTED]: "resource_exhausted",
  [status.FAILED_PRECONDITION]: "failed_precondition",
  [status.ABORTED]: "aborted",
  [status.OUT_OF_RANGE]: "out_of_range",
  [status.UNIMPLEMENTED]: "unimplemented",
  [status.INTERNAL]: "internal",
  [status.UNAVAILABLE]: "unavailable",
  [status.DATA_LOSS]: "data_loss",
  [status.UNAUTHENTICATED]: "unauthenticated",
});

type GrpcErrorLike = Error & {
  readonly code: number;
  readonly details: string;
  readonly metadata: GrpcMetadataLike;
};

type GrpcMetadataLike = {
  getMap(): unknown;
};

function isGrpcMetadataLike(metadata: unknown): metadata is GrpcMetadataLike {
  return (
    typeof metadata === "object" &&
    metadata !== null &&
    typeof (metadata as Partial<GrpcMetadataLike>).getMap === "function"
  );
}

function isGrpcErrorLike(error: unknown): error is GrpcErrorLike {
  if (!(error instanceof Error)) return false;
  const candidate = error as Partial<GrpcErrorLike>;
  return (
    typeof candidate.code === "number" &&
    Number.isInteger(candidate.code) &&
    candidate.code >= status.OK &&
    candidate.code <= status.UNAUTHENTICATED &&
    typeof candidate.details === "string" &&
    isGrpcMetadataLike(candidate.metadata)
  );
}

/**
 * Convert a gRPC service error to a transport-independent `KmsError`.
 * Existing SDK errors are returned unchanged, as are ordinary non-gRPC
 * `Error` objects. A status of OK normalizes to `undefined`.
 */
export function mapGrpcError(error: ServiceError | Error | unknown): Error | undefined {
  if (error === undefined || error === null) return undefined;
  if (error instanceof KmsError) return error;
  if (!isGrpcErrorLike(error)) {
    return error instanceof Error
      ? error
      : new Error("unknown non-Error failure", { cause: error });
  }
  if (error.code === status.OK) return undefined;

  const code = GRPC_CODES[error.code as status] ?? "unknown";
  const details =
    typeof error.details === "string" && error.details.length > 0 ? error.details : error.message;
  return new KmsError(code, details || code.replaceAll("_", " "), {
    cause: error,
    grpcCode: error.code as status,
  });
}

/**
 * Normalize a secret-bearing RPC failure without retaining server-supplied
 * text or causes that could reflect plaintext or credentials.
 */
export function mapSecretGrpcError(error: ServiceError | Error | unknown): KmsError | undefined {
  if (error === undefined || error === null) return undefined;
  if (error instanceof KmsError) {
    return new KmsError(error.code, "KMS secret operation failed", {
      ...(error.grpcCode === undefined ? {} : { grpcCode: error.grpcCode }),
    });
  }
  const errorName = safeErrorName(error);
  if (errorName === "AbortError") {
    return new KmsError("cancelled", "KMS secret operation failed");
  }
  if (errorName === "TimeoutError") {
    return new KmsError("deadline_exceeded", "KMS secret operation failed");
  }
  const grpcCode = grpcCodeOf(error);
  if (grpcCode === status.OK) return undefined;
  const code = grpcCode === undefined ? "unknown" : (GRPC_CODES[grpcCode] ?? "unknown");
  return new KmsError(code, "KMS secret operation failed", {
    ...(grpcCode === undefined ? {} : { grpcCode }),
  });
}

/**
 * Preserve the one distinct post-commit purge outcome without retaining any
 * other server-provided text or cause.
 */
export function mapPurgeSecretGrpcError(
  error: ServiceError | Error | unknown,
): KmsError | undefined {
  if (isPurgeCleanupPending(error)) return new PurgeCleanupPendingError();
  return mapSecretGrpcError(error);
}

function isPurgeCleanupPending(error: unknown): boolean {
  try {
    return (
      isGrpcErrorLike(error) &&
      error.code === status.UNAVAILABLE &&
      error.details === PURGE_CLEANUP_PENDING_MESSAGE
    );
  } catch {
    return false;
  }
}

function safeErrorName(error: unknown): string | undefined {
  if (!(error instanceof Error)) return undefined;
  try {
    return typeof error.name === "string" ? error.name : undefined;
  } catch {
    return undefined;
  }
}

function grpcCodeOf(error: unknown): status | undefined {
  if (!(error instanceof Error)) return undefined;
  let code: unknown;
  try {
    code = (error as Partial<GrpcErrorLike>).code;
  } catch {
    return undefined;
  }
  return typeof code === "number" &&
    Number.isInteger(code) &&
    code >= status.OK &&
    code <= status.UNAUTHENTICATED
    ? (code as status)
    : undefined;
}

/** Backwards-friendly alias used by transport implementations. */
export const normalizeError = mapGrpcError;

/** Narrow an unknown value to a `KmsError`, optionally requiring a code. */
export function isKmsError(error: unknown, code?: KmsErrorCode): error is KmsError {
  return error instanceof KmsError && (code === undefined || error.code === code);
}

/**
 * Add operation context without losing a normalized error's stable code.
 * The context must describe only resource identity/operation, never plaintext.
 */
export function wrapError(message: string, error: unknown): Error {
  if (error instanceof KmsError) {
    return new KmsError(error.code, `${message}: ${error.message}`, {
      cause: error,
      ...(error.grpcCode === undefined ? {} : { grpcCode: error.grpcCode }),
    });
  }
  if (error instanceof Error) return new Error(`${message}: ${error.message}`, { cause: error });
  return new Error(message, { cause: error });
}
