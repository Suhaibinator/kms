from __future__ import annotations

import asyncio
import hashlib
import math
from typing import Dict, List, Optional

import grpc
import pytest

import kms_paramstore
import kms_paramstore.release as release_module
from kms_paramstore._gen import kms_pb2
from kms_paramstore._refs import NamespaceRef
from kms_paramstore.async_release import AsyncReleaseLoader, AsyncReleaseLoaderConfig
from kms_paramstore.release import ClassifiedReleaseError, ReleaseCandidateError
from kms_paramstore.release import ReleaseCommitError, ReleaseStartupError
from kms_paramstore.secret import Secret


class _RpcFailure(grpc.RpcError):
    def __init__(self, code: grpc.StatusCode, details: str) -> None:
        self._code = code
        self._details = details

    def code(self):
        return self._code

    def details(self):
        return self._details


def _ref(key: str) -> kms_pb2.ResourceRef:
    return kms_pb2.ResourceRef(
        namespace=kms_pb2.NamespaceRef(env="prod", app="app"), key=key
    )


def _release(version: int, revision: int, schema_version: int = 1):
    value = f"value-{version}"
    release = kms_pb2.ConfigurationRelease(
        namespace=kms_pb2.NamespaceRef(env="prod", app="app"),
        name="runtime",
        version=version,
        schema_version=schema_version,
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
        self.half_closed = False
        self.drained = False

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
            self.drained = True
            raise StopAsyncIteration
        if isinstance(item, BaseException):
            raise item
        return item

    def push(self, event) -> None:
        self.queue.put_nowait(event)

    def cancel(self) -> bool:
        self.cancelled = True
        self.queue.put_nowait(self._CLOSED)
        return True

    async def done_writing(self) -> None:
        if not self.half_closed:
            self.half_closed = True
            self.queue.put_nowait(self._CLOSED)


class _AsyncReleaseStub:
    def __init__(self, initial) -> None:
        if initial is None:
            self.release, self.revision = kms_pb2.ConfigurationRelease(), 0
        else:
            self.release, self.revision = initial
        self.calls: List[_AsyncCall] = []
        self.registrations: List[object] = []
        self.acknowledgements: List[object] = []
        self.active_requests: List[object] = []
        self.resolve_requests: List[object] = []
        self.resolved_schema = 1
        self.inactive = initial is None

    async def ResolveReleaseSchema(self, request, **_kwargs):
        self.resolve_requests.append(request)
        return kms_pb2.ResolveReleaseSchemaResponse(schema_version=self.resolved_schema)

    async def GetActiveRelease(self, request, **_kwargs):
        self.active_requests.append(request)
        if self.inactive:
            raise _RpcFailure(grpc.StatusCode.NOT_FOUND, "track has no active release")
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
        self.inactive = False
        event = kms_pb2.WatchReleaseEvent(
            activation=kms_pb2.ReleaseActivationEvent(release=self.release),
            revision=self.revision,
        )
        for call in self.calls:
            call.push(event)

    def disconnect(self) -> None:
        for call in list(self.calls):
            call.push(_AsyncCall._CLOSED)

    def reject_watch(self, code: grpc.StatusCode) -> None:
        for call in list(self.calls):
            call.push(_RpcFailure(code, "watch rejected"))


class _AsyncClient:
    def __init__(self) -> None:
        self._channel = object()
        self._client_name = "async-tests"
        self._param_stub = _AsyncParameterStub()
        self.binding_keys: List[str] = []
        self.bound = False
        self.state = "enabled"
        self.destroyed_at_unix_ms = 0
        self.expires_at_unix_ms = 0
        self.metadata_versions: List[int] = []

    async def _resolve_namespace_arg(self, namespace):
        return namespace or NamespaceRef("prod", "app")

    def _auth_metadata(self):
        return []

    def _call_timeout(self, timeout):
        return timeout or 1.0

    async def get_secret(
        self,
        key,
        *,
        version=0,
        label="",
        binding_key="",
        timeout=None,
    ):
        del label, timeout
        self.binding_keys.append(binding_key)
        if self.bound and binding_key != "async-binding-key":
            raise RuntimeError("credential unavailable")
        env, app, resource_key = key[1:].split("/", 2)
        return Secret(
            f"secret-{version}".encode(),
            env=env,
            app=app,
            key=resource_key,
            version=version,
            content_type="string",
        )

    async def _get_secret_metadata_version(self, key, *, version, timeout=None):
        del timeout
        self.metadata_versions.append(version)
        env, app, resource_key = key[1:].split("/", 2)
        return kms_paramstore.models.SecretInfo(
            env=env, app=app, key=resource_key, content_type="string",
            bound=self.bound,
            versions=tuple(
                kms_paramstore.models.SecretVersion(
                    version=version, state=self.state, bound=self.bound,
                    destroyed_at_unix_ms=self.destroyed_at_unix_ms,
                    expires_at_unix_ms=self.expires_at_unix_ms,

                ) for version in range(1, 10)
            ),
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
    async def poll():
        while not predicate():
            await asyncio.sleep(0.005)

    await asyncio.wait_for(poll(), timeout=timeout)


def _loader(monkeypatch, initial, **config):
    stub = _AsyncReleaseStub(initial)
    monkeypatch.setattr(
        "kms_paramstore.async_release.kms_pb2_grpc.ConfigurationReleaseServiceStub",
        lambda _channel: stub,
    )
    client = _AsyncClient()
    settings = {
        "name": "runtime",
        "schema_version": 1,
        "reconcile_interval": 10.0,
        "reconnect_initial": 0.01,
        "reconnect_max": 0.02,
    }
    settings.update(config)
    loader = AsyncReleaseLoader(
        client,
        AsyncReleaseLoaderConfig(**settings),
    )
    return loader, stub, client


def test_async_schema_selector_requires_exactly_one_valid_track() -> None:
    with pytest.raises(kms_paramstore.ConfigError, match="exactly one"):
        AsyncReleaseLoaderConfig(name="runtime")
    with pytest.raises(kms_paramstore.ConfigError, match="exactly one"):
        AsyncReleaseLoaderConfig(
            name="runtime", schema_version=0, schema_sha256="a" * 64
        )
    assert AsyncReleaseLoaderConfig(name="runtime", schema_version=0).schema_version == 0


@pytest.mark.parametrize("digest", ["A" * 64, "a" * 63 + "F"])
def test_async_schema_digest_selector_rejects_uppercase_before_start(digest: str) -> None:
    with pytest.raises(kms_paramstore.ConfigError, match="lowercase"):
        AsyncReleaseLoaderConfig(name="runtime", schema_sha256=digest)


def test_async_digest_selector_resolves_once_and_pins_every_transport(monkeypatch):
    async def scenario():
        loader, stub, _client = _loader(
            monkeypatch, _release(1, 10), schema_version=None,
            schema_sha256="a" * 64,
        )
        task = asyncio.create_task(loader.run(lambda _cancel, _snapshot: _Prepared()))
        await _wait_for(lambda: loader.status().state == "applied")
        loader.stop()
        await task
        assert len(stub.resolve_requests) == 1
        assert stub.resolve_requests[0].schema_sha256 == "a" * 64
        assert all(request.schema_version == 1 for request in stub.active_requests)
        assert stub.registrations and stub.acknowledgements
        assert all(request.schema_version == 1 for request in stub.registrations)
        assert all(request.schema_version == 1 for request in stub.acknowledgements)

    asyncio.run(scenario())


def _ack_rejection(loader, acknowledgement, *, sequence=None, revision=999):
    return kms_pb2.WatchReleaseEvent(
        acknowledgement_rejected=kms_pb2.ReleaseAcknowledgementRejectedEvent(
            namespace=kms_pb2.NamespaceRef(env="prod", app="app"),
            name="runtime",
            schema_version=1,
            version=acknowledgement.version,
            activation_revision=acknowledgement.activation_revision,
            client_name=loader.client_name,
            instance_id=loader.instance_id,
            state=acknowledgement.state,
            sequence=sequence if sequence is not None else acknowledgement.sequence,
            reason="activation_unavailable",
        ),
        revision=revision,
    )


def test_async_delayed_ack_rejection_preserves_newer_generation(monkeypatch):
    loader, _stub, _client = _loader(monkeypatch, _release(1, 10))
    loader._namespace = NamespaceRef("prod", "app")
    first = release_module._Candidate(*_release(1, 10))
    second = release_module._Candidate(*_release(2, 20))
    loader._ack(first, "received")
    old = loader._ack_latest["received"][1]
    assert old.sequence > 0
    loader._ack(second, "received")
    newer = loader._ack_latest["received"][1]
    loader._discard_rejected_ack(_ack_rejection(loader, old).acknowledgement_rejected)
    assert loader._ack_latest["received"][1] is newer
    foreign = _ack_rejection(loader, newer)
    foreign.acknowledgement_rejected.instance_id = "other"
    loader._discard_rejected_ack(foreign.acknowledgement_rejected)
    assert loader._ack_latest["received"][1] is newer
    loader._discard_rejected_ack(_ack_rejection(loader, newer).acknowledgement_rejected)
    assert "received" not in loader._ack_latest


def test_async_ack_rejection_does_not_advance_cursor_or_block_activation(monkeypatch):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, _release(1, 10))
        prepared = []

        def prepare(_cancel, snapshot):
            prepared.append(snapshot.version)
            return _Prepared()

        task = asyncio.create_task(loader.run(prepare))
        await _wait_for(lambda: prepared == [1])
        await _wait_for(lambda: bool(stub.acknowledgements and stub.calls))
        acknowledgement = stub.acknowledgements[-1]
        stub.calls[-1].push(_ack_rejection(loader, acknowledgement, revision=10_000))
        stub.activate(_release(2, 11))
        await _wait_for(lambda: prepared == [1, 2])
        loader.stop()
        await task
        assert loader._last_seen_revision == 11

    asyncio.run(scenario())


def test_async_loader_waits_on_inactive_track_then_applies_first_activation(monkeypatch):
    async def scenario():
        loader, stub, _client = _loader(
            monkeypatch,
            None,
            reconcile_interval=0.02,
            schema_version=None,
            schema_sha256="a" * 64,
        )
        prepared = _Prepared()
        task = asyncio.create_task(loader.run(lambda _cancel, _snapshot: prepared))
        await _wait_for(lambda: bool(stub.registrations))
        await _wait_for(lambda: len(stub.active_requests) >= 2)
        assert prepared.commits == 0
        stub.activate(_release(1, 1))
        await _wait_for(lambda: prepared.commits == 1)
        loader.stop()
        await task
        assert len(stub.resolve_requests) == 1
        assert stub.registrations[0].last_seen_revision == 0
        assert stub.registrations[0].schema_version == 1

    asyncio.run(scenario())


def test_async_loader_can_cancel_while_waiting_on_inactive_track(monkeypatch):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, None)
        external_stop = asyncio.Event()
        task = asyncio.create_task(
            loader.run(lambda _cancel, _snapshot: _Prepared(), stop_event=external_stop)
        )
        await _wait_for(lambda: bool(stub.registrations))
        external_stop.set()
        await asyncio.wait_for(task, timeout=2)

    asyncio.run(scenario())


