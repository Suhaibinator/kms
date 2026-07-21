"""kms_paramstore: Python SDK for the KMS parameter store and secret manager.

Typical use::

    from kms_paramstore import Client, tls_from_files

    with Client("parameter-store.prod.internal:8443", namespace="prod/gradethis",
                token="<client-token>", tls=tls_from_files("server-ca.crt")) as client:
        db_password = client.get_secret("postgres-password")  # relative to namespace
        print(db_password)          # [REDACTED]
        connect(db_password.value)  # explicit access to plaintext

The SDK hides gRPC boilerplate, supports TLS/mTLS, caches reads, redacts secrets
in logs and errors, and provides declarative :class:`SecretValue` /
:class:`ParameterValue` config fields with hot reload.
"""

from __future__ import annotations

from .client import Client, WhoAmI
from .config import TLSConfig
from .errors import (
    ConfigError,
    FailedPreconditionError,
    NoNamespaceError,
    NotFoundError,
    NotInitializedError,
    ParamStoreError,
    PermissionDeniedError,
    UnauthenticatedError,
)
from .models import Parameter, PutResult, PutSecretResult, SecretInfo, SecretVersion
from .release import (
    PreparedRelease,
    ReleaseCommitError,
    ReleaseEntry,
    ReleaseLoader,
    ReleaseLoaderConfig,
    ReleaseLoaderError,
    ReleaseSnapshot,
    ReleaseStartupError,
    ReleaseStats,
    ReleaseStatus,
    SecretTokenProvider,
    run_typed_release,
)
from .secret import Secret, new_secret
from .tls import mtls_from_files, tls_from_bytes, tls_from_files
from .values import ParameterHandle, ParameterValue, SecretValue
from .watch import Event, EventType

__version__ = "0.1.0"

__all__ = [
    "Client",
    "WhoAmI",
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
    "PreparedRelease",
    "ReleaseCommitError",
    "ReleaseEntry",
    "ReleaseLoader",
    "ReleaseLoaderConfig",
    "ReleaseLoaderError",
    "ReleaseSnapshot",
    "ReleaseStartupError",
    "ReleaseStats",
    "ReleaseStatus",
    "SecretTokenProvider",
    "run_typed_release",
    "ParamStoreError",
    "NotFoundError",
    "PermissionDeniedError",
    "UnauthenticatedError",
    "FailedPreconditionError",
    "NotInitializedError",
    "ConfigError",
    "NoNamespaceError",
    "tls_from_files",
    "mtls_from_files",
    "tls_from_bytes",
    "__version__",
]
