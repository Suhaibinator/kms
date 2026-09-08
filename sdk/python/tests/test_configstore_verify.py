from __future__ import annotations

import asyncio
from dataclasses import asdict, dataclass

import pytest

from kms_paramstore.configstore import ContractEntry, verify_defaults, verify_defaults_async


@dataclass
class _Verdict:
    alias: str
    verdict: str


@dataclass
class _Response:
    release_name: str = "runtime"
    release_version: int = 4
    activation_revision: int = 8
    schema_version: int = 2
    schema_matches: bool = True
    entries: tuple[_Verdict, ...] = (_Verdict("runtime", "match"),)
    unverified_count: int = 0


class _Client:
    def __init__(self, schema_version: int = 2) -> None:
        self.request = None
        self.schema_version = schema_version

    def verify_release_defaults(self, **kwargs):
        self.request = kwargs
        return _Response(schema_version=self.schema_version)


class _AsyncClient(_Client):
    async def verify_release_defaults(self, **kwargs):
        self.request = kwargs
        return _Response(schema_version=self.schema_version)


def test_verify_sends_hashes_only_and_renders_value_free_report() -> None:
    client = _Client()
    result = verify_defaults(
        client, namespace="prod/app", schema_sha256="a" * 64,
        contract=(ContractEntry("password", "secret"), ContractEntry("runtime", "parameter", "json")),
        groups={"runtime": '{"port":8080}'},
    )
    assert result.passed
    assert client.request["entries"][0]["sha256"]
    assert "8080" not in repr(client.request)
    assert "result: active release matches source defaults" in result.report()


def test_async_verify_matches_sync() -> None:
    client = _AsyncClient()
    result = asyncio.run(verify_defaults_async(
        client, namespace="prod/app", schema_sha256="a" * 64,
        contract=(ContractEntry("runtime", "parameter", "json"),),
        groups={"runtime": "{}"},
    ))
    assert result.passed


@pytest.mark.parametrize("schema_version", [0, 2])
@pytest.mark.parametrize("asynchronous", [False, True])
def test_managed_verification_retains_schema_identity(schema_version: int, asynchronous: bool) -> None:
    options = dict(
        namespace="prod/app", schema_sha256="a" * 64,
        contract=(ContractEntry("runtime", "parameter", "json"),),
        groups={"runtime": "{}"},
    )
    if asynchronous:
        result = asyncio.run(verify_defaults_async(_AsyncClient(schema_version), **options))
    else:
        result = verify_defaults(_Client(schema_version), **options)
    assert result.schema_version == schema_version
    assert asdict(result)["schema_version"] == schema_version
    assert f"prod/app runtime@4#8  schema_version: {schema_version}  schema: match" in result.report()


def test_async_verify_validates_namespace_and_groups_like_sync() -> None:
    with pytest.raises(TypeError, match="requires namespace"):
        asyncio.run(verify_defaults_async(
            _AsyncClient(), namespace=" ", schema_sha256="a" * 64,
            contract=(), groups={},
        ))
    with pytest.raises(ValueError, match="missing encoded parameter group runtime"):
        asyncio.run(verify_defaults_async(
            _AsyncClient(), namespace="prod/app", schema_sha256="a" * 64,
            contract=(ContractEntry("runtime", "parameter", "json"),), groups={},
        ))
