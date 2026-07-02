"""Helpers for building gRPC transport credentials (TLS / mTLS)."""

from __future__ import annotations

from pathlib import Path
from typing import Optional

import grpc

__all__ = ["tls_from_files", "mtls_from_files", "tls_from_bytes"]


def _read(path: "str | Path") -> bytes:
    return Path(path).read_bytes()


def tls_from_files(ca_cert: "str | Path") -> grpc.ChannelCredentials:
    """Server-authenticated TLS trusting the given CA certificate (PEM)."""
    return grpc.ssl_channel_credentials(root_certificates=_read(ca_cert))


def mtls_from_files(
    client_cert: "str | Path",
    client_key: "str | Path",
    ca_cert: "str | Path",
) -> grpc.ChannelCredentials:
    """Mutual TLS using a client certificate/key and trusting the given CA."""
    return grpc.ssl_channel_credentials(
        root_certificates=_read(ca_cert),
        private_key=_read(client_key),
        certificate_chain=_read(client_cert),
    )


def tls_from_bytes(
    ca_cert: Optional[bytes] = None,
    client_cert: Optional[bytes] = None,
    client_key: Optional[bytes] = None,
) -> grpc.ChannelCredentials:
    """Build channel credentials from in-memory PEM bytes."""
    return grpc.ssl_channel_credentials(
        root_certificates=ca_cert,
        private_key=client_key,
        certificate_chain=client_cert,
    )
