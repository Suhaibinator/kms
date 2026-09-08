from __future__ import annotations

import asyncio
import hashlib
import json
import threading

import pytest

from kms_paramstore._gen import kms_pb2
from kms_paramstore.configstore import (
    Callbacks,
    ConfigBinding,
    ReleaseIdentity,
    start_async_managed_config,
    start_managed_config,
)
from kms_paramstore.release import _release_digest
from tests.helpers import wait_until
from tests.test_async_release import _AsyncClient, _AsyncReleaseStub, _wait_for
from tests.test_configstore_runtime import RuntimeConfig
from tests.test_release import _Client, _ReleaseStub


def _payload(key: str, version: int) -> str:
    return json.dumps(
        {"port": 8080, "label_set": [str(version)]}
        if key == "runtime" else {"enabled": True}
    )


def _candidate(version: int, schema: int):
    namespace = kms_pb2.NamespaceRef(env="prod", app="app")
    entries = [
        kms_pb2.ConfigurationReleaseEntry(
            alias=alias, kind="parameter",
            ref=kms_pb2.ResourceRef(namespace=namespace, key=alias),
            version=version, content_type="json",
            parameter_digest=hashlib.sha256(_payload(alias, version).encode()).hexdigest(),
        )
        for alias in ("features", "runtime")
    ]
    entries.append(kms_pb2.ConfigurationReleaseEntry(
        alias="db_password", kind="secret",
        ref=kms_pb2.ResourceRef(namespace=namespace, key="password"),
        version=version, content_type="string",
    ))
    release = kms_pb2.ConfigurationRelease(
        namespace=namespace, name="runtime", version=version,
        schema_version=schema, entries=entries,
    )
    release.digest = _release_digest(release)
    return release, version * 10


def _parameter(request):
    return kms_pb2.GetParameterResponse(parameter=kms_pb2.Parameter(
        ref=request.ref, version=request.version, content_type="json",
        value=_payload(request.ref.key, request.version),
    ))


def _selector(schema: int, digest: bool):
    return {"schema_sha256": "a" * 64} if digest else {"schema_version": schema}


def _assert_queued_status(manager, schema: int) -> None:
    status = manager.status()
    assert status.observed == ReleaseIdentity(
        namespace="prod/app", name="runtime", version=3,
        activation_revision=30, schema_version=schema,
    )
    assert status.applied.version == 1
    assert status.applied.schema_version == schema
    assert status.applied.digest


@pytest.mark.parametrize("schema,digest", [(0, False), (7, False), (7, True)])
def test_sync_managed_status_identifies_unresolved_queued_candidate(monkeypatch, schema, digest):
    stub = _ReleaseStub(_candidate(1, schema))
    stub.resolved_schema = schema
    monkeypatch.setattr(
        "kms_paramstore.release.kms_pb2_grpc.ConfigurationReleaseServiceStub", lambda _: stub
    )
    client = _Client()
    monkeypatch.setattr(client._param_stub, "GetParameter", lambda request, **_: _parameter(request))
    manager = start_managed_config(
        client, release="runtime", binding=ConfigBinding(RuntimeConfig, {}),
        callbacks=Callbacks(lambda _: None), reconcile_interval=10,
        **_selector(schema, digest),
    )
    entered = threading.Event()
    unblock = threading.Event()
    original = client._get_secret_metadata_version

    def blocked(key, *, version, **kwargs):
        result = original(key, version=version, **kwargs)
        if version >= 2:
            entered.set()
            assert unblock.wait(5), "test did not release secret metadata lookup"
        return result

    monkeypatch.setattr(client, "_get_secret_metadata_version", blocked)
    try:
        assert wait_until(lambda: bool(stub.calls and stub.registrations), timeout=2)
        stub.activate(_candidate(2, schema))
        assert entered.wait(2)
        stub.activate(_candidate(3, schema))
        assert wait_until(lambda: manager.loader.status().observed_version == 3, timeout=2)
        _assert_queued_status(manager, schema)
        unblock.set()
        assert wait_until(lambda: manager.status().applied.version == 3, timeout=2)
        assert manager.status().observed.digest == _candidate(3, schema)[0].digest
        assert len(stub.resolve_requests) == int(digest)
    finally:
        unblock.set()
        manager.stop()
        manager.wait(2)


@pytest.mark.parametrize("schema,digest", [(0, False), (7, False), (7, True)])
def test_async_managed_status_identifies_unresolved_queued_candidate(monkeypatch, schema, digest):
    async def scenario():
        stub = _AsyncReleaseStub(_candidate(1, schema))
        stub.resolved_schema = schema
        monkeypatch.setattr(
            "kms_paramstore.async_release.kms_pb2_grpc.ConfigurationReleaseServiceStub", lambda _: stub
        )
        client = _AsyncClient()

        async def parameter(request, **_):
            return _parameter(request)

        monkeypatch.setattr(client._param_stub, "GetParameter", parameter)
        manager = await start_async_managed_config(
            client, release="runtime", binding=ConfigBinding(RuntimeConfig, {}),
            callbacks=Callbacks(lambda _: None), reconcile_interval=10,
            **_selector(schema, digest),
        )
        entered = asyncio.Event()
        unblock = asyncio.Event()
        original = client._get_secret_metadata_version

        async def blocked(key, *, version, **kwargs):
            result = await original(key, version=version, **kwargs)
            if version >= 2:
                entered.set()
                await asyncio.wait_for(unblock.wait(), timeout=5)
            return result

        monkeypatch.setattr(client, "_get_secret_metadata_version", blocked)
        try:
            await _wait_for(lambda: bool(stub.calls and stub.registrations))
            stub.activate(_candidate(2, schema))
            await asyncio.wait_for(entered.wait(), timeout=2)
            stub.activate(_candidate(3, schema))
            await _wait_for(lambda: manager.loader.status().observed_version == 3)
            _assert_queued_status(manager, schema)
            unblock.set()
            await _wait_for(lambda: manager.status().applied.version == 3)
            assert manager.status().observed.digest == _candidate(3, schema)[0].digest
            assert len(stub.resolve_requests) == int(digest)
        finally:
            unblock.set()
            await manager.stop_async()
            await asyncio.wait_for(manager.wait_async(), timeout=2)

    asyncio.run(scenario())
