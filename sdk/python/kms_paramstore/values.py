"""Declarative, store-backed config fields (the Python descriptor idiom).

Declare :class:`SecretValue` / :class:`ParameterValue` as class attributes on a
config object, then resolve them all with a single :meth:`Client.resolve` call::

    class AppConfig:
        stripe_key = SecretValue("/prod/gradethis/stripe-api-key", token="...")
        rate_limit = ParameterValue("/prod/gradethis/rate-limit", dynamic=True)

    cfg = AppConfig()
    client.resolve(cfg)

    cfg.stripe_key            # -> a redacting Secret
    cfg.stripe_key.value      # -> bytes plaintext (explicit access only)
    print(cfg.stripe_key)     # -> [REDACTED]
    cfg.rate_limit.get()      # -> latest value, hot-reloaded when dynamic

Resolution order for each field: environment override, then store fetch, then
``default``, else an error naming the path (fail fast at startup).
"""

from __future__ import annotations

import functools
import os
import threading
import weakref
from concurrent.futures import ThreadPoolExecutor
from typing import TYPE_CHECKING, Callable, Dict, List, Optional, Tuple

from . import errors
from .secret import Secret

if TYPE_CHECKING:
    from .client import Client

__all__ = ["SecretValue", "ParameterValue", "resolve_targets"]


# ---------------------------------------------------------------------------
# Per-instance mutable state
# ---------------------------------------------------------------------------


class _SecretState:
    __slots__ = ("lock", "initialized", "from_env", "secret")

    def __init__(self) -> None:
        self.lock = threading.RLock()
        self.initialized = False
        self.from_env = False
        self.secret = Secret(b"")


class _ParamState:
    __slots__ = ("lock", "initialized", "from_env", "dynamic", "client", "value", "callbacks")

    def __init__(self) -> None:
        self.lock = threading.RLock()
        self.initialized = False
        self.from_env = False
        self.dynamic = False
        self.client: Optional["Client"] = None
        self.value = ""
        self.callbacks: List[Callable[[str, str], None]] = []

    def apply_update(self, new_val: str, present: bool) -> None:
        # Deletions keep the last-known value (apps rarely want config to vanish).
        if not present:
            return
        with self.lock:
            old = self.value
            if old == new_val:
                return
            self.value = new_val
            cbs = list(self.callbacks)
            client = self.client
        if client is None:
            return
        for cb in cbs:
            client._enqueue_callback(functools.partial(cb, old, new_val))


# ---------------------------------------------------------------------------
# Parameter handle returned by descriptor access
# ---------------------------------------------------------------------------


class ParameterHandle:
    """Handle for a resolved :class:`ParameterValue`; hot-reloads when dynamic."""

    __slots__ = ("_key", "_state")

    def __init__(self, key: str, state: _ParamState) -> None:
        self._key = key
        self._state = state

    @property
    def initialized(self) -> bool:
        with self._state.lock:
            return self._state.initialized

    def get(self) -> str:
        """Return the latest value (``""`` before resolution)."""
        return self._state.value

    @property
    def value(self) -> str:
        return self._state.value

    def on_change(self, fn: Callable[[str, str], None]) -> None:
        """Register a callback ``fn(old, new)`` fired on a dispatch thread.

        A callback on a non-dynamic or env-pinned value never fires.
        """
        with self._state.lock:
            self._state.callbacks.append(fn)

    def __repr__(self) -> str:
        return f"ParameterHandle({self._key!r}={self._state.value!r})"

    def __str__(self) -> str:
        return self._state.value


# ---------------------------------------------------------------------------
# Descriptors
# ---------------------------------------------------------------------------


class _DescriptorBase:
    def __init__(self) -> None:
        self._name = ""
        self._state_key = ""
        # Fallback store for instances without a usable __dict__.
        self._weak: "weakref.WeakKeyDictionary[object, object]" = weakref.WeakKeyDictionary()

    def __set_name__(self, owner: type, name: str) -> None:
        self._name = name
        self._state_key = f"_kms_paramstore_state_{name}"

    def _new_state(self) -> object:
        raise NotImplementedError

    def _state_for(self, instance: object) -> object:
        # Prefer the instance __dict__ (no weakref requirement); fall back to a
        # WeakKeyDictionary for slotted instances.
        d = getattr(instance, "__dict__", None)
        if d is not None:
            st = d.get(self._state_key)
            if st is None:
                st = self._new_state()
                d[self._state_key] = st
            return st
        st = self._weak.get(instance)
        if st is None:
            st = self._new_state()
            self._weak[instance] = st
        return st

    def _init(self, instance: object, client: "Client", *, timeout: Optional[float] = None) -> None:
        raise NotImplementedError


