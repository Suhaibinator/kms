from __future__ import annotations

import pytest

from kms_paramstore import NotFoundError
from tests.conftest import NS


def test_put_and_get_parameter(client, server):
    _addr, store = server
    res = client.put_parameter("rate", "100", content_type="integer")
    assert res.version == 1
    assert res.revision >= 1

    assert client.get_parameter("rate") == "100"

    res2 = client.put_parameter("rate", "200")
    assert res2.version == 2
    assert client.get_parameter("rate") == "200"
    # explicit older version
    assert client.get_parameter("rate", version=1) == "100"


def test_absolute_path_addresses_another_namespace(client, server):
    _addr, store = server
    # An absolute /env/app/key resolves cross-namespace regardless of the client's.
    client.put_parameter("/other/svc/rate", "7")
    assert client.get_parameter("/other/svc/rate") == "7"
    assert ("other", "svc", "rate") in store.params


def test_relative_key_resolves_to_client_namespace(client, server):
    _addr, store = server
    client.put_parameter("billing/limit", "5")
    assert ("prod", "app", "billing/limit") in store.params
    p = store.current_param(("prod", "app", "billing/limit"))
    assert p.ref.namespace.env == "prod" and p.ref.key == "billing/limit"


def test_get_parameter_not_found(client):
    with pytest.raises(NotFoundError):
        client.get_parameter("missing")


def test_list_parameters_pagination(client, server):
    for i in range(5):
        client.put_parameter(f"list/p{i}", str(i))
    seen = []
    token = ""
    while True:
        items, token = client.list_parameters(NS, "list", page_size=2, page_token=token)
        seen.extend(p.key for p in items)
        if not token:
            break
    assert seen == [f"list/p{i}" for i in range(5)]


def test_list_parameters_defaults_to_client_namespace(client):
    client.put_parameter("a/one", "1")
    client.put_parameter("a/two", "2")
    client.put_parameter("b/three", "3")
    items, _ = client.list_parameters(key_prefix="a", page_size=100)
    keys = {p.key for p in items}
    assert keys == {"a/one", "a/two"}
    # display path reflects the namespace
    assert {p.path for p in items} == {"/prod/app/a/one", "/prod/app/a/two"}


def test_delete_parameter(client):
    client.put_parameter("gone", "v")
    rev = client.delete_parameter("gone")
    assert rev >= 1
    with pytest.raises(NotFoundError):
        client.get_parameter("gone")
    with pytest.raises(NotFoundError):
        client.delete_parameter("gone")
