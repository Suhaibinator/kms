from __future__ import annotations

import asyncio
import hashlib
from typing import Dict, List, Optional

import pytest

import kms_paramstore.release as release_module
from kms_paramstore._gen import kms_pb2
from kms_paramstore._refs import NamespaceRef
from kms_paramstore.async_release import AsyncReleaseLoader, AsyncReleaseLoaderConfig
from kms_paramstore.release import ClassifiedReleaseError, ReleaseCandidateError
from kms_paramstore.secret import Secret


def _ref(key: str) -> kms_pb2.ResourceRef:
    return kms_pb2.ResourceRef(
        namespace=kms_pb2.NamespaceRef(env="prod", app="app"), key=key
    )


def _release(version: int, revision: int):
    value = f"value-{version}"
    release = kms_pb2.ConfigurationRelease(
        namespace=kms_pb2.NamespaceRef(env="prod", app="app"),
        name="runtime",
        version=version,
        schema_id="runtime",
        schema_version=1,
        entries=[
            kms_pb2.ConfigurationReleaseEntry(
                alias="setting",
                kind="parameter",
                ref=_ref("setting"),
                version=version,
                content_type="string",
                parameter_digest=hashlib.sha256(value.encode()).hexdigest(),
            ),
            kms_pb2.ConfigurationReleaseEntry(
                alias="password",
                kind="secret",
                ref=_ref("password"),
                version=version,
                content_type="string",
                has_access_token=True,
            ),
        ],
    )
    release.digest = release_module._release_digest(release)
    return release, revision


class _AsyncParameterStub:
    async def GetParameter(self, request, **_kwargs):
        value = f"value-{request.version}"
        return kms_pb2.GetParameterResponse(
            parameter=kms_pb2.Parameter(
                ref=request.ref,
                value=value,
                content_type="string",
                version=request.version,
            )
        )


class _AsyncCall:
    _CLOSED = object()

    def __init__(self, owner: "_AsyncReleaseStub") -> None:
        self.owner = owner
        self.queue: "asyncio.Queue[object]" = asyncio.Queue()
        self.cancelled = False

    async def write(self, request) -> None:
        if request.WhichOneof("request") == "register":
            self.owner.registrations.append(request.register)
        else:
            self.owner.acknowledgements.append(request.acknowledgement)

    def __aiter__(self):
        return self

    async def __anext__(self):
        item = await self.queue.get()
        if item is self._CLOSED:
            raise StopAsyncIteration
        return item

    def push(self, event) -> None:
        self.queue.put_nowait(event)

    def cancel(self) -> bool:
        self.cancelled = True
        self.queue.put_nowait(self._CLOSED)
        return True

    async def done_writing(self) -> None:
        return None


class _AsyncReleaseStub:
    def __init__(self, initial) -> None:
        self.release, self.revision = initial
        self.calls: List[_AsyncCall] = []
        self.registrations: List[object] = []
        self.acknowledgements: List[object] = []

    async def GetActiveRelease(self, _request, **_kwargs):
        release = kms_pb2.ConfigurationRelease()
        release.CopyFrom(self.release)
        return kms_pb2.GetActiveReleaseResponse(
            release=release, activation_revision=self.revision
        )

    def WatchRelease(self, **_kwargs):
        call = _AsyncCall(self)
        self.calls.append(call)
        return call

    def activate(self, release_and_revision) -> None:
        self.release, self.revision = release_and_revision
        event = kms_pb2.WatchReleaseEvent(
            activation=kms_pb2.ReleaseActivationEvent(release=self.release),
            revision=self.revision,
        )
        for call in self.calls:
            call.push(event)

    def disconnect(self) -> None:
        for call in list(self.calls):
            call.push(_AsyncCall._CLOSED)


class _AsyncClient:
    def __init__(self) -> None:
        self._channel = object()
        self._client_name = "async-tests"
        self._param_stub = _AsyncParameterStub()
        self.tokens: List[str] = []

    async def _resolve_namespace_arg(self, namespace):
        return namespace or NamespaceRef("prod", "app")

    def _auth_metadata(self, secret_token: str = ""):
        return [("x-kms-secret-token", secret_token)] if secret_token else []

    def _call_timeout(self, timeout):
        return timeout or 1.0

    async def get_secret(
        self,
        key,
        *,
        version=0,
        label="",
        secret_token="",
        timeout=None,
    ):
        del label, timeout
        self.tokens.append(secret_token)
        env, app, resource_key = key[1:].split("/", 2)
        return Secret(
            f"secret-{version}".encode(),
            env=env,
            app=app,
            key=resource_key,
            version=version,
            content_type="string",
        )


class _Prepared:
    def __init__(self, divergent=False, count=0) -> None:
        self.commits = 0
        self.aborts = 0
        self.divergent = divergent
        self.count = count

    def commit(self) -> None:
        self.commits += 1

    def abort(self) -> None:
        self.aborts += 1

    def release_divergence(self):
        return self.divergent, self.count


async def _wait_for(predicate, timeout=2.0):
    async with asyncio.timeout(timeout):
        while not predicate():
            await asyncio.sleep(0.005)


