from __future__ import annotations

import json
import sys
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import pytest

from kms_paramstore.configstore import canonical_parameter_value, parameter_hash


def test_shared_go_canonical_vectors() -> None:
    path = Path(__file__).parents[2] / "go/configstore/testdata/canonical_vectors.json"
    vectors = json.loads(path.read_text(encoding="utf-8"))
    for vector in vectors:
        if vector.get("error"):
            with pytest.raises(ValueError):
                canonical_parameter_value(vector["content_type"], vector["input"])
        else:
            canonical = canonical_parameter_value(vector["content_type"], vector["input"])
            assert canonical.decode() == vector["canonical"], vector["name"]
            assert parameter_hash(vector["content_type"], vector["input"]) == vector["sha256"]
            assert canonical_parameter_value(vector["content_type"], canonical) == canonical


def test_bytes_passthrough_is_defensive() -> None:
    value = b"raw\x00bytes"
    assert canonical_parameter_value("bytes", value) == value


def test_deep_canonicalization_is_thread_safe_and_does_not_change_recursion_limit() -> None:
    before = sys.getrecursionlimit()
    document = "[" * 1000 + "[]" + "]" * 1000
    with ThreadPoolExecutor(max_workers=8) as executor:
        results = list(executor.map(lambda _: canonical_parameter_value("json", document), range(32)))
    assert all(result == document.encode() for result in results)
    assert sys.getrecursionlimit() == before
