"""Immutable, redacted managed-configuration value types."""

from __future__ import annotations

import copy
from dataclasses import dataclass, field
from types import MappingProxyType
from typing import Any, Generic, Mapping, TypeVar

from pydantic import BaseModel
from ..secret import Secret

T = TypeVar("T", bound=BaseModel)

REJECTION_CATEGORIES = frozenset(
    {
        "config_contract_mismatch",
        "config_decode_failed",
        "config_validation_failed",
        "default_mismatch",
        "restart_required",
        "internal",
    }
)


@dataclass(frozen=True)
class ReleaseIdentity:
    namespace: str = ""
    name: str = ""
    version: int = 0
    activation_revision: int = 0
    schema_id: str = ""
    schema_version: int = 0
    digest: str = ""

    @classmethod
    def from_candidate(cls, candidate: object) -> "ReleaseIdentity":
        return cls(
            namespace=str(getattr(candidate, "namespace", "")),
            name=str(getattr(candidate, "name", "")),
            version=int(getattr(candidate, "version", 0)),
            activation_revision=int(getattr(candidate, "activation_revision", 0)),
            schema_id=str(getattr(candidate, "schema_id", "")),
            schema_version=int(getattr(candidate, "schema_version", 0)),
            digest=str(getattr(candidate, "digest", "")),
        )

    @property
    def is_zero(self) -> bool:
        return not (self.namespace or self.name or self.version or self.activation_revision or self.digest)

    def __str__(self) -> str:
        if not self.namespace and not self.name:
            return f"release@{self.version}#{self.activation_revision}"
        return f"{self.namespace}/{self.name}@{self.version}#{self.activation_revision}"


class ConfigSnapshot(Generic[T]):
    """Immutable generation holder returning defensive Pydantic copies."""

    __slots__ = ("_config", "release", "_sealed")
    _config: T
    release: ReleaseIdentity
    _sealed: bool

    def __init__(self, config: T, release: ReleaseIdentity = ReleaseIdentity()) -> None:
        object.__setattr__(self, "_config", _clone_value(config))
        object.__setattr__(self, "release", release)
        object.__setattr__(self, "_sealed", True)

    def __setattr__(self, name: str, value: object) -> None:
        if getattr(self, "_sealed", False):
            raise AttributeError("configstore: snapshot is immutable")
        object.__setattr__(self, name, value)

    def config(self) -> T:
        return _clone_value(self._config)

    def get(self, key: str) -> Any:
        return _clone_value(getattr(self._config, key))

    def __repr__(self) -> str:
        return f"ConfigSnapshot(release={self.release!r}, config=[REDACTED])"


@dataclass(frozen=True)
class FieldDifference:
    path: str
    expected: Any
    actual: Any


@dataclass(frozen=True)
class FieldChange:
    path: str
    previous: Any
    current: Any


@dataclass(frozen=True)
class DefaultMismatchReport:
    phase: str
    release: ReleaseIdentity
    _fields: tuple[FieldDifference, ...]
    severity: str = "error"

    def fields(self) -> tuple[FieldDifference, ...]:
        return copy.deepcopy(self._fields)

    def __str__(self) -> str:
        paths = ",".join(item.path for item in self._fields)
        return f"configstore: default mismatch ({self.phase}/{self.severity}) for {self.release} fields={paths}"


@dataclass(frozen=True)
class AppliedReport:
    phase: str
    release: ReleaseIdentity
    default_divergent: bool
    _changed: tuple[FieldChange, ...] = ()
    _groups: Mapping[str, str] = field(default_factory=dict)

    def changed(self) -> tuple[FieldChange, ...]:
        return copy.deepcopy(self._changed)

    def groups(self) -> Mapping[str, str]:
        return MappingProxyType(dict(self._groups))

    def __str__(self) -> str:
        paths = ",".join(item.path for item in self._changed)
        return f"configstore: applied ({self.phase}) {self.release} divergent={str(self.default_divergent).lower()} changed={paths}"


@dataclass(frozen=True)
class CandidateRejectionReport:
    category: str
    release: ReleaseIdentity
    _paths: tuple[str, ...] = ()

    def paths(self) -> tuple[str, ...]:
        return self._paths


class CandidateError(Exception):
    """Classified candidate error whose string never renders its cause."""

    def __init__(self, category: str, cause: BaseException | None = None, paths: tuple[str, ...] = ()) -> None:
        self.category = category if category in REJECTION_CATEGORIES else "internal"
        self.cause = cause
        self.paths = paths
        super().__init__(f"configstore: candidate rejected ({self.category})")

    @property
    def release_rejection_category(self) -> str:
        return self.category


@dataclass(frozen=True)
class ManagedConfigStatus:
    state: str = "idle"
    ready: bool = False
    observed: ReleaseIdentity = ReleaseIdentity()
    applied: ReleaseIdentity = ReleaseIdentity()
    default_divergent: bool = False
    last_rejection_category: str = ""
    last_failure_unix_ms: int = 0
    reconnects: int = 0


@dataclass(frozen=True)
class ManagedConfigStats:
    candidates: int = 0
    applied: int = 0
    rejected: Mapping[str, int] = field(default_factory=dict)
    reconnects: int = 0
    default_divergent: bool = False
    applied_release_version: int = 0
    applied_activation_revision: int = 0


def _clone_value(value: Any) -> Any:
    if isinstance(value, Secret):
        return Secret(
            value.value,
            env=value.env,
            app=value.app,
            key=value.key,
            version=value.version,
            content_type=value.content_type,
        )
    if isinstance(value, BaseModel):
        updates = {
            name: _clone_value(getattr(value, name))
            for name in value.__class__.model_fields
        }
        return value.model_copy(update=updates, deep=True)
    if isinstance(value, dict):
        return {_clone_value(key): _clone_value(item) for key, item in value.items()}
    if isinstance(value, list):
        return [_clone_value(item) for item in value]
    if isinstance(value, tuple):
        return tuple(_clone_value(item) for item in value)
    if isinstance(value, set):
        return {_clone_value(item) for item in value}
    return copy.deepcopy(value)
