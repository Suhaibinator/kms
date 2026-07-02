"""Lightweight public data types returned by the client.

These decouple callers from the generated protobuf messages.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Dict, List

__all__ = [
    "Parameter",
    "SecretVersion",
    "SecretInfo",
    "PutResult",
    "PutSecretResult",
]


@dataclass
class Parameter:
    path: str
    value: str
    content_type: str
    version: int
    metadata_json: str = "{}"
    created_by: str = ""
    created_at_unix_ms: int = 0
    labels: Dict[str, int] = field(default_factory=dict)


@dataclass
class SecretVersion:
    version: int
    state: str
    created_by: str = ""
    created_at_unix_ms: int = 0
    destroyed_at_unix_ms: int = 0
    expires_at_unix_ms: int = 0
    metadata_json: str = "{}"


@dataclass
class SecretInfo:
    """Secret-level metadata. Never carries plaintext."""

    path: str
    content_type: str
    client_bound: bool
    has_access_token: bool
    metadata_json: str = "{}"
    created_at_unix_ms: int = 0
    updated_at_unix_ms: int = 0
    labels: Dict[str, int] = field(default_factory=dict)
    versions: List[SecretVersion] = field(default_factory=list)


@dataclass
class PutResult:
    version: int
    revision: int


@dataclass
class PutSecretResult:
    version: int
    revision: int
    access_token: str = ""  # set only when a token was minted; never retrievable again


def _parameter_from_proto(p) -> Parameter:
    return Parameter(
        path=p.path,
        value=p.value,
        content_type=p.content_type,
        version=p.version,
        metadata_json=p.metadata_json,
        created_by=p.created_by,
        created_at_unix_ms=p.created_at_unix_ms,
        labels=dict(p.labels),
    )


def _secret_info_from_proto(s) -> SecretInfo:
    return SecretInfo(
        path=s.path,
        content_type=s.content_type,
        client_bound=s.client_bound,
        has_access_token=s.has_access_token,
        metadata_json=s.metadata_json,
        created_at_unix_ms=s.created_at_unix_ms,
        updated_at_unix_ms=s.updated_at_unix_ms,
        labels=dict(s.labels),
        versions=[
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
        ],
    )
