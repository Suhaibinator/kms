"""Test-only TLS certificate generation for loopback gRPC integration tests."""

from __future__ import annotations

import shutil
import subprocess
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class TestTLSMaterial:
    ca_cert: Path
    wrong_ca_cert: Path
    server_cert: Path
    server_key: Path


def _run_openssl(openssl: str, *args: str) -> None:
    subprocess.run(
        [openssl, *args],
        check=True,
        capture_output=True,
        text=True,
        timeout=15,
    )


def _create_ca(openssl: str, directory: Path, name: str) -> tuple[Path, Path]:
    config = directory / f"{name}.cnf"
    cert = directory / f"{name}.crt"
    key = directory / f"{name}.key"
    config.write_text(
        """\
[req]
distinguished_name = dn
prompt = no
x509_extensions = v3_ca

[dn]
CN = KMS Python SDK test CA

[v3_ca]
basicConstraints = critical, CA:true
keyUsage = critical, keyCertSign, cRLSign
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid:always
""",
        encoding="utf-8",
    )
    _run_openssl(
        openssl,
        "req",
        "-x509",
        "-newkey",
        "rsa:2048",
        "-nodes",
        "-sha256",
        "-days",
        "2",
        "-config",
        str(config),
        "-keyout",
        str(key),
        "-out",
        str(cert),
    )
    return cert, key


def create_test_tls_material(directory: Path) -> TestTLSMaterial:
    """Create an ephemeral CA and a localhost-only server certificate.

    OpenSSL is part of the Ubuntu runner image used by the Python SDK CI job.
    Generating the material at test time avoids checking a reusable private key
    into the repository while keeping the test independent of network services.
    """
    openssl = shutil.which("openssl")
    if openssl is None:
        raise RuntimeError("OpenSSL is required for the Python TLS integration test")

    directory.mkdir(parents=True, exist_ok=True)
    ca_cert, ca_key = _create_ca(openssl, directory, "ca")
    wrong_ca_cert, _ = _create_ca(openssl, directory, "wrong-ca")

    csr_config = directory / "server-csr.cnf"
    csr_config.write_text(
        """\
[req]
distinguished_name = dn
prompt = no

[dn]
CN = localhost
""",
        encoding="utf-8",
    )
    extensions = directory / "server-ext.cnf"
    extensions.write_text(
        """\
basicConstraints = critical, CA:false
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:localhost
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid,issuer
""",
        encoding="utf-8",
    )

    server_key = directory / "server.key"
    server_csr = directory / "server.csr"
    server_cert = directory / "server.crt"
    _run_openssl(
        openssl,
        "req",
        "-new",
        "-newkey",
        "rsa:2048",
        "-nodes",
        "-sha256",
        "-config",
        str(csr_config),
        "-keyout",
        str(server_key),
        "-out",
        str(server_csr),
    )
    _run_openssl(
        openssl,
        "x509",
        "-req",
        "-in",
        str(server_csr),
        "-CA",
        str(ca_cert),
        "-CAkey",
        str(ca_key),
        "-set_serial",
        "1",
        "-days",
        "2",
        "-sha256",
        "-extfile",
        str(extensions),
        "-out",
        str(server_cert),
    )

    return TestTLSMaterial(
        ca_cert=ca_cert,
        wrong_ca_cert=wrong_ca_cert,
        server_cert=server_cert,
        server_key=server_key,
    )
