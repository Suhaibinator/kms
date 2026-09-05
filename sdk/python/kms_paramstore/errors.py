"""Exception types for the paramstore SDK.

RPCs that carry secret plaintext or credentials use a fixed-message mapper so a
buggy or hostile server cannot reflect request material into an SDK exception.
Other RPCs preserve the server's status message for operational context.
"""

from __future__ import annotations

from typing import Optional

import grpc

__all__ = [
    "ParamStoreError",
    "NotFoundError",
    "PermissionDeniedError",
    "UnauthenticatedError",
    "FailedPreconditionError",
    "RateLimitedError",
    "PurgeCleanupPendingError",
    "NotInitializedError",
    "ConfigError",
    "NoNamespaceError",
    "map_grpc_error",
    "map_secret_grpc_error",
    "map_purge_grpc_error",
]


_PURGE_CLEANUP_PENDING_MESSAGE = (
    "secret purge committed; database artifact cleanup is pending"
)


class ParamStoreError(Exception):
    """Base class with a stable, transport-independent programmatic code."""

    code = "unknown"

    def __init__(
        self,
        message: str = "",
        *,
        code: Optional[str] = None,
        grpc_code: Optional[grpc.StatusCode] = None,
    ) -> None:
        super().__init__(message)
        self.code = code or type(self).code
        self.grpc_code = grpc_code


class NotFoundError(ParamStoreError):
    """A parameter or secret (or the requested version/label) does not exist."""

    code = "not_found"


class PermissionDeniedError(ParamStoreError):
    """The caller is authenticated but not authorized for the path/operation."""

    code = "permission_denied"


class UnauthenticatedError(ParamStoreError):
    """The client identity token is missing, invalid, or expired."""

    code = "unauthenticated"


class FailedPreconditionError(ParamStoreError):
    """The request is well-formed but server state forbids it.

    For example, an invalid binding transition or a disabled version.
    """

    code = "failed_precondition"


class NotInitializedError(ParamStoreError):
    """A declarative value was read before ``Client.resolve``/``init`` ran."""

    code = "not_initialized"


class ConfigError(ParamStoreError):
    """The SDK was configured incorrectly (bad endpoint, missing key, ...)."""

    code = "invalid_argument"


class NoNamespaceError(ConfigError):
    """A relative key was used but the client has no namespace to resolve it.

    Raised when a relative (non-``/``-prefixed) key is resolved on a client that
    was neither given a ``namespace=`` nor bound to one (``WhoAmI`` reports the
    identity unbound). Give the client a ``namespace`` or use an absolute
    ``/env/app/key``.
    """

    code = "no_namespace"


class RateLimitedError(ParamStoreError):
    """The server exhausted a per-identity operation budget."""

    code = "resource_exhausted"


class PurgeCleanupPendingError(ParamStoreError):
    """The purge committed, but active database artifacts need cleanup.

    The server is fail-closed until it can finish cleanup. The caller must not
    retry either purge: its bound-cohort or unbound-version preview guard is
    now stale, and a retired binding key may already have been discarded. No
    mutation result accompanies this error.
    """

    code = "purge_cleanup_pending"

    def __init__(self) -> None:
        super().__init__(
            _PURGE_CLEANUP_PENDING_MESSAGE,
            grpc_code=grpc.StatusCode.UNAVAILABLE,
        )


# Map gRPC status codes to SDK exception types. Codes not listed here surface as
# a generic ParamStoreError that preserves the code name and message.
_CODE_MAP = {
    grpc.StatusCode.NOT_FOUND: NotFoundError,
    grpc.StatusCode.PERMISSION_DENIED: PermissionDeniedError,
    grpc.StatusCode.UNAUTHENTICATED: UnauthenticatedError,
    grpc.StatusCode.FAILED_PRECONDITION: FailedPreconditionError,
    grpc.StatusCode.RESOURCE_EXHAUSTED: RateLimitedError,
}

