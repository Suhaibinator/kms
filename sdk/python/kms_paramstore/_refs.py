"""Client-side namespace and key handling (the Python peer of ``internal/keyutil``).

A resource is addressed by an explicit ``(env, app, key)``. The server never
parses a path string; the ``/env/app/key`` form survives only as a *display*
format and as *client-side* SDK sugar. This module owns:

- parsing the ``"env/app"`` namespace config string,
- splitting an absolute ``"/env/app/key"`` display path (the cross-namespace
  escape hatch) into an explicit :class:`Ref`, and
- building the generated ``NamespaceRef`` / ``ResourceRef`` protobufs.

There is no key-pattern matcher: the namespace is the unit of subscription, and
a subscriber receives every change in it. Any narrower interest is applied by
the client in its callback, never on the wire.

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
    "to_proto_namespace",
    "to_proto_ref",
]

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


def parse_namespace(s: str) -> NamespaceRef:
    """Parse the SDK config form ``"env/app"`` into a :class:`NamespaceRef`.

    Only structural validity (both halves present, no extra slash) is checked
    here; the character set is left to the server, which is authoritative on
    naming — mirroring the Go SDK's ``parseNamespace``.
    """
    env, sep, app = s.partition("/")
    if not sep or not env or not app or "/" in app:
        raise errors.ConfigError(f"namespace {s!r} must be of the form env/app")
    return NamespaceRef(env, app)


def split_display_path(p: str) -> Ref:
    """Split an absolute display path ``"/env/app/key"`` into a :class:`Ref`.

    The key may contain interior slashes (they are part of the name). Used only
    as the SDK's cross-namespace escape hatch; the server never sees the string.
    Only structure is checked (three non-empty segments); key syntax is the
    server's authority.
    """
    if not p.startswith("/"):
        raise errors.ConfigError(f"path {p!r} must start with '/'")
    parts = p[1:].split("/", 2)
    if len(parts) < 3 or not parts[0] or not parts[1] or not parts[2]:
        raise errors.ConfigError(f"path {p!r} must be of the form /env/app/key")
    return Ref(NamespaceRef(parts[0], parts[1]), parts[2])


def to_proto_namespace(ns: NamespaceRef) -> kms_pb2.NamespaceRef:
    return kms_pb2.NamespaceRef(env=ns.env, app=ns.app)


def to_proto_ref(ref: Ref) -> kms_pb2.ResourceRef:
    return kms_pb2.ResourceRef(namespace=to_proto_namespace(ref.ns), key=ref.key)
