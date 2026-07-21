from __future__ import annotations

from unittest import mock

import grpc
import pytest

from kms_paramstore import Client, ConfigError, ParamStoreError, TLSConfig
from tests._fake_server import start_server
from tests._tls_support import create_test_tls_material
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


def test_tls_config_performs_verified_loopback_rpc(tmp_path):
    material = create_test_tls_material(tmp_path)
    server_credentials = grpc.ssl_server_credentials(
        ((material.server_key.read_bytes(), material.server_cert.read_bytes()),)
    )
    server, address, _ = start_server(
        whoami_namespace=NS,
        server_credentials=server_credentials,
    )

    try:
        # Exercise the production TLSConfig file-loading and credential path,
        # then cross a real TLS socket and gRPC handler boundary.
        client = Client(address, tls=TLSConfig(ca=material.ca_cert), timeout=2)
        try:
            identity = client.who_am_i()
            assert identity.name == "test-client"
            assert identity.namespace == NS
            assert identity.auth_method == "token"
        finally:
            client.close()

        # The same endpoint and RPC must fail when the only changed input is
        # the trust root. This prevents a false-positive test that merely proves
        # the server is reachable without proving certificate verification.
        untrusted_client = Client(
            address,
            tls=TLSConfig(ca=material.wrong_ca_cert),
            timeout=2,
        )
        try:
            with pytest.raises(ParamStoreError, match="(?i)unavailable"):
                untrusted_client.who_am_i()
        finally:
            untrusted_client.close()

        # Trusting the issuer is insufficient when the endpoint name is not in
        # the leaf SAN. The certificate intentionally contains DNS:localhost
        # but no IP SAN for 127.0.0.1.
        wrong_name_address = address.replace("localhost", "127.0.0.1", 1)
        wrong_name_client = Client(
            wrong_name_address,
            tls=TLSConfig(ca=material.ca_cert),
            timeout=2,
        )
        try:
            with pytest.raises(ParamStoreError, match="(?i)unavailable"):
                wrong_name_client.who_am_i()
        finally:
            wrong_name_client.close()
    finally:
        server.stop(grace=0).wait(timeout=5)


def test_tls_and_insecure_opt_in_are_mutually_exclusive():
    with pytest.raises(ConfigError, match="(?i)mutually exclusive"):
        Client("example.internal:8443", tls=object(), insecure=True)


def test_prebuilt_channel_remains_an_explicit_transport():
    channel = mock.MagicMock()
    client = Client(channel=channel)
    client.close()

    channel.close.assert_not_called()