@pytest.mark.parametrize(
    ("code", "error_type", "initial"),
    [
        (grpc.StatusCode.NOT_FOUND, kms_paramstore.NotFoundError, None),
        (
            grpc.StatusCode.PERMISSION_DENIED,
            kms_paramstore.PermissionDeniedError,
            _release(1, 10),
        ),
    ],
)
def test_async_loader_surfaces_terminal_watch_rejection(
    monkeypatch, code, error_type, initial
):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, initial)
        task = asyncio.create_task(
            loader.run(lambda _cancel, _snapshot: _Prepared())
        )
        await _wait_for(lambda: bool(stub.registrations))
        if initial is not None:
            await _wait_for(lambda: loader.status().state == "applied")
        stub.reject_watch(code)
        with pytest.raises(error_type):
            await task

    asyncio.run(scenario())


def test_async_foreign_schema_event_cannot_replace_pending_candidate(monkeypatch):
    matching = _release(1, 10)
    foreign = _release(2, 20)
    foreign[0].schema_version = 2
    foreign[0].digest = release_module._release_digest(foreign[0])
    loader, _stub, _client = _loader(monkeypatch, matching)
    loader._namespace = NamespaceRef("prod", "app")
    loader._offer_candidate(release_module._Candidate(*matching))
    loader._offer_candidate(release_module._Candidate(*foreign))
    assert loader._candidate_queue.qsize() == 1
    assert loader._candidate_queue.get_nowait().release.schema_version == 1
    assert loader.status().observed_revision == 10


