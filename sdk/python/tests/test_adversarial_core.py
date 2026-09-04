from __future__ import annotations

import asyncio
import math
import threading
import time
from dataclasses import FrozenInstanceError
from types import SimpleNamespace
from unittest import mock

import grpc
import pytest

import kms_paramstore
from kms_paramstore import (
    AsyncClient, Client, ConfigError, FailedPreconditionError, Page, Parameter,
    ParameterValue, ParamStoreError, PermissionDeniedError, Secret, SecretValue,
)
from kms_paramstore._gen import kms_pb2
from kms_paramstore._refs import NamespaceRef, Ref
from kms_paramstore.async_watch import AsyncSubscriptionManager
from kms_paramstore.cache import Cache
from tests._fake_server import start_server
from tests.conftest import NS
from tests.helpers import wait_until


def test_cache_is_bounded_cleans_expired_and_never_retains_secrets():
    cache = Cache(60, max_entries=2)
    for index in range(3):
        cache.put_param(f"/prod/app/{index}", 0, "", str(index))
    assert cache.parameter_size == 2
    assert cache.get_param("/prod/app/0", 0, "") is None

    secret = Secret(b"canary", env="prod", app="app", key="secret")
    cache.put_secret(secret.path, 0, "", secret)
    assert cache.get_secret(secret.path, 0, "") is None
    assert cache.secret_size == 0

    expiring = Cache(0.001, max_entries=2)
    expiring.put_param("/prod/app/old", 0, "", "old")
    time.sleep(0.01)
    expiring.put_param("/prod/app/new", 0, "", "new")
    assert expiring.parameter_size == 1

    # Completed RPC reads release their per-path invalidation fences instead
    # of creating a second unbounded map alongside the entry cache.
    read = cache.begin_parameter_read("/prod/app/read")
    cache.end_read(read)
    assert not cache._param_generation


def test_model_default_collections_are_immutable():
    parameter = Parameter("prod", "app", "key", "value", "string", 1)
    with pytest.raises(TypeError):
        parameter.labels["current"] = 1
    with pytest.raises(FrozenInstanceError):
        parameter.value = "other"


def test_transport_configuration_rejects_ambiguity_and_nonfinite_timing():
    channel = mock.MagicMock()
    credentials = mock.MagicMock(spec=grpc.ChannelCredentials)
    with pytest.raises(ConfigError, match="channel cannot"):
        Client(channel=channel, tls=credentials)
    with pytest.raises(ConfigError, match="channel cannot"):
        Client(channel=channel, insecure=True)
    for value in (0, -1, math.inf, math.nan):
        with pytest.raises(ConfigError):
            Client(channel=channel, timeout=value)
        with pytest.raises(ConfigError):
            AsyncClient(channel=channel, reconcile_interval=value)
    assert not hasattr(kms_paramstore, "TLSConfig")


def test_sync_parameter_secret_token_bypasses_cache():
    server, address, store = start_server(whoami_namespace=NS)
    client = Client(address, namespace=NS, insecure=True, cache_ttl=60)
    try:
        client.put_parameter("gated/parameter", "one")
        assert client.get_parameter("gated/parameter") == "one"
        store.put_param("prod", "app", "gated/parameter", value="two")
        assert client.get_parameter("gated/parameter") == "one"
        assert client.get_parameter("gated/parameter", secret_token="share") == "two"
        assert client.get_parameter_info("gated/parameter", secret_token="share").value == "two"
        assert client.get_parameter("gated/parameter") == "one"
    finally:
        client.close()
        server.stop(grace=0).wait(timeout=5)


def test_sync_snapshot_and_unknown_delete_invalidate_cache():
    client = Client(channel=mock.MagicMock(), namespace=NS, cache_ttl=60)
    try:
        client._cache.put_param("/prod/app/unknown", 0, "", "stale")
        sub = client._subs()
        with sub._lock:
            sub._stream_namespaces = [("prod", "app")]
        sub._apply_snapshot(kms_pb2.Snapshot(), 4)
        assert client._cache.get_param("/prod/app/unknown", 0, "") is None

        client._cache.put_param("/prod/app/deleted", 0, "", "stale")
        sub._set_value(("prod", "app", "deleted"), "", False, 0, 5)
        assert client._cache.get_param("/prod/app/deleted", 0, "") is None
    finally:
        client.close()


def test_sync_callback_can_close_its_client():
    server, address, store = start_server(whoami_namespace=NS)
    client = Client(address, namespace=NS, insecure=True)
    closed = threading.Event()

    def callback(_event):
        client.close()
        closed.set()

    client.watch(callback)
    try:
        assert wait_until(lambda: bool(store.subs))
        store.put_param("prod", "app", "close/from-callback", value="yes")
        assert closed.wait(5), "callback-initiated close did not return"
        assert client.closed
    finally:
        client.close()
        server.stop(grace=0).wait(timeout=5)


