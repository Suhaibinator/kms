from __future__ import annotations

import pytest

from kms_paramstore import (
    Client,
    ConfigError,
    ParameterValue,
    ParamStoreError,
    Secret,
    SecretValue,
)
from tests.conftest import NS, NS_APP, NS_ENV
from tests.helpers import wait_until


def test_resolve_from_store(client, server):
    _addr, store = server
    store.put_param(NS_ENV, NS_APP, "cfg/rate", value="42")
    client.put_secret("cfg/api", b"sk_live_x")

    class Cfg:
        rate = ParameterValue("cfg/rate")
        api = SecretValue("cfg/api")

    cfg = Cfg()
    client.resolve(cfg)

    assert cfg.rate.get() == "42"
    assert cfg.rate.initialized
    assert cfg.api.value == b"sk_live_x"
    assert cfg.api.string_value == "sk_live_x"
    # resolved secret handle still redacts
    assert str(cfg.api) == "[REDACTED]"
    assert isinstance(cfg.api, Secret)


def test_absolute_key_in_descriptor(client, server):
    _addr, store = server
    store.put_param_path("/other/svc/rate", "9")

    class Cfg:
        rate = ParameterValue("/other/svc/rate", static=True)

    cfg = Cfg()
    client.resolve(cfg)
    assert cfg.rate.get() == "9"


def test_env_override_takes_precedence(client, server, monkeypatch):
    _addr, store = server
    store.put_param(NS_ENV, NS_APP, "cfg/x", value="from-store")
    monkeypatch.setenv("MY_X", "from-env")

    class Cfg:
        x = ParameterValue("cfg/x", env_var="MY_X")
        s = SecretValue("cfg/s", env_var="MY_S")

    monkeypatch.setenv("MY_S", "env-secret")
    cfg = Cfg()
    client.resolve(cfg)
    assert cfg.x.get() == "from-env"
    assert cfg.s.value == b"env-secret"


def test_default_used_only_on_not_found(client):
    class Cfg:
        p = ParameterValue("absent", default="dev-default")
        s = SecretValue("absent-secret", default="dev-secret")

    cfg = Cfg()
    client.resolve(cfg)
    assert cfg.p.get() == "dev-default"
    assert cfg.s.value == b"dev-secret"


def test_missing_parameter_default_stays_hot_reloaded(client, server):
    _addr, store = server

    class Cfg:
        p = ParameterValue("created-later", default="dev-default")

    cfg = Cfg()
    client.resolve(cfg)
    assert cfg.p.get() == "dev-default"
    assert wait_until(lambda: (NS_ENV, NS_APP) in {
        ns for sub in store.subs for ns in sub.namespaces
    })

    store.put_param(NS_ENV, NS_APP, "created-later", value="from-store")
    assert wait_until(lambda: cfg.p.get() == "from-store"), "fallback value never subscribed"


def test_default_not_used_on_other_error_by_default():
    from tests._fake_server import start_server

    srv, addr, _store = start_server(require_bearer="need-token", whoami_namespace=NS)
    try:
        # No token -> UNAUTHENTICATED, which is NOT an absent value. A configured
        # namespace avoids a WhoAmI round trip (which would also be unauthenticated).
        c = Client(addr, namespace=NS)

        class Cfg:
            p = ParameterValue("x", default="dev-default")

        cfg = Cfg()
        with pytest.raises(ParamStoreError):
            c.resolve(cfg)
        c.close()

        # With the permissive fallback flag, the default is used instead.
        c2 = Client(addr, namespace=NS, fallback_to_defaults_on_error=True)
        cfg2 = Cfg()
        c2.resolve(cfg2)
        assert cfg2.p.get() == "dev-default"
        c2.close()
    finally:
        srv.stop(grace=0)


def test_missing_key_env_default_raises(client):
    class Cfg:
        p = ParameterValue()  # nothing configured

    with pytest.raises(ConfigError):
        client.resolve(Cfg())


def test_resolve_walks_nested_config(client, server):
    _addr, store = server
    store.put_param(NS_ENV, NS_APP, "rate", value="5")
    client.put_secret("db/pass", b"pw")

    class DB:
        password = SecretValue("db/pass")

    class App:
        rate = ParameterValue("rate")

        def __init__(self):
            self.db = DB()

    app = App()
    client.resolve(app)
    assert app.rate.get() == "5"
    assert app.db.password.value == b"pw"


def test_init_is_idempotent(client, server):
    _addr, store = server
    store.put_param(NS_ENV, NS_APP, "idem", value="one")

    class Cfg:
        p = ParameterValue("idem", static=True)

    cfg = Cfg()
    client.resolve(cfg)
    assert cfg.p.get() == "one"
    # Change the store, re-resolve: an already-initialized field is untouched.
    store.put_param(NS_ENV, NS_APP, "idem", value="two")
    client.resolve(cfg)
    assert cfg.p.get() == "one"


def test_per_instance_isolation(client, server):
    _addr, store = server

    class Cfg:
        p = ParameterValue("iso", static=True)

    store.put_param(NS_ENV, NS_APP, "iso", value="first")
    a = Cfg()
    client.resolve(a)

    store.put_param(NS_ENV, NS_APP, "iso", value="second")
    b = Cfg()
    client.resolve(b)

    # Each instance holds its own resolved value.
    assert a.p.get() == "first"
    assert b.p.get() == "second"


def test_resolve_many_fields_concurrently(client, server):
    _addr, store = server
    for i in range(12):
        store.put_param(NS_ENV, NS_APP, f"many/p{i}", value=str(i))

    ns = {f"p{i}": ParameterValue(f"many/p{i}", static=True) for i in range(12)}
    Cfg = type("Cfg", (), ns)
    cfg = Cfg()
    client.resolve(cfg)
    for i in range(12):
        assert getattr(cfg, f"p{i}").get() == str(i)
