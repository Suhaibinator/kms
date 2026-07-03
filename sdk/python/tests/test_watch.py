from __future__ import annotations

import threading

from kms_paramstore import Client, EventType, ParameterValue
from tests.conftest import NS, NS_APP, NS_ENV
from tests.helpers import wait_until


def _selectors(store):
    """All (env, app, pattern) selectors currently registered server-side."""
    out = []
    for s in store.subs:
        out.extend(s.selectors)
    return out


def test_watch_callback_fires_on_change(client, server):
    _addr, store = server
    received = []
    lock = threading.Lock()

    def cb(ev):
        with lock:
            received.append(ev)

    stop = client.watch("w/*", cb)
    try:
        # Wait until the server has registered the subscription.
        assert wait_until(lambda: len(store.subs) >= 1), "subscription not registered"
        store.put_param(NS_ENV, NS_APP, "w/a", value="v1")
        assert wait_until(lambda: any(e.key == "w/a" for e in received)), "no event delivered"
        with lock:
            ev = next(e for e in received if e.key == "w/a")
        assert ev.type == EventType.PUT
        assert ev.value == "v1"
        assert ev.namespace == "prod/app"
        assert ev.path == "/prod/app/w/a"
    finally:
        stop()


def test_watch_absolute_pattern_other_namespace(client, server):
    _addr, store = server
    received = []
    stop = client.watch("/other/svc/*", lambda ev: received.append(ev))
    try:
        assert wait_until(lambda: ("other", "svc", "*") in _selectors(store))
        store.put_param("other", "svc", "k", value="1")
        assert wait_until(lambda: any(e.key == "k" and e.namespace == "other/svc" for e in received))
    finally:
        stop()


def test_watch_stop_unregisters(client, server):
    _addr, store = server
    hits = []
    stop = client.watch("x/*", lambda ev: hits.append(ev))
    assert wait_until(lambda: (NS_ENV, NS_APP, "x/*") in _selectors(store))
    stop()
    # Like the Go SDK, the stream stays up and reconnects with the reduced
    # selector set; the stopped watcher's selector must no longer be registered.
    assert wait_until(
        lambda: (NS_ENV, NS_APP, "x/*") not in _selectors(store)
    ), "selector still registered after stop"
    hits.clear()
    store.put_param(NS_ENV, NS_APP, "x/a", value="v")
    assert hits == [], "stopped watcher still received events"


def test_hot_reload_on_by_default(client, server):
    _addr, store = server
    store.put_param(NS_ENV, NS_APP, "dyn/rate", value="1")

    class Cfg:
        rate = ParameterValue("dyn/rate")  # hot reload on by default

    cfg = Cfg()
    changes = []
    lock = threading.Lock()

    client.resolve(cfg)
    assert cfg.rate.get() == "1"

    cfg.rate.on_change(lambda old, new: (lock.acquire(), changes.append((old, new)), lock.release()))

    # A non-static field registers ONE namespace-wide selector {ns, "*"}.
    assert wait_until(lambda: (NS_ENV, NS_APP, "*") in _selectors(store))
    store.put_param(NS_ENV, NS_APP, "dyn/rate", value="2")

    assert wait_until(lambda: cfg.rate.get() == "2"), "value did not hot-reload"
    assert wait_until(lambda: len(changes) >= 1), "on_change did not fire"
    with lock:
        assert ("1", "2") in changes


def test_static_parameter_does_not_subscribe(client, server):
    _addr, store = server
    store.put_param(NS_ENV, NS_APP, "cfg/log", value="text")

    class Cfg:
        log = ParameterValue("cfg/log", static=True)

    cfg = Cfg()
    client.resolve(cfg)
    assert cfg.log.get() == "text"

    # A static field opens no subscription at all.
    store.put_param(NS_ENV, NS_APP, "cfg/log", value="json")
    assert not store.subs, "static parameter should not register a subscription"
    assert cfg.log.get() == "text"


def test_namespace_wide_selector_shared(client, server):
    _addr, store = server
    store.put_param(NS_ENV, NS_APP, "a", value="1")
    store.put_param(NS_ENV, NS_APP, "b", value="2")

    class Cfg:
        a = ParameterValue("a")
        b = ParameterValue("b")

    cfg = Cfg()
    client.resolve(cfg)
    # Both non-static fields ride the single namespace-wide "*" selector.
    assert wait_until(lambda: (NS_ENV, NS_APP, "*") in _selectors(store))
    assert wait_until(lambda: sum(1 for s in _selectors(store) if s == (NS_ENV, NS_APP, "*")) == 1)


def test_env_pinned_does_not_hot_reload(client, server, monkeypatch):
    _addr, store = server
    store.put_param(NS_ENV, NS_APP, "dyn/pinned", value="store-value")
    monkeypatch.setenv("PINNED", "env-value")

    class Cfg:
        pinned = ParameterValue("dyn/pinned", env_var="PINNED")  # not static, but pinned

    cfg = Cfg()
    client.resolve(cfg)
    assert cfg.pinned.get() == "env-value"

    # An env-pinned value is never registered on any selector, so a store change
    # has no code path to reach it — assert deterministically.
    store.put_param(NS_ENV, NS_APP, "dyn/pinned", value="changed")
    assert (NS_ENV, NS_APP, "*") not in _selectors(store)
    assert cfg.pinned.get() == "env-value"


def test_heartbeat_ack(server):
    addr, store = server
    c = Client(addr, namespace=NS)
    try:
        store.put_param(NS_ENV, NS_APP, "hb/p", value="v")  # revision -> 1
        stop = c.watch("hb/p", lambda ev: None)
        assert wait_until(lambda: len(store.subs) >= 1)
        rev = store.heartbeat()
        assert wait_until(lambda: store.subs and store.subs[0].acked >= rev), "no ack for heartbeat"
        stop()
    finally:
        c.close()


def test_reconnect_after_server_restart(server):
    # The subscription should recover values after a transient stream failure.
    addr, store = server
    c = Client(addr, namespace=NS)
    try:
        store.put_param(NS_ENV, NS_APP, "rc/p", value="1")

        class Cfg:
            p = ParameterValue("rc/p")  # hot reload on by default

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
        store.put_param(NS_ENV, NS_APP, "rc/p", value="2")
        assert wait_until(lambda: cfg.p.get() == "2", timeout=10), "did not recover after reconnect"
    finally:
        c.close()