def test_sync_list_timeout_reaches_rpc_layer():
    client = Client(channel=mock.MagicMock(), namespace=NS)
    observed = []

    def listing(ns, prefix, token, page_size=0, timeout=None):
        observed.append(timeout)
        return kms_pb2.ListParametersResponse()

    client._list_parameters_raw = listing
    try:
        client.list_parameters(timeout=0.25)
        assert observed == [0.25]
    finally:
        client.close()


class _ReflectedSecretError(grpc.RpcError):
    def __init__(self, details: str) -> None:
        self._details = details

    def code(self):
        return grpc.StatusCode.PERMISSION_DENIED

    def details(self):
        return self._details

    def __str__(self) -> str:
        return self._details


class _RejectingSecretStub:
    def __init__(self, details: str) -> None:
        self._details = details

    def __getattr__(self, _name):
        def reject(*_args, **_kwargs):
            raise _ReflectedSecretError(self._details)

        return reject


class _AsyncRejectingSecretStub:
    def __init__(self, details: str) -> None:
        self._details = details

    def __getattr__(self, _name):
        async def reject(*_args, **_kwargs):
            raise _ReflectedSecretError(self._details)

        return reject


class _PurgeStatusError(grpc.RpcError):
    def __init__(self, code: grpc.StatusCode, details: str) -> None:
        self._code = code
        self._details = details

    def code(self):
        return self._code

    def details(self):
        return self._details

    def __str__(self) -> str:
        return self._details


class _PurgeStub:
    def __init__(self, error: grpc.RpcError) -> None:
        self._error = error

    def PurgeSecretBindingCohort(self, *_args, **_kwargs):
        raise self._error


class _AsyncPurgeStub:
    def __init__(self, error: grpc.RpcError) -> None:
        self._error = error

    async def PurgeSecretBindingCohort(self, *_args, **_kwargs):
        raise self._error


def test_all_sync_secret_bearing_rpcs_discard_reflected_remote_details():
    plaintext = "secret-plaintext-reflection-canary"
    token = "secret-token-reflection-canary"
    binding_key = "binding-key-reflection-canary-000000000000"
    details = f"server reflected {plaintext} {token} {binding_key}"
    client = Client(channel=mock.MagicMock(), namespace=NS)
    client._secret_stub = _RejectingSecretStub(details)
    calls = (
        lambda: client.get_secret("key", secret_token=token, binding_key=binding_key),
        lambda: client.put_secret("key", plaintext, binding_key=binding_key),
        lambda: client.bind_secret("key", binding_key=binding_key),
        lambda: client.unbind_secret("key", binding_key=binding_key),
        lambda: client.preview_secret_binding_cohort("key", binding_key=binding_key),
        lambda: client.rotate_secret_binding_key(
            "key", binding_key=binding_key, new_binding_key=binding_key + "-new",
        ),
        lambda: client.purge_secret_binding_cohort("key", binding_key=binding_key),
    )
    try:
        for call in calls:
            with pytest.raises(PermissionDeniedError) as caught:
                call()
            rendered = str(caught.value) + repr(caught.value)
            assert caught.value.code == "permission_denied"
            assert caught.value.grpc_code is grpc.StatusCode.PERMISSION_DENIED
            assert caught.value.__context__ is None
            assert rendered == (
                "secret operation failed (permission_denied)"
                "PermissionDeniedError('secret operation failed (permission_denied)')"
            )
            assert plaintext not in rendered
            assert token not in rendered
            assert binding_key not in rendered
    finally:
        client.close()


def test_all_async_secret_bearing_rpcs_discard_reflected_remote_details():
    async def exercise() -> None:
        plaintext = "async-secret-plaintext-reflection-canary"
        token = "async-secret-token-reflection-canary"
        binding_key = "async-binding-key-reflection-canary-000000"
        details = f"server reflected {plaintext} {token} {binding_key}"
        client = AsyncClient(channel=mock.MagicMock(), namespace=NS)
        client._secret_stub = _AsyncRejectingSecretStub(details)
        calls = (
            lambda: client.get_secret("key", secret_token=token, binding_key=binding_key),
            lambda: client.put_secret("key", plaintext, binding_key=binding_key),
            lambda: client.bind_secret("key", binding_key=binding_key),
            lambda: client.unbind_secret("key", binding_key=binding_key),
            lambda: client.preview_secret_binding_cohort("key", binding_key=binding_key),
            lambda: client.rotate_secret_binding_key(
                "key", binding_key=binding_key, new_binding_key=binding_key + "-new",
            ),
            lambda: client.purge_secret_binding_cohort("key", binding_key=binding_key),
        )
        try:
            for call in calls:
                with pytest.raises(PermissionDeniedError) as caught:
                    await call()
                rendered = str(caught.value) + repr(caught.value)
                assert caught.value.code == "permission_denied"
                assert caught.value.grpc_code is grpc.StatusCode.PERMISSION_DENIED
                assert caught.value.__context__ is None
                assert plaintext not in rendered
                assert token not in rendered
                assert binding_key not in rendered
        finally:
            await client.close()

    asyncio.run(exercise())