def test_async_active_read_and_digest_resolution_reject_foreign_schema(monkeypatch):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, _release(1, 10))
        loader._namespace = NamespaceRef("prod", "app")
        stub.release.schema_version = 2
        with pytest.raises(ReleaseStartupError, match="wrong schema"):
            await loader._read_active()

        digest_loader, digest_stub, _client = _loader(
            monkeypatch, _release(1, 10), schema_version=None,
            schema_sha256="a" * 64,
        )
        digest_loader._namespace = NamespaceRef("prod", "app")
        digest_stub.resolved_schema = 0
        with pytest.raises(ReleaseStartupError, match="version 0"):
            await digest_loader._ensure_schema_version()

    asyncio.run(scenario())


@pytest.mark.parametrize("schema_version", [0, 1])
@pytest.mark.parametrize("foreign_field", ["env", "app", "name"])
def test_async_active_read_rejects_foreign_release_track(
    monkeypatch, schema_version, foreign_field
):
    async def scenario():
        loader, stub, _client = _loader(
            monkeypatch,
            _release(1, 10, schema_version),
            schema_version=schema_version,
        )
        loader._namespace = NamespaceRef("prod", "app")
        if foreign_field == "env":
            stub.release.namespace.env = "other-env"
        elif foreign_field == "app":
            stub.release.namespace.app = "other-app"
        else:
            stub.release.name = "other-release"

        with pytest.raises(ReleaseStartupError, match="wrong release track"):
            await loader._read_active()

    asyncio.run(scenario())


def test_async_loader_applies_redacts_and_acknowledges(monkeypatch):
    async def scenario():
        order: List[str] = []

        async def validate(_cancel, manifest):
            order.append("manifest")
            assert not hasattr(manifest.entry("password"), "has_access_token")

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
        assert client.metadata_versions == [1]
        applied = [a for a in stub.acknowledgements if a.state == "applied"][-1]
        assert applied.applied_divergent
        assert applied.divergent_field_count == 65_535

    asyncio.run(scenario())


