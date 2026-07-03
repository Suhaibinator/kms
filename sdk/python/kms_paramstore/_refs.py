"""Client-side namespace and key handling (the Python peer of ``internal/keyutil``).

A resource is addressed by an explicit ``(env, app, key)``. The server never
parses a path string; the ``/env/app/key`` form survives only as a *display*
format and as *client-side* SDK sugar. This module owns:

- parsing the ``"env/app"`` namespace config string,
- splitting an absolute ``"/env/app/key"`` display path (the cross-namespace
  escape hatch) into an explicit :class:`Ref`,
- splitting an absolute watch pattern ``"/env/app/pattern"``,
- key-pattern matching (exact, ``"*"``, or ``"prefix/*"``), and
- building the generated ``NamespaceRef`` / ``ResourceRef`` protobufs.

Only the shapes needed to *split* a display path are validated here (env/app
labels); relative keys are handed to the server, which is the authority on key
syntax. This keeps the SDK from rejecting keys a newer server would accept.
"""

from __future__ import annotations

from dataclasses import dataclass

from . import errors
from ._gen import kms_pb2

__all__ = [
    "NamespaceRef",
    "Ref",
    "parse_namespace",
    "split_display_path",
    "split_display_pattern",
    "match_key",
    "to_proto_namespace",
    "to_proto_ref",
]

_MAX_LABEL = 64


@dataclass(frozen=True)
class NamespaceRef:
    """A namespace: a fixed ``(env, app)`` pair. Renders as ``"env/app"``."""

    env: str
    app: str

    def __str__(self) -> str:
        return f"{self.env}/{self.app}"


@dataclass(frozen=True)
class Ref:
    """One parameter or secret: a namespace plus a relative key.

    Renders as the absolute display path ``"/env/app/key"``.
    """

    ns: NamespaceRef
    key: str

    def __str__(self) -> str:
        return f"/{self.ns.env}/{self.ns.app}/{self.key}"


def _validate_label(kind: str, s: str) -> None:
    if not s:
        raise errors.ConfigError(f"{kind} must not be empty")
    if len(s) > _MAX_LABEL:
        raise errors.ConfigError(f"{kind} {s!r} exceeds {_MAX_LABEL} characters")
    if s[0] == "-" or s[-1] == "-":
        raise errors.ConfigError(f"{kind} {s!r} must not start or end with '-'")
    for ch in s:
        if not (ch.islower() and ch.isascii() or ch.isdigit() or ch == "-"):
            raise errors.ConfigError(f"{kind} {s!r} contains invalid character {ch!r}")


def parse_namespace(s: str) -> NamespaceRef:
    """Parse the SDK config form ``"env/app"`` into a :class:`NamespaceRef`."""
    env, sep, app = s.partition("/")
    if not sep:
        raise errors.ConfigError(f"namespace {s!r} must be of the form env/app")
    _validate_label("env", env)
    _validate_label("app", app)
    return NamespaceRef(env, app)


def split_display_path(p: str) -> Ref:
    """Split an absolute display path ``"/env/app/key"`` into a :class:`Ref`.

    The key may contain interior slashes (they are part of the name). Used only
    as the SDK's cross-namespace escape hatch; the server never sees the string.
    """
    if not p.startswith("/"):
        raise errors.ConfigError(f"path {p!r} must start with '/'")
    parts = p[1:].split("/", 2)
    if len(parts) < 3 or not parts[0] or not parts[1] or not parts[2]:
        raise errors.ConfigError(f"path {p!r} must be of the form /env/app/key")
    _validate_label("env", parts[0])
    _validate_label("app", parts[1])
    return Ref(NamespaceRef(parts[0], parts[1]), parts[2])


def split_display_pattern(p: str) -> "tuple[NamespaceRef, str]":
    """Split an absolute watch pattern ``"/env/app/pattern"``.

    ``/env/app`` (no trailing pattern) is treated as ``"*"`` (all keys).
    """
    if not p.startswith("/"):
        raise errors.ConfigError(f"pattern {p!r} must start with '/'")
    parts = p[1:].split("/", 2)
    if len(parts) < 2 or not parts[0] or not parts[1]:
        raise errors.ConfigError(f"pattern {p!r} must be of the form /env/app/pattern")
    _validate_label("env", parts[0])
    _validate_label("app", parts[1])
    key_pattern = parts[2] if len(parts) == 3 and parts[2] else "*"
    return NamespaceRef(parts[0], parts[1]), key_pattern


def match_key(pattern: str, key: str) -> bool:
    """Report whether ``key`` matches ``pattern``.

    ``""`` or ``"*"`` matches every key; ``"prefix/*"`` matches ``"prefix"`` and
    everything beneath it; otherwise the match is exact.
    """
    if pattern in ("", "*"):
        return True
    if pattern.endswith("/*"):
        base = pattern[:-2]
        return key == base or key.startswith(base + "/")
    return pattern == key


def to_proto_namespace(ns: NamespaceRef) -> kms_pb2.NamespaceRef:
    return kms_pb2.NamespaceRef(env=ns.env, app=ns.app)


def to_proto_ref(ref: Ref) -> kms_pb2.ResourceRef:
    return kms_pb2.ResourceRef(namespace=to_proto_namespace(ref.ns), key=ref.key)
