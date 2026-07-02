"""Transport-security configuration."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Optional, Union

import grpc

__all__ = ["TLSConfig", "DEFAULT_TIMEOUT"]

DEFAULT_TIMEOUT = 5.0  # seconds

PemSource = Union[str, Path, bytes]


def _read(v: PemSource) -> bytes:
    if isinstance(v, (bytes, bytearray)):
        return bytes(v)
    return Path(v).read_bytes()


@dataclass
class TLSConfig:
    """PEM sources for TLS / mTLS, given as file paths or raw bytes.

    ``ca`` enables server authentication. Provide ``cert`` and ``key`` as well
    for mutual TLS.
    """

    ca: Optional[PemSource] = None
    cert: Optional[PemSource] = None
    key: Optional[PemSource] = None

    def to_credentials(self) -> grpc.ChannelCredentials:
        return grpc.ssl_channel_credentials(
            root_certificates=_read(self.ca) if self.ca is not None else None,
            private_key=_read(self.key) if self.key is not None else None,
            certificate_chain=_read(self.cert) if self.cert is not None else None,
        )
