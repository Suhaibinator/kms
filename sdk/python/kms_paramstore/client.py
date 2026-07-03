"""The parameter-store client."""

from __future__ import annotations

import logging
import os
import queue
import sys
import threading
from dataclasses import dataclass
from typing import Callable, List, Optional, Tuple

import grpc

from . import errors
from ._refs import (
    NamespaceRef,
    Ref,
    parse_namespace,
    split_display_path,
    split_display_pattern,
    to_proto_namespace,
    to_proto_ref,
)
from .cache import Cache
from .config import DEFAULT_TIMEOUT, TLSConfig
from .models import (
    Parameter,
    PutResult,
    PutSecretResult,
    SecretInfo,
    _parameter_from_proto,
    _secret_info_from_proto,
)
from ._gen import kms_pb2, kms_pb2_grpc
from .secret import Secret
from .watch import Event, _SubManager

__all__ = ["Client", "WhoAmI"]

_MD_AUTHORIZATION = "authorization"
_MD_SECRET_TOKEN = "x-kms-secret-token"

# Bounds the callback dispatch queue. Value updates are applied independently of
# this queue, so a full queue only drops (best-effort) change notifications.
_CALLBACK_QUEUE_SIZE = 1024


def _default_client_name() -> str:
    argv0 = sys.argv[0] if sys.argv else ""
    if argv0:
        return os.path.basename(argv0)
    return "kms-paramstore-client"


@dataclass
class WhoAmI:
    """The identity the server sees for this connection (from ``WhoAmI``)."""

    name: str
    kind: str
    namespace: Optional[str]  # "env/app", or None when the identity is unbound
    auth_method: str  # "mtls" | "token" | ""


