from __future__ import annotations

import threading
import time

from kms_paramstore import Client, EventType, ParameterValue
from tests.conftest import NS, NS_APP, NS_ENV
from tests.helpers import wait_until


def _namespaces(store):
    """All (env, app) namespaces currently subscribed server-side."""
    out = []
    for s in store.subs:
        out.extend(s.namespaces)
    return out


def test_watch_fires_for_every_key_in_namespace(client, server):
    # The namespace is the unit of subscription: a plain watch(cb) receives
    # every change in the client's namespace, including keys it never named.
    _addr, store = server
    received = []
    lock = threading.Lock()

    def cb(ev):
        with lock:
            received.append(ev)

    stop = client.watch(cb)
    try:
        assert wait_until(lambda: (NS_ENV, NS_APP) in _namespaces(store)), "namespace not subscribed"
        store.put_param(NS_ENV, NS_APP, "w/a", value="v1")
        assert wait_until(lambda: any(e.key == "w/a" for e in received)), "no event delivered"
        with lock:
            ev = next(e for e in received if e.key == "w/a")
        assert ev.type == EventType.PUT
        assert ev.value == "v1"
        assert ev.namespace == "prod/app"
        assert ev.path == "/prod/app/w/a"

        # A change to an unrelated key in the same namespace is delivered too.
        store.put_param(NS_ENV, NS_APP, "totally/other", value="z")
        assert wait_until(lambda: any(e.key == "totally/other" for e in received)), \
            "namespace subscriber missed a key it never selected"
    finally:
        stop()


def test_watch_namespace_other(client, server):
    # watch_namespace subscribes to another namespace the client is authorized
    # for; the callback filters keys by its own convention.
    _addr, store = server
    received = []
    stop = client.watch_namespace("other/svc", lambda ev: received.append(ev))
    try:
        assert wait_until(lambda: ("other", "svc") in _namespaces(store))
        store.put_param("other", "svc", "k", value="1")
        assert wait_until(lambda: any(e.key == "k" and e.namespace == "other/svc" for e in received))
    finally:
        stop()


def test_watch_stop_unregisters(client, server):
    _addr, store = server
    hits = []
    stop = client.watch(lambda ev: hits.append(ev))
    assert wait_until(lambda: (NS_ENV, NS_APP) in _namespaces(store))
    stop()
    # Like the Go SDK, the stream stays up and reconnects with the reduced
    # namespace set; the stopped watcher's namespace must no longer be
    # subscribed (no other watcher or param holds it).
    assert wait_until(
        lambda: (NS_ENV, NS_APP) not in _namespaces(store)
    ), "namespace still subscribed after stop"
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

    # A non-static field subscribes to the client's namespace.
    assert wait_until(lambda: (NS_ENV, NS_APP) in _namespaces(store))
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


def test_namespace_subscription_shared(client, server):
    _addr, store = server
    store.put_param(NS_ENV, NS_APP, "a", value="1")
    store.put_param(NS_ENV, NS_APP, "b", value="2")

    class Cfg:
        a = ParameterValue("a")
        b = ParameterValue("b")

    cfg = Cfg()
    client.resolve(cfg)
    # Both non-static fields share the single namespace subscription.
    assert wait_until(lambda: (NS_ENV, NS_APP) in _namespaces(store))
    assert wait_until(lambda: sum(1 for n in _namespaces(store) if n == (NS_ENV, NS_APP)) == 1)


def test_env_pinned_does_not_hot_reload(client, server, monkeypatch):
    _addr, store = server
    store.put_param(NS_ENV, NS_APP, "dyn/pinned", value="store-value")
    monkeypatch.setenv("PINNED", "env-value")

    class Cfg:
        pinned = ParameterValue("dyn/pinned", env_var="PINNED")  # not static, but pinned

    cfg = Cfg()
    client.resolve(cfg)
    assert cfg.pinned.get() == "env-value"

    # An env-pinned value never subscribes, so a store change has no code path
    # to reach it — assert deterministically.
    store.put_param(NS_ENV, NS_APP, "dyn/pinned", value="changed")
    assert (NS_ENV, NS_APP) not in _namespaces(store)
    assert cfg.pinned.get() == "env-value"


def test_heartbeat_ack(server):
    addr, store = server
    c = Client(addr, namespace=NS)
    try:
        store.put_param(NS_ENV, NS_APP, "hb/p", value="v")  # revision -> 1
        stop = c.watch(lambda ev: None)
        assert wait_until(lambda: len(store.subs) >= 1)
        rev = store.heartbeat()
        assert wait_until(lambda: store.subs and store.subs[0].acked >= rev), "no ack for heartbeat"
        stop()
    finally:
        c.close()