def test_sync_purge_cleanup_pending_is_exact_public_and_drops_rpc_context():
    canonical = "secret purge committed; database artifact cleanup is pending"
    binding_key = "sync-purge-binding-key-canary-000000000000"
    client = Client(channel=mock.MagicMock(), namespace=NS)
    try:
        client._secret_stub = _PurgeStub(
            _PurgeStatusError(grpc.StatusCode.UNAVAILABLE, canonical)
        )
        with pytest.raises(kms_paramstore.PurgeCleanupPendingError) as caught:
            client.purge_secret_binding_cohort("key", binding_key=binding_key)
        assert caught.value.code == "purge_cleanup_pending"
        assert caught.value.grpc_code is grpc.StatusCode.UNAVAILABLE
        assert str(caught.value) == canonical
        assert caught.value.__cause__ is None
        assert caught.value.__context__ is None

        reflected = canonical + " " + binding_key
        client._secret_stub = _PurgeStub(
            _PurgeStatusError(grpc.StatusCode.UNAVAILABLE, reflected)
        )
        with pytest.raises(ParamStoreError) as generic:
            client.purge_secret_binding_cohort("key", binding_key=binding_key)
        assert type(generic.value) is ParamStoreError
        assert generic.value.code == "unavailable"
        assert str(generic.value) == "secret operation failed (unavailable)"
        assert generic.value.__context__ is None
        assert binding_key not in str(generic.value) + repr(generic.value)
    finally:
        client.close()


def test_async_purge_cleanup_pending_requires_unavailable_and_drops_rpc_context():
    async def exercise() -> None:
        canonical = "secret purge committed; database artifact cleanup is pending"
        binding_key = "async-purge-binding-key-canary-0000000000"
        client = AsyncClient(channel=mock.MagicMock(), namespace=NS)
        try:
            client._secret_stub = _AsyncPurgeStub(
                _PurgeStatusError(grpc.StatusCode.UNAVAILABLE, canonical)
            )
            with pytest.raises(kms_paramstore.PurgeCleanupPendingError) as caught:
                await client.purge_secret_binding_cohort(
                    "key", binding_key=binding_key
                )
            assert caught.value.code == "purge_cleanup_pending"
            assert caught.value.grpc_code is grpc.StatusCode.UNAVAILABLE
            assert str(caught.value) == canonical
            assert caught.value.__context__ is None

            client._secret_stub = _AsyncPurgeStub(
                _PurgeStatusError(grpc.StatusCode.INTERNAL, canonical)
            )
            with pytest.raises(ParamStoreError) as generic:
                await client.purge_secret_binding_cohort(
                    "key", binding_key=binding_key
                )
            assert not isinstance(
                generic.value, kms_paramstore.PurgeCleanupPendingError
            )
            assert generic.value.code == "internal"
            assert str(generic.value) == "secret operation failed (internal)"
            assert generic.value.__context__ is None
            assert binding_key not in str(generic.value) + repr(generic.value)
        finally:
            await client.close()

    asyncio.run(exercise())


def test_async_reconcile_uses_captured_fence_and_tombstones_absence():
    async def exercise() -> None:
        client = SimpleNamespace(_cache=Cache(60), _logf=lambda *args: None)
        manager = AsyncSubscriptionManager(client)
        manager._namespaces.add(("prod", "app"))
        changing = ("prod", "app", "changing")
        absent = ("prod", "app", "absent")
        manager._known[changing] = ("old", True, 2)
        manager._known[absent] = ("present", True, 0)
        manager._last_revision = 3

        async def listing(_namespace, **kwargs):
            manager._apply_parameter(changing, "live", True, 2, 5)
            return Page((Parameter("prod", "app", "changing", "stale", "string", 1),))

        client._list_parameters_ref = listing
        await manager._reconcile_once()
        assert manager._known[changing] == ("live", True, 5)
        assert manager._known[absent] == ("", False, 3)

    asyncio.run(exercise())


