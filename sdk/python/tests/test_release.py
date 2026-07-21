from __future__ import annotations

import hashlib
import queue
import threading
import time
from dataclasses import FrozenInstanceError
from typing import Dict, List, Optional

import pytest

import kms_paramstore.release as release_module
from kms_paramstore import (
    ReleaseCommitError,
    ReleaseLoader,
    ReleaseLoaderConfig,
    ReleaseStartupError,
    run_typed_release,
)
from kms_paramstore._gen import kms_pb2
from kms_paramstore._refs import NamespaceRef
from kms_paramstore.secret import Secret
from tests.helpers import wait_until


def _ref(key: str) -> kms_pb2.ResourceRef:
    return kms_pb2.ResourceRef(
        namespace=kms_pb2.NamespaceRef(env="prod", app="app"), key=key
    )


def _digest(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def _release(version: int, revision: int, param_version: Optional[int] = None):
    param_version = param_version or version
    value = f"value-{param_version}"
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
                version=param_version,
                content_type="string",
                parameter_digest=_digest(value),
            ),
            kms_pb2.ConfigurationReleaseEntry(
                alias="password",
                kind="secret",
                ref=_ref("password"),
                version=param_version,
                content_type="string",
                has_access_token=True,
            ),
        ],
    )
    release.digest = release_module._release_digest(release)
    return release, revision


class _ParameterStub:
    def __init__(self) -> None:
        self.values: Dict[int, str] = {
            version: f"value-{version}" for version in range(1, 10)
        }

    def GetParameter(self, request, **_kwargs):
        value = self.values[request.version]
        return kms_pb2.GetParameterResponse(
            parameter=kms_pb2.Parameter(
                ref=request.ref,
                value=value,
                content_type="string",
                version=request.version,
            )
        )


class _SecretStub:
    def __init__(self) -> None:
        self.tokens: List[str] = []

    def GetSecret(self, request, *, metadata, **_kwargs):
        token = dict(metadata).get("x-kms-secret-token", "")
        self.tokens.append(token)
        if token != "local-token":
            raise AssertionError("protected secret fetched without its local token")
        return kms_pb2.GetSecretResponse(
            ref=request.ref,
            value=f"secret-{request.version}".encode(),
            version=request.version,
            content_type="string",
        )


class _Call:
    _CLOSED = object()

    def __init__(self, requests, owner: "_ReleaseStub") -> None:
        self._queue: "queue.Queue[object]" = queue.Queue()
        self._closed = threading.Event()
        self._owner = owner

        def read_requests() -> None:
            try:
                for request in requests:
                    with owner.lock:
                        if request.WhichOneof("request") == "register":
                            owner.registrations.append(request.register)
                        else:
                            owner.acknowledgements.append(request.acknowledgement)
            except Exception:
                pass

        threading.Thread(target=read_requests, daemon=True).start()

    def __iter__(self):
        return self

    def __next__(self):
        item = self._queue.get(timeout=2.0)
        if item is self._CLOSED:
            raise RuntimeError("stream disconnected")
        return item

    def push(self, event) -> None:
        if not self._closed.is_set():
            self._queue.put(event)

    def disconnect(self) -> None:
        self._queue.put(self._CLOSED)

    def cancel(self) -> bool:
        self._closed.set()
        self._queue.put(self._CLOSED)
        return True


class _ReleaseStub:
    def __init__(self, initial) -> None:
        self.lock = threading.Lock()
        self.release, self.revision = initial
        self.calls: List[_Call] = []
        self.registrations: List[object] = []
        self.acknowledgements: List[object] = []

    def GetActiveRelease(self, _request, **_kwargs):
        with self.lock:
            release = kms_pb2.ConfigurationRelease()
            release.CopyFrom(self.release)
            revision = self.revision
        return kms_pb2.GetActiveReleaseResponse(
            release=release, activation_revision=revision
        )

    def WatchRelease(self, requests, **_kwargs):
        call = _Call(requests, self)
        with self.lock:
            self.calls.append(call)
        return call

    def activate(self, release_and_revision) -> None:
        release, revision = release_and_revision
        with self.lock:
            self.release = release
            self.revision = revision
            calls = list(self.calls)
        event = kms_pb2.WatchReleaseEvent(
            activation=kms_pb2.ReleaseActivationEvent(release=release), revision=revision
        )
        for call in calls:
            call.push(event)

    def disconnect(self) -> None:
        with self.lock:
            calls = list(self.calls)
        for call in calls:
            call.disconnect()


