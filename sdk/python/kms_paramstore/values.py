"""Declarative, store-backed config fields (the Python descriptor idiom).

Declare :class:`SecretValue` / :class:`ParameterValue` as class attributes on a
config object, then resolve them all with a single :meth:`Client.resolve` call::

    class AppConfig:
        stripe_key = SecretValue("stripe-api-key", token="...")
        rate_limit = ParameterValue("rate-limit")              # hot-reloads
        log_format = ParameterValue("log-format", static=True)  # boot-time only

    cfg = AppConfig()
    client.resolve(cfg)

    cfg.stripe_key            # -> a redacting Secret
    cfg.stripe_key.value      # -> bytes plaintext (explicit access only)
    print(cfg.stripe_key)     # -> [REDACTED]
    cfg.rate_limit.get()      # -> latest value, hot-reloaded (default)

Keys are relative to the client namespace, or absolute ``/env/app/key``.
Parameters hot-reload by default; pass ``static=True`` for a boot-time-only read.

Resolution order for each field: environment override, then store fetch, then
``default``, else an error naming the key (fail fast at startup).
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
    __slots__ = ("lock", "initialized", "from_env", "static", "default", "client", "value", "callbacks")

    def __init__(self) -> None:
        self.lock = threading.RLock()
        self.initialized = False
        self.from_env = False
        self.static = False
        self.default: Optional[str] = None
        self.client: Optional["Client"] = None
        self.value = ""
        self.callbacks: List[Callable[[str, str], None]] = []

    def apply_update(self, new_val: str, present: bool) -> None:
        # A deletion reverts to the configured default so the app stops serving a
        # value the store no longer has. With no default the last-known value is
        # kept, since apps rarely want config to vanish underneath them.
        if not present:
            if self.default is None:
                return
            new_val = self.default
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
    """Handle for a resolved :class:`ParameterValue`; hot-reloads unless static."""

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

        A callback on a static or env-pinned value never fires.
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
        key: relative key (``"stripe-api-key"``) or absolute ``"/env/app/key"``.
        token: per-secret access token; for client-bound secrets it is also the
            client key share.
        env_var: optional environment variable that, when set and non-empty,
            overrides the store value (no namespace resolution is needed then).
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
                    st.secret = Secret(ev.encode("utf-8"), key=self._key)
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
                        st.secret = Secret(self._default.encode("utf-8"), key=self._key)
                        st.initialized = True
                        return
                    raise errors.ParamStoreError(f"resolve secret {self._key!r}: {err}") from err
                st.secret = sec
                st.initialized = True
                return
            if self._default is not None:
                st.secret = Secret(self._default.encode("utf-8"), key=self._key)
                st.initialized = True
                return
            raise errors.ConfigError("SecretValue has no key, env_var, or default configured")


class ParameterValue(_DescriptorBase):
    """A declarative, store-backed non-secret field.

    Hot reload is on by default: the value tracks the Subscribe stream and
    :meth:`ParameterHandle.get` always returns the latest value without an RPC.
    Every non-static field in a namespace shares that namespace's single
    subscription. Pass ``static=True`` for a boot-time-only read (no
    subscription). An
    env-pinned field never hot-reloads regardless of ``static``.

    Args:
        key: relative key (``"rate-limit"``) or absolute ``"/env/app/key"``.
        env_var: environment variable that, when set and non-empty, pins the
            value (no store fetch, no hot reload, no namespace resolution).
        default: optional fallback (development only).
        static: opt out of hot reload — read once at resolve time.
    """

    def __init__(self, key: str = "", *, env_var: Optional[str] = None, default: Optional[str] = None,
                 static: bool = False) -> None:
        super().__init__()
        self._key = key
        self._env_var = env_var or ""
        self._default = default
        self._static = static

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
            st.default = self._default  # revert target for a later deletion
            if self._env_var:
                ev = os.environ.get(self._env_var, "")
                if ev != "":
                    st.value = ev
                    st.from_env = True
                    st.static = True
                    st.initialized = True
                    if not self._static:
                        client._logf("parameter %r pinned to env %s; hot reload disabled", self._key, self._env_var)
                    return
            ref, value = self._resolve_from_store(client, timeout)
            st.value = value
            st.initialized = True
            # Hot reload on by default: register on the namespace subscription.
            if not self._static and ref is not None:
                st.static = False
                client._subs().register_param(ref, value, st.apply_update)
            else:
                st.static = True

    def _resolve_from_store(self, client: "Client", timeout: Optional[float]):
        """Return ``(ref, value)``; ``ref`` is None only for a default-only descriptor."""
        if self._key:
            # A relative key on a namespace-less client is a config error naming
            # the key; it must propagate rather than fall back to a default.
            ref = client._resolve_ref(self._key)
            try:
                value = client._get_parameter_ref(ref, timeout=timeout)
            except Exception as err:
                if self._default is not None and client._default_allowed_for_error(err):
                    client._logf("parameter %r fetch failed (%s); using default", self._key, err)
                    # Keep the resolved ref so non-static values subscribe and
                    # can replace the fallback when the missing parameter is
                    # created later.
                    return ref, self._default
                raise errors.ParamStoreError(f"resolve parameter {self._key!r}: {err}") from err
            return ref, value
        if self._default is not None:
            return None, self._default
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
