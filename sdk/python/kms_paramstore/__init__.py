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

from importlib.metadata import PackageNotFoundError, version

from .async_client import AsyncClient
from .async_release import (
    AsyncManifestValidator,
    AsyncReleaseLoader,
    AsyncReleaseLoaderConfig,
    AsyncSecretTokenProvider,
    run_typed_release_async,
)
from .client import Client
from .config import TLSConfig
from .errors import (
    ConfigError,
    FailedPreconditionError,
    NoNamespaceError,
    NotFoundError,
    NotInitializedError,
    ParamStoreError,
    PermissionDeniedError,
    RateLimitedError,
    UnauthenticatedError,
)
from .models import (
    ApplicationDefaultsApplyEntry,
    ApplicationDefaultsApplyResult,
    Page,
    Parameter,
    ParameterMetadata,
    ParameterVersion,
    PromoteSecretResult,
    PutResult,
    PutSecretResult,
    SecretInfo,
    SecretVersion,
    WhoAmI,
    VerifyDefaultEntry,
    VerifyDefaultVerdict,
    VerifyReleaseDefaultsResult,
)
from .release import (
    ClassifiedReleaseError,
    PreparedRelease,
    ReleaseCandidateError,
    ReleaseCommitError,
    ReleaseDivergenceReporter,
    ReleaseEntry,
    ReleaseLoader,
    ReleaseLoaderConfig,
    ReleaseLoaderError,
    ReleaseManifest,
    ReleaseSnapshot,
    ReleaseStartupError,
    ReleaseStats,
    ReleaseStatus,
    RELEASE_REJECTION_CATEGORIES,
    RELEASE_STATES,
    SecretTokenProvider,
    run_typed_release,
)
from .secret import Secret, new_secret
from .tls import mtls_from_files, tls_from_bytes, tls_from_files
from .values import ParameterHandle, ParameterValue, SecretValue
from .watch import Event, EventType, WatchStatus

try:
    __version__ = version("kms-paramstore")
except PackageNotFoundError:
    __version__ = "0.2.0"

__all__ = [
    "Client",
    "AsyncClient",
    "AsyncReleaseLoader",
    "AsyncReleaseLoaderConfig",
    "AsyncManifestValidator",
    "AsyncSecretTokenProvider",
    "WhoAmI",
    "TLSConfig",
    "Secret",
    "new_secret",
    "SecretValue",
    "ParameterValue",
    "ParameterHandle",
    "Event",
    "EventType",
    "WatchStatus",
    "Page",
    "Parameter",
    "ParameterMetadata",
    "ParameterVersion",
    "SecretInfo",
    "SecretVersion",
    "PutResult",
    "PutSecretResult",
    "PromoteSecretResult",
    "VerifyDefaultEntry",
    "VerifyDefaultVerdict",
    "VerifyReleaseDefaultsResult",
    "ApplicationDefaultsApplyEntry",
    "ApplicationDefaultsApplyResult",
    "PreparedRelease",
    "ReleaseDivergenceReporter",
    "ClassifiedReleaseError",
    "ReleaseCandidateError",
    "ReleaseCommitError",
    "ReleaseEntry",
    "ReleaseManifest",
    "ReleaseLoader",
    "ReleaseLoaderConfig",
    "ReleaseLoaderError",
    "ReleaseSnapshot",
    "ReleaseStartupError",
    "ReleaseStats",
    "ReleaseStatus",
    "RELEASE_REJECTION_CATEGORIES",
    "RELEASE_STATES",
    "SecretTokenProvider",
    "run_typed_release",
    "run_typed_release_async",
    "ParamStoreError",
    "NotFoundError",
    "PermissionDeniedError",
    "RateLimitedError",
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
