from __future__ import annotations

import threading

from kms_paramstore import Client, EventType, ParameterValue
from tests.helpers import wait_until


def test_watch_callback_fires_on_change(client, server):
    _addr, store = server
    received = []
    lock = threading.Lock()

    def cb(ev):
        with lock:
            received.append(ev)

    stop = client.watch("/w/*", cb)
    try:
        # Wait until the server has registered the subscription.
        assert wait_until(lambda: len(store.subs) >= 1), "subscription not registered"
        store.put_param("/w/a", "v1")
        assert wait_until(lambda: any(e.path == "/w/a" for e in received)), "no event delivered"
        with lock:
            ev = next(e for e in received if e.path == "/w/a")
        assert ev.type == EventType.PUT
        assert ev.value == "v1"
    finally:
        stop()


def test_watch_stop_unregisters(client, server):
    _addr, store = server
    hits = []
    stop = client.watch("/x/*", lambda ev: hits.append(ev))
    assert wait_until(lambda: len(store.subs) >= 1)
    stop()
    # Like the Go SDK, the stream stays up and reconnects with the reduced path
    # set; the stopped watcher's pattern must no longer be subscribed. Once that
    # holds, a change to the old pattern is deterministically not delivered
    # (the server only enqueues events matching a subscription's paths).
    assert wait_until(
        lambda: len(store.subs) == 0 or all(list(s.paths) == [] for s in store.subs)
    ), "pattern still subscribed after stop"
    hits.clear()
    store.put_param("/x/a", "v")
    assert hits == [], "stopped watcher still received events"


def test_dynamic_parameter_hot_reload(client, server):
    _addr, store = server
    store.put_param("/dyn/rate", "1")

    class Cfg:
        rate = ParameterValue("/dyn/rate", dynamic=True)

    cfg = Cfg()
    changes = []
    lock = threading.Lock()

    client.resolve(cfg)
    assert cfg.rate.get() == "1"

    cfg.rate.on_change(lambda old, new: (lock.acquire(), changes.append((old, new)), lock.release()))

    # Wait for the subscription to be live, then push a change.
    assert wait_until(lambda: len(store.subs) >= 1)
    store.put_param("/dyn/rate", "2")

    assert wait_until(lambda: cfg.rate.get() == "2"), "value did not hot-reload"
    assert wait_until(lambda: len(changes) >= 1), "on_change did not fire"
    with lock:
        assert ("1", "2") in changes


def test_env_pinned_dynamic_does_not_hot_reload(client, server, monkeypatch):
    _addr, store = server
    store.put_param("/dyn/pinned", "store-value")
    monkeypatch.setenv("PINNED", "env-value")

    class Cfg:
        pinned = ParameterValue("/dyn/pinned", env_var="PINNED", dynamic=True)

    cfg = Cfg()
    client.resolve(cfg)
    assert cfg.pinned.get() == "env-value"

    # An env-pinned value is never registered on the subscription, so a store
    # change has no code path to reach it — assert deterministically.
    store.put_param("/dyn/pinned", "changed")
    assert not any("/dyn/pinned" in list(s.paths) for s in store.subs)
    assert cfg.pinned.get() == "env-value"


def test_heartbeat_ack(server):
    addr, store = server
    c = Client(addr)
    try:
        store.put_param("/hb/p", "v")  # revision -> 1
        stop = c.watch("/hb/p", lambda ev: None)
        assert wait_until(lambda: len(store.subs) >= 1)
        rev = store.heartbeat()
        assert wait_until(lambda: store.subs and store.subs[0].acked >= rev), "no ack for heartbeat"
        stop()
    finally:
        c.close()


def test_reconnect_after_server_restart(server):
    # The subscription should recover values after a transient stream failure.
    addr, store = server
    c = Client(addr)
    try:
        store.put_param("/rc/p", "1")

        class Cfg:
            p = ParameterValue("/rc/p", dynamic=True)

        cfg = Cfg()
        c.resolve(cfg)
        assert wait_until(lambda: len(store.subs) >= 1)

        # Drop the active server-side stream; the client must reconnect.
        for sub in list(store.subs):
            with sub.cond:
                sub.closed = True
                sub.cond.notify()
        # After reconnection a new change still propagates.
        assert wait_until(lambda: len(store.subs) >= 1, timeout=10)
        store.put_param("/rc/p", "2")
        assert wait_until(lambda: cfg.p.get() == "2", timeout=10), "did not recover after reconnect"
    finally:
        c.close()
