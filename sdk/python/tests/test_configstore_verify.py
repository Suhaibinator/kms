from __future__ import annotations

import asyncio
from dataclasses import dataclass

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
    schema_matches: bool = True
    entries: tuple[_Verdict, ...] = (_Verdict("runtime", "match"),)
    unverified_count: int = 0


class _Client:
    def __init__(self) -> None:
        self.request = None

    def verify_release_defaults(self, **kwargs):
        self.request = kwargs
        return _Response()


class _AsyncClient(_Client):
    async def verify_release_defaults(self, **kwargs):
        self.request = kwargs
        return _Response()


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
