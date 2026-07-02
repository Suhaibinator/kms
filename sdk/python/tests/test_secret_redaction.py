from __future__ import annotations

import io
import json
import logging

import pytest

from kms_paramstore import Secret


def test_str_repr_format_redact():
    s = Secret(b"hunter2", path="/p", version=3, content_type="string")
    assert str(s) == "[REDACTED]"
    assert repr(s) == "[REDACTED]"
    assert f"{s}" == "[REDACTED]"
    assert f"{s!r}" == "[REDACTED]"
    assert "{}".format(s) == "[REDACTED]"
    assert "%s" % (s,) == "[REDACTED]"
    assert "%r" % (s,) == "[REDACTED]"


def test_plaintext_accessors():
    s = Secret(b"hunter2")
    assert s.value == b"hunter2"
    assert s.string_value == "hunter2"
    assert not s.is_empty()
    assert bool(s) is True
    assert len(s) == 7


def test_metadata_exposed_not_value():
    s = Secret(b"x", path="/a/b", version=7, content_type="text/plain")
    assert s.path == "/a/b"
    assert s.version == 7
    assert s.content_type == "text/plain"


def test_logging_does_not_leak(caplog):
    s = Secret(b"topsecret")
    logger = logging.getLogger("test.redaction")
    with caplog.at_level(logging.INFO, logger="test.redaction"):
        logger.info("resolved secret: %s", s)
        logger.info("resolved secret repr: %r", s)
    text = caplog.text
    assert "topsecret" not in text
    assert "[REDACTED]" in text


def test_percent_and_stream_formatting_do_not_leak():
    s = Secret(b"leakme")
    buf = io.StringIO()
    print(s, file=buf)
    assert "leakme" not in buf.getvalue()
    assert "[REDACTED]" in buf.getvalue()


def test_json_dumps_does_not_leak_silently():
    s = Secret(b"leakme")
    # A raw Secret is not JSON-serializable, so it raises rather than leaking.
    with pytest.raises(TypeError):
        json.dumps({"api_key": s})


def test_equality_by_plaintext_without_rendering():
    assert Secret(b"a") == Secret(b"a")
    assert Secret(b"a") != Secret(b"b")


def test_unhashable():
    with pytest.raises(TypeError):
        hash(Secret(b"a"))
    with pytest.raises(TypeError):
        {Secret(b"a")}