def test_async_bound_loader_resolves_independent_credentials_and_missing_key_rejects(monkeypatch):
    async def scenario():
        source = {"password": "async-binding-key"}
        loader, _stub, client = _loader(
            monkeypatch, _release(1, 10), binding_keys=source,
        )
        client.bound = True
        source["password"] = "changed"
        prepared = _Prepared()
        task = asyncio.create_task(loader.run(lambda _cancel, _snapshot: prepared))
        await _wait_for(lambda: prepared.commits == 1)
        loader.stop()
        await task
        assert client.binding_keys == ["async-binding-key"]

        missing, _stub, missing_client = _loader(monkeypatch, _release(1, 10))
        missing_client.bound = True
        with pytest.raises(ReleaseCandidateError) as caught:
            await missing.run(lambda _cancel, _snapshot: _Prepared())
        assert caught.value.category == "binding_key_unavailable"
        assert missing_client.binding_keys == []

    asyncio.run(scenario())


def test_async_loader_rejects_enabled_version_with_destroyed_timestamp(monkeypatch):
    async def scenario():
        loader, _stub, client = _loader(monkeypatch, _release(1, 10))
        client.destroyed_at_unix_ms = 1
        with pytest.raises(ReleaseCandidateError) as caught:
            await loader.run(lambda _cancel, _snapshot: _Prepared())
        assert caught.value.category == "resolution_failed"
        assert client.binding_keys == []

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


@pytest.mark.parametrize("schema_version", [0, 1])
@pytest.mark.parametrize("foreign_field", ["env", "app", "name"])
def test_async_active_precommit_fence_includes_full_track(
    monkeypatch, schema_version, foreign_field
):
    async def scenario():
        loader, stub, _client = _loader(
            monkeypatch,
            _release(1, 10, schema_version),
            schema_version=schema_version,
        )
        initial = _Prepared()
        stale = _Prepared()
        current = _Prepared()

        async def prepare(_cancel, snapshot):
            if snapshot.version == 2:
                foreign = kms_pb2.ConfigurationRelease()
                foreign.CopyFrom(stub.release)
                if foreign_field == "env":
                    foreign.namespace.env = "other-env"
                elif foreign_field == "app":
                    foreign.namespace.app = "other-app"
                else:
                    foreign.name = "different-release"
                stub.release = foreign
                return stale
            return initial if snapshot.version == 1 else current

        task = asyncio.create_task(loader.run(prepare))
        await _wait_for(lambda: initial.commits == 1)
        stub.activate(_release(2, 20, schema_version))
        await _wait_for(lambda: stale.aborts == 1)
        assert loader.status().applied_version == 1
        assert loader.status().last_failure_category == "active_check_failed"
        stub.activate(_release(3, 30, schema_version))
        await _wait_for(lambda: loader.status().applied_version == 3)
        loader.stop()
        await task
        assert stale.commits == 0
        assert current.commits == 1

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
        rejected = [a for a in stub.acknowledgements if a.state == "rejected"]
        assert rejected[-1].rejection_category == "restart_required"
        assert rejected[-1].diagnostic == ""
        assert len(stub.calls) == 1
        assert stub.calls[0].half_closed
        assert stub.calls[0].drained
        assert not stub.calls[0].cancelled

    asyncio.run(scenario())


@pytest.mark.parametrize("bad_digest", ["é" * 64, "g" * 64, "0" * 63])
def test_async_malformed_release_digest_is_classified(monkeypatch, bad_digest):
    async def scenario():
        initial = _release(1, 10)
        initial[0].digest = bad_digest
        loader, stub, _client = _loader(monkeypatch, initial)
        with pytest.raises(ReleaseCandidateError) as caught:
            await loader.run(lambda _cancel, _snapshot: _Prepared())
        assert caught.value.category == "digest_mismatch"
        rejected = [a for a in stub.acknowledgements if a.state == "rejected"]
        assert rejected[-1].rejection_category == "digest_mismatch"

    asyncio.run(scenario())


def test_async_uppercase_parameter_digest_is_accepted(monkeypatch):
    async def scenario():
        initial = _release(1, 10)
        initial[0].entries[0].parameter_digest = (
            initial[0].entries[0].parameter_digest.upper()
        )
        initial[0].digest = release_module._release_digest(initial[0])
        loader, _stub, _client = _loader(monkeypatch, initial)
        prepared = _Prepared()
        task = asyncio.create_task(loader.run(lambda _cancel, _snapshot: prepared))
        await _wait_for(lambda: prepared.commits == 1)
        loader.stop()
        await task
        assert loader.status().state == "applied"

    asyncio.run(scenario())


