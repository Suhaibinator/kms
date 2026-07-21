from __future__ import annotations

from unittest import mock

import pytest

from kms_paramstore import Client, ConfigError
from tests.conftest import NS


def test_transport_security_must_be_explicit():
    with mock.patch(
        "kms_paramstore.client.grpc.insecure_channel",
        side_effect=AssertionError("default selected an insecure channel"),
    ) as insecure_channel:
        with pytest.raises(ConfigError, match="(?i)tls|insecure"):
            Client("example.internal:8443", token="identity-token")

    insecure_channel.assert_not_called()


def test_explicit_insecure_transport_remains_available(server):
    addr, store = server
    client = Client(addr, namespace=NS, insecure=True)
    try:
        client.put_parameter("transport/control", "ok")
        assert client.get_parameter("transport/control") == "ok"
        assert ("prod", "app", "transport/control") in store.params
    finally:
        client.close()


def test_tls_transport_remains_available():
    credentials = object()
    channel = mock.MagicMock()

    with (
        mock.patch(
            "kms_paramstore.client.grpc.secure_channel", return_value=channel
        ) as secure_channel,
        mock.patch("kms_paramstore.client.grpc.insecure_channel") as insecure_channel,
    ):
        client = Client("example.internal:8443", tls=credentials)
        client.close()

    secure_channel.assert_called_once()
    assert secure_channel.call_args.args[:2] == ("example.internal:8443", credentials)
    insecure_channel.assert_not_called()


def test_tls_and_insecure_opt_in_are_mutually_exclusive():
    with pytest.raises(ConfigError, match="(?i)mutually exclusive"):
        Client("example.internal:8443", tls=object(), insecure=True)


def test_prebuilt_channel_remains_an_explicit_transport():
    channel = mock.MagicMock()
    client = Client(channel=channel)
    client.close()

    channel.close.assert_not_called()