def _loader(monkeypatch, initial, **config):
    stub = _AsyncReleaseStub(initial)
    monkeypatch.setattr(
        "kms_paramstore.async_release.kms_pb2_grpc.ConfigurationReleaseServiceStub",
        lambda _channel: stub,
    )
    client = _AsyncClient()
    loader = AsyncReleaseLoader(
        client,
        AsyncReleaseLoaderConfig(
            name="runtime",
            reconcile_interval=10.0,
            reconnect_initial=0.01,
            reconnect_max=0.02,
            secret_token_provider=lambda _alias, _path, _cancel: ("token", True),
            **config,
        ),
    )
    return loader, stub, client


def test_async_loader_applies_redacts_and_acknowledges(monkeypatch):
    async def scenario():
        order: List[str] = []

        async def validate(_cancel, manifest):
            order.append("manifest")
            assert manifest.entry("password").has_access_token

        loader, stub, client = _loader(
            monkeypatch, _release(1, 10), validate_manifest=validate
        )
        prepared = _Prepared(divergent=True, count=70_000)

        async def prepare(_cancel, snapshot):
            order.append("prepare")
            assert snapshot.parameters == {"setting": "value-1"}
            assert snapshot.secrets["password"].string_value == "secret-1"
            assert "secret-1" not in repr(snapshot)
            return prepared

        task = asyncio.create_task(loader.run(prepare))
        await _wait_for(lambda: loader.status().state == "applied")
        await _wait_for(lambda: any(a.state == "applied" for a in stub.acknowledgements))
        loader.stop()
        await task
        assert prepared.commits == 1
        assert prepared.aborts == 0
        assert order[0] == "manifest"
        assert client.tokens == ["token"]
        applied = [a for a in stub.acknowledgements if a.state == "applied"][-1]
        assert applied.applied_divergent
        assert applied.divergent_field_count == 65_535

    asyncio.run(scenario())


def test_async_loader_supersedes_and_aborts_stale_candidate(monkeypatch):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, _release(1, 10))
        stale = _Prepared()
        current = _Prepared()
        preparing = asyncio.Event()

        async def prepare(cancel, snapshot):
            if snapshot.version == 1:
                preparing.set()
                await cancel.wait()
                return stale
            return current

        task = asyncio.create_task(loader.run(prepare))
        await preparing.wait()
        stub.activate(_release(2, 11))
        await _wait_for(lambda: loader.status().applied_version == 2)
        loader.stop()
        await task
        assert stale.commits == 0
        assert stale.aborts == 1
        assert current.commits == 1
        assert loader.stats().rejections["superseded"] >= 1

    asyncio.run(scenario())


def test_async_classified_failure_is_redacted_and_fetch_free(monkeypatch):
    async def scenario():
        sensitive = "secret local validation detail"

        async def validate(_cancel, _manifest):
            raise ClassifiedReleaseError("restart_required", sensitive)

        loader, stub, client = _loader(
            monkeypatch, _release(1, 10), validate_manifest=validate
        )
        with pytest.raises(ReleaseCandidateError) as caught:
            await loader.run(lambda _cancel, _snapshot: _Prepared())
        assert caught.value.category == "restart_required"
        assert sensitive not in str(caught.value)
        assert not client.tokens
        rejected = [a for a in stub.acknowledgements if a.state == "rejected"]
        assert rejected[-1].rejection_category == "restart_required"
        assert rejected[-1].diagnostic == ""

    asyncio.run(scenario())


def test_async_rejection_preserves_lkg_and_replays_acks(monkeypatch):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, _release(1, 10))
        applied = _Prepared()

        async def prepare(_cancel, snapshot):
            if snapshot.version == 2:
                raise ClassifiedReleaseError(
                    "default_mismatch", "sensitive candidate detail"
                )
            return applied

        task = asyncio.create_task(loader.run(prepare))
        await _wait_for(lambda: loader.status().applied_version == 1)
        stub.activate(_release(2, 11))
        await _wait_for(lambda: loader.status().last_failure_category == "default_mismatch")
        assert loader.status().applied_version == 1
        assert applied.commits == 1

        stub.disconnect()
        await _wait_for(lambda: len(stub.registrations) >= 2)
        replayed = [a for a in stub.acknowledgements if a.activation_revision == 11]
        assert any(a.state == "rejected" for a in replayed)
        assert all(a.diagnostic == "" for a in replayed)
        loader.stop()
        await task

    asyncio.run(scenario())


def test_async_loader_rejects_overlap_but_allows_sequential_runs(monkeypatch):
    async def scenario():
        loader, _stub, _client = _loader(monkeypatch, _release(1, 10))
        first = _Prepared()
        first_run = asyncio.create_task(loader.run(lambda _cancel, _snapshot: first))
        await _wait_for(lambda: first.commits == 1)
        with pytest.raises(Exception, match="already running"):
            await loader.run(lambda _cancel, _snapshot: _Prepared())
        loader.stop()
        await first_run

        second = _Prepared()
        second_run = asyncio.create_task(loader.run(lambda _cancel, _snapshot: second))
        await _wait_for(lambda: second.commits == 1)
        loader.stop()
        await second_run
        assert first.commits == second.commits == 1

    asyncio.run(scenario())