@pytest.mark.parametrize("schema_version", [0, 1])
def test_async_watch_validates_envelopes_before_cursor_and_reconnect(
    monkeypatch, schema_version
):
    async def scenario():
        loader, stub, _client = _loader(
            monkeypatch,
            _release(1, 10, schema_version),
            schema_version=schema_version,
        )
        task = asyncio.create_task(
            loader.run(lambda _cancel, _snapshot: _Prepared())
        )
        await _wait_for(
            lambda: loader.status().applied_version == 1 and bool(stub.calls)
        )
        candidates = loader.stats().candidates

        invalid_events = []
        for index, foreign_field in enumerate(
            ("env", "app", "name", "schema"), start=1
        ):
            foreign, _ = _release(2, 100 + index, schema_version)
            if foreign_field == "env":
                foreign.namespace.env = "other-env"
            elif foreign_field == "app":
                foreign.namespace.app = "other-app"
            elif foreign_field == "name":
                foreign.name = "other-release"
            else:
                foreign.schema_version = schema_version + 1
            envelope = (
                kms_pb2.ReleaseSnapshotEvent(release=foreign)
                if index % 2
                else kms_pb2.ReleaseActivationEvent(release=foreign)
            )
            invalid_events.append(
                kms_pb2.WatchReleaseEvent(
                    **(
                        {"snapshot": envelope}
                        if index % 2
                        else {"activation": envelope}
                    ),
                    revision=100 + index,
                )
            )
        invalid_events.extend(
            [
                kms_pb2.WatchReleaseEvent(
                    activation=kms_pb2.ReleaseActivationEvent(), revision=110
                ),
                kms_pb2.WatchReleaseEvent(revision=111),
            ]
        )
        for event in invalid_events:
            stub.calls[-1].push(event)
        await asyncio.sleep(0.1)
        assert loader._last_seen_revision == 10
        assert loader.stats().candidates == candidates

        registrations = len(stub.registrations)
        stub.disconnect()
        await _wait_for(lambda: len(stub.registrations) > registrations)
        assert stub.registrations[-1].last_seen_revision == 10

        stub.calls[-1].push(
            kms_pb2.WatchReleaseEvent(heartbeat=kms_pb2.Heartbeat(), revision=200)
        )
        await _wait_for(lambda: loader._last_seen_revision == 200)
        stub.calls[-1].push(
            kms_pb2.WatchReleaseEvent(heartbeat=kms_pb2.Heartbeat(), revision=150)
        )
        await asyncio.sleep(0.05)
        assert loader._last_seen_revision == 200

        registrations = len(stub.registrations)
        stub.disconnect()
        await _wait_for(lambda: len(stub.registrations) > registrations)
        assert stub.registrations[-1].last_seen_revision == 200

        stub.activate(_release(2, 20, schema_version))
        await _wait_for(lambda: loader.status().applied_version == 2)
        assert loader._last_seen_revision == 200
        loader.stop()
        await task

    asyncio.run(scenario())


@pytest.mark.parametrize("schema_version", [0, 1])
def test_async_retries_selected_candidate_after_newer_heartbeat(
    monkeypatch, schema_version
):
    async def scenario():
        loader, stub, _client = _loader(
            monkeypatch,
            _release(1, 10, schema_version),
            schema_version=schema_version,
            reconcile_interval=0.5,
        )
        attempts = []

        def prepare(_cancel, snapshot):
            attempts.append(snapshot.version)
            if snapshot.version == 2 and attempts.count(2) == 1:
                raise ValueError("temporary prepare failure")
            return _Prepared()

        task = asyncio.create_task(loader.run(prepare))
        await _wait_for(
            lambda: loader.status().applied_version == 1 and bool(stub.calls)
        )
        stub.activate(_release(2, 20, schema_version))
        await _wait_for(lambda: loader.status().state == "rejected")
        stub.calls[-1].push(
            kms_pb2.WatchReleaseEvent(heartbeat=kms_pb2.Heartbeat(), revision=100)
        )
        await _wait_for(lambda: loader._last_seen_revision == 100)
        await _wait_for(lambda: loader.status().applied_version == 2)
        assert attempts.count(2) == 2

        candidates = loader.stats().candidates
        stale, _ = _release(1, 10, schema_version)
        stub.calls[-1].push(
            kms_pb2.WatchReleaseEvent(
                snapshot=kms_pb2.ReleaseSnapshotEvent(release=stale), revision=10
            )
        )
        await asyncio.sleep(0.1)
        assert loader.status().applied_version == 2
        assert loader.stats().candidates == candidates
        loader.stop()
        await task

    asyncio.run(scenario())


@pytest.mark.parametrize("schema_version", [0, 1])
def test_async_reconciliation_rejects_foreign_track_and_recovers(
    monkeypatch, schema_version
):
    async def scenario():
        loader, stub, _client = _loader(
            monkeypatch,
            _release(1, 10, schema_version),
            schema_version=schema_version,
            reconcile_interval=0.05,
        )
        task = asyncio.create_task(
            loader.run(lambda _cancel, _snapshot: _Prepared())
        )
        await _wait_for(lambda: loader.status().applied_version == 1)
        foreign, _ = _release(2, 20, schema_version)
        foreign.namespace.env = "other-env"
        stub.release = foreign
        stub.revision = 20
        await _wait_for(
            lambda: loader.status().last_failure_category == "active_check_failed"
        )
        assert loader.status().applied_version == 1

        stub.release, _ = _release(2, 20, schema_version)
        await _wait_for(lambda: loader.status().applied_version == 2)
        loader.stop()
        await task

    asyncio.run(scenario())


