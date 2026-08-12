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
  | "unknown";

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
  readonly details?: string;
};

function isGrpcErrorLike(error: unknown): error is GrpcErrorLike {
  return (
    error instanceof Error &&
    typeof (error as Partial<GrpcErrorLike>).code === "number" &&
    Number.isInteger((error as Partial<GrpcErrorLike>).code)
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
