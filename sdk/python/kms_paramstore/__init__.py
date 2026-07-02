"""kms_paramstore: Python SDK for the KMS parameter store and secret manager.

Typical use::

    from kms_paramstore import Client

    with Client("parameter-store.prod.internal:8443", token="<client-token>") as client:
        db_password = client.get_secret("/prod/gradethis/postgres-password")
        print(db_password)          # [REDACTED]
        connect(db_password.value)  # explicit access to plaintext

The SDK hides gRPC boilerplate, supports TLS/mTLS, caches reads, redacts secrets
in logs and errors, and provides declarative :class:`SecretValue` /
:class:`ParameterValue` config fields with hot reload.
"""

from __future__ import annotations

from .client import Client
from .config import TLSConfig
from .errors import (
    ConfigError,
    FailedPreconditionError,
    NotFoundError,
    NotInitializedError,
    ParamStoreError,
    PermissionDeniedError,
    UnauthenticatedError,
)
from .models import Parameter, PutResult, PutSecretResult, SecretInfo, SecretVersion
from .secret import Secret, new_secret
from .tls import mtls_from_files, tls_from_bytes, tls_from_files
from .values import ParameterHandle, ParameterValue, SecretValue
from .watch import Event, EventType

__version__ = "0.1.0"

__all__ = [
    "Client",
    "TLSConfig",
    "Secret",
    "new_secret",
    "SecretValue",
    "ParameterValue",
    "ParameterHandle",
    "Event",
    "EventType",
    "Parameter",
    "SecretInfo",
    "SecretVersion",
    "PutResult",
    "PutSecretResult",
    "ParamStoreError",
    "NotFoundError",
    "PermissionDeniedError",
    "UnauthenticatedError",
    "FailedPreconditionError",
    "NotInitializedError",
    "ConfigError",
    "tls_from_files",
    "mtls_from_files",
    "tls_from_bytes",
    "__version__",
]
