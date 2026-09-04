from __future__ import annotations

import hashlib
import math
import queue
import threading
import time
from dataclasses import FrozenInstanceError
from typing import Dict, List, Optional

import pytest

import kms_paramstore.release as release_module
import kms_paramstore
from kms_paramstore import (
    ReleaseCommitError,
    ReleaseLoader,
    ReleaseLoaderConfig,
    ReleaseStartupError,
    run_typed_release,
)
from kms_paramstore.release import ClassifiedReleaseError
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
        self.binding_keys: List[str] = []
        self.bound = False
        self.has_access_token = True
        self.state = "enabled"
        self.destroyed_at_unix_ms = 0
        self.expires_at_unix_ms = 0
        self.expected_binding_key = "local-binding-key"
        self.metadata_calls = 0

    def GetSecretMetadata(self, request, **_kwargs):
        self.metadata_calls += 1
        return kms_pb2.GetSecretMetadataResponse(
            secret=kms_pb2.SecretMetadata(
                ref=request.ref, content_type="string", labels={"current": 1},
                versions=[kms_pb2.SecretVersionInfo(
                    version=version, state=self.state,
                    destroyed_at_unix_ms=self.destroyed_at_unix_ms,
                    expires_at_unix_ms=self.expires_at_unix_ms,
                    bound=self.bound, has_access_token=self.has_access_token,
                ) for version in range(1, 10)],
            )
        )

    def GetSecret(self, request, *, metadata, **_kwargs):
        assert "x-kms-secret-token" not in dict(metadata)
        token = request.secret_token
        self.tokens.append(token)
        self.binding_keys.append(request.binding_key)
        if self.has_access_token and token != "local-token":
            raise AssertionError("protected secret fetched without its local token")
        if self.bound and request.binding_key != self.expected_binding_key:
            raise AssertionError("bound secret fetched without its binding key")
        return kms_pb2.GetSecretResponse(
            ref=request.ref,
            value=f"secret-{request.version}".encode(),
            version=request.version,
            content_type="string",
        )