def test_async_empty_initial_active_fails_before_watch(monkeypatch):
    async def scenario():
        loader, stub, _client = _loader(
            monkeypatch, (kms_pb2.ConfigurationRelease(), 0)
        )
        called = False

        def prepare(_cancel, _snapshot):
            nonlocal called
            called = True
            return _Prepared()

        with pytest.raises(Exception, match="response was empty"):
            await loader.run(prepare)
        assert not called
        assert stub.calls == []
        assert loader.status().state == "idle"

    asyncio.run(scenario())


def test_async_commit_failure_uses_public_rejected_state(monkeypatch):
    async def scenario():
        class Broken(_Prepared):
            def commit(self):
                raise RuntimeError("commit failed")

        loader, stub, _client = _loader(monkeypatch, _release(1, 10))
        with pytest.raises(ReleaseCommitError):
            await loader.run(lambda _cancel, _snapshot: Broken())
        assert loader.status().state == "rejected"
        assert loader.status().last_failure_category == "internal"
        assert loader.stats().rejections["internal"] == 1
        assert not any(a.state == "applied" for a in stub.acknowledgements)

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
        stub.activate(_release(3, 12))
        await _wait_for(lambda: loader.status().applied_version == 3)
        assert loader.status().state == "applied"
        assert loader.status().last_failure_category == ""
        assert loader.status().last_failure_unix_ms == 0
        loader.stop()
        await task

    asyncio.run(scenario())


@pytest.mark.parametrize(
    ("alias", "category"),
    [("setting", "digest_mismatch"), ("password", "version_mismatch")],
)
def test_async_empty_pinned_content_type_rejects_without_replacing_lkg(
    monkeypatch, alias, category
):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, _release(1, 10))
        prepared: Dict[int, _Prepared] = {}
        published = []

        async def prepare(_cancel, snapshot):
            published.append(snapshot.version)
            return prepared.setdefault(snapshot.version, _Prepared())

        task = asyncio.create_task(loader.run(prepare))
        await _wait_for(lambda: loader.status().applied_version == 1)

        tampered = _release(2, 20)
        entry = next(item for item in tampered[0].entries if item.alias == alias)
        entry.content_type = ""
        tampered[0].digest = release_module._release_digest(tampered[0])
        stub.activate(tampered)

        await _wait_for(lambda: loader.status().last_failure_category == category)
        assert loader.status().applied_version == 1
        assert published == [1]
        assert prepared[1].commits == 1
        assert 2 not in prepared
        await _wait_for(
            lambda: any(
                ack.state == "rejected" and ack.activation_revision == 20
                for ack in stub.acknowledgements
            )
        )
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


def test_async_status_stats_and_prepared_state_are_canonical(monkeypatch):
    async def scenario():
        loader, _stub, _client = _loader(monkeypatch, _release(1, 10))

        def prepare(_cancel, _snapshot):
            assert loader.status().state == "received"
            return _Prepared()

        task = asyncio.create_task(loader.run(prepare))
        await _wait_for(lambda: loader.status().state == "applied")
        loader.stop()
        await task
        status = loader.status()
        stats = loader.stats()
        assert status.last_resolution_duration_ms >= 0
        assert status.reconnects == stats.reconnects
        assert stats.candidates == 1
        assert stats.applied == 1
        assert stats.rejected == stats.rejections

    asyncio.run(scenario())


@pytest.mark.parametrize("outcome", ["rejected", "superseded"])
def test_async_old_outcome_cannot_unlock_newer_inflight_reconciliation(monkeypatch, outcome):
    async def scenario():
        loader, _stub, _client = _loader(monkeypatch, _release(1, 10))
        loader._namespace = NamespaceRef("prod", "app")
        release_a, revision_a = _release(1, 10)
        release_b, revision_b = _release(2, 11)
        candidate_a = release_module._Candidate(release_a, revision_a)
        candidate_b = release_module._Candidate(release_b, revision_b)

        loader._offer_candidate(candidate_a)
        assert loader._candidate_queue.get_nowait() == candidate_a
        loader._active_identity = candidate_a.identity
        loader._active_cancel = asyncio.Event()
        loader._offer_candidate(candidate_b)
        loader._record_retry_eligibility(candidate_a, outcome)
        assert loader._retry_identity is None
        assert loader._candidate_queue.get_nowait() == candidate_b

        # B is in flight while status may still describe A's rejection.
        loader._active_identity = candidate_b.identity
        loader._offer_candidate(candidate_b, source="reconciliation")
        assert loader._candidate_queue.empty()

    asyncio.run(scenario())