class _Client:
    def __init__(self) -> None:
        self._channel = object()
        self._client_name = "python-tests"
        self._param_stub = _ParameterStub()
        self._secret_stub = _SecretStub()

    def _resolve_namespace_arg(self, namespace):
        return namespace or NamespaceRef("prod", "app")

    def _auth_metadata(self, secret_token: str = ""):
        return [("x-kms-secret-token", secret_token)] if secret_token else []

    def _call_timeout(self, timeout):
        return timeout or 1.0

    def get_secret(
        self,
        key,
        *,
        version=0,
        label="",
        secret_token="",
        timeout=None,
    ):
        del label
        env, app, resource_key = key[1:].split("/", 2)
        response = self._secret_stub.GetSecret(
            kms_pb2.GetSecretRequest(
                ref=kms_pb2.ResourceRef(
                    namespace=kms_pb2.NamespaceRef(env=env, app=app),
                    key=resource_key,
                ),
                version=version,
            ),
            metadata=self._auth_metadata(secret_token),
            timeout=self._call_timeout(timeout),
        )
        return Secret(
            response.value,
            env=response.ref.namespace.env,
            app=response.ref.namespace.app,
            key=response.ref.key,
            version=response.version,
            content_type=response.content_type,
        )


class _Prepared:
    def __init__(self) -> None:
        self.commits = 0
        self.aborts = 0

    def commit(self) -> None:
        self.commits += 1

    def abort(self) -> None:
        self.aborts += 1


def _loader(monkeypatch, initial, **config):
    stub = _ReleaseStub(initial)
    monkeypatch.setattr(
        release_module.kms_pb2_grpc,
        "ConfigurationReleaseServiceStub",
        lambda _channel: stub,
    )
    client = _Client()
    loader = ReleaseLoader(
        client,
        ReleaseLoaderConfig(
            name="runtime",
            reconcile_interval=10.0,
            reconnect_initial=0.01,
            reconnect_max=0.02,
            secret_token_provider=lambda _alias, _path: ("local-token", True),
            **config,
        ),
    )
    return loader, stub, client


def _run_in_thread(loader, prepare):
    errors: List[BaseException] = []

    def run() -> None:
        try:
            loader.run(prepare)
        except BaseException as exc:
            errors.append(exc)

    thread = threading.Thread(target=run)
    thread.start()
    return thread, errors


def test_initial_snapshot_is_complete_immutable_redacting_and_acknowledged(monkeypatch):
    loader, stub, client = _loader(monkeypatch, _release(1, 10))
    prepared = _Prepared()
    snapshots = []

    def prepare(_cancel, snapshot):
        snapshots.append(snapshot)
        return prepared

    thread, raised = _run_in_thread(loader, prepare)
    assert wait_until(lambda: loader.status().state == "applied")
    assert wait_until(
        lambda: {ack.state for ack in stub.acknowledgements}
        >= {"received", "prepared", "applied"}
    )
    loader.stop()
    thread.join(timeout=2)

    assert not raised
    assert prepared.commits == 1
    assert prepared.aborts == 0
    snapshot = snapshots[0]
    assert snapshot.parameters == {"setting": "value-1"}
    assert snapshot.secrets["password"].string_value == "secret-1"
    assert "secret-1" not in repr(snapshot)
    assert "value-1" not in repr(snapshot)
    assert "[REDACTED]" in repr(snapshot)
    assert client._secret_stub.tokens == ["local-token"]
    with pytest.raises(TypeError):
        snapshot.parameters["setting"] = "changed"
    with pytest.raises(FrozenInstanceError):
        snapshot.version = 99
    assert loader.stats().acknowledgements["applied"] == 1


