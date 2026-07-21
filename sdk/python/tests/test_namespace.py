from __future__ import annotations

import pytest

from kms_paramstore import Client, ConfigError, NoNamespaceError
from tests._fake_server import start_server


def _server(**kw):
    return start_server(**kw)


def test_whoami_discovers_namespace():
    srv, addr, store = _server(whoami_namespace="prod/gradethis")
    try:
        # No namespace configured: the client discovers it from WhoAmI on first use.
        c = Client(addr, insecure=True)
        c.put_parameter("rate", "10")  # relative key -> resolves to prod/gradethis
        assert ("prod", "gradethis", "rate") in store.params
        assert c.get_parameter("rate") == "10"

        me = c.who_am_i()
        assert me.namespace == "prod/gradethis"
        assert me.kind == "client"
        c.close()
    finally:
        srv.stop(grace=0)


def test_whoami_called_once_and_cached():
    srv, addr, store = _server(whoami_namespace="prod/gradethis")
    try:
        c = Client(addr, insecure=True)
        # Several relative-key ops; discovery must not depend on repeated WhoAmI.
        c.put_parameter("a", "1")
        c.put_parameter("b", "2")
        assert c.get_parameter("a") == "1"
        # Flip the server's reported namespace; the cached one still governs.
        store.whoami_namespace = "staging/other"
        c.put_parameter("c", "3")
        assert ("prod", "gradethis", "c") in store.params
        c.close()
    finally:
        srv.stop(grace=0)


def test_unbound_identity_relative_key_raises():
    srv, addr, store = _server(whoami_namespace=None)  # unbound
    try:
        c = Client(addr, insecure=True)
        with pytest.raises(NoNamespaceError) as ei:
            c.get_parameter("rate")
        assert "rate" in str(ei.value)  # message names the key
        # NoNamespaceError is a ConfigError.
        assert isinstance(ei.value, ConfigError)
        c.close()
    finally:
        srv.stop(grace=0)


def test_unbound_identity_absolute_key_works():
    srv, addr, store = _server(whoami_namespace=None)
    try:
        c = Client(addr, insecure=True)
        # Absolute paths need no namespace.
        c.put_parameter("/prod/svc/k", "v")
        assert c.get_parameter("/prod/svc/k") == "v"
        c.close()
    finally:
        srv.stop(grace=0)


def test_bad_namespace_config_fails_fast():
    srv, addr, _store = _server(whoami_namespace="prod/app")
    try:
        # Structural failures are caught client-side...
        with pytest.raises(ConfigError):
            Client(addr, namespace="no-slash", insecure=True)
        with pytest.raises(ConfigError):
            Client(addr, namespace="prod/", insecure=True)  # empty app
        with pytest.raises(ConfigError):
            Client(addr, namespace="prod/app/extra", insecure=True)  # extra slash
        # ...but the character set is the server's authority, so a name the SDK
        # can't prove wrong (uppercase) is accepted client-side (Go parity).
        Client(addr, namespace="prod/App", insecure=True).close()
    finally:
        srv.stop(grace=0)


def test_configured_namespace_skips_whoami():
    # require_bearer with no token would make WhoAmI fail; a configured namespace
    # must avoid that call entirely for absolute-free relative reads.
    srv, addr, store = _server(require_bearer="tok", whoami_namespace="prod/app")
    try:
        c = Client(addr, namespace="prod/app", token="tok", insecure=True)
        c.put_parameter("k", "v")
        assert c.get_parameter("k") == "v"
        c.close()
    finally:
        srv.stop(grace=0)
