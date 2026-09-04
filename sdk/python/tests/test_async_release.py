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
        self.binding_keys: List[str] = []
        self.bound = False
        self.has_access_token = True
        self.state = "enabled"

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
        secret_token="",
        binding_key="",
        timeout=None,
    ):
        del label, timeout
        self.tokens.append(secret_token)
        self.binding_keys.append(binding_key)
        if self.has_access_token and secret_token != "token":
            raise RuntimeError("credential unavailable")
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

    async def get_secret_metadata(self, key, *, timeout=None):
        del timeout
        env, app, resource_key = key[1:].split("/", 2)
        return kms_paramstore.models.SecretInfo(
            env=env, app=app, key=resource_key, content_type="string",
            bound=self.bound, has_access_token=self.has_access_token,
            versions=tuple(
                kms_paramstore.models.SecretVersion(
                    version=version, state=self.state, bound=self.bound,
                    has_access_token=self.has_access_token,
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
        "reconcile_interval": 10.0,
        "reconnect_initial": 0.01,
        "reconnect_max": 0.02,
        "secret_token_provider": lambda _alias, _path, _cancel: ("token", True),
    }
    settings.update(config)
    loader = AsyncReleaseLoader(
        client,
        AsyncReleaseLoaderConfig(**settings),
    )
    return loader, stub, client


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
        assert client.tokens == ["token"]
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
        assert client.tokens == ["token"]
        assert client.binding_keys == ["async-binding-key"]

        missing, _stub, missing_client = _loader(monkeypatch, _release(1, 10))
        missing_client.bound = True
        with pytest.raises(ReleaseCandidateError) as caught:
            await missing.run(lambda _cancel, _snapshot: _Prepared())
        assert caught.value.category == "token_unavailable"
        assert missing_client.binding_keys == []

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


def test_async_active_fence_includes_release_name(monkeypatch):
    async def scenario():
        loader, stub, _client = _loader(monkeypatch, _release(1, 10))
        stale = _Prepared()
        current = _Prepared()

        async def prepare(_cancel, snapshot):
            if snapshot.version == 1:
                renamed = kms_pb2.ConfigurationRelease()
                renamed.CopyFrom(stub.release)
                renamed.name = "different-release"
                stub.release = renamed
                return stale
            return current

        task = asyncio.create_task(loader.run(prepare))
        await _wait_for(lambda: stale.aborts == 1)
        stub.activate(_release(2, 11))
        await _wait_for(lambda: loader.status().applied_version == 2)
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
        assert not client.tokens
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