def test_initial_digest_mismatch_fails_startup_and_rejects(monkeypatch):
    initial = _release(1, 10)
    initial[0].entries[0].parameter_digest = "0" * 64
    initial[0].digest = release_module._release_digest(initial[0])
    loader, _stub, _client = _loader(monkeypatch, initial)

    with pytest.raises(ReleaseStartupError, match="digest_mismatch"):
        loader.run(lambda _cancel, _snapshot: pytest.fail("prepare must not run"))

    assert loader.status().last_failure_category == "digest_mismatch"
    assert loader.stats().acknowledgements["rejected"] == 1


def test_release_projection_digest_mismatch_fails_before_prepare(monkeypatch):
    initial = _release(1, 10)
    initial[0].digest = "0" * 64
    loader, _stub, _client = _loader(monkeypatch, initial)

    with pytest.raises(ReleaseStartupError, match="digest_mismatch"):
        loader.run(lambda _cancel, _snapshot: pytest.fail("prepare must not run"))

    assert loader.status().last_failure_category == "digest_mismatch"


def test_prepare_exception_cannot_put_secret_plaintext_in_sdk_error(monkeypatch):
    loader, _stub, _client = _loader(monkeypatch, _release(1, 10))

    def prepare(_cancel, snapshot):
        raise ValueError(snapshot.secrets["password"].string_value)

    with pytest.raises(ReleaseStartupError) as raised:
        loader.run(prepare)

    assert "secret-1" not in str(raised.value)
    assert raised.value.__cause__ is None


def test_newer_activation_cancels_and_aborts_stale_candidate(monkeypatch):
    loader, stub, _client = _loader(monkeypatch, _release(1, 10))
    prepared: Dict[int, _Prepared] = {}
    second_started = threading.Event()

    def prepare(cancel, snapshot):
        item = prepared.setdefault(snapshot.version, _Prepared())
        if snapshot.version == 2:
            second_started.set()
            assert cancel.wait(timeout=2.0)
        return item

    thread, raised = _run_in_thread(loader, prepare)
    assert wait_until(lambda: loader.status().applied_version == 1)
    stub.activate(_release(2, 20))
    assert second_started.wait(timeout=2)
    stub.activate(_release(3, 30))
    assert wait_until(lambda: loader.status().applied_version == 3)
    loader.stop()
    thread.join(timeout=2)

    assert not raised
    assert prepared[1].commits == 1
    assert prepared[2].commits == 0
    assert prepared[2].aborts == 1
    assert prepared[3].commits == 1
    assert loader.stats().rejections["superseded"] >= 1


def test_prepare_rejection_keeps_last_known_good(monkeypatch):
    loader, stub, _client = _loader(monkeypatch, _release(1, 10))
    prepared: Dict[int, _Prepared] = {}

    def prepare(_cancel, snapshot):
        if snapshot.version == 2:
            raise ValueError("do not include this potentially sensitive message")
        return prepared.setdefault(snapshot.version, _Prepared())

    thread, raised = _run_in_thread(loader, prepare)
    assert wait_until(lambda: loader.status().applied_version == 1)
    stub.activate(_release(2, 20))
    assert wait_until(lambda: loader.status().last_failure_category == "prepare_failed")
    assert loader.status().applied_version == 1
    stub.activate(_release(3, 30))
    assert wait_until(lambda: loader.status().applied_version == 3)
    loader.stop()
    thread.join(timeout=2)

    assert not raised
    rejected = [ack for ack in stub.acknowledgements if ack.state == "rejected"]
    assert wait_until(lambda: bool(rejected) or any(
        ack.state == "rejected" for ack in stub.acknowledgements
    ))
    rejected = [ack for ack in stub.acknowledgements if ack.state == "rejected"]
    assert rejected[-1].diagnostic == ""
    assert "sensitive" not in rejected[-1].diagnostic


def test_commit_exception_is_fatal_and_never_aborted_or_applied(monkeypatch):
    loader, stub, _client = _loader(monkeypatch, _release(1, 10))

    class BrokenCommit(_Prepared):
        def commit(self) -> None:
            raise RuntimeError("commit failed")

    item = BrokenCommit()
    with pytest.raises(ReleaseCommitError):
        loader.run(lambda _cancel, _snapshot: item)

    assert item.aborts == 0
    assert loader.status().state == "fatal"
    assert "applied" not in {ack.state for ack in stub.acknowledgements}