def test_async_exact_latest_rejection_retries_only_from_reconciliation(monkeypatch):
    async def scenario():
        loader, _stub, _client = _loader(monkeypatch, _release(1, 10))
        loader._namespace = NamespaceRef("prod", "app")
        release, revision = _release(1, 10)
        candidate = release_module._Candidate(release, revision)
        loader._offer_candidate(candidate)
        assert loader._candidate_queue.get_nowait() == candidate
        loader._record_retry_eligibility(candidate, "rejected")
        loader._offer_candidate(candidate)
        assert loader._candidate_queue.empty()
        loader._offer_candidate(candidate, source="reconciliation")
        assert loader._candidate_queue.get_nowait() == candidate

    asyncio.run(scenario())


@pytest.mark.parametrize("failure", ["raises", "returns"])
def test_async_abort_contract_failure_is_fatal_internal(monkeypatch, failure):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, _release(1, 10))

        class BrokenAbort(_Prepared):
            def abort(self):
                self.aborts += 1
                if failure == "raises":
                    raise RuntimeError("abort failed")
                return object()

        def prepare(_cancel, _snapshot):
            stub.release, stub.revision = _release(2, 11)
            return BrokenAbort()

        with pytest.raises(ReleaseCommitError, match="abort failed"):
            await loader.run(prepare)
        assert loader.status().last_failure_category == "internal"
        assert loader.stats().rejected["internal"] == 1

    asyncio.run(scenario())


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("reconcile_interval", 0),
        ("reconcile_interval", math.nan),
        ("reconnect_initial", math.inf),
        ("reconnect_max", -1),
        ("request_timeout", math.nan),
        ("request_timeout", 0),
    ],
)
def test_async_release_timing_must_be_finite_positive(monkeypatch, field, value):
    with pytest.raises(Exception, match="finite and positive|backoff"):
        _loader(monkeypatch, _release(1, 10), **{field: value})


def test_async_initial_grpc_failure_is_wrapped_as_startup_error(monkeypatch):
    class Unavailable(grpc.RpcError):
        def code(self):
            return grpc.StatusCode.UNAVAILABLE

        def details(self):
            return "unavailable"

    async def scenario():
        loader, stub, _client = _loader(monkeypatch, _release(1, 10))

        async def fail(_request, **_kwargs):
            raise Unavailable()

        stub.GetActiveRelease = fail
        with pytest.raises(ReleaseStartupError, match="unable to read"):
            await loader.run(lambda _cancel, _snapshot: _Prepared())
        assert not stub.calls

    asyncio.run(scenario())


@pytest.mark.parametrize("prepare_result", ["prepared", "failed", "cancelled", "stopped"])
def test_async_terminal_watch_cancels_cooperative_prepare(monkeypatch, prepare_result):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, _release(1, 10))
        entered = asyncio.Event()
        cancelled = asyncio.Event()
        cleanup = asyncio.Event()
        prepared = _Prepared()

        async def prepare(cancel, _snapshot):
            entered.set()
            await cancel.wait()
            cancelled.set()
            await cleanup.wait()
            if prepare_result == "failed":
                raise RuntimeError("preparation failed during cancellation")
            if prepare_result == "cancelled":
                raise asyncio.CancelledError()
            if prepare_result == "stopped":
                loader.stop()
            return prepared

        task = asyncio.create_task(loader.run(prepare))
        try:
            await asyncio.wait_for(entered.wait(), 2)
            await _wait_for(lambda: bool(stub.calls))
            stub.reject_watch(grpc.StatusCode.PERMISSION_DENIED)
            await asyncio.wait_for(loader._watch_done.wait(), 2)
            await asyncio.wait_for(cancelled.wait(), 0.5)
            assert not task.done(), "cooperative cleanup must finish before run exits"
            cleanup.set()
            with pytest.raises(kms_paramstore.PermissionDeniedError):
                await asyncio.wait_for(task, 2)
            assert prepared.commits == 0
            assert prepared.aborts == (1 if prepare_result in {"prepared", "stopped"} else 0)
        finally:
            cleanup.set()
            loader.stop()
            await asyncio.gather(task, return_exceptions=True)

    asyncio.run(scenario())