def test_async_unknown_event_does_not_advance_and_key_revision_never_regresses():
    async def exercise() -> None:
        client = SimpleNamespace(_cache=Cache(60), _logf=lambda *args: None)
        manager = AsyncSubscriptionManager(client)
        rk = ("prod", "app", "key")
        manager._apply_parameter(rk, "new", True, 2, 5)
        manager._apply_parameter(rk, "unversioned", True, 3, 0)
        assert manager._known[rk][2] == 5

        class Call:
            async def write(self, _request):
                return None

        await manager._handle_event(kms_pb2.SubscribeEvent(revision=999), set(), Call())
        assert manager.current_revision == 0

    asyncio.run(exercise())


def test_async_watch_run_terminates_when_cancelled_without_client_close():
    async def exercise() -> None:
        client = SimpleNamespace(_cache=Cache(0), _logf=lambda *args: None)
        manager = AsyncSubscriptionManager(client)
        never = asyncio.Event()

        async def blocked_stream():
            await never.wait()

        manager._run_stream = blocked_stream
        task = asyncio.create_task(manager._run())
        await asyncio.sleep(0)
        task.cancel()
        await asyncio.wait_for(task, timeout=0.5)
        assert task.done()
        assert not manager._closed

    asyncio.run(exercise())


def test_sync_watch_unsubscribe_remains_idempotent_after_client_close():
    client = Client(channel=mock.MagicMock(), namespace=NS)

    class Manager:
        def __init__(self):
            self.removed = 0

        def register_watcher(self, _namespace, _callback):
            return object()

        def remove_watcher(self, _watcher):
            self.removed += 1

        def stop(self):
            return None

    manager = Manager()
    client._sub = manager
    unsubscribe = client.watch(lambda _event: None)
    client.close()
    unsubscribe()
    unsubscribe()
    assert manager.removed == 1


def test_all_async_public_operations_fail_after_close():
    async def exercise() -> None:
        client = AsyncClient(channel=mock.MagicMock(), namespace=NS)
        await client.close()

        class Config:
            value = ParameterValue(default="local")

        calls = [
            lambda: client.who_am_i(),
            lambda: client.get_parameter("key"),
            lambda: client.get_parameter_info("key"),
            lambda: client.put_parameter("key", "value"),
            lambda: client.list_parameters(),
            lambda: client.delete_parameter("key"),
            lambda: client.get_parameter_metadata("key"),
            lambda: client.get_secret("key"),
            lambda: client.put_secret("key", b"value"),
            lambda: client.list_secrets(),
            lambda: client.get_secret_metadata("key"),
            lambda: client.delete_secret("key"),
            lambda: client.set_secret_enabled("key", True),
            lambda: client.destroy_secret_version("key", 1),
            lambda: client.promote_secret_version("key", 1),
            lambda: client.verify_release_defaults(namespace=NS, entries=[]),
            lambda: client.apply_application_defaults(namespace=NS, artifact=b"{}"),
            lambda: client.resolve(Config()),
            lambda: client.watch(lambda event: None),
            lambda: client.watch_namespace(NS, lambda event: None),
        ]
        for call in calls:
            with pytest.raises(FailedPreconditionError):
                await call()

    asyncio.run(exercise())


def test_async_resolve_hot_reload_token_bypass_and_callback_close():
    server, address, store = start_server(whoami_namespace=NS)

    async def wait_for(predicate, timeout=5.0):
        async def poll():
            while not predicate():
                await asyncio.sleep(0.01)

        await asyncio.wait_for(poll(), timeout)

    async def exercise() -> None:
        client = AsyncClient(address, namespace=NS, insecure=True, cache_ttl=60)
        store.put_param("prod", "app", "async/config", value="one")

        class Config:
            parameter = ParameterValue("async/config")
            secret = SecretValue(default="local")

        config = Config()
        await client.resolve(config)
        assert config.parameter.value == "one"
        assert config.secret.value == b"local"
        await wait_for(lambda: bool(store.subs))
        store.put_param("prod", "app", "async/config", value="two")
        await wait_for(lambda: config.parameter.value == "two")

        # A protected parameter read bypasses the tokenless cache.
        store.put_param("prod", "app", "async/config", value="three")
        assert await client.get_parameter("async/config", secret_token="share") == "three"

        callback_closed = asyncio.Event()

        async def callback(event):
            if event.key == "async/close":
                await client.close()
                callback_closed.set()

        await client.watch(callback)
        store.put_param("prod", "app", "async/close", value="yes")
        await wait_for(callback_closed.is_set)
        assert client.closed

    try:
        asyncio.run(exercise())
    finally:
        server.stop(grace=0).wait(timeout=5)