def test_deleted_param_reverts_via_reconnect_snapshot(server):
    # A parameter deleted while the stream was disconnected past the replay
    # window must be recovered from the reconnect snapshot diff and revert the
    # field to its configured default (Go parity M3).
    addr, store = server
    c = Client(addr, namespace=NS)
    try:
        store.put_param(NS_ENV, NS_APP, "dp/rate", value="5")

        class Cfg:
            rate = ParameterValue("dp/rate", default="1")  # hot reload on

        cfg = Cfg()
        changes = []
        c.resolve(cfg)
        assert cfg.rate.get() == "5"
        cfg.rate.on_change(lambda old, new: changes.append((old, new)))
        assert wait_until(lambda: len(store.subs) >= 1)

        # Delete server-side WITHOUT broadcasting (the client misses the event),
        # bump the revision, then force a reconnect so a fresh snapshot omits it.
        with store.lock:
            store.params.pop((NS_ENV, NS_APP, "dp/rate"), None)
            store._next_rev()
        for sub in list(store.subs):
            with sub.cond:
                sub.closed = True
                sub.cond.notify()

        assert wait_until(lambda: cfg.rate.get() == "1", timeout=10), "did not revert to default"
        assert wait_until(lambda: ("5", "1") in changes), "on_change did not fire the revert"
    finally:
        c.close()


def test_deleted_param_reverts_via_reconcile_notfound(server):
    # The periodic reconcile lists the whole namespace; a registered parameter
    # absent from that listing was deleted while the stream missed the event and
    # must revert to default — not be swallowed (Go parity M3).
    addr, store = server
    c = Client(addr, namespace=NS)
    try:
        store.put_param(NS_ENV, NS_APP, "dp/rl", value="5")

        class Cfg:
            rate = ParameterValue("dp/rl", default="1")

        cfg = Cfg()
        changes = []
        c.resolve(cfg)
        cfg.rate.on_change(lambda old, new: changes.append((old, new)))
        assert wait_until(lambda: len(store.subs) >= 1)

        # Delete server-side (missed by the stream), then run reconcile directly.
        with store.lock:
            store.params.pop((NS_ENV, NS_APP, "dp/rl"), None)
        c._subs()._reconcile()

        assert wait_until(lambda: cfg.rate.get() == "1"), "reconcile did not revert to default"
        assert wait_until(lambda: ("5", "1") in changes)
    finally:
        c.close()


def test_no_default_keeps_last_known_on_delete(server):
    # With no default, a deletion keeps the last-known value (apps rarely want
    # config to vanish underneath them).
    addr, store = server
    c = Client(addr, namespace=NS)
    try:
        store.put_param(NS_ENV, NS_APP, "dp/keep", value="7")

        class Cfg:
            v = ParameterValue("dp/keep")  # no default

        cfg = Cfg()
        c.resolve(cfg)
        assert wait_until(lambda: len(store.subs) >= 1)
        with store.lock:
            store.params.pop((NS_ENV, NS_APP, "dp/keep"), None)
        c._subs()._reconcile()
        time.sleep(0.1)
        assert cfg.v.get() == "7", "value with no default should be kept on delete"
    finally:
        c.close()


def test_stale_reconcile_write_is_fenced(server):
    # A reconcile/reconnect read that raced a newer live event must be dropped so
    # it cannot regress a fresher value (Go parity M2).
    addr, store = server
    c = Client(addr, namespace=NS)
    try:
        store.put_param(NS_ENV, NS_APP, "sf/k", value="1")

        class Cfg:
            v = ParameterValue("sf/k")

        cfg = Cfg()
        c.resolve(cfg)
        assert wait_until(lambda: len(store.subs) >= 1)
        sub = c._subs()
        rk = (NS_ENV, NS_APP, "sf/k")

        # A newer live event applies "2" at revision 5.
        sub._set_value(rk, "2", True, 2, 5, reconcile=False)
        assert wait_until(lambda: cfg.v.get() == "2")

        # A reconcile read captured at an older snapshot revision (3) must NOT
        # regress the value back to "1".
        sub._set_value(rk, "1", True, 1, 3, reconcile=True)
        time.sleep(0.1)
        assert cfg.v.get() == "2", "stale reconcile write regressed a newer value"

        # A live event older than what we already applied is likewise dropped.
        sub._set_value(rk, "0", True, 1, 4, reconcile=False)
        time.sleep(0.1)
        assert cfg.v.get() == "2", "stale live event regressed a newer value"
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
