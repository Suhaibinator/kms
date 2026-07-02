"""Exception types for the paramstore SDK.

Every exception here, and any message it carries, is guaranteed free of secret
plaintext: the SDK maps server ``grpc`` status errors onto these types using only
the status code and the server's (non-secret) message.
"""

from __future__ import annotations

import grpc

__all__ = [
    "ParamStoreError",
    "NotFoundError",
    "PermissionDeniedError",
    "UnauthenticatedError",
    "FailedPreconditionError",
    "NotInitializedError",
    "ConfigError",
    "map_grpc_error",
]


class ParamStoreError(Exception):
    """Base class for all errors raised by the SDK."""


class NotFoundError(ParamStoreError):
    """A parameter or secret (or the requested version/label) does not exist."""


class PermissionDeniedError(ParamStoreError):
    """The caller is authenticated but not authorized for the path/operation."""


class UnauthenticatedError(ParamStoreError):
    """The client identity token is missing, invalid, or expired."""


class FailedPreconditionError(ParamStoreError):
    """The request is well-formed but server state forbids it.

    For example, a client-bound mode mismatch or a disabled version.
    """


class NotInitializedError(ParamStoreError):
    """A declarative value was read before ``Client.resolve``/``init`` ran."""


class ConfigError(ParamStoreError):
    """The SDK was configured incorrectly (bad endpoint, missing key, ...)."""


# Map gRPC status codes to SDK exception types. Codes not listed here surface as
# a generic ParamStoreError that preserves the code name and message.
_CODE_MAP = {
    grpc.StatusCode.NOT_FOUND: NotFoundError,
    grpc.StatusCode.PERMISSION_DENIED: PermissionDeniedError,
    grpc.StatusCode.UNAUTHENTICATED: UnauthenticatedError,
    grpc.StatusCode.FAILED_PRECONDITION: FailedPreconditionError,
}


def map_grpc_error(err: grpc.RpcError) -> ParamStoreError:
    """Translate a ``grpc.RpcError`` into an SDK exception.

    The server's status *message* is preserved for context but never contains
    plaintext. Non-status errors are wrapped in a generic ParamStoreError.
    """
    code = None
    message = str(err)
    if isinstance(err, grpc.Call):
        code = err.code()
        message = err.details() or ""
    exc_type = _CODE_MAP.get(code, ParamStoreError)
    if exc_type is ParamStoreError and code is not None:
        return ParamStoreError(f"{code.name.lower()}: {message}")
    return exc_type(message)
