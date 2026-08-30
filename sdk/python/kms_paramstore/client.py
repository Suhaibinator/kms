"""The parameter-store client."""

from __future__ import annotations

import logging
import math
import os
import queue
import sys
import threading
from collections.abc import Iterable, Mapping
from typing import Callable, List, Optional, Tuple

import grpc

from . import errors
from ._refs import (
    NamespaceRef,
    Ref,
    parse_namespace,
    split_display_path,
    to_proto_namespace,
    to_proto_ref,
)
from .cache import Cache
from .config import DEFAULT_TIMEOUT
from ._defaults import apply_result, make_apply_request, make_verify_request, verify_result
from .models import (
    ApplicationDefaultsApplyResult,
    Page,
    Parameter,
    ParameterMetadata,
    PromoteSecretResult,
    PutResult,
    PutSecretResult,
    SecretInfo,
    VerifyDefaultEntry,
    VerifyReleaseDefaultsResult,
    WhoAmI,
    _parameter_from_proto,
    _parameter_metadata_from_proto,
    _secret_info_from_proto,
)
from ._gen import kms_pb2, kms_pb2_grpc
from .secret import Secret
from .watch import Event, WatchStatus, _SubManager

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

    When the client builds its own channel, transport security must be explicit:
    pass ``tls=...`` for TLS/mTLS, or ``insecure=True`` only for local
    development. A pre-built ``channel`` is already an explicit transport.
    """

    def __init__(
        self,
        endpoint: str = "",
        *,
        token: str = "",
        namespace: Optional[str] = None,
        tls: Optional[grpc.ChannelCredentials] = None,
        insecure: bool = False,
        cache_ttl: float = 0.0,
        cache_max_entries: int = 4096,
        timeout: float = DEFAULT_TIMEOUT,
        client_name: Optional[str] = None,
        fallback_to_defaults_on_error: bool = False,
        logger: Optional[logging.Logger] = None,
        reconcile_interval: float = 300.0,
        channel_options: Optional[List[Tuple[str, object]]] = None,
        channel: Optional[grpc.Channel] = None,
    ) -> None:
        if channel is not None and (tls is not None or insecure):
            raise errors.ConfigError("channel cannot be combined with tls or insecure=True")
        if channel is None and not endpoint:
            raise errors.ConfigError("endpoint is required")
        if channel is None and tls is not None and insecure:
            raise errors.ConfigError("tls and insecure=True are mutually exclusive")
        if channel is None and tls is None and not insecure:
            raise errors.ConfigError(
                "transport security is required; pass tls=..., or set insecure=True "
                "only for local development"
            )
        # Token is optional: an mTLS client certificate authenticates on its own
        # (the server derives the identity from the cert), and dev servers may be
        # unauthenticated. When both are present the token still travels too.
        self._token = token
        if isinstance(timeout, bool) or not isinstance(timeout, (int, float)) or not math.isfinite(timeout) or timeout <= 0:
            raise errors.ConfigError("timeout must be finite and positive")
        if isinstance(cache_ttl, bool) or not isinstance(cache_ttl, (int, float)) or not math.isfinite(cache_ttl):
            raise errors.ConfigError("cache_ttl must be finite")
        if isinstance(reconcile_interval, bool) or not isinstance(reconcile_interval, (int, float)) or not math.isfinite(reconcile_interval) or reconcile_interval <= 0:
            raise errors.ConfigError("reconcile_interval must be finite and positive")
        self._timeout = float(timeout)
        self._client_name = client_name or _default_client_name()
        self._logger = logger or logging.getLogger("kms_paramstore")
        self._fallback = fallback_to_defaults_on_error
        try:
            self._cache = Cache(cache_ttl, cache_max_entries)
        except ValueError as exc:
            raise errors.ConfigError(str(exc)) from exc
        self._reconcile_interval = reconcile_interval

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
            if tls is not None:
                self._channel = grpc.secure_channel(endpoint, tls, options=options)
            else:
                # Reaching this branch requires the explicit insecure=True
                # opt-in validated above.
                self._channel = grpc.insecure_channel(endpoint, options=options)
            self._owns_channel = True

        self._param_stub = kms_pb2_grpc.ParameterServiceStub(self._channel)
        self._secret_stub = kms_pb2_grpc.SecretServiceStub(self._channel)
        self._watch_stub = kms_pb2_grpc.WatchServiceStub(self._channel)
        self._release_stub = kms_pb2_grpc.ConfigurationReleaseServiceStub(self._channel)
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
        if threading.current_thread() is not self._cb_thread:
            self._cb_thread.join(timeout=2.0)
        if self._owns_channel:
            self._channel.close()

    @property
    def closed(self) -> bool:
        return self._closed.is_set()

    def _assert_open(self) -> None:
        if self.closed:
            raise errors.FailedPreconditionError("KMS client is closed")

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
        if timeout is not None:
            if isinstance(timeout, bool) or not isinstance(timeout, (int, float)) or not math.isfinite(timeout) or timeout <= 0:
                raise errors.ConfigError("timeout must be finite and positive")
            return float(timeout)
        return self._timeout

    def _subs(self) -> _SubManager:
        self._assert_open()
        with self._sub_lock:
            if self._sub is None:
                self._sub = _SubManager(self, reconcile_interval=self._reconcile_interval)
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
        self._assert_open()
        if not key:
            raise errors.ConfigError("key must not be empty")
        if key.startswith("/"):
            return split_display_path(key)
        return Ref(self._require_namespace(key), key)

    def _resolve_namespace_arg(self, namespace: "Optional[str | NamespaceRef]") -> NamespaceRef:
        self._assert_open()
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
        self._assert_open()
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
        return WhoAmI(identity=resp.name, kind=resp.kind, namespace=namespace, auth_method=resp.auth_method)

    # --- parameters --------------------------------------------------------

    def get_parameter(
        self,
        key: str,
        *,
        version: int = 0,
        label: str = "",
        secret_token: str = "",
        timeout: Optional[float] = None,
    ) -> str:
        """Return the value of a non-secret parameter.

        ``key`` is relative to the client namespace, or an absolute
        ``/env/app/key``. By default reads the ``current`` label; pass
        ``version`` or ``label`` to read another.
        """
        version, label = _normalize_selector(version, label)
        return self._get_parameter_ref(
            self._resolve_ref(key), version=version, label=label,
            secret_token=secret_token, timeout=timeout
        )

    def get_parameter_info(
        self,
        key: str,
        *,
        version: int = 0,
        label: str = "",
        secret_token: str = "",
        timeout: Optional[float] = None,
    ) -> Parameter:
        """Return a parameter value together with its immutable metadata."""
        version, label = _normalize_selector(version, label)
        return self._fetch_parameter_info(
            self._resolve_ref(key), version=version, label=label,
            secret_token=secret_token, timeout=timeout
        )

    def _get_parameter_ref(
        self, ref: Ref, *, version: int = 0, label: str = "", secret_token: str = "", timeout: Optional[float] = None
    ) -> str:
        if not secret_token:
            cached = self._cache.get_param(str(ref), version, label)
            if cached is not None:
                return cached
        return self._fetch_parameter(ref, version=version, label=label, secret_token=secret_token, timeout=timeout)

    def _fetch_parameter(
        self, ref: Ref, *, version: int, label: str, secret_token: str, timeout: Optional[float] = None
    ) -> str:
        parameter = self._fetch_parameter_info(
            ref, version=version, label=label, secret_token=secret_token, timeout=timeout
        )
        return parameter.value

    def _fetch_parameter_info(
        self, ref: Ref, *, version: int, label: str, secret_token: str = "", timeout: Optional[float] = None
    ) -> Parameter:
        version, label = _normalize_selector(version, label)
        generation = None if secret_token else self._cache.begin_parameter_read(str(ref))
        try:
            try:
                resp = self._param_stub.GetParameter(
                    kms_pb2.GetParameterRequest(ref=to_proto_ref(ref), version=version, label=label),
                    metadata=self._auth_metadata(secret_token),
                    timeout=self._call_timeout(timeout),
                )
            except grpc.RpcError as e:
                raise errors.map_grpc_error(e) from None
            if not resp.HasField("parameter"):
                raise errors.ParamStoreError("KMS parameter response was empty", code="internal")
            parameter = _parameter_from_proto(resp.parameter)
            _assert_read_identity("parameter", ref, parameter.env, parameter.app, parameter.key, parameter.version, version)
            if not secret_token:
                self._cache.put_param_if_unchanged(generation, version, label, parameter.value)
            return parameter
        finally:
            self._cache.end_read(generation)

    def _list_parameters_raw(
        self, ns: NamespaceRef, key_prefix: str, page_token: str,
        page_size: int = 0, timeout: Optional[float] = None,
    ):
        return self._param_stub.ListParameters(
            kms_pb2.ListParametersRequest(
                namespace=to_proto_namespace(ns), key_prefix=key_prefix, page_size=page_size, page_token=page_token
            ),
            metadata=self._auth_metadata(),
            timeout=self._call_timeout(timeout),
        )

    def list_parameters(
        self,
        namespace: "Optional[str | NamespaceRef]" = None,
        key_prefix: str = "",
        *,
        page_size: int = 0,
        page_token: str = "",
        timeout: Optional[float] = None,
    ) -> Page[Parameter]:
        """List parameters in a namespace under an optional key prefix.

        ``namespace`` defaults to the client namespace. Returns
        ``(parameters, next_page_token)``.
        """
        ns = self._resolve_namespace_arg(namespace)
        page_size = _valid_page_size(page_size)
        try:
            resp = self._list_parameters_raw(ns, key_prefix, page_token, page_size, timeout)
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        return Page(tuple(_parameter_from_proto(p) for p in resp.parameters), resp.next_page_token)

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

    def get_parameter_metadata(
        self, key: str, *, timeout: Optional[float] = None
    ) -> ParameterMetadata:
        """Return parameter metadata and version history without its value."""
        ref = self._resolve_ref(key)
        try:
            resp = self._param_stub.GetParameterMetadata(
                kms_pb2.GetParameterMetadataRequest(ref=to_proto_ref(ref)),
                metadata=self._auth_metadata(),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        if not resp.HasField("ref"):
            resp.ref.CopyFrom(to_proto_ref(ref))
        return _parameter_metadata_from_proto(resp)

    def verify_release_defaults(
        self,
        *,
        namespace: str,
        entries: Iterable[VerifyDefaultEntry | Mapping[str, object]],
        release: str = "",
        profile: str = "",
        schema_sha256: str = "",
        timeout: Optional[float] = None,
    ) -> VerifyReleaseDefaultsResult:
        """Compare value-free default hashes with the active release."""
        request, requested = make_verify_request(
            namespace=namespace, release=release, profile=profile,
            schema_sha256=schema_sha256, entries=entries,
        )
        try:
            response = self._release_stub.VerifyReleaseDefaults(
                request, metadata=self._auth_metadata(), timeout=self._call_timeout(timeout)
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        return verify_result(response, requested)

    def apply_application_defaults(
        self,
        *,
        namespace: str,
        artifact: "bytes | bytearray | str",
        overwrite: bool = False,
        execute: bool = False,
        plan_digest: str = "",
        update_definition: bool = False,
        timeout: Optional[float] = None,
    ) -> ApplicationDefaultsApplyResult:
        """Preview or execute a generated parameter-only defaults artifact."""
        request = make_apply_request(
            namespace=namespace, artifact=artifact, overwrite=overwrite,
            execute=execute, plan_digest=plan_digest, update_definition=update_definition,
        )
        try:
            response = self._admin_stub.ApplyApplicationDefaults(
                request, metadata=self._auth_metadata(), timeout=self._call_timeout(timeout)
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        return apply_result(response, expected_execute=execute)

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
        version, label = _normalize_selector(version, label)
        cache_key = str(ref)
        if not secret_token:
            cached = self._cache.get_secret(cache_key, version, label)
            if cached is not None:
                return cached
        generation = None if secret_token else self._cache.begin_secret_read(cache_key)
        try:
            try:
                resp = self._secret_stub.GetSecret(
                    kms_pb2.GetSecretRequest(ref=to_proto_ref(ref), version=version, label=label),
                    metadata=self._auth_metadata(secret_token),
                    timeout=self._call_timeout(timeout),
                )
            except grpc.RpcError as e:
                raise errors.map_grpc_error(e) from None
            if not resp.HasField("ref"):
                raise errors.ParamStoreError("KMS secret response omitted resource reference", code="internal")
            rref = resp.ref
            _assert_read_identity(
                "secret", ref, rref.namespace.env, rref.namespace.app, rref.key, resp.version, version
            )
            s = Secret(
                resp.value,
                env=rref.namespace.env or ref.ns.env,
                app=rref.namespace.app or ref.ns.app,
                key=rref.key or ref.key,
                version=resp.version,
                content_type=resp.content_type,
            )
            if not secret_token:
                self._cache.put_secret_if_unchanged(generation, version, label, s)
            return s
        finally:
            self._cache.end_read(generation)

    def put_secret(
        self,
        key: str,
        value: "bytes | bytearray | str",
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
        if not isinstance(value, (bytes, bytearray, str)):
            raise errors.ConfigError("secret value must be bytes, bytearray, or str")
        if (
            isinstance(expires_at_unix_ms, bool)
            or not isinstance(expires_at_unix_ms, int)
            or not 0 <= expires_at_unix_ms < 2**63
        ):
            raise errors.ConfigError("expires_at_unix_ms must be a non-negative int64 integer")
        if isinstance(value, str):
            value = value.encode("utf-8")
        elif isinstance(value, bytearray):
            value = bytes(value)
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

    def list_secrets(
        self,
        namespace: "Optional[str | NamespaceRef]" = None,
        key_prefix: str = "",
        *,
        page_size: int = 0,
        page_token: str = "",
        timeout: Optional[float] = None,
    ) -> Page[SecretInfo]:
        """List secret metadata only; plaintext is never returned."""
        ns = self._resolve_namespace_arg(namespace)
        try:
            resp = self._secret_stub.ListSecrets(
                kms_pb2.ListSecretsRequest(
                    namespace=to_proto_namespace(ns), key_prefix=key_prefix,
                    page_size=_valid_page_size(page_size), page_token=page_token,
                ),
                metadata=self._auth_metadata(),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        return Page(tuple(_secret_info_from_proto(s) for s in resp.secrets), resp.next_page_token)

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
        if not resp.HasField("secret"):
            raise errors.ParamStoreError("KMS secret metadata response was empty", code="internal")
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

    def set_secret_enabled(
        self,
        key: str,
        enabled: bool,
        *,
        version: int = 0,
        secret_token: str = "",
        timeout: Optional[float] = None,
    ) -> int:
        """Enable or disable one version, or all versions when ``version`` is 0."""
        _valid_uint64(version, "version")
        ref = self._resolve_ref(key)
        try:
            resp = self._secret_stub.DisableSecret(
                kms_pb2.DisableSecretRequest(ref=to_proto_ref(ref), version=version, enable=enabled),
                metadata=self._auth_metadata(secret_token),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        self._cache.invalidate_secret(str(ref))
        return resp.revision

    def destroy_secret_version(
        self,
        key: str,
        version: int,
        *,
        secret_token: str = "",
        timeout: Optional[float] = None,
    ) -> int:
        """Permanently destroy the plaintext for one exact secret version."""
        _valid_uint64(version, "version", nonzero=True)
        ref = self._resolve_ref(key)
        try:
            resp = self._secret_stub.DestroySecretVersion(
                kms_pb2.DestroySecretVersionRequest(ref=to_proto_ref(ref), version=version),
                metadata=self._auth_metadata(secret_token),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        self._cache.invalidate_secret(str(ref))
        return resp.revision

    def promote_secret_version(
        self,
        key: str,
        version: int,
        *,
        secret_token: str = "",
        timeout: Optional[float] = None,
    ) -> PromoteSecretResult:
        """Move the ``current`` label to one exact secret version."""
        _valid_uint64(version, "version", nonzero=True)
        ref = self._resolve_ref(key)
        try:
            resp = self._secret_stub.PromoteSecretVersion(
                kms_pb2.PromoteSecretVersionRequest(ref=to_proto_ref(ref), version=version),
                metadata=self._auth_metadata(secret_token),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as e:
            raise errors.map_grpc_error(e) from None
        self._cache.invalidate_secret(str(ref))
        return PromoteSecretResult(resp.current_version, resp.previous_version, resp.revision)

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

    def watch(self, callback: Callable[[Event], None]) -> Callable[[], None]:
        """Subscribe to the client's whole namespace.

        The namespace ``(env, app)`` is the unit of subscription: ``callback``
        fires for **every** change in the client's namespace, on a dedicated
        dispatch thread. There are no key patterns — an application interested
        in only some keys filters by its own convention inside ``callback``
        (e.g. ``if ev.key.startswith("billing/"): ...``). Returns a ``stop``
        function that unregisters the watcher.
        """
        return self.watch_namespace(None, callback)

    def watch_namespace(
        self, namespace: "Optional[str | NamespaceRef]", callback: Callable[[Event], None]
    ) -> Callable[[], None]:
        """Subscribe to a namespace, firing ``callback`` for every change in it.

        ``namespace`` is an ``"env/app"`` string (or :class:`NamespaceRef`), or
        ``None`` for the client's own namespace. The namespace must be one the
        client is authorized for; the server streams every change in it and the
        callback filters by its own convention. Returns a ``stop`` function.
        """
        if callback is None:
            raise errors.ConfigError("watch requires a callback")
        ns = self._resolve_namespace_arg(namespace)
        w = self._subs().register_watcher(ns, callback)
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

    @property
    def watch_status(self) -> WatchStatus:
        """An immutable, value-free snapshot of watch/reconciliation health."""
        if self._sub is None:
            return WatchStatus(
                state="stopped" if self.closed else "idle",
                reconciliation="not_started",
                current_revision=0,
                reconnect_count=0,
                namespace_count=0,
                tracked_parameter_count=0,
                watcher_count=0,
                parameter_handler_count=0,
            )
        return self._sub.status()


def _valid_uint64(value: int, name: str, *, nonzero: bool = False) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < (1 if nonzero else 0) or value >= 2**64:
        constraint = "a positive" if nonzero else "a non-negative"
        raise errors.ConfigError(f"{name} must be {constraint} uint64 integer")
    return value


def _valid_page_size(value: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0 or value > 1000:
        raise errors.ConfigError("page_size must be an integer between 0 and 1000")
    return value


def _normalize_selector(version: int, label: str) -> Tuple[int, str]:
    _valid_uint64(version, "version")
    if not isinstance(label, str):
        raise errors.ConfigError("label must be a string")
    return version, "" if version else label


def _assert_read_identity(
    kind: str, ref: Ref, env: str, app: str, key: str, returned_version: int, requested_version: int
) -> None:
    if (env, app, key) != (ref.ns.env, ref.ns.app, ref.key):
        raise errors.ParamStoreError(f"KMS {kind} response identity mismatch", code="internal")
    if requested_version and returned_version != requested_version:
        raise errors.ParamStoreError(f"KMS {kind} response version mismatch", code="internal")