def test_async_terminal_watch_before_candidate_install_fences_preparation(monkeypatch):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, _release(1, 10))
        entered = asyncio.Event()
        proceed = asyncio.Event()
        prepared = _Prepared()
        preparations = []
        process = loader._process_candidate

        async def held_process(candidate, prepare):
            entered.set()
            await proceed.wait()
            return await process(candidate, prepare)

        def prepare(_cancel, snapshot):
            preparations.append(snapshot)
            return prepared

        monkeypatch.setattr(loader, "_process_candidate", held_process)
        task = asyncio.create_task(loader.run(prepare))
        try:
            await asyncio.wait_for(entered.wait(), 2)
            await _wait_for(lambda: bool(stub.calls))
            stub.reject_watch(grpc.StatusCode.UNAUTHENTICATED)
            await asyncio.wait_for(loader._watch_done.wait(), 2)
            proceed.set()
            with pytest.raises(kms_paramstore.UnauthenticatedError):
                await asyncio.wait_for(task, 2)
            assert preparations == []
            assert loader.stats().resolutions == 0
            assert prepared.commits == prepared.aborts == 0
        finally:
            proceed.set()
            loader.stop()
            await asyncio.gather(task, return_exceptions=True)

    asyncio.run(scenario())


@pytest.mark.parametrize("read_fails", [False, True])
def test_async_terminal_watch_during_precommit_aborts_once(monkeypatch, read_fails):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, _release(1, 10))
        entered = asyncio.Event()
        proceed = asyncio.Event()
        prepared = _Prepared()
        read_active = loader._read_active
        reads = 0

        async def held_read():
            nonlocal reads
            reads += 1
            if reads == 2:
                entered.set()
                await proceed.wait()
                if read_fails:
                    raise RuntimeError("active read failed after watch rejection")
            return await read_active()

        monkeypatch.setattr(loader, "_read_active", held_read)
        task = asyncio.create_task(loader.run(lambda _cancel, _snapshot: prepared))
        try:
            await asyncio.wait_for(entered.wait(), 2)
            await _wait_for(lambda: bool(stub.calls))
            stub.reject_watch(grpc.StatusCode.PERMISSION_DENIED)
            await asyncio.wait_for(loader._watch_done.wait(), 2)
            proceed.set()
            with pytest.raises(kms_paramstore.PermissionDeniedError):
                await asyncio.wait_for(task, 2)
            assert prepared.commits == 0
            assert prepared.aborts == 1
        finally:
            proceed.set()
            loader.stop()
            await asyncio.gather(task, return_exceptions=True)

    asyncio.run(scenario())


def test_async_terminal_watch_does_not_mask_abort_contract_failure(monkeypatch):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, _release(1, 10))

        class BrokenAbort(_Prepared):
            def abort(self):
                self.aborts += 1
                raise RuntimeError("abort failed")

        prepared = BrokenAbort()

        async def prepare(cancel, _snapshot):
            await _wait_for(lambda: bool(stub.calls))
            stub.reject_watch(grpc.StatusCode.PERMISSION_DENIED)
            await cancel.wait()
            return prepared

        with pytest.raises(ReleaseCommitError, match="abort"):
            await asyncio.wait_for(loader.run(prepare), 2)
        assert isinstance(loader._watch_error, kms_paramstore.PermissionDeniedError)
        assert prepared.commits == 0
        assert prepared.aborts == 1
        assert loader.status().last_failure_category == "internal"

    asyncio.run(scenario())


def test_async_process_session_targets_keep_last_applied_on_rejection(monkeypatch):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, _release(1, 10), instance_id="fixed")
        target = kms_pb2.InstanceReleaseTarget(release=_release(1, 10)[0], target_revision=10, activation_revision=10)
        sessions = []

        async def register(request, **_kwargs):
            sessions.append(request.session.session_id)
            return kms_pb2.ReleaseSessionResponse(pin_capable=True)

        async def get_target(request, **_kwargs):
            assert request.session.session_id == sessions[0]
            out = kms_pb2.InstanceReleaseTarget()
            out.CopyFrom(target)
            return out

        stub.RegisterReleaseSession = register
        stub.GetInstanceRelease = get_target

        def prepare(_cancel, snapshot):
            if snapshot.version == 3:
                raise ValueError("rejected pin")
            return _Prepared()

        task = asyncio.create_task(loader.run(prepare))
        try:
            await _wait_for(lambda: loader.status().applied_version == 1 and bool(stub.calls))
            target.CopyFrom(kms_pb2.InstanceReleaseTarget(release=_release(2, 20)[0], target_revision=20, pinned=True, pin_revision=20))
            stub.calls[-1].push(kms_pb2.WatchReleaseEvent(target=target, revision=20))
            await _wait_for(lambda: loader.status().applied_version == 2)
            await _wait_for(lambda: any(a.state == "applied" and a.target_revision == 20 and a.activation_revision == 0 and a.session_id == sessions[0] for a in stub.acknowledgements))
            target.CopyFrom(kms_pb2.InstanceReleaseTarget(release=_release(3, 30)[0], target_revision=30, pinned=True, pin_revision=30))
            stub.calls[-1].push(kms_pb2.WatchReleaseEvent(target=target, revision=30))
            await _wait_for(lambda: loader.status().state == "rejected")
            assert loader.status().applied_version == 2
        finally:
            loader.stop()
            await task
        replacement, _, _ = _loader(monkeypatch, _release(1, 10), instance_id="fixed")
        assert replacement._session_id != loader._session_id

    asyncio.run(scenario())
