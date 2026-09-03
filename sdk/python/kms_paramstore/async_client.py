"""Event-loop-bound asynchronous parameter-store client."""

from __future__ import annotations

import asyncio
import logging
import math
import os
import sys
from collections.abc import Iterable, Mapping
from typing import Callable, List, Optional, Tuple

import grpc

from . import errors
from ._gen import kms_pb2, kms_pb2_grpc
from ._refs import NamespaceRef, Ref, parse_namespace, split_display_path, to_proto_namespace, to_proto_ref
from .async_watch import AsyncSubscriptionManager, AsyncWatchCallback
from .cache import Cache
from .client import (
    _assert_read_identity,
    _normalize_selector,
    _valid_page_size,
    _valid_uint64,
)
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
    WhoAmI,
    VerifyDefaultEntry,
    VerifyReleaseDefaultsResult,
    _parameter_from_proto,
    _parameter_metadata_from_proto,
    _secret_info_from_proto,
)
from .secret import Secret
from .watch import WatchStatus

__all__ = ["AsyncClient"]


class AsyncClient:
    """A separate ``grpc.aio`` client whose resources belong to one event loop."""

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
        channel: Optional[grpc.aio.Channel] = None,
    ) -> None:
        if channel is not None and (tls is not None or insecure):
            raise errors.ConfigError("channel cannot be combined with tls or insecure=True")
        if channel is None and not endpoint:
            raise errors.ConfigError("endpoint is required")
        if channel is None and tls is not None and insecure:
            raise errors.ConfigError("tls and insecure=True are mutually exclusive")
        if channel is None and tls is None and not insecure:
            raise errors.ConfigError(
                "transport security is required; pass tls=..., or set insecure=True only for local development"
            )
        if isinstance(timeout, bool) or not isinstance(timeout, (int, float)) or not math.isfinite(timeout) or timeout <= 0:
            raise errors.ConfigError("timeout must be finite and positive")
        if isinstance(cache_ttl, bool) or not isinstance(cache_ttl, (int, float)) or not math.isfinite(cache_ttl):
            raise errors.ConfigError("cache_ttl must be finite")
        if isinstance(reconcile_interval, bool) or not isinstance(reconcile_interval, (int, float)) or not math.isfinite(reconcile_interval) or reconcile_interval <= 0:
            raise errors.ConfigError("reconcile_interval must be finite and positive")
        self._token = token
        self._timeout = float(timeout)
        argv0 = sys.argv[0] if sys.argv else ""
        self._client_name = client_name or (os.path.basename(argv0) if argv0 else "kms-paramstore-client")
        self._logger = logger or logging.getLogger("kms_paramstore")
        self._fallback = fallback_to_defaults_on_error
        try:
            self._cache = Cache(cache_ttl, cache_max_entries)
        except ValueError as exc:
            raise errors.ConfigError(str(exc)) from exc
        self._reconcile_interval = reconcile_interval
        self._namespace = parse_namespace(namespace) if namespace else None
        self._namespace_discovered = bool(namespace)
        self._namespace_lock: Optional[asyncio.Lock] = None
        self._closed = False
        self._close_lock: Optional[asyncio.Lock] = None
        self._sub: Optional[AsyncSubscriptionManager] = None

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
                self._channel = grpc.aio.secure_channel(endpoint, tls, options=options)
            else:
                self._channel = grpc.aio.insecure_channel(endpoint, options=options)
            self._owns_channel = True

        self._param_stub = kms_pb2_grpc.ParameterServiceStub(self._channel)
        self._secret_stub = kms_pb2_grpc.SecretServiceStub(self._channel)
        self._watch_stub = kms_pb2_grpc.WatchServiceStub(self._channel)
        self._admin_stub = kms_pb2_grpc.AdminServiceStub(self._channel)
        self._release_stub = kms_pb2_grpc.ConfigurationReleaseServiceStub(self._channel)

    async def __aenter__(self) -> "AsyncClient":
        return self

    async def __aexit__(self, *exc) -> None:
        await self.close()

    @property
    def closed(self) -> bool:
        return self._closed

    async def close(self) -> None:
        if self._close_lock is None:
            self._close_lock = asyncio.Lock()
        async with self._close_lock:
            if self._closed:
                return
            self._closed = True
            if self._sub is not None:
                await self._sub.close()
            if self._owns_channel:
                await self._channel.close()

    def _assert_open(self) -> None:
        if self._closed:
            raise errors.FailedPreconditionError("KMS client is closed")

    def _logf(self, fmt: str, *args: object) -> None:
        try:
            self._logger.warning("kms_paramstore: " + fmt, *args)
        except Exception:
            pass

    def _default_allowed_for_error(self, err: Exception) -> bool:
        return isinstance(err, errors.NotFoundError) or self._fallback

    def _enqueue_callback(self, callback: Callable[[], object]) -> None:
        try:
            self._subs().dispatch(callback)
        except asyncio.QueueFull:
            self._logf("async callback queue full, dropping notification")

    def _auth_metadata(self) -> List[Tuple[str, str]]:
        metadata: List[Tuple[str, str]] = []
        if self._token:
            metadata.append(("authorization", "Bearer " + self._token))
        return metadata

    def _call_timeout(self, timeout: Optional[float]) -> float:
        if timeout is not None:
            if isinstance(timeout, bool) or not isinstance(timeout, (int, float)) or not math.isfinite(timeout) or timeout <= 0:
                raise errors.ConfigError("timeout must be finite and positive")
            return float(timeout)
        return self._timeout

    async def _client_namespace(self) -> Optional[NamespaceRef]:
        if self._namespace_discovered:
            return self._namespace
        if self._namespace_lock is None:
            self._namespace_lock = asyncio.Lock()
        async with self._namespace_lock:
            if not self._namespace_discovered:
                identity = await self.who_am_i()
                self._namespace = parse_namespace(identity.namespace) if identity.namespace else None
                self._namespace_discovered = True
        return self._namespace

    async def _resolve_ref(self, key: str) -> Ref:
        self._assert_open()
        if not key:
            raise errors.ConfigError("key must not be empty")
        if key.startswith("/"):
            return split_display_path(key)
        namespace = await self._client_namespace()
        if namespace is None:
            raise errors.NoNamespaceError(
                f"relative key {key!r} requires a namespace; this client has none"
            )
        return Ref(namespace, key)

    async def _resolve_namespace_arg(
        self, namespace: "Optional[str | NamespaceRef]"
    ) -> NamespaceRef:
        self._assert_open()
        if isinstance(namespace, NamespaceRef):
            return namespace
        if namespace is not None:
            return parse_namespace(namespace)
        resolved = await self._client_namespace()
        if resolved is None:
            raise errors.NoNamespaceError("operation requires a bound namespace")
        return resolved

    async def who_am_i(self, *, timeout: Optional[float] = None) -> WhoAmI:
        self._assert_open()
        try:
            response = await self._admin_stub.WhoAmI(
                kms_pb2.WhoAmIRequest(), metadata=self._auth_metadata(),
                timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        ns = response.namespace
        namespace = f"{ns.env}/{ns.app}" if ns.env and ns.app else None
        return WhoAmI(response.name, response.kind, namespace, response.auth_method)

    async def get_parameter(
        self, key: str, *, version: int = 0, label: str = "", secret_token: str = "",
        timeout: Optional[float] = None
    ) -> str:
        version, label = _normalize_selector(version, label)
        ref = await self._resolve_ref(key)
        if not secret_token:
            cached = self._cache.get_param(str(ref), version, label)
            if cached is not None:
                return cached
        return (await self._fetch_parameter_info(
            ref, version=version, label=label, secret_token=secret_token, timeout=timeout
        )).value

    async def get_parameter_info(
        self, key: str, *, version: int = 0, label: str = "", secret_token: str = "",
        timeout: Optional[float] = None
    ) -> Parameter:
        version, label = _normalize_selector(version, label)
        return await self._fetch_parameter_info(
            await self._resolve_ref(key), version=version, label=label,
            secret_token=secret_token, timeout=timeout
        )

    async def _fetch_parameter_info(
        self, ref: Ref, *, version: int = 0, label: str = "", secret_token: str = "",
        timeout: Optional[float] = None,
    ) -> Parameter:
        self._assert_open()
        version, label = _normalize_selector(version, label)
        generation = None if secret_token else self._cache.begin_parameter_read(str(ref))
        try:
            try:
                response = await self._param_stub.GetParameter(
                    kms_pb2.GetParameterRequest(ref=to_proto_ref(ref), version=version, label=label),
                    metadata=self._auth_metadata(), timeout=self._call_timeout(timeout),
                )
            except grpc.RpcError as exc:
                raise errors.map_grpc_error(exc) from None
            if not response.HasField("parameter"):
                raise errors.ParamStoreError("KMS parameter response was empty", code="internal")
            parameter = _parameter_from_proto(response.parameter)
            _assert_read_identity(
                "parameter", ref, parameter.env, parameter.app, parameter.key, parameter.version, version
            )
            if not secret_token:
                self._cache.put_param_if_unchanged(generation, version, label, parameter.value)
            return parameter
        finally:
            self._cache.end_read(generation)

    async def put_parameter(
        self, key: str, value: str, *, content_type: str = "", metadata_json: str = "",
        timeout: Optional[float] = None,
    ) -> PutResult:
        ref = await self._resolve_ref(key)
        try:
            response = await self._param_stub.PutParameter(
                kms_pb2.PutParameterRequest(
                    ref=to_proto_ref(ref), value=value, content_type=content_type,
                    metadata_json=metadata_json,
                ), metadata=self._auth_metadata(), timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        self._cache.invalidate_param(str(ref))
        return PutResult(response.version, response.revision)

    async def _list_parameters_ref(
        self, namespace: NamespaceRef, *, key_prefix: str = "", page_size: int = 0,
        page_token: str = "", timeout: Optional[float] = None,
    ) -> Page[Parameter]:
        self._assert_open()
        try:
            response = await self._param_stub.ListParameters(
                kms_pb2.ListParametersRequest(
                    namespace=to_proto_namespace(namespace), key_prefix=key_prefix,
                    page_size=_valid_page_size(page_size), page_token=page_token,
                ), metadata=self._auth_metadata(), timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        return Page(tuple(_parameter_from_proto(p) for p in response.parameters), response.next_page_token)

    async def list_parameters(
        self, namespace: "Optional[str | NamespaceRef]" = None, key_prefix: str = "", *,
        page_size: int = 0, page_token: str = "", timeout: Optional[float] = None,
    ) -> Page[Parameter]:
        return await self._list_parameters_ref(
            await self._resolve_namespace_arg(namespace), key_prefix=key_prefix,
            page_size=page_size, page_token=page_token, timeout=timeout,
        )

    async def delete_parameter(self, key: str, *, timeout: Optional[float] = None) -> int:
        ref = await self._resolve_ref(key)
        try:
            response = await self._param_stub.DeleteParameter(
                kms_pb2.DeleteParameterRequest(ref=to_proto_ref(ref)),
                metadata=self._auth_metadata(), timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        self._cache.invalidate_param(str(ref))
        return response.revision

    async def get_parameter_metadata(
        self, key: str, *, timeout: Optional[float] = None
    ) -> ParameterMetadata:
        ref = await self._resolve_ref(key)
        try:
            response = await self._param_stub.GetParameterMetadata(
                kms_pb2.GetParameterMetadataRequest(ref=to_proto_ref(ref)),
                metadata=self._auth_metadata(), timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        if not response.HasField("ref"):
            response.ref.CopyFrom(to_proto_ref(ref))
        return _parameter_metadata_from_proto(response)

    async def verify_release_defaults(
        self, *, namespace: str,
        entries: Iterable[VerifyDefaultEntry | Mapping[str, object]], release: str = "",
        profile: str = "", schema_sha256: str = "", timeout: Optional[float] = None,
    ) -> VerifyReleaseDefaultsResult:
        self._assert_open()
        request, requested = make_verify_request(
            namespace=namespace, release=release, profile=profile,
            schema_sha256=schema_sha256, entries=entries,
        )
        try:
            response = await self._release_stub.VerifyReleaseDefaults(
                request, metadata=self._auth_metadata(), timeout=self._call_timeout(timeout)
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        return verify_result(response, requested)

    async def apply_application_defaults(
        self, *, namespace: str, artifact: "bytes | bytearray | str",
        overwrite: bool = False, execute: bool = False, plan_digest: str = "",
        update_definition: bool = False, timeout: Optional[float] = None,
    ) -> ApplicationDefaultsApplyResult:
        self._assert_open()
        request = make_apply_request(
            namespace=namespace, artifact=artifact, overwrite=overwrite,
            execute=execute, plan_digest=plan_digest, update_definition=update_definition,
        )
        try:
            response = await self._admin_stub.ApplyApplicationDefaults(
                request, metadata=self._auth_metadata(), timeout=self._call_timeout(timeout)
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        return apply_result(response, expected_execute=execute)

    async def get_secret(
        self, key: str, *, version: int = 0, label: str = "", secret_token: str = "",
        timeout: Optional[float] = None,
    ) -> Secret:
        version, label = _normalize_selector(version, label)
        ref = await self._resolve_ref(key)
        if not secret_token:
            cached = self._cache.get_secret(str(ref), version, label)
            if cached is not None:
                return cached
        generation = None if secret_token else self._cache.begin_secret_read(str(ref))
        try:
            try:
                response = await self._secret_stub.GetSecret(
                    kms_pb2.GetSecretRequest(
                        ref=to_proto_ref(ref), version=version, label=label, secret_token=secret_token
                    ),
                    metadata=self._auth_metadata(), timeout=self._call_timeout(timeout),
                )
            except grpc.RpcError as exc:
                raise errors.map_grpc_error(exc) from None
            if not response.HasField("ref"):
                raise errors.ParamStoreError("KMS secret response omitted resource reference", code="internal")
            rref = response.ref
            _assert_read_identity(
                "secret", ref, rref.namespace.env, rref.namespace.app, rref.key,
                response.version, version,
            )
            secret = Secret(
                response.value, env=rref.namespace.env, app=rref.namespace.app, key=rref.key,
                version=response.version, content_type=response.content_type,
            )
            if not secret_token:
                self._cache.put_secret_if_unchanged(generation, version, label, secret)
            return secret
        finally:
            self._cache.end_read(generation)

    async def resolve(self, config_obj: object, *, timeout: Optional[float] = None) -> None:
        """Resolve every declarative value using this client's event loop."""
        self._assert_open()
        from .values import resolve_targets_async

        await resolve_targets_async(self, config_obj, timeout=timeout)

    async def put_secret(
        self, key: str, value: "bytes | bytearray | str", *, content_type: str = "",
        metadata_json: str = "", client_bound: bool = False,
        generate_access_token: bool = False, expires_at_unix_ms: int = 0,
        secret_token: str = "", timeout: Optional[float] = None,
    ) -> PutSecretResult:
        if not isinstance(value, (bytes, bytearray, str)):
            raise errors.ConfigError("secret value must be bytes, bytearray, or str")
        if isinstance(expires_at_unix_ms, bool) or not isinstance(expires_at_unix_ms, int) or not 0 <= expires_at_unix_ms < 2**63:
            raise errors.ConfigError("expires_at_unix_ms must be a non-negative int64 integer")
        ref = await self._resolve_ref(key)
        plaintext = value.encode() if isinstance(value, str) else bytes(value)
        try:
            response = await self._secret_stub.PutSecret(
                kms_pb2.PutSecretRequest(
                    ref=to_proto_ref(ref), value=plaintext, content_type=content_type,
                    metadata_json=metadata_json, client_bound=client_bound,
                    generate_access_token=generate_access_token,
                    expires_at_unix_ms=expires_at_unix_ms,
                    secret_token=secret_token,
                ), metadata=self._auth_metadata(), timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        self._cache.invalidate_secret(str(ref))
        return PutSecretResult(response.version, response.revision, response.access_token)

    async def list_secrets(
        self, namespace: "Optional[str | NamespaceRef]" = None, key_prefix: str = "", *,
        page_size: int = 0, page_token: str = "", timeout: Optional[float] = None,
    ) -> Page[SecretInfo]:
        ns = await self._resolve_namespace_arg(namespace)
        try:
            response = await self._secret_stub.ListSecrets(
                kms_pb2.ListSecretsRequest(
                    namespace=to_proto_namespace(ns), key_prefix=key_prefix,
                    page_size=_valid_page_size(page_size), page_token=page_token,
                ), metadata=self._auth_metadata(), timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        return Page(tuple(_secret_info_from_proto(s) for s in response.secrets), response.next_page_token)

    async def get_secret_metadata(self, key: str, *, timeout: Optional[float] = None) -> SecretInfo:
        ref = await self._resolve_ref(key)
        try:
            response = await self._secret_stub.GetSecretMetadata(
                kms_pb2.GetSecretMetadataRequest(ref=to_proto_ref(ref)),
                metadata=self._auth_metadata(), timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        if not response.HasField("secret"):
            raise errors.ParamStoreError("KMS secret metadata response was empty", code="internal")
        return _secret_info_from_proto(response.secret)

    async def _secret_revision_mutation(self, method, request, ref: Ref, secret_token: str, timeout) -> int:
        try:
            response = await method(
                request, metadata=self._auth_metadata(), timeout=self._call_timeout(timeout)
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        self._cache.invalidate_secret(str(ref))
        return response.revision

    async def delete_secret(self, key: str, *, timeout: Optional[float] = None) -> int:
        ref = await self._resolve_ref(key)
        return await self._secret_revision_mutation(
            self._secret_stub.DeleteSecret, kms_pb2.DeleteSecretRequest(ref=to_proto_ref(ref)),
            ref, "", timeout,
        )

    async def set_secret_enabled(
        self, key: str, enabled: bool, *, version: int = 0, secret_token: str = "",
        timeout: Optional[float] = None,
    ) -> int:
        _valid_uint64(version, "version")
        ref = await self._resolve_ref(key)
        return await self._secret_revision_mutation(
            self._secret_stub.DisableSecret,
            kms_pb2.DisableSecretRequest(ref=to_proto_ref(ref), version=version, enable=enabled),
            ref, secret_token, timeout,
        )

    async def destroy_secret_version(
        self, key: str, version: int, *, secret_token: str = "", timeout: Optional[float] = None
    ) -> int:
        _valid_uint64(version, "version", nonzero=True)
        ref = await self._resolve_ref(key)
        return await self._secret_revision_mutation(
            self._secret_stub.DestroySecretVersion,
            kms_pb2.DestroySecretVersionRequest(ref=to_proto_ref(ref), version=version),
            ref, secret_token, timeout,
        )

    async def promote_secret_version(
        self, key: str, version: int, *, secret_token: str = "", timeout: Optional[float] = None
    ) -> PromoteSecretResult:
        _valid_uint64(version, "version", nonzero=True)
        ref = await self._resolve_ref(key)
        try:
            response = await self._secret_stub.PromoteSecretVersion(
                kms_pb2.PromoteSecretVersionRequest(ref=to_proto_ref(ref), version=version),
                metadata=self._auth_metadata(), timeout=self._call_timeout(timeout),
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        self._cache.invalidate_secret(str(ref))
        return PromoteSecretResult(response.current_version, response.previous_version, response.revision)

    def _subs(self) -> AsyncSubscriptionManager:
        self._assert_open()
        if self._sub is None:
            self._sub = AsyncSubscriptionManager(self, reconcile_interval=self._reconcile_interval)
        return self._sub

    async def watch(self, callback: AsyncWatchCallback) -> Callable[[], None]:
        return self._subs().watch(await self._resolve_namespace_arg(None), callback)

    async def watch_namespace(
        self, namespace: "str | NamespaceRef", callback: AsyncWatchCallback
    ) -> Callable[[], None]:
        return self._subs().watch(await self._resolve_namespace_arg(namespace), callback)

    @property
    def current_revision(self) -> int:
        return self._sub.current_revision if self._sub is not None else 0

    @property
    def watch_status(self) -> WatchStatus:
        if self._sub is not None:
            return self._sub.status
        return WatchStatus(
            state="stopped" if self.closed else "idle", reconciliation="not_started",
            current_revision=0, reconnect_count=0, namespace_count=0,
            tracked_parameter_count=0, watcher_count=0, parameter_handler_count=0,
        )
