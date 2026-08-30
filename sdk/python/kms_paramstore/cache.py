"""An optional in-memory TTL read cache for parameters and secrets.

Entries are keyed by path, then by a ``(version, label)`` subkey so different
reads of the same path do not collide. Invalidation is per-path, which lets
watch events cheaply drop every cached view of a changed path.
"""

from __future__ import annotations

import threading
import time
import math
from dataclasses import dataclass, field
from typing import Dict, Iterable, Optional, Tuple

from .secret import Secret

__all__ = ["Cache"]


def _subkey(version: int, label: str) -> Tuple[int, str]:
    return (version, label)


@dataclass
class _GenerationState:
    epoch: object = field(default_factory=object)
    readers: int = 0


@dataclass
class _ReadGeneration:
    kind: str
    path: str
    state: _GenerationState
    epoch: object
    released: bool = False


class Cache:
    """Thread-safe TTL cache. A ttl <= 0 disables caching (all reads miss)."""

    def __init__(self, ttl_seconds: float, max_entries: int = 4096) -> None:
        if not isinstance(ttl_seconds, (int, float)) or not math.isfinite(ttl_seconds):
            raise ValueError("cache ttl must be finite")
        if isinstance(max_entries, bool) or not isinstance(max_entries, int) or max_entries <= 0:
            raise ValueError("cache max_entries must be a positive integer")
        self._ttl = ttl_seconds
        self._max_entries = max_entries
        self._lock = threading.Lock()
        self._params: Dict[str, Dict[Tuple[int, str], Tuple[str, float]]] = {}
        self._secrets: Dict[str, Dict[Tuple[int, str], Tuple[Secret, float]]] = {}
        self._param_generation: Dict[str, _GenerationState] = {}
        self._secret_generation: Dict[str, _GenerationState] = {}

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
            key = _subkey(version, label)
            entry = by_key.get(key)
            if entry is None:
                return None
            if time.monotonic() > entry[1]:
                by_key.pop(key, None)
                if not by_key:
                    self._params.pop(path, None)
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
            self._evict(self._params)

    def begin_parameter_read(self, path: str) -> Optional[_ReadGeneration]:
        return self._begin_read(self._param_generation, "parameter", path)

    def put_param_if_unchanged(
        self, token: Optional[_ReadGeneration], version: int, label: str, value: str
    ) -> None:
        if token is None:
            return
        with self._lock:
            if not self._is_current(self._param_generation, token, "parameter"):
                return
            self._params.setdefault(token.path, {})[_subkey(version, label)] = (
                value,
                time.monotonic() + self._ttl,
            )
            self._evict(self._params)

    def get_secret(self, path: str, version: int, label: str) -> Optional[Secret]:
        if not self.enabled:
            return None
        with self._lock:
            by_key = self._secrets.get(path)
            if not by_key:
                return None
            key = _subkey(version, label)
            entry = by_key.get(key)
            if entry is None:
                return None
            if time.monotonic() > entry[1]:
                by_key.pop(key, None)
                if not by_key:
                    self._secrets.pop(path, None)
                return None
            return entry[0].clone()

    def put_secret(self, path: str, version: int, label: str, secret: Secret) -> None:
        if not self.enabled:
            return
        with self._lock:
            self._secrets.setdefault(path, {})[_subkey(version, label)] = (
                secret.clone(),
                time.monotonic() + self._ttl,
            )
            self._evict(self._secrets)

    def begin_secret_read(self, path: str) -> Optional[_ReadGeneration]:
        return self._begin_read(self._secret_generation, "secret", path)

    def put_secret_if_unchanged(
        self, token: Optional[_ReadGeneration], version: int, label: str, secret: Secret
    ) -> None:
        if token is None:
            return
        with self._lock:
            if not self._is_current(self._secret_generation, token, "secret"):
                return
            self._secrets.setdefault(token.path, {})[_subkey(version, label)] = (
                secret.clone(),
                time.monotonic() + self._ttl,
            )
            self._evict(self._secrets)

    def end_read(self, token: Optional[_ReadGeneration]) -> None:
        """Release invalidation bookkeeping after an RPC settles."""
        if token is None:
            return
        with self._lock:
            if token.released:
                return
            token.released = True
            token.state.readers -= 1
            generations = (
                self._param_generation if token.kind == "parameter" else self._secret_generation
            )
            if token.state.readers == 0 and generations.get(token.path) is token.state:
                generations.pop(token.path, None)

    def invalidate_param(self, path: str) -> None:
        if not self.enabled:
            return
        with self._lock:
            state = self._param_generation.get(path)
            if state is not None:
                state.epoch = object()
            self._params.pop(path, None)

    def invalidate_secret(self, path: str) -> None:
        if not self.enabled:
            return
        with self._lock:
            state = self._secret_generation.get(path)
            if state is not None:
                state.epoch = object()
            self._secrets.pop(path, None)

    def invalidate_secrets_in_namespaces(self, namespaces: Iterable[Tuple[str, str]]) -> None:
        """Drop all secret entries in the authoritative snapshot scope."""
        if not self.enabled:
            return
        scope = set(namespaces)
        if not scope:
            return
        with self._lock:
            for path in set(self._secrets) | set(self._secret_generation):
                parts = path.split("/", 3)
                if len(parts) == 4 and (parts[1], parts[2]) in scope:
                    state = self._secret_generation.get(path)
                    if state is not None:
                        state.epoch = object()
                    self._secrets.pop(path, None)

    def invalidate_parameters_in_namespaces(self, namespaces: Iterable[Tuple[str, str]]) -> None:
        """Drop every parameter selector and fence in-flight reads in scope."""
        scope = set(namespaces)
        if not scope:
            return
        with self._lock:
            for path in set(self._params) | set(self._param_generation):
                parts = path.split("/", 3)
                if len(parts) == 4 and (parts[1], parts[2]) in scope:
                    state = self._param_generation.get(path)
                    if state is not None:
                        state.epoch = object()
                    self._params.pop(path, None)

    @property
    def parameter_size(self) -> int:
        with self._lock:
            return sum(len(entries) for entries in self._params.values())

    @property
    def secret_size(self) -> int:
        with self._lock:
            return sum(len(entries) for entries in self._secrets.values())

    def _evict(self, cache) -> None:
        now = time.monotonic()
        for path, entries in list(cache.items()):
            for key, (_value, expires_at) in list(entries.items()):
                if expires_at <= now:
                    entries.pop(key, None)
            if not entries:
                cache.pop(path, None)
        count = sum(len(entries) for entries in cache.values())
        while count > self._max_entries:
            path = next(iter(cache))
            entries = cache[path]
            key = next(iter(entries))
            entries.pop(key)
            count -= 1
            if not entries:
                cache.pop(path, None)

    def _begin_read(
        self, generations: Dict[str, _GenerationState], kind: str, path: str
    ) -> Optional[_ReadGeneration]:
        if not self.enabled:
            return None
        with self._lock:
            state = generations.get(path)
            if state is None:
                state = _GenerationState()
                generations[path] = state
            state.readers += 1
            return _ReadGeneration(kind, path, state, state.epoch)

    @staticmethod
    def _is_current(
        generations: Dict[str, _GenerationState], token: _ReadGeneration, kind: str
    ) -> bool:
        return (
            not token.released
            and token.kind == kind
            and generations.get(token.path) is token.state
            and token.state.epoch is token.epoch
        )
