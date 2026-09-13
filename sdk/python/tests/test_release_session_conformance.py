"""The synchronous and asynchronous loaders share the causal replay contract."""
import asyncio
import json
from pathlib import Path

import grpc
import pytest

from kms_paramstore._refs import NamespaceRef
from kms_paramstore.release import ReleaseLoaderError, _Candidate
from tests import test_release as sync
from tests import test_async_release as asynchronous


CASES = json.loads((Path(__file__).resolve().parents[2] / "testdata" /
                    "release_ack_conformance.json").read_text())["cases"]


def test_sync_reconnect_retains_session_but_new_execution_resets_it(monkeypatch):
    loader, stub, _ = sync._loader(monkeypatch, sync._release(1, 10))
    sessions = []
    for _ in range(2):
        prepared = sync._Prepared()
        thread, failures = sync._run_in_thread(loader, lambda _cancel, _snapshot: prepared)
        assert sync.wait_until(lambda: prepared.commits == 1 and
                               any(ack.session_id == loader._session_id and ack.state == "applied"
                                   for ack in stub.acknowledgements))
        session = loader._session_id
        sessions.append(session)
        assert loader._ack_sequence == 3
        original = {ack.sequence: ack.SerializeToString() for ack in stub.acknowledgements
                    if ack.session_id == session}
        count = len(stub.acknowledgements)
        stub.disconnect()
        assert sync.wait_until(lambda: len(stub.acknowledgements) >= count + 3)
        assert loader._session_id == session
        assert loader._ack_sequence == 3
        assert all(ack.SerializeToString() == original[ack.sequence]
                   for ack in stub.acknowledgements[count:])
        loader.stop()
        thread.join(timeout=2)
        assert not failures
    assert sessions[0] != sessions[1]


def test_sync_old_request_iterator_cannot_mark_new_session_flushed(monkeypatch):
    loader, _, _ = sync._loader(monkeypatch, sync._release(1, 10))
    loader._ack(_Candidate(sync._release(1, 10)[0], 1), "applied")
    requests = loader._watch_requests()
    next(requests)
    next(requests)
    loader._session_id = "new-execution"
    loader._ack_flushed_by_state.clear()
    with pytest.raises(StopIteration):
        next(requests)
    assert not loader._ack_flushed_by_state


def test_async_reconnect_and_unchanged_target_do_not_create_events(monkeypatch):
    async def scenario():
        loader, stub, _ = asynchronous._loader(monkeypatch, asynchronous._release(1, 10))
        sessions = []
        for _ in range(2):
            prepared = asynchronous._Prepared()
            task = asyncio.create_task(loader.run(lambda _cancel, _snapshot: prepared))
            await asynchronous._wait_for(lambda: prepared.commits == 1 and
                                         any(ack.session_id == loader._session_id and ack.state == "applied"
                                             for ack in stub.acknowledgements))
            session = loader._session_id
            sessions.append(session)
            original = {ack.sequence: ack.SerializeToString() for ack in stub.acknowledgements
                        if ack.session_id == session}
            count = len(stub.acknowledgements)
            stub.disconnect()
            await asynchronous._wait_for(lambda: len(stub.acknowledgements) >= count + 3)
            stub.activate(asynchronous._release(1, 10))
            await asyncio.sleep(0.05)
            assert prepared.commits == 1
            assert loader._session_id == session
            assert loader._ack_generation == 3
            assert all(ack.SerializeToString() == original[ack.sequence]
                       for ack in stub.acknowledgements[count:])
            loader.stop()
            await task
        assert sessions[0] != sessions[1]

    asyncio.run(scenario())


