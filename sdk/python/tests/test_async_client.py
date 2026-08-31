from __future__ import annotations

import asyncio
from types import SimpleNamespace
from unittest import mock

import grpc
import pytest

from kms_paramstore import AsyncClient, EventType, Page, RateLimitedError
from tests._fake_server import start_server
from tests.conftest import NS
from kms_paramstore._gen import kms_pb2


def test_async_client_core_surface_and_close():
    server, address, _store = start_server(whoami_namespace=NS)

    async def exercise() -> None:
        async with AsyncClient(address, namespace=NS, insecure=True) as client:
            put = await client.put_parameter("async/rate", "7", content_type="integer")
            assert put.version == 1
            assert await client.get_parameter("async/rate") == "7"
            info = await client.get_parameter_info("async/rate")
            assert info.path == "/prod/app/async/rate"
            metadata = await client.get_parameter_metadata("async/rate")
            assert metadata.versions[0].version == 1
            page = await client.list_parameters(key_prefix="async/")
            assert isinstance(page, Page)
            assert [item.key for item in page.items] == ["async/rate"]

            first = await client.put_secret("async/secret", "one")
            second = await client.put_secret("async/secret", b"two")
            assert (first.version, second.version) == (1, 2)
            assert (await client.get_secret("async/secret")).value == b"two"
            secrets = await client.list_secrets(key_prefix="async/")
            assert [item.key for item in secrets.items] == ["async/secret"]
            await client.set_secret_enabled("async/secret", False, version=2)
            await client.set_secret_enabled("async/secret", True, version=2)
            promoted = await client.promote_secret_version("async/secret", 1)
            assert promoted.current_version == 1
            await client.destroy_secret_version("async/secret", 2)
        assert client.closed

    try:
        asyncio.run(exercise())
    finally:
        server.stop(grace=0).wait(timeout=5)


def test_async_watch_stream_and_shutdown():
    server, address, store = start_server(whoami_namespace=NS)

    async def wait_for(predicate, timeout: float = 5.0) -> None:
        async def poll() -> None:
            while not predicate():
                await asyncio.sleep(0.01)

        await asyncio.wait_for(poll(), timeout=timeout)

    async def exercise() -> None:
        client = AsyncClient(address, namespace=NS, insecure=True, reconcile_interval=60)
        received = []
        stop = await client.watch(received.append)
        await wait_for(lambda: bool(store.subs))
        store.put_param("prod", "app", "async/watched", value="yes")
        await wait_for(lambda: any(event.key == "async/watched" for event in received))
        event = next(event for event in received if event.key == "async/watched")
        assert event.type is EventType.PUT
        assert client.watch_status.state == "connected"
        stop()
        await client.close()
        assert client.watch_status.state == "stopped"

    try:
        asyncio.run(exercise())
    finally:
        server.stop(grace=0).wait(timeout=5)


def test_async_defaults_transport_wrappers():
    async def exercise() -> None:
        client = AsyncClient(channel=mock.MagicMock())
        digest = "b" * 64

        async def verify(*args, **kwargs):
            return kms_pb2.VerifyReleaseDefaultsResponse(
                name="app", version=1, activation_revision=3, schema_matches=True,
                entries=[kms_pb2.VerifyEntryVerdict(alias="settings", verdict="match")],
                match_count=1,
            )

        async def apply(*args, **kwargs):
            return kms_pb2.ApplyApplicationDefaultsResponse(
                profile="dev", plan_digest="plan", executed=False,
                entries=[kms_pb2.DefaultsApplyEntry(
                    alias="settings", key="settings", content_type="json", status="create"
                )],
            )

        client._release_stub = SimpleNamespace(VerifyReleaseDefaults=verify)
        client._admin_stub = SimpleNamespace(ApplyApplicationDefaults=apply)
        verified = await client.verify_release_defaults(
            namespace=NS, entries=[{"alias": "settings", "content_type": "json", "sha256": digest}]
        )
        assert verified.passed

        async def rate_limited(*args, **kwargs):
            raise grpc.aio.AioRpcError(
                grpc.StatusCode.RESOURCE_EXHAUSTED, (), (), "budget spent", "budget spent"
            )

        client._release_stub = SimpleNamespace(VerifyReleaseDefaults=rate_limited)
        with pytest.raises(RateLimitedError):
            await client.verify_release_defaults(
                namespace=NS,
                entries=[{"alias": "settings", "content_type": "json", "sha256": digest}],
            )
        preview = await client.apply_application_defaults(namespace=NS, artifact="{}")
        assert preview.entries[0].status == "create"
        await client.close()

    asyncio.run(exercise())