class _Call:
    _CLOSED = object()
    _EOF = object()

    def __init__(self, requests, owner: "_ReleaseStub") -> None:
        self._queue: "queue.Queue[object]" = queue.Queue()
        self._closed = threading.Event()
        self._owner = owner
        self.half_closed = False
        self.drained = False
        self.cancelled = False

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
            finally:
                self.half_closed = True
                self._queue.put(self._EOF)

        threading.Thread(target=read_requests, daemon=True).start()

    def __iter__(self):
        return self

    def __next__(self):
        item = self._queue.get(timeout=2.0)
        if item is self._EOF:
            self.drained = True
            raise StopIteration
        if item is self._CLOSED:
            raise RuntimeError("stream disconnected")
        return item

    def push(self, event) -> None:
        if not self._closed.is_set():
            self._queue.put(event)

    def disconnect(self) -> None:
        self._queue.put(self._CLOSED)

    def cancel(self) -> bool:
        self.cancelled = True
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

    def _auth_metadata(self):
        return []

    def _call_timeout(self, timeout):
        return timeout or 1.0

    def get_secret(
        self,
        key,
        *,
        version=0,
        label="",
        secret_token="",
        binding_key="",
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
                secret_token=secret_token,
                binding_key=binding_key,
            ),
            metadata=self._auth_metadata(),
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

    def get_secret_metadata(self, key, *, timeout=None):
        del timeout
        env, app, resource_key = key[1:].split("/", 2)
        response = self._secret_stub.GetSecretMetadata(
            kms_pb2.GetSecretMetadataRequest(ref=kms_pb2.ResourceRef(
                namespace=kms_pb2.NamespaceRef(env=env, app=app), key=resource_key,
            ))
        )
        return kms_paramstore.models._secret_info_from_proto(response.secret)


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
    settings = {
        "name": "runtime",
        "reconcile_interval": 10.0,
        "reconnect_initial": 0.01,
        "reconnect_max": 0.02,
        "secret_token_provider": lambda _alias, _path: ("local-token", True),
    }
    settings.update(config)
    loader = ReleaseLoader(
        client,
        ReleaseLoaderConfig(**settings),
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


def test_release_protocols_and_async_loader_are_publicly_exported():
    assert kms_paramstore.ReleaseDivergenceReporter is release_module.ReleaseDivergenceReporter
    assert kms_paramstore.ReleaseManifest is release_module.ReleaseManifest
    assert kms_paramstore.ClassifiedReleaseError is release_module.ClassifiedReleaseError
    assert kms_paramstore.ReleaseCandidateError is release_module.ReleaseCandidateError
    assert kms_paramstore.AsyncReleaseLoader.__name__ == "AsyncReleaseLoader"
    assert kms_paramstore.AsyncReleaseLoaderConfig.__name__ == "AsyncReleaseLoaderConfig"
    assert "restart_required" in kms_paramstore.RELEASE_REJECTION_CATEGORIES
    assert kms_paramstore.RELEASE_STATES == (
        "received",
        "prepared",
        "applied",
        "rejected",
    )


def test_release_loader_rejects_overlap_but_allows_sequential_runs(monkeypatch):
    loader, stub, _client = _loader(monkeypatch, _release(1, 10))
    first = _Prepared()
    first_thread, first_raised = _run_in_thread(
        loader, lambda _cancel, _snapshot: first
    )
    assert wait_until(lambda: first.commits == 1)
    with pytest.raises(Exception, match="already running"):
        loader.run(lambda _cancel, _snapshot: _Prepared())
    loader.stop()
    first_thread.join(timeout=2)
    assert not first_raised

    # A reused loader registers from the new authoritative startup read, not
    # the previous run's remembered stream revision.
    with stub.lock:
        stub.release, stub.revision = _release(2, 5)
    second = _Prepared()
    second_thread, second_raised = _run_in_thread(
        loader, lambda _cancel, _snapshot: second
    )
    assert wait_until(lambda: second.commits == 1)
    loader.stop()
    second_thread.join(timeout=2)
    assert not second_raised
    assert stub.registrations[-1].last_seen_revision == 5


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


def test_bound_release_uses_defensive_alias_key_copy_and_both_credentials(monkeypatch):
    keys = {"password": "local-binding-key"}
    loader, stub, client = _loader(
        monkeypatch, _release(1, 10), binding_keys=keys,
    )
    client._secret_stub.bound = True
    keys["password"] = "mutated-after-construction"
    prepared = _Prepared()
    thread, raised = _run_in_thread(loader, lambda _cancel, _snapshot: prepared)
    assert wait_until(lambda: prepared.commits == 1)
    loader.stop()
    thread.join(timeout=2)
    assert not raised
    assert client._secret_stub.tokens == ["local-token"]
    assert client._secret_stub.binding_keys == ["local-binding-key"]
    assert "local-binding-key" not in repr(loader._config)


def test_missing_binding_key_is_token_unavailable_before_plaintext_fetch(monkeypatch):
    loader, stub, client = _loader(monkeypatch, _release(1, 10))
    client._secret_stub.bound = True
    with pytest.raises(ReleaseStartupError) as caught:
        loader.run(lambda _cancel, _snapshot: _Prepared())
    assert getattr(caught.value, "category") == "token_unavailable"
    assert client._secret_stub.binding_keys == []


def test_wrong_binding_key_and_unavailable_live_version_are_resolution_failures(monkeypatch):
    loader, _stub, client = _loader(
        monkeypatch, _release(1, 10), binding_keys={"password": "wrong"},
    )
    client._secret_stub.bound = True
    with pytest.raises(ReleaseStartupError) as wrong:
        loader.run(lambda _cancel, _snapshot: _Prepared())
    assert getattr(wrong.value, "category") == "resolution_failed"

    loader, _stub, client = _loader(monkeypatch, _release(1, 10))
    client._secret_stub.state = "disabled"
    with pytest.raises(ReleaseStartupError) as disabled:
        loader.run(lambda _cancel, _snapshot: _Prepared())
    assert getattr(disabled.value, "category") == "resolution_failed"
    assert client._secret_stub.tokens == []

    loader, _stub, client = _loader(monkeypatch, _release(1, 10))
    client._secret_stub.state = "enabled"
    client._secret_stub.destroyed_at_unix_ms = 1
    with pytest.raises(ReleaseStartupError) as destroyed:
        loader.run(lambda _cancel, _snapshot: _Prepared())
    assert getattr(destroyed.value, "category") == "resolution_failed"
    assert client._secret_stub.tokens == []


def test_foreign_release_entry_is_rejected_before_resource_fetch(monkeypatch):
    initial = _release(1, 10)
    initial[0].entries[1].ref.namespace.app = "other"
    initial[0].digest = release_module._release_digest(initial[0])
    loader, _stub, client = _loader(monkeypatch, initial)
    with pytest.raises(ReleaseStartupError) as caught:
        loader.run(lambda _cancel, _snapshot: _Prepared())
    assert getattr(caught.value, "category") == "resolution_failed"
    assert client._secret_stub.metadata_calls == 0


def test_manifest_validation_precedes_fetch_and_is_immutable(monkeypatch):
    order = []

    def validate(cancel, manifest):
        assert not cancel.is_set()
        order.append("manifest")
        assert manifest.namespace == "prod/app"
        assert not hasattr(manifest.entry("password"), "has_access_token")
        with pytest.raises(TypeError):
            manifest.entries["other"] = manifest.entry("setting")

    loader, stub, client = _loader(
        monkeypatch,
        _release(1, 10),
        validate_manifest=validate,
    )
    original_get = client._param_stub.GetParameter

    def get_parameter(*args, **kwargs):
        order.append("fetch")
        return original_get(*args, **kwargs)

    client._param_stub.GetParameter = get_parameter
    prepared = _Prepared()
    thread, raised = _run_in_thread(loader, lambda _cancel, _snapshot: prepared)
    assert wait_until(lambda: loader.status().state == "applied")
    loader.stop()
    thread.join(timeout=2)
    assert not raised
    assert order[0] == "manifest"
    assert "fetch" in order


def test_classified_manifest_failure_is_redacted_and_propagated(monkeypatch):
    sensitive = "secret plaintext from local validation"

    def validate(_cancel, _manifest):
        raise ClassifiedReleaseError("config_contract_mismatch", sensitive)

    loader, stub, client = _loader(
        monkeypatch,
        _release(1, 10),
        validate_manifest=validate,
    )
    with pytest.raises(ReleaseStartupError) as caught:
        loader.run(lambda _cancel, _snapshot: _Prepared())
    assert getattr(caught.value, "category") == "config_contract_mismatch"
    assert sensitive not in str(caught.value)
    assert not client._secret_stub.tokens
    rejected = [ack for ack in stub.acknowledgements if ack.state == "rejected"]
    assert rejected
    assert rejected[-1].rejection_category == "config_contract_mismatch"
    assert rejected[-1].diagnostic == ""
    assert len(stub.calls) == 1
    assert stub.calls[0].half_closed
    assert stub.calls[0].drained
    assert not stub.calls[0].cancelled


@pytest.mark.parametrize("bad_digest", ["é" * 64, "g" * 64, "0" * 63])
def test_malformed_release_digest_is_classified_not_raised(monkeypatch, bad_digest):
    initial = _release(1, 10)
    initial[0].digest = bad_digest
    loader, stub, _client = _loader(monkeypatch, initial)
    with pytest.raises(ReleaseStartupError) as caught:
        loader.run(lambda _cancel, _snapshot: _Prepared())
    assert getattr(caught.value, "category") == "digest_mismatch"
    rejected = [ack for ack in stub.acknowledgements if ack.state == "rejected"]
    assert rejected[-1].rejection_category == "digest_mismatch"


def test_uppercase_parameter_digest_is_accepted(monkeypatch):
    initial = _release(1, 10)
    initial[0].entries[0].parameter_digest = initial[0].entries[0].parameter_digest.upper()
    initial[0].digest = release_module._release_digest(initial[0])
    loader, _stub, _client = _loader(monkeypatch, initial)
    prepared = _Prepared()
    thread, raised = _run_in_thread(loader, lambda _cancel, _snapshot: prepared)
    assert wait_until(lambda: prepared.commits == 1)
    loader.stop()
    thread.join(timeout=2)
    assert not raised


def test_empty_initial_active_fails_before_watch_or_prepare(monkeypatch):
    empty = kms_pb2.ConfigurationRelease()
    loader, stub, _client = _loader(monkeypatch, (empty, 0))
    called = False

    def prepare(_cancel, _snapshot):
        nonlocal called
        called = True
        return _Prepared()

    with pytest.raises(ReleaseStartupError, match="response was empty"):
        loader.run(prepare)
    assert not called
    assert stub.calls == []


def test_classified_prepare_failure_preserves_lkg_and_category(monkeypatch):
    loader, stub, _client = _loader(monkeypatch, _release(1, 10))

    def prepare(_cancel, snapshot):
        if snapshot.version == 2:
            raise ClassifiedReleaseError(
                "restart_required", "sensitive restart field names"
            )
        return _Prepared()

    thread, raised = _run_in_thread(loader, prepare)
    assert wait_until(lambda: loader.status().applied_version == 1)
    stub.activate(_release(2, 11))
    assert wait_until(lambda: loader.status().last_failure_category == "restart_required")
    assert loader.status().applied_version == 1
    rejected = [ack for ack in stub.acknowledgements if ack.state == "rejected"]
    assert rejected[-1].rejection_category == "restart_required"
    assert rejected[-1].diagnostic == ""
    loader.stop()
    thread.join(timeout=2)
    assert not raised


def test_stale_ack_cannot_replace_replay_state_or_increment_counter(monkeypatch):
    loader, _stub, _client = _loader(monkeypatch, _release(1, 10))
    newer_release, newer_revision = _release(2, 20)
    stale_release, stale_revision = _release(1, 10)
    newer = release_module._Candidate(newer_release, newer_revision)
    stale = release_module._Candidate(stale_release, stale_revision)
    loader._ack(newer, "rejected", "restart_required")
    count = loader.stats().acknowledgements["rejected"]
    loader._ack(stale, "rejected", "default_mismatch")
    assert loader.stats().acknowledgements["rejected"] == count
    assert loader._ack_latest["rejected"][1].activation_revision == 20
    assert loader._ack_latest["rejected"][1].rejection_category == "restart_required"


@pytest.mark.parametrize(
    ("reported", "expected"),
    [
        ((True, 3), (True, 3)),
        ((True, -1), (True, 0)),
        ((True, 100_000), (True, 65_535)),
        ((False, 9), (False, 0)),
    ],
)
def test_applied_ack_carries_only_bounded_divergence(monkeypatch, reported, expected):
    class DivergentPrepared(_Prepared):
        def release_divergence(self):
            return reported

    loader, stub, _client = _loader(monkeypatch, _release(1, 10))
    thread, raised = _run_in_thread(
        loader, lambda _cancel, _snapshot: DivergentPrepared()
    )
    assert wait_until(
        lambda: any(ack.state == "applied" for ack in stub.acknowledgements)
    )
    loader.stop()
    thread.join(timeout=2)
    assert not raised
    applied = [ack for ack in stub.acknowledgements if ack.state == "applied"][-1]
    assert (applied.applied_divergent, applied.divergent_field_count) == expected
    assert all(
        not ack.applied_divergent and ack.divergent_field_count == 0
        for ack in stub.acknowledgements
        if ack.state != "applied"
    )


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


def test_superseded_candidate_stops_after_blocked_live_metadata(monkeypatch):
    loader, stub, client = _loader(monkeypatch, _release(1, 10))
    prepared: Dict[int, _Prepared] = {}

    thread, raised = _run_in_thread(
        loader,
        lambda _cancel, snapshot: prepared.setdefault(snapshot.version, _Prepared()),
    )
    assert wait_until(lambda: loader.status().applied_version == 1)
    client._secret_stub.tokens.clear()
    client._secret_stub.binding_keys.clear()

    metadata_entered = threading.Event()
    release_metadata = threading.Event()
    original_get_metadata = client.get_secret_metadata
    should_block = True

    def blocking_get_metadata(*args, **kwargs):
        nonlocal should_block
        result = original_get_metadata(*args, **kwargs)
        if should_block:
            should_block = False
            metadata_entered.set()
            assert release_metadata.wait(timeout=2)
        return result

    client.get_secret_metadata = blocking_get_metadata
    stub.activate(_release(2, 20))
    assert metadata_entered.wait(timeout=2)
    stub.activate(_release(3, 30))
    assert wait_until(lambda: loader.status().observed_version == 3)
    release_metadata.set()

    assert wait_until(lambda: loader.status().applied_version == 3)
    loader.stop()
    thread.join(timeout=2)

    assert not raised
    assert 2 not in prepared
    assert prepared[3].commits == 1
    assert client._secret_stub.tokens == ["local-token"]


def test_active_fence_includes_release_name(monkeypatch):
    loader, stub, _client = _loader(monkeypatch, _release(1, 10))
    stale = _Prepared()
    current = _Prepared()

    def prepare(_cancel, snapshot):
        if snapshot.version == 1:
            with stub.lock:
                renamed = kms_pb2.ConfigurationRelease()
                renamed.CopyFrom(stub.release)
                renamed.name = "different-release"
                stub.release = renamed
            return stale
        return current

    thread, raised = _run_in_thread(loader, prepare)
    assert wait_until(lambda: stale.aborts == 1)
    stub.activate(_release(2, 11))
    assert wait_until(lambda: loader.status().applied_version == 2)
    loader.stop()
    thread.join(timeout=2)
    assert not raised
    assert stale.commits == 0
    assert current.commits == 1


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
    assert loader.status().state == "applied"
    assert loader.status().last_failure_category == ""
    assert loader.status().last_failure_unix_ms == 0
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


@pytest.mark.parametrize(
    ("alias", "category"),
    [("setting", "digest_mismatch"), ("password", "version_mismatch")],
)
def test_empty_pinned_content_type_rejects_without_replacing_lkg(
    monkeypatch, alias, category
):
    loader, stub, _client = _loader(monkeypatch, _release(1, 10))
    prepared: Dict[int, _Prepared] = {}
    published = []

    def prepare(_cancel, snapshot):
        published.append(snapshot.version)
        return prepared.setdefault(snapshot.version, _Prepared())

    thread, raised = _run_in_thread(loader, prepare)
    assert wait_until(lambda: loader.status().applied_version == 1)

    tampered = _release(2, 20)
    entry = next(item for item in tampered[0].entries if item.alias == alias)
    entry.content_type = ""
    # Recompute the projection digest so this reaches exact-version resolution;
    # an empty pin must never act as a wildcard.
    tampered[0].digest = release_module._release_digest(tampered[0])
    stub.activate(tampered)

    assert wait_until(lambda: loader.status().last_failure_category == category)
    assert loader.status().applied_version == 1
    assert published == [1]
    assert prepared[1].commits == 1
    assert 2 not in prepared
    assert wait_until(
        lambda: any(
            ack.state == "rejected" and ack.activation_revision == 20
            for ack in stub.acknowledgements
        )
    )
    loader.stop()
    thread.join(timeout=2)
    assert not raised


def test_commit_exception_is_fatal_and_never_aborted_or_applied(monkeypatch):
    loader, stub, _client = _loader(monkeypatch, _release(1, 10))

    class BrokenCommit(_Prepared):
        def commit(self) -> None:
            raise RuntimeError("commit failed")

    item = BrokenCommit()
    with pytest.raises(ReleaseCommitError):
        loader.run(lambda _cancel, _snapshot: item)

    assert item.aborts == 0
    assert loader.status().state == "rejected"
    assert loader.stats().rejections["internal"] == 1
    assert "applied" not in {ack.state for ack in stub.acknowledgements}


def test_commit_return_value_is_fatal_contract_violation(monkeypatch):
    class ReturningPrepared(_Prepared):
        def commit(self):
            self.commits += 1
            return "not-none"

    loader, stub, _client = _loader(monkeypatch, _release(1, 10))
    prepared = ReturningPrepared()
    with pytest.raises(ReleaseCommitError):
        loader.run(lambda _cancel, _snapshot: prepared)
    assert prepared.commits == 1
    assert prepared.aborts == 0
    assert loader.status().state == "rejected"
    assert loader.stats().rejections["internal"] == 1
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


def test_sync_loader_dedupes_replayed_and_unchanged_active_identity(monkeypatch):
    loader, stub, _client = _loader(monkeypatch, _release(1, 10))
    prepared = _Prepared()
    thread, raised = _run_in_thread(loader, lambda _cancel, _snapshot: prepared)
    assert wait_until(lambda: prepared.commits == 1)
    assert loader.stats().candidates == 1

    # Both a replayed stream activation and a reconciliation offer carry the
    # already applied identity and must not resolve/prepare/commit it again.
    stub.activate(_release(1, 10))
    loader._offer_candidate(release_module._Candidate(*_release(1, 10)))
    time.sleep(0.1)
    assert prepared.commits == 1
    assert loader.stats().candidates == 1

    loader.stop()
    thread.join(timeout=2)
    assert not raised


@pytest.mark.parametrize("outcome", ["rejected", "superseded"])
def test_sync_old_outcome_cannot_unlock_newer_inflight_reconciliation(monkeypatch, outcome):
    loader, _stub, _client = _loader(monkeypatch, _release(1, 10))
    release_a, revision_a = _release(1, 10)
    release_b, revision_b = _release(2, 11)
    candidate_a = release_module._Candidate(release_a, revision_a)
    candidate_b = release_module._Candidate(release_b, revision_b)

    loader._offer_candidate(candidate_a)
    assert loader._take_candidate(0.01) == candidate_a
    loader._active_identity = candidate_a.identity
    loader._active_cancel = threading.Event()
    loader._offer_candidate(candidate_b)
    loader._record_retry_eligibility(candidate_a, outcome)
    assert loader._retry_identity is None
    assert loader._take_candidate(0.01) == candidate_b

    # B is now in flight. A's stale rejected status must not make an unchanged
    # reconciliation of B eligible for a duplicate preparation.
    loader._active_identity = candidate_b.identity
    loader._offer_candidate(candidate_b, source="reconciliation")
    assert loader._take_candidate(0.01) is None


def test_sync_exact_latest_rejection_retries_only_from_reconciliation(monkeypatch):
    loader, _stub, _client = _loader(monkeypatch, _release(1, 10))
    release, revision = _release(1, 10)
    candidate = release_module._Candidate(release, revision)
    loader._offer_candidate(candidate)
    assert loader._take_candidate(0.01) == candidate
    loader._record_retry_eligibility(candidate, "rejected")
    loader._offer_candidate(candidate)
    assert loader._take_candidate(0.01) is None
    loader._offer_candidate(candidate, source="reconciliation")
    assert loader._take_candidate(0.01) == candidate


def test_sync_status_stats_and_prepared_state_are_canonical(monkeypatch):
    loader, _stub, _client = _loader(monkeypatch, _release(1, 10))

    def prepare(_cancel, _snapshot):
        assert loader.status().state == "received"
        return _Prepared()

    thread, raised = _run_in_thread(loader, prepare)
    assert wait_until(lambda: loader.status().state == "applied")
    loader.stop()
    thread.join(timeout=2)
    assert not raised
    status = loader.status()
    stats = loader.stats()
    assert status.last_resolution_duration_ms >= 0
    assert status.reconnects == stats.reconnects
    assert stats.candidates == 1
    assert stats.applied == 1
    assert stats.rejected == stats.rejections


@pytest.mark.parametrize("failure", ["raises", "returns"])
def test_sync_abort_contract_failure_is_fatal_internal(monkeypatch, failure):
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
        loader.run(prepare)
    assert loader.status().last_failure_category == "internal"
    assert loader.stats().rejected["internal"] == 1


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
def test_sync_release_timing_must_be_finite_positive(monkeypatch, field, value):
    with pytest.raises(Exception, match="finite and positive|backoff"):
        _loader(monkeypatch, _release(1, 10), **{field: value})


def test_sync_external_stop_relay_exits_after_each_reused_run(monkeypatch):
    loader, stub, _client = _loader(monkeypatch, _release(1, 10))
    external = threading.Event()
    for version in (1, 2):
        stub.release, stub.revision = _release(version, 9 + version)
        prepared = _Prepared()
        raised = []

        def run():
            try:
                loader.run(lambda _cancel, _snapshot: prepared, stop_event=external)
            except BaseException as exc:
                raised.append(exc)

        thread = threading.Thread(target=run)
        thread.start()
        assert wait_until(lambda: prepared.commits == 1)
        relay = loader._relay_thread
        assert relay is not None and relay.is_alive()
        loader.stop()
        thread.join(timeout=2)
        assert not raised
        assert not relay.is_alive()
