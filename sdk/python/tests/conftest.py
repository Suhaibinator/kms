from __future__ import annotations

import os
import sys

import pytest

# Ensure the SDK package is importable when pytest is run from anywhere.
_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if _ROOT not in sys.path:
    sys.path.insert(0, _ROOT)

from kms_paramstore import Client  # noqa: E402
from tests._fake_server import start_server  # noqa: E402


@pytest.fixture
def server():
    srv, addr, store = start_server()
    try:
        yield addr, store
    finally:
        srv.stop(grace=0)


@pytest.fixture
def client(server):
    addr, _store = server
    c = Client(addr, cache_ttl=0)
    try:
        yield c
    finally:
        c.close()
