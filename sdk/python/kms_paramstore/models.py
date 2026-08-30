"""Lightweight public data types returned by the client.

These decouple callers from the generated protobuf messages. A resource is
addressed by an explicit ``(env, app, key)``; each type also exposes a
``namespace`` (``"env/app"``) and a ``path`` (``"/env/app/key"``) display helper.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from types import MappingProxyType
from typing import Generic, Iterator, Mapping, Tuple, TypeVar

__all__ = [
    "Parameter",
    "ParameterMetadata",
    "ParameterVersion",
    "SecretVersion",
    "SecretInfo",
    "PutResult",
    "PutSecretResult",
    "PromoteSecretResult",
    "Page",
    "WhoAmI",
    "VerifyDefaultEntry",
    "VerifyDefaultVerdict",
    "VerifyReleaseDefaultsResult",
    "ApplicationDefaultsApplyEntry",
    "ApplicationDefaultsApplyResult",
]

T = TypeVar("T")


def _display_namespace(env: str, app: str) -> str:
    return f"{env}/{app}"


def _display_path(env: str, app: str, key: str) -> str:
    if env and app:
        return f"/{env}/{app}/{key}"
    return key


@dataclass(frozen=True)
class Parameter:
    env: str
    app: str
    key: str
    value: str
    content_type: str
    version: int
    metadata_json: str = "{}"
    created_by: str = ""
    created_at_unix_ms: int = 0
    labels: Mapping[str, int] = field(default_factory=dict)

    @property
    def namespace(self) -> str:
        return _display_namespace(self.env, self.app)

    @property
    def path(self) -> str:
        return _display_path(self.env, self.app, self.key)


@dataclass(frozen=True)
class ParameterVersion:
    version: int
    content_type: str
    state: str
    created_by: str = ""
    created_at_unix_ms: int = 0
    metadata_json: str = "{}"


@dataclass(frozen=True)
class ParameterMetadata:
    env: str
    app: str
    key: str
    content_type: str
    metadata_json: str = "{}"
    created_at_unix_ms: int = 0
    updated_at_unix_ms: int = 0
    labels: Mapping[str, int] = field(default_factory=dict)
    versions: Tuple[ParameterVersion, ...] = ()

    @property
    def namespace(self) -> str:
        return _display_namespace(self.env, self.app)

    @property
    def path(self) -> str:
        return _display_path(self.env, self.app, self.key)


@dataclass(frozen=True)
class SecretVersion:
    version: int
    state: str
    created_by: str = ""
    created_at_unix_ms: int = 0
    destroyed_at_unix_ms: int = 0
    expires_at_unix_ms: int = 0
    metadata_json: str = "{}"


@dataclass(frozen=True)
class SecretInfo:
    """Secret-level metadata. Never carries plaintext."""

    env: str
    app: str
    key: str
    content_type: str
    client_bound: bool
    has_access_token: bool
    metadata_json: str = "{}"
    created_at_unix_ms: int = 0
    updated_at_unix_ms: int = 0
    labels: Mapping[str, int] = field(default_factory=dict)
    versions: Tuple[SecretVersion, ...] = ()

    @property
    def namespace(self) -> str:
        return _display_namespace(self.env, self.app)

    @property
    def path(self) -> str:
        return _display_path(self.env, self.app, self.key)


@dataclass(frozen=True)
class PutResult:
    version: int
    revision: int


@dataclass(frozen=True)
class PutSecretResult:
    version: int
    revision: int
    access_token: str = ""  # set only when a token was minted; never retrievable again


@dataclass(frozen=True)
class PromoteSecretResult:
    current_version: int
    previous_version: int
    revision: int


@dataclass(frozen=True)
class Page(Generic[T]):
    """One immutable page of list results.

    Iteration retains the v0.1 ``items, token = ...`` migration path while the
    named attributes match the Go and TypeScript page contract.
    """

    items: Tuple[T, ...]
    next_page_token: str = ""

    def __iter__(self) -> Iterator[object]:
        yield self.items
        yield self.next_page_token


@dataclass(frozen=True)
class WhoAmI:
    identity: str
    kind: str
    namespace: str | None
    auth_method: str

    @property
    def name(self) -> str:
        """Deprecated v0.1 spelling retained as a read-only alias."""
        return self.identity


@dataclass(frozen=True)
class VerifyDefaultEntry:
    alias: str
    content_type: str
    sha256: str


@dataclass(frozen=True)
class VerifyDefaultVerdict:
    alias: str
    verdict: str


@dataclass(frozen=True)
class VerifyReleaseDefaultsResult:
    release_name: str
    release_version: int
    activation_revision: int
    schema_matches: bool
    entries: Tuple[VerifyDefaultVerdict, ...]
    match_count: int
    differs_count: int
    missing_count: int
    unknown_alias_count: int
    secret_alias_count: int
    unsupported_count: int
    unverified_count: int

    @property
    def passed(self) -> bool:
        return self.schema_matches and all(item.verdict == "match" for item in self.entries)


@dataclass(frozen=True)
class ApplicationDefaultsApplyEntry:
    alias: str
    key: str
    content_type: str
    status: str
    current_version: int
    applied_version: int
    revision: int


@dataclass(frozen=True)
class ApplicationDefaultsApplyResult:
    profile: str
    schema_sha256: str
    artifact_digest: str
    plan_digest: str
    entries: Tuple[ApplicationDefaultsApplyEntry, ...]
    missing_secrets: Tuple[str, ...]
    executed: bool
    definition_changed: bool
    definition_updated: bool


def _parameter_from_proto(p) -> Parameter:
    ref = p.ref
    return Parameter(
        env=ref.namespace.env,
        app=ref.namespace.app,
        key=ref.key,
        value=p.value,
        content_type=p.content_type,
        version=p.version,
        metadata_json=p.metadata_json,
        created_by=p.created_by,
        created_at_unix_ms=p.created_at_unix_ms,
        labels=MappingProxyType(dict(p.labels)),
    )


def _secret_info_from_proto(s) -> SecretInfo:
    ref = s.ref
    return SecretInfo(
        env=ref.namespace.env,
        app=ref.namespace.app,
        key=ref.key,
        content_type=s.content_type,
        client_bound=s.client_bound,
        has_access_token=s.has_access_token,
        metadata_json=s.metadata_json,
        created_at_unix_ms=s.created_at_unix_ms,
        updated_at_unix_ms=s.updated_at_unix_ms,
        labels=MappingProxyType(dict(s.labels)),
        versions=tuple(
            SecretVersion(
                version=v.version,
                state=v.state,
                created_by=v.created_by,
                created_at_unix_ms=v.created_at_unix_ms,
                destroyed_at_unix_ms=v.destroyed_at_unix_ms,
                expires_at_unix_ms=v.expires_at_unix_ms,
                metadata_json=v.metadata_json,
            )
            for v in s.versions
        ),
    )


def _parameter_metadata_from_proto(p) -> ParameterMetadata:
    ref = p.ref
    return ParameterMetadata(
        env=ref.namespace.env,
        app=ref.namespace.app,
        key=ref.key,
        content_type=p.content_type,
        metadata_json=p.metadata_json,
        created_at_unix_ms=p.created_at_unix_ms,
        updated_at_unix_ms=p.updated_at_unix_ms,
        labels=MappingProxyType(dict(p.labels)),
        versions=tuple(
            ParameterVersion(
                version=v.version,
                content_type=v.content_type,
                state=v.state,
                created_by=v.created_by,
                created_at_unix_ms=v.created_at_unix_ms,
                metadata_json=v.metadata_json,
            )
            for v in p.versions
        ),
    )