def test_reconnect_reuses_instance_id_and_resumes_last_seen_revision(monkeypatch):
    loader, stub, _client = _loader(monkeypatch, _release(1, 10))
    thread, raised = _run_in_thread(loader, lambda _cancel, _snapshot: _Prepared())
    assert wait_until(lambda: loader.status().state == "applied")
    assert wait_until(lambda: len(stub.registrations) >= 1)
    assert wait_until(
        lambda: {ack.state for ack in stub.acknowledgements}
        >= {"received", "prepared", "applied"}
    )
    applied_before = sum(ack.state == "applied" for ack in stub.acknowledgements)
    stub.disconnect()
    assert wait_until(lambda: len(stub.registrations) >= 2)
    assert wait_until(
        lambda: sum(ack.state == "applied" for ack in stub.acknowledgements)
        > applied_before
    )
    loader.stop()
    thread.join(timeout=2)

    assert not raised
    assert {registration.instance_id for registration in stub.registrations} == {
        loader.instance_id
    }
    assert stub.registrations[-1].last_seen_revision == 10
    assert len(loader._ack_latest) <= 4


def test_successful_stream_event_resets_reconnect_backoff(monkeypatch):
    loader, _stub, _client = _loader(monkeypatch, _release(1, 10))
    outcomes = iter((False, False, True, False))

    def watch_once():
        outcome = next(outcomes)
        if not outcome and loader.stats().reconnects >= 3:
            loader.stop()
        return outcome

    caps = []
    monkeypatch.setattr(loader, "_watch_once", watch_once)
    monkeypatch.setattr(
        release_module.random,
        "uniform",
        lambda _minimum, cap: caps.append(cap) or 0.0,
    )
    loader._watch_loop()

    assert caps[:3] == [0.01, 0.02, 0.01]


def test_resource_resolution_respects_concurrency_bound(monkeypatch):
    entries = []
    for version in range(1, 33):
        value = f"value-{version}"
        entries.append(
            kms_pb2.ConfigurationReleaseEntry(
                alias=f"setting_{version}",
                kind="parameter",
                ref=_ref(f"setting/{version}"),
                version=version,
                content_type="string",
                parameter_digest=_digest(value),
            )
        )
    release = kms_pb2.ConfigurationRelease(
        namespace=kms_pb2.NamespaceRef(env="prod", app="app"),
        name="runtime",
        version=1,
        entries=entries,
    )
    release.digest = release_module._release_digest(release)
    loader, _stub, client = _loader(
        monkeypatch, (release, 10), max_concurrent_fetches=4
    )

    class CountingParameters(_ParameterStub):
        def __init__(self) -> None:
            super().__init__()
            self.values = {
                version: f"value-{version}" for version in range(1, 33)
            }
            self.lock = threading.Lock()
            self.active = 0
            self.maximum = 0

        def GetParameter(self, request, **kwargs):
            with self.lock:
                self.active += 1
                self.maximum = max(self.maximum, self.active)
            try:
                time.sleep(0.01)
                return super().GetParameter(request, **kwargs)
            finally:
                with self.lock:
                    self.active -= 1

    counting = CountingParameters()
    client._param_stub = counting
    thread, raised = _run_in_thread(loader, lambda _cancel, _snapshot: _Prepared())
    assert wait_until(lambda: loader.status().state == "applied")
    loader.stop()
    thread.join(timeout=2)

    assert not raised
    assert 1 < counting.maximum <= 4


def test_typed_helper_uses_explicit_decode(monkeypatch):
    loader, _stub, _client = _loader(monkeypatch, _release(1, 10))
    stop = threading.Event()
    seen = []

    class StopsOnCommit(_Prepared):
        def commit(self) -> None:
            super().commit()
            stop.set()

    def decode(snapshot):
        return int(snapshot.parameters["setting"].split("-")[1])

    def prepare(_cancel, decoded):
        seen.append(decoded)
        return StopsOnCommit()

    run_typed_release(loader, decode, prepare, stop_event=stop)
    assert seen == [1]
