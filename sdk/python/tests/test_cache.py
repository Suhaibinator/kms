from __future__ import annotations

import pytest

from kms_paramstore import Client
from kms_paramstore.errors import PermissionDeniedError
from tests.conftest import NS, NS_APP, NS_ENV
from tests.helpers import wait_until


def _client(addr, ttl):
    return Client(addr, namespace=NS, cache_ttl=ttl)


def test_parameter_cache_hit_avoids_refetch(server):
    addr, store = server
    c = _client(addr, ttl=30)
    try:
        c.put_parameter("c/p", "v1")
        assert c.get_parameter("c/p") == "v1"  # fills cache
        # Mutate the store directly (server-side), bypassing the client cache.
        store.put_param(NS_ENV, NS_APP, "c/p", value="v2")
        # A cache hit still returns the old value.
        assert c.get_parameter("c/p") == "v1"
    finally:
        c.close()


def test_parameter_cache_ttl_expiry(server):
    addr, store = server
    c = _client(addr, ttl=0.05)
    try:
        c.put_parameter("c/ttl", "v1")
        assert c.get_parameter("c/ttl") == "v1"
        store.put_param(NS_ENV, NS_APP, "c/ttl", value="v2")
        # Once the short TTL lapses the next read misses the cache and refetches.
        assert wait_until(lambda: c.get_parameter("c/ttl") == "v2", timeout=2)
    finally:
        c.close()


def test_put_invalidates_cache(server):
    addr, _store = server
    c = _client(addr, ttl=30)
    try:
        c.put_parameter("c/inv", "v1")
        assert c.get_parameter("c/inv") == "v1"
        # Writing through the client invalidates its cache.
        c.put_parameter("c/inv", "v2")
        assert c.get_parameter("c/inv") == "v2"
    finally:
        c.close()


def test_secret_cache(server):
    addr, store = server
    c = _client(addr, ttl=30)
    try:
        c.put_secret("c/s", b"one")
        assert c.get_secret("c/s").value == b"one"
        # server-side change not seen through cache
        store.secrets[(NS_ENV, NS_APP, "c/s")]["versions"].append((b"two", "application/octet-stream"))
        assert c.get_secret("c/s").value == b"one"
        # writing through the client invalidates
        c.put_secret("c/s", b"three")
        assert c.get_secret("c/s").value == b"three"
    finally:
        c.close()


def test_token_gated_secret_bypasses_cache(server):
    addr, store = server
    c = _client(addr, ttl=30)
    try:
        res = c.put_secret("c/gated", b"one", generate_access_token=True)
        token = res.access_token
        assert token
        assert c.get_secret("c/gated", secret_token=token).value == b"one"
        # The token read must not have populated the cache: a token-less read
        # has to reach the server's token gate and be rejected there.
        with pytest.raises(PermissionDeniedError):
            c.get_secret("c/gated")
        # Nor may a token read be served from cache: it sees server-side changes
        # immediately.
        store.secrets[(NS_ENV, NS_APP, "c/gated")]["versions"].append((b"two", "application/octet-stream"))
        assert c.get_secret("c/gated", secret_token=token).value == b"two"
    finally:
        c.close()


def test_disabled_cache_always_refetches(server):
    addr, store = server
    c = _client(addr, ttl=0)  # disabled
    try:
        c.put_parameter("c/none", "v1")
        assert c.get_parameter("c/none") == "v1"
        store.put_param(NS_ENV, NS_APP, "c/none", value="v2")
        assert c.get_parameter("c/none") == "v2"
    finally:
        c.close()
