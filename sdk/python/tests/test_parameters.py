from __future__ import annotations

import pytest

from kms_paramstore import NotFoundError


def test_put_and_get_parameter(client, server):
    _addr, store = server
    res = client.put_parameter("/app/rate", "100", content_type="integer")
    assert res.version == 1
    assert res.revision >= 1

    assert client.get_parameter("/app/rate") == "100"

    res2 = client.put_parameter("/app/rate", "200")
    assert res2.version == 2
    assert client.get_parameter("/app/rate") == "200"
    # explicit older version
    assert client.get_parameter("/app/rate", version=1) == "100"


def test_get_parameter_not_found(client):
    with pytest.raises(NotFoundError):
        client.get_parameter("/missing")


def test_list_parameters_pagination(client, server):
    for i in range(5):
        client.put_parameter(f"/list/p{i}", str(i))
    seen = []
    token = ""
    while True:
        items, token = client.list_parameters("/list", page_size=2, page_token=token)
        seen.extend(p.path for p in items)
        if not token:
            break
    assert seen == [f"/list/p{i}" for i in range(5)]


def test_list_parameters_prefix_filtering(client):
    client.put_parameter("/a/one", "1")
    client.put_parameter("/a/two", "2")
    client.put_parameter("/b/three", "3")
    items, _ = client.list_parameters("/a", page_size=100)
    paths = {p.path for p in items}
    assert paths == {"/a/one", "/a/two"}


def test_delete_parameter(client):
    client.put_parameter("/gone", "v")
    rev = client.delete_parameter("/gone")
    assert rev >= 1
    with pytest.raises(NotFoundError):
        client.get_parameter("/gone")
    with pytest.raises(NotFoundError):
        client.delete_parameter("/gone")
