from __future__ import annotations

import json
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
