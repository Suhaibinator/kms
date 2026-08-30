"""Exception types for the paramstore SDK.

Every exception here, and any message it carries, is guaranteed free of secret
plaintext: the SDK maps server ``grpc`` status errors onto these types using only
the status code and the server's (non-secret) message.
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
    "NotInitializedError",
    "ConfigError",
    "NoNamespaceError",
    "map_grpc_error",
]


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

    For example, a client-bound mode mismatch or a disabled version.
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