@pytest.mark.parametrize("case", CASES, ids=lambda case: case["name"])
def test_sync_causal_replay(case, monkeypatch):
    loader, _, _ = sync._loader(monkeypatch, sync._release(4, 153))
    candidate = _Candidate(sync._release(4, 153)[0], 1, activation_revision=153)
    for state in case["states"]:
        loader._ack(candidate, state)
    replays = []
    for _ in range(2):
        requests = loader._watch_requests()
        registration = next(requests).register
        assert registration.session_id == loader._session_id
        acknowledgements = [next(requests).acknowledgement
                            for _ in case["retained_sequences"]]
        assert [ack.sequence for ack in acknowledgements] == case["retained_sequences"]
        assert all(ack.target_revision == 1 and ack.activation_revision == 153
                   for ack in acknowledgements)
        replays.append([ack.SerializeToString() for ack in acknowledgements])
        requests.close()
    assert replays[0] == replays[1]


@pytest.mark.parametrize("case", CASES, ids=lambda case: case["name"])
def test_async_causal_replay(case, monkeypatch):
    async def scenario():
        loader, _, _ = asynchronous._loader(monkeypatch, asynchronous._release(4, 153))
        loader._namespace = NamespaceRef("prod", "app")
        candidate = _Candidate(asynchronous._release(4, 153)[0], 1, activation_revision=153)
        for state in case["states"]:
            loader._ack(candidate, state)

        class Call:
            def __init__(self):
                self.acks = []

            async def write(self, request):
                self.acks.append(request.acknowledgement)

        first, second = Call(), Call()
        await loader._flush_acks(first, replay=True)
        await loader._flush_acks(second, replay=True)
        assert [ack.sequence for ack in first.acks] == case["retained_sequences"]
        assert all(ack.target_revision == 1 and ack.activation_revision == 153
                   for ack in first.acks)
        assert [ack.SerializeToString() for ack in first.acks] == [
            ack.SerializeToString() for ack in second.acks]

    asyncio.run(scenario())


def test_async_partial_flush_preserves_concurrent_event(monkeypatch):
    async def scenario():
        loader, _, _ = asynchronous._loader(monkeypatch, asynchronous._release(1, 10))
        loader._namespace = NamespaceRef("prod", "app")
        candidate = _Candidate(asynchronous._release(1, 10)[0], 1, activation_revision=10)
        loader._ack(candidate, "received")
        original = loader._ack_latest["received"][1].SerializeToString()
        loader._ack(candidate, "prepared")

        class Interrupted:
            async def write(self, request):
                if request.acknowledgement.state == "received":
                    loader._ack(candidate, "applied")
                else:
                    raise RuntimeError("disconnected")

        with pytest.raises(RuntimeError, match="disconnected"):
            await loader._flush_acks(Interrupted(), replay=True)

        class Reconnected:
            def __init__(self):
                self.acks = []

            async def write(self, request):
                self.acks.append(request.acknowledgement)

        call = Reconnected()
        await loader._flush_acks(call, replay=True)
        assert [ack.sequence for ack in call.acks] == [1, 2, 3]
        assert call.acks[0].SerializeToString() == original
        assert call.acks[-1].state == "applied"

    asyncio.run(scenario())


@pytest.mark.parametrize("missing", [True, False])
def test_sync_requires_session_server(monkeypatch, missing):
    loader, stub, _ = sync._loader(monkeypatch, sync._release(1, 10))
    if missing:
        loader._stub = object()
    else:
        def unavailable(*args, **kwargs):
            raise sync._RpcFailure(grpc.StatusCode.UNIMPLEMENTED, "old server")
        stub.RegisterReleaseSession = unavailable
    with pytest.raises(ReleaseLoaderError, match="upgrade the KMS server"):
        loader._register_session()
    assert not loader._session_registered


@pytest.mark.parametrize("missing", [True, False])
def test_async_requires_session_server(monkeypatch, missing):
    async def scenario():
        loader, stub, _ = asynchronous._loader(monkeypatch, asynchronous._release(1, 10))
        loader._namespace = NamespaceRef("prod", "app")
        if missing:
            loader._stub = object()
        else:
            async def unavailable(*args, **kwargs):
                raise asynchronous._RpcFailure(grpc.StatusCode.UNIMPLEMENTED, "old server")
            stub.RegisterReleaseSession = unavailable
        with pytest.raises(ReleaseLoaderError, match="upgrade the KMS server"):
            await loader._register_session()
        assert not loader._session_registered

    asyncio.run(scenario())
