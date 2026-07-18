"""An optional in-memory TTL read cache for parameters and secrets.

Entries are keyed by path, then by a ``(version, label)`` subkey so different
reads of the same path do not collide. Invalidation is per-path, which lets
watch events cheaply drop every cached view of a changed path.
"""

from __future__ import annotations

import threading
import time
from typing import Dict, Iterable, Optional, Tuple

from .secret import Secret

__all__ = ["Cache"]


def _subkey(version: int, label: str) -> Tuple[int, str]:
    return (version, label)


class Cache:
    """Thread-safe TTL cache. A ttl <= 0 disables caching (all reads miss)."""

    def __init__(self, ttl_seconds: float) -> None:
        self._ttl = ttl_seconds
        self._lock = threading.Lock()
        self._params: Dict[str, Dict[Tuple[int, str], Tuple[str, float]]] = {}
        self._secrets: Dict[str, Dict[Tuple[int, str], Tuple[Secret, float]]] = {}

    @property
    def enabled(self) -> bool:
        return self._ttl > 0

    def get_param(self, path: str, version: int, label: str) -> Optional[str]:
        if not self.enabled:
            return None
        with self._lock:
            by_key = self._params.get(path)
            if not by_key:
                return None
            entry = by_key.get(_subkey(version, label))
            if entry is None or time.monotonic() > entry[1]:
                return None
            return entry[0]

    def put_param(self, path: str, version: int, label: str, value: str) -> None:
        if not self.enabled:
            return
        with self._lock:
            self._params.setdefault(path, {})[_subkey(version, label)] = (
                value,
                time.monotonic() + self._ttl,
            )

    def get_secret(self, path: str, version: int, label: str) -> Optional[Secret]:
        if not self.enabled:
            return None
        with self._lock:
            by_key = self._secrets.get(path)
            if not by_key:
                return None
            entry = by_key.get(_subkey(version, label))
            if entry is None or time.monotonic() > entry[1]:
                return None
            return entry[0]

    def put_secret(self, path: str, version: int, label: str, secret: Secret) -> None:
        if not self.enabled:
            return
        with self._lock:
            self._secrets.setdefault(path, {})[_subkey(version, label)] = (
                secret,
                time.monotonic() + self._ttl,
            )

    def invalidate_param(self, path: str) -> None:
        if not self.enabled:
            return
        with self._lock:
            self._params.pop(path, None)

    def invalidate_secret(self, path: str) -> None:
        if not self.enabled:
            return
        with self._lock:
            self._secrets.pop(path, None)

    def invalidate_secrets_in_namespaces(self, namespaces: Iterable[Tuple[str, str]]) -> None:
        """Drop all secret entries in the authoritative snapshot scope."""
        if not self.enabled:
            return
        scope = set(namespaces)
        if not scope:
            return
        with self._lock:
            for path in list(self._secrets):
                parts = path.split("/", 3)
                if len(parts) == 4 and (parts[1], parts[2]) in scope:
                    self._secrets.pop(path, None)