class Client:
    """A connection to the parameter store.

    Safe for concurrent use; share one instance for the process lifetime and
    call :meth:`close` (or use it as a context manager) to release resources.

    Keys are resolved SDK-side against the client's namespace: a relative key
    (``"rate-limit"``, ``"billing/stripe-key"``) is looked up in ``namespace``;
    an absolute ``"/env/app/key"`` addresses another namespace directly. When
    ``namespace`` is omitted it is discovered once via ``WhoAmI`` from a
    namespace-bound identity; an unbound identity plus a relative key raises
    :class:`~kms_paramstore.errors.NoNamespaceError`.
    """

    def __init__(
        self,
        endpoint: str = "",
        token: str = "",
        *,
        namespace: Optional[str] = None,
        tls: "Optional[TLSConfig | grpc.ChannelCredentials]" = None,
        cache_ttl: float = 0.0,
        timeout: float = DEFAULT_TIMEOUT,
        client_name: Optional[str] = None,
        fallback_to_defaults_on_error: bool = False,
        logger: Optional[logging.Logger] = None,
        channel_options: Optional[List[Tuple[str, object]]] = None,
        channel: Optional[grpc.Channel] = None,
    ) -> None:
        if channel is None and not endpoint:
            raise errors.ConfigError("endpoint is required")
        # Token is optional: an mTLS client certificate authenticates on its own
        # (the server derives the identity from the cert), and dev servers may be
        # unauthenticated. When both are present the token still travels too.
        self._token = token
        self._timeout = timeout if timeout and timeout > 0 else DEFAULT_TIMEOUT
        self._client_name = client_name or _default_client_name()
        self._logger = logger or logging.getLogger("kms_paramstore")
        self._fallback = fallback_to_defaults_on_error
        self._cache = Cache(cache_ttl)

        # Namespace: from config (parsed now, failing fast on a bad string) or
        # discovered lazily via WhoAmI on first relative-key use.
        self._ns_lock = threading.Lock()
        if namespace:
            self._namespace: Optional[NamespaceRef] = parse_namespace(namespace)
            self._namespace_discovered = True
        else:
            self._namespace = None
            self._namespace_discovered = False

        if channel is not None:
            self._channel = channel
            self._owns_channel = False
        else:
            options = list(channel_options or []) + [
                ("grpc.keepalive_time_ms", 30000),
                ("grpc.keepalive_timeout_ms", 10000),
                ("grpc.keepalive_permit_without_calls", 1),
            ]
            creds = tls.to_credentials() if isinstance(tls, TLSConfig) else tls
            if creds is not None:
                self._channel = grpc.secure_channel(endpoint, creds, options=options)
            else:
                self._channel = grpc.insecure_channel(endpoint, options=options)
            self._owns_channel = True

        self._param_stub = kms_pb2_grpc.ParameterServiceStub(self._channel)
        self._secret_stub = kms_pb2_grpc.SecretServiceStub(self._channel)
        self._watch_stub = kms_pb2_grpc.WatchServiceStub(self._channel)
        self._admin_stub = kms_pb2_grpc.AdminServiceStub(self._channel)

        self._closed = threading.Event()
        self._cb_queue: "queue.Queue[Callable[[], None]]" = queue.Queue(_CALLBACK_QUEUE_SIZE)
        self._cb_thread = threading.Thread(
            target=self._dispatch_callbacks, name="kms-paramstore-callbacks", daemon=True
        )
        self._cb_thread.start()

        self._sub_lock = threading.Lock()
        self._sub: Optional[_SubManager] = None

    # --- context manager ---------------------------------------------------

    def __enter__(self) -> "Client":
        return self

    def __exit__(self, *exc) -> None:
        self.close()

    def close(self) -> None:
        """Release the connection and stop all background threads. Idempotent."""
        if self._closed.is_set():
            return
        self._closed.set()
        if self._sub is not None:
            self._sub.stop()
        # Unblock the dispatch thread.
        try:
            self._cb_queue.put_nowait(lambda: None)
        except queue.Full:
            pass
        self._cb_thread.join(timeout=2.0)
        if self._owns_channel:
            self._channel.close()

    # --- internal helpers --------------------------------------------------

    def _logf(self, fmt: str, *args: object) -> None:
        try:
            self._logger.warning("kms_paramstore: " + fmt, *args)
        except Exception:
            pass

    def _auth_metadata(self, secret_token: str = "") -> List[Tuple[str, str]]:
        md: List[Tuple[str, str]] = []
        if self._token:
            md.append((_MD_AUTHORIZATION, "Bearer " + self._token))
        if secret_token:
            md.append((_MD_SECRET_TOKEN, secret_token))
        return md

    def _call_timeout(self, timeout: Optional[float]) -> float:
        if timeout is not None and timeout > 0:
            return timeout
        return self._timeout

    def _subs(self) -> _SubManager:
        with self._sub_lock:
            if self._sub is None:
                self._sub = _SubManager(self)
            return self._sub

    def _default_allowed_for_error(self, err: Exception) -> bool:
        # A declarative field may fall back to its Default only on an affirmative
        # "not found", unless the caller opted into permissive fallback.
        return isinstance(err, errors.NotFoundError) or self._fallback

    def _enqueue_callback(self, fn: Callable[[], None]) -> None:
        try:
            self._cb_queue.put_nowait(fn)
        except queue.Full:
            self._logf("callback queue full, dropping change notification")

    def _dispatch_callbacks(self) -> None:
        while True:
            fn = self._cb_queue.get()
            if self._closed.is_set():
                # Drain remaining and exit.
                return
            try:
                fn()
            except Exception as e:
                self._logf("recovered exception in change callback: %s", e)

    # --- namespace resolution ---------------------------------------------

    def _client_namespace(self) -> Optional[NamespaceRef]:
        """Return the client's namespace, discovering it once via WhoAmI.

        Returns ``None`` when the identity is unbound (WhoAmI reported no
        namespace). Cached for the client's lifetime.
        """
        with self._ns_lock:
            if not self._namespace_discovered:
                self._namespace = self._discover_namespace()
                self._namespace_discovered = True
            return self._namespace

    def _discover_namespace(self) -> Optional[NamespaceRef]:
        me = self.who_am_i()
        if me.namespace:
            env, _, app = me.namespace.partition("/")
            return NamespaceRef(env, app)
        return None

    def _require_namespace(self, key: str) -> NamespaceRef:
        ns = self._client_namespace()
        if ns is None:
            raise errors.NoNamespaceError(
                f"relative key {key!r} requires a namespace; this client has none "
                f"(pass namespace=... or use an absolute /env/app/key)"
            )
        return ns

    def _resolve_ref(self, key: str) -> Ref:
        """Resolve a user-supplied key to an explicit :class:`Ref`.

        Absolute ``"/env/app/key"`` splits directly; a relative key resolves
        against the client namespace (discovering it via WhoAmI if needed).
        """
        if not key:
            raise errors.ConfigError("key must not be empty")
        if key.startswith("/"):
            return split_display_path(key)
        return Ref(self._require_namespace(key), key)

    def _resolve_namespace_arg(self, namespace: "Optional[str | NamespaceRef]") -> NamespaceRef:
        if namespace is None:
            ns = self._client_namespace()
            if ns is None:
                raise errors.NoNamespaceError(
                    "listing requires a namespace; this client has none "
                    "(pass namespace=... or an explicit env/app)"
                )
            return ns
        if isinstance(namespace, NamespaceRef):
            return namespace
        return parse_namespace(namespace)

    def who_am_i(self, *, timeout: Optional[float] = None) -> WhoAmI:
        """Return the identity the server sees for this connection.

        Callable by any authenticated identity (no policy check). This is the
        SDK's namespace-discovery mechanism.
        """
        try:
            resp = self._admin_stub.WhoAmI(
                kms_pb2.WhoAmIRequest(),
                metadata=self._auth_metadata(),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        ns = resp.namespace
        namespace = f"{ns.env}/{ns.app}" if ns.env and ns.app else None
        return WhoAmI(name=resp.name, kind=resp.kind, namespace=namespace, auth_method=resp.auth_method)

    # --- parameters --------------------------------------------------------

    def get_parameter(
        self,
        key: str,
        *,
        version: int = 0,
        label: str = "",
        timeout: Optional[float] = None,
    ) -> str:
        """Return the value of a non-secret parameter.

        ``key`` is relative to the client namespace, or an absolute
        ``/env/app/key``. By default reads the ``current`` label; pass
        ``version`` or ``label`` to read another.
        """
        return self._get_parameter_ref(self._resolve_ref(key), version=version, label=label, timeout=timeout)

    def _get_parameter_ref(
        self, ref: Ref, *, version: int = 0, label: str = "", secret_token: str = "", timeout: Optional[float] = None
    ) -> str:
        cached = self._cache.get_param(str(ref), version, label)
        if cached is not None:
            return cached
        return self._fetch_parameter(ref, version=version, label=label, secret_token=secret_token, timeout=timeout)

    def _fetch_parameter(
        self, ref: Ref, *, version: int, label: str, secret_token: str, timeout: Optional[float] = None
    ) -> str:
        try:
            resp = self._param_stub.GetParameter(
                kms_pb2.GetParameterRequest(ref=to_proto_ref(ref), version=version, label=label),
                metadata=self._auth_metadata(secret_token),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        value = resp.parameter.value
        self._cache.put_param(str(ref), version, label, value)
        return value

    def _list_parameters_raw(self, ns: NamespaceRef, key_prefix: str, page_token: str, page_size: int = 0):
        return self._param_stub.ListParameters(
            kms_pb2.ListParametersRequest(
                namespace=to_proto_namespace(ns), key_prefix=key_prefix, page_size=page_size, page_token=page_token
            ),
            metadata=self._auth_metadata(),
            timeout=self._timeout,
        )

    def list_parameters(
        self,
        namespace: "Optional[str | NamespaceRef]" = None,
        key_prefix: str = "",
        *,
        page_size: int = 0,
        page_token: str = "",
    ) -> Tuple[List[Parameter], str]:
        """List parameters in a namespace under an optional key prefix.

        ``namespace`` defaults to the client namespace. Returns
        ``(parameters, next_page_token)``.
        """
        ns = self._resolve_namespace_arg(namespace)
        try:
            resp = self._list_parameters_raw(ns, key_prefix, page_token, page_size)
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        return [_parameter_from_proto(p) for p in resp.parameters], resp.next_page_token

    def put_parameter(
        self,
        key: str,
        value: str,
        *,
        content_type: str = "",
        metadata_json: str = "",
        timeout: Optional[float] = None,
    ) -> PutResult:
        """Create a new immutable version of a parameter (tooling use)."""
        ref = self._resolve_ref(key)
        try:
            resp = self._param_stub.PutParameter(
                kms_pb2.PutParameterRequest(
                    ref=to_proto_ref(ref), value=value, content_type=content_type, metadata_json=metadata_json
                ),
                metadata=self._auth_metadata(),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        self._cache.invalidate_param(str(ref))
        return PutResult(version=resp.version, revision=resp.revision)

    def delete_parameter(self, key: str, *, timeout: Optional[float] = None) -> int:
        """Delete a parameter and all its versions. Returns the revision."""
        ref = self._resolve_ref(key)
        try:
            resp = self._param_stub.DeleteParameter(
                kms_pb2.DeleteParameterRequest(ref=to_proto_ref(ref)),
                metadata=self._auth_metadata(),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        self._cache.invalidate_param(str(ref))
        return resp.revision

    # --- secrets -----------------------------------------------------------

    def get_secret(
        self,
        key: str,
        *,
        version: int = 0,
        label: str = "",
        secret_token: str = "",
        timeout: Optional[float] = None,
    ) -> Secret:
        """Return a secret as a redacting :class:`Secret`.

        ``key`` is relative to the client namespace, or an absolute
        ``/env/app/key``. Use ``secret_token`` for token-protected or
        client-bound secrets; for client-bound secrets it also carries the
        client key share.

        Token-gated reads bypass the client cache entirely: caching them under
        the token-less key would let later calls without ``secret_token`` read
        the plaintext from cache, skipping the server's per-secret token check,
        and would keep serving after a token rotation until the TTL expired.
        """
        ref = self._resolve_ref(key)
        cache_key = str(ref)
        if not secret_token:
            cached = self._cache.get_secret(cache_key, version, label)
            if cached is not None:
                return cached
        try:
            resp = self._secret_stub.GetSecret(
                kms_pb2.GetSecretRequest(ref=to_proto_ref(ref), version=version, label=label),
                metadata=self._auth_metadata(secret_token),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        rref = resp.ref
        s = Secret(
            resp.value,
            env=rref.namespace.env or ref.ns.env,
            app=rref.namespace.app or ref.ns.app,
            key=rref.key or ref.key,
            version=resp.version,
            content_type=resp.content_type,
        )
        if not secret_token:
            self._cache.put_secret(cache_key, version, label, s)
        return s

    def put_secret(
        self,
        key: str,
        value: bytes,
        *,
        content_type: str = "",
        metadata_json: str = "",
        client_bound: bool = False,
        generate_access_token: bool = False,
        expires_at_unix_ms: int = 0,
        secret_token: str = "",
        timeout: Optional[float] = None,
    ) -> PutSecretResult:
        """Create a new immutable version of a secret (tooling use)."""
        ref = self._resolve_ref(key)
        if isinstance(value, str):
            value = value.encode("utf-8")
        try:
            resp = self._secret_stub.PutSecret(
                kms_pb2.PutSecretRequest(
                    ref=to_proto_ref(ref),
                    value=value,
                    content_type=content_type,
                    metadata_json=metadata_json,
                    client_bound=client_bound,
                    generate_access_token=generate_access_token,
                    expires_at_unix_ms=expires_at_unix_ms,
                ),
                metadata=self._auth_metadata(secret_token),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        self._cache.invalidate_secret(str(ref))
        return PutSecretResult(version=resp.version, revision=resp.revision, access_token=resp.access_token)

    def get_secret_metadata(self, key: str, *, timeout: Optional[float] = None) -> SecretInfo:
        """Return secret-level metadata (never plaintext)."""
        ref = self._resolve_ref(key)
        try:
            resp = self._secret_stub.GetSecretMetadata(
                kms_pb2.GetSecretMetadataRequest(ref=to_proto_ref(ref)),
                metadata=self._auth_metadata(),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        return _secret_info_from_proto(resp.secret)

    def delete_secret(self, key: str, *, timeout: Optional[float] = None) -> int:
        """Delete a secret and all versions. Returns the revision."""
        ref = self._resolve_ref(key)
        try:
            resp = self._secret_stub.DeleteSecret(
                kms_pb2.DeleteSecretRequest(ref=to_proto_ref(ref)),
                metadata=self._auth_metadata(),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        self._cache.invalidate_secret(str(ref))
        return resp.revision

    # --- declarative resolution -------------------------------------------

    def resolve(self, config_obj: object, *, timeout: Optional[float] = None) -> None:
        """Resolve every declarative field on ``config_obj`` in place.

        Walks the object (and nested config objects) for :class:`SecretValue` /
        :class:`ParameterValue` descriptors and initializes each, issuing the
        independent fetches concurrently. Raises the first error encountered
        after all in-flight fetches settle.
        """
        from .values import resolve_targets  # local import avoids a cycle

        resolve_targets(self, config_obj, timeout=timeout)

    # --- watch / hot reload ------------------------------------------------

    def watch(self, pattern: str, callback: Callable[[Event], None]) -> Callable[[], None]:
        """Subscribe to a key pattern within a namespace.

        ``pattern`` is relative to the client namespace (``"billing/*"``,
        ``"*"``, or an exact key) or an absolute ``"/env/app/pattern"``.
        ``callback`` is invoked for every change on a dedicated dispatch thread.
        Returns a ``stop`` function that unregisters the watcher.
        """
        if not pattern:
            raise errors.ConfigError("watch requires a non-empty pattern")
        if callback is None:
            raise errors.ConfigError("watch requires a callback")
        if pattern.startswith("/"):
            ns, key_pattern = split_display_pattern(pattern)
        else:
            ns, key_pattern = self._require_namespace(pattern), pattern
        w = self._subs().register_watcher(ns, key_pattern, callback)
        stopped = threading.Event()

        def stop() -> None:
            if not stopped.is_set():
                stopped.set()
                self._subs().remove_watcher(w)

        return stop

    @property
    def current_revision(self) -> int:
        """The last revision applied by the subscription, or 0 if not watching."""
        if self._sub is None:
            return 0
        return self._sub._get_rev()
