"""The parameter-store client."""

from __future__ import annotations

import logging
import os
import queue
import sys
import threading
from typing import Callable, List, Optional, Tuple

import grpc

from . import errors
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

__all__ = ["Client"]

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


class Client:
    """A connection to the parameter store.

    Safe for concurrent use; share one instance for the process lifetime and
    call :meth:`close` (or use it as a context manager) to release resources.
    """

    def __init__(
        self,
        endpoint: str = "",
        token: str = "",
        *,
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
        self._token = token
        self._timeout = timeout if timeout and timeout > 0 else DEFAULT_TIMEOUT
        self._client_name = client_name or _default_client_name()
        self._logger = logger or logging.getLogger("kms_paramstore")
        self._fallback = fallback_to_defaults_on_error
        self._cache = Cache(cache_ttl)

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

    # --- parameters --------------------------------------------------------

    def get_parameter(
        self,
        path: str,
        *,
        version: int = 0,
        label: str = "",
        timeout: Optional[float] = None,
    ) -> str:
        """Return the value of a non-secret parameter.

        By default reads the ``current`` label; pass ``version`` or ``label`` to
        read another.
        """
        cached = self._cache.get_param(path, version, label)
        if cached is not None:
            return cached
        return self._fetch_parameter(path, version=version, label=label, secret_token="", timeout=timeout)

    def _fetch_parameter(
        self, path: str, *, version: int, label: str, secret_token: str, timeout: Optional[float] = None
    ) -> str:
        try:
            resp = self._param_stub.GetParameter(
                kms_pb2.GetParameterRequest(path=path, version=version, label=label),
                metadata=self._auth_metadata(secret_token),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        value = resp.parameter.value
        self._cache.put_param(path, version, label, value)
        return value

    def _list_parameters_raw(self, prefix: str, page_token: str, page_size: int = 0):
        return self._param_stub.ListParameters(
            kms_pb2.ListParametersRequest(path_prefix=prefix, page_size=page_size, page_token=page_token),
            metadata=self._auth_metadata(),
            timeout=self._timeout,
        )

    def list_parameters(
        self, prefix: str = "", *, page_size: int = 0, page_token: str = ""
    ) -> Tuple[List[Parameter], str]:
        """List parameters under a prefix. Returns ``(parameters, next_page_token)``."""
        try:
            resp = self._list_parameters_raw(prefix, page_token, page_size)
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        return [_parameter_from_proto(p) for p in resp.parameters], resp.next_page_token

    def put_parameter(
        self,
        path: str,
        value: str,
        *,
        content_type: str = "",
        metadata_json: str = "",
        timeout: Optional[float] = None,
    ) -> PutResult:
        """Create a new immutable version of a parameter (tooling use)."""
        try:
            resp = self._param_stub.PutParameter(
                kms_pb2.PutParameterRequest(
                    path=path, value=value, content_type=content_type, metadata_json=metadata_json
                ),
                metadata=self._auth_metadata(),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        self._cache.invalidate_param(path)
        return PutResult(version=resp.version, revision=resp.revision)

    def delete_parameter(self, path: str, *, timeout: Optional[float] = None) -> int:
        """Delete a parameter and all its versions. Returns the revision."""
        try:
            resp = self._param_stub.DeleteParameter(
                kms_pb2.DeleteParameterRequest(path=path),
                metadata=self._auth_metadata(),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        self._cache.invalidate_param(path)
        return resp.revision

    # --- secrets -----------------------------------------------------------

    def get_secret(
        self,
        path: str,
        *,
        version: int = 0,
        label: str = "",
        secret_token: str = "",
        timeout: Optional[float] = None,
    ) -> Secret:
        """Return a secret as a redacting :class:`Secret`.

        Use ``secret_token`` for token-protected or client-bound secrets; for
        client-bound secrets it also carries the client key share.
        """
        cached = self._cache.get_secret(path, version, label)
        if cached is not None:
            return cached
        try:
            resp = self._secret_stub.GetSecret(
                kms_pb2.GetSecretRequest(path=path, version=version, label=label),
                metadata=self._auth_metadata(secret_token),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        s = Secret(
            resp.value,
            path=resp.path or path,
            version=resp.version,
            content_type=resp.content_type,
        )
        self._cache.put_secret(path, version, label, s)
        return s

    def put_secret(
        self,
        path: str,
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
        if isinstance(value, str):
            value = value.encode("utf-8")
        try:
            resp = self._secret_stub.PutSecret(
                kms_pb2.PutSecretRequest(
                    path=path,
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
        self._cache.invalidate_secret(path)
        return PutSecretResult(version=resp.version, revision=resp.revision, access_token=resp.access_token)

    def get_secret_metadata(self, path: str, *, timeout: Optional[float] = None) -> SecretInfo:
        """Return secret-level metadata (never plaintext)."""
        try:
            resp = self._secret_stub.GetSecretMetadata(
                kms_pb2.GetSecretMetadataRequest(path=path),
                metadata=self._auth_metadata(),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        return _secret_info_from_proto(resp.secret)

    def delete_secret(self, path: str, *, timeout: Optional[float] = None) -> int:
        """Delete a secret and all versions. Returns the revision."""
        try:
            resp = self._secret_stub.DeleteSecret(
                kms_pb2.DeleteSecretRequest(path=path),
                metadata=self._auth_metadata(),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        self._cache.invalidate_secret(path)
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
        """Subscribe to a path or prefix pattern (e.g. ``/prod/payments/*``).

        ``callback`` is invoked for every change on a dedicated dispatch thread.
        Returns a ``stop`` function that unregisters the watcher.
        """
        if not pattern:
            raise errors.ConfigError("watch requires a non-empty pattern")
        if callback is None:
            raise errors.ConfigError("watch requires a callback")
        w = self._subs().register_watcher(pattern, callback)
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