class SecretValue(_DescriptorBase):
    """A declarative, store-backed secret field that resolves to a :class:`Secret`.

    Args:
        key: store path, e.g. ``"/prod/gradethis/stripe-api-key"``.
        token: per-secret access token; for client-bound secrets it is also the
            client key share.
        env_var: optional environment variable that, when set and non-empty,
            overrides the store value.
        default: optional fallback (development only).
    """

    def __init__(self, key: str = "", *, token: Optional[str] = None, env_var: Optional[str] = None,
                 default: Optional[str] = None) -> None:
        super().__init__()
        self._key = key
        self._token = token or ""
        self._env_var = env_var or ""
        self._default = default

    def _new_state(self) -> object:
        return _SecretState()

    def __get__(self, instance: object, owner: Optional[type] = None) -> "SecretValue | Secret":
        if instance is None:
            return self
        st = self._state_for(instance)
        assert isinstance(st, _SecretState)
        with st.lock:
            if not st.initialized:
                raise errors.NotInitializedError(f"secret {self._key!r} read before resolve")
            return st.secret

    def _init(self, instance: object, client: "Client", *, timeout: Optional[float] = None) -> None:
        st = self._state_for(instance)
        assert isinstance(st, _SecretState)
        with st.lock:
            if st.initialized:
                return
            if self._env_var:
                ev = os.environ.get(self._env_var, "")
                if ev != "":
                    st.secret = Secret(ev.encode("utf-8"), path=self._key)
                    st.from_env = True
                    st.initialized = True
                    client._logf("secret %r resolved from env %s (store fetch skipped)", self._key, self._env_var)
                    return
            if self._key:
                try:
                    sec = client.get_secret(self._key, secret_token=self._token, timeout=timeout)
                except Exception as err:
                    if self._default is not None and client._default_allowed_for_error(err):
                        client._logf("secret %r fetch failed (%s); using default", self._key, err)
                        st.secret = Secret(self._default.encode("utf-8"), path=self._key)
                        st.initialized = True
                        return
                    raise errors.ParamStoreError(f"resolve secret {self._key!r}: {err}") from err
                st.secret = sec
                st.initialized = True
                return
            if self._default is not None:
                st.secret = Secret(self._default.encode("utf-8"), path=self._key)
                st.initialized = True
                return
            raise errors.ConfigError("SecretValue has no key, env_var, or default configured")


class ParameterValue(_DescriptorBase):
    """A declarative, store-backed non-secret field.

    With ``dynamic=True`` the value hot-reloads over the Subscribe stream and
    :meth:`ParameterHandle.get` always returns the latest value without an RPC.
    """

    def __init__(self, key: str = "", *, env_var: Optional[str] = None, default: Optional[str] = None,
                 dynamic: bool = False) -> None:
        super().__init__()
        self._key = key
        self._env_var = env_var or ""
        self._default = default
        self._dynamic = dynamic

    def _new_state(self) -> object:
        return _ParamState()

    def __get__(self, instance: object, owner: Optional[type] = None) -> "ParameterValue | ParameterHandle":
        if instance is None:
            return self
        st = self._state_for(instance)
        assert isinstance(st, _ParamState)
        return ParameterHandle(self._key, st)

    def _init(self, instance: object, client: "Client", *, timeout: Optional[float] = None) -> None:
        st = self._state_for(instance)
        assert isinstance(st, _ParamState)
        with st.lock:
            if st.initialized:
                return
            st.client = client
            if self._env_var:
                ev = os.environ.get(self._env_var, "")
                if ev != "":
                    st.value = ev
                    st.from_env = True
                    st.initialized = True
                    if self._dynamic:
                        client._logf("parameter %r pinned to env %s; hot reload disabled", self._key, self._env_var)
                    return
            st.value = self._resolve_from_store(client, timeout)
            st.initialized = True
            if self._dynamic and self._key:
                st.dynamic = True
                client._subs().register_param(self._key, st.value, st.apply_update)

    def _resolve_from_store(self, client: "Client", timeout: Optional[float]) -> str:
        if self._key:
            try:
                return client.get_parameter(self._key, timeout=timeout)
            except Exception as err:
                if self._default is not None and client._default_allowed_for_error(err):
                    client._logf("parameter %r fetch failed (%s); using default", self._key, err)
                    return self._default
                raise errors.ParamStoreError(f"resolve parameter {self._key!r}: {err}") from err
        if self._default is not None:
            return self._default
        raise errors.ConfigError("ParameterValue has no key, env_var, or default configured")


# ---------------------------------------------------------------------------
# Resolution walk
# ---------------------------------------------------------------------------


def _has_descriptor(cls: type) -> bool:
    for klass in cls.__mro__:
        for attr in vars(klass).values():
            if isinstance(attr, _DescriptorBase):
                return True
    return False


def _collect_targets(obj: object, targets: List[Tuple[_DescriptorBase, object]], visited: set) -> None:
    if obj is None or id(obj) in visited:
        return
    visited.add(id(obj))
    for klass in type(obj).__mro__:
        for attr in vars(klass).values():
            if isinstance(attr, _DescriptorBase):
                targets.append((attr, obj))
    inst_dict: Optional[Dict[str, object]] = getattr(obj, "__dict__", None)
    if inst_dict:
        for v in list(inst_dict.values()):
            if _is_config_like(v):
                _collect_targets(v, targets, visited)


def _is_config_like(v: object) -> bool:
    if v is None or isinstance(v, (str, bytes, bytearray, int, float, bool, complex, Secret, ParameterHandle)):
        return False
    return _has_descriptor(type(v))


def resolve_targets(client: "Client", config_obj: object, *, timeout: Optional[float] = None) -> None:
    """Resolve every declarative field on ``config_obj`` concurrently."""
    targets: List[Tuple[_DescriptorBase, object]] = []
    _collect_targets(config_obj, targets, set())
    if not targets:
        return

    errors_out: List[Optional[Exception]] = [None] * len(targets)

    def run(i: int, desc: _DescriptorBase, instance: object) -> None:
        try:
            desc._init(instance, client, timeout=timeout)
        except Exception as e:  # recorded and re-raised below
            errors_out[i] = e

    # The service exposes no batch-read RPC, so "batch into as few RPCs as
    # possible" means issuing the independent fetches concurrently.
    with ThreadPoolExecutor(max_workers=min(16, len(targets))) as pool:
        futures = [pool.submit(run, i, desc, inst) for i, (desc, inst) in enumerate(targets)]
        for f in futures:
            f.result()

    for e in errors_out:
        if e is not None:
            raise e