_CODE_NAMES = {
    grpc.StatusCode.CANCELLED: "cancelled",
    grpc.StatusCode.UNKNOWN: "unknown",
    grpc.StatusCode.INVALID_ARGUMENT: "invalid_argument",
    grpc.StatusCode.DEADLINE_EXCEEDED: "deadline_exceeded",
    grpc.StatusCode.NOT_FOUND: "not_found",
    grpc.StatusCode.ALREADY_EXISTS: "already_exists",
    grpc.StatusCode.PERMISSION_DENIED: "permission_denied",
    grpc.StatusCode.RESOURCE_EXHAUSTED: "resource_exhausted",
    grpc.StatusCode.FAILED_PRECONDITION: "failed_precondition",
    grpc.StatusCode.ABORTED: "aborted",
    grpc.StatusCode.OUT_OF_RANGE: "out_of_range",
    grpc.StatusCode.UNIMPLEMENTED: "unimplemented",
    grpc.StatusCode.INTERNAL: "internal",
    grpc.StatusCode.UNAVAILABLE: "unavailable",
    grpc.StatusCode.DATA_LOSS: "data_loss",
    grpc.StatusCode.UNAUTHENTICATED: "unauthenticated",
}


def map_grpc_error(err: grpc.RpcError) -> ParamStoreError:
    """Translate a ``grpc.RpcError`` into an SDK exception.

    The server's status *message* is preserved for context but never contains
    plaintext. Non-status errors are wrapped in a generic ParamStoreError.
    """
    code = None
    message = str(err)
    code_method = getattr(err, "code", None)
    details_method = getattr(err, "details", None)
    if callable(code_method):
        candidate = code_method()
        if isinstance(candidate, grpc.StatusCode):
            code = candidate
            if callable(details_method):
                message = details_method() or ""
    exc_type = _CODE_MAP.get(code, ParamStoreError)
    stable_code = _CODE_NAMES.get(code, "unknown")
    if exc_type is ParamStoreError and code is not None:
        return ParamStoreError(
            f"{stable_code}: {message}", code=stable_code, grpc_code=code
        )
    return exc_type(message, grpc_code=code)


def _grpc_error_code(err: BaseException) -> Optional[grpc.StatusCode]:
    if not isinstance(err, grpc.RpcError):
        return None
    code = None
    try:
        code_method = getattr(err, "code", None)
    except Exception:
        code_method = None
    if callable(code_method):
        try:
            candidate = code_method()
        except Exception:
            candidate = None
        if isinstance(candidate, grpc.StatusCode):
            code = candidate
    return code


def _secret_error_from_code(code: Optional[grpc.StatusCode]) -> ParamStoreError:
    """Build a fixed-message secret error from structured status only."""

    exc_type = _CODE_MAP.get(code, ParamStoreError)
    stable_code = _CODE_NAMES.get(code, "unknown")
    message = f"secret operation failed ({stable_code})"
    if exc_type is ParamStoreError:
        return ParamStoreError(message, code=stable_code, grpc_code=code)
    return exc_type(message, grpc_code=code)


def map_secret_grpc_error(err: BaseException) -> ParamStoreError:
    """Translate a secret-bearing transport error without trusting its details.

    Only the structured gRPC status code affects the result.  In particular,
    neither ``details()`` nor ``str(err)`` is used because a misbehaving peer
    could reflect secret plaintext, an access token, or a binding key there.
    """
    return _secret_error_from_code(_grpc_error_code(err))


def map_purge_grpc_error(err: BaseException) -> ParamStoreError:
    """Translate a purge RPC error, recognizing only the committed sentinel.

    Remote details are inspected solely for an exact comparison with KMS's
    fixed cleanup-pending message. Every other detail is discarded and mapped
    using the ordinary fixed-message secret error path.
    """
    code = _grpc_error_code(err)
    if code is grpc.StatusCode.UNAVAILABLE and isinstance(err, grpc.RpcError):
        try:
            details_method = getattr(err, "details", None)
        except Exception:
            details_method = None
        if callable(details_method):
            try:
                details = details_method()
            except Exception:
                details = None
            if details == _PURGE_CLEANUP_PENDING_MESSAGE:
                return PurgeCleanupPendingError()
    return _secret_error_from_code(code)
