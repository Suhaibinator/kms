from __future__ import annotations

from pathlib import Path
from typing import Annotated
import asyncio
import datetime as dt
import json

import pytest
from pydantic import BaseModel, ConfigDict, Field

from kms_paramstore.configgen import StaleArtifactsError, generate_artifacts, write_artifacts
from kms_paramstore.configstore import Parameter, SecretField
from kms_paramstore.configstore import Callbacks, ContractEntry
from kms_paramstore.release import ReleaseSnapshot
from kms_paramstore.secret import Secret


class GeneratedConfig(BaseModel):
    model_config = ConfigDict(frozen=True, strict=True, extra="forbid", arbitrary_types_allowed=True)

    port: Annotated[int, Field(ge=1, le=65535), Parameter("runtime", views=("server",))] = 8080
    password: Annotated[Secret, SecretField("password", reload="restart", views=("server",))]


def test_generation_is_deterministic_and_contract_matches_other_sdks(tmp_path: Path) -> None:
    first = generate_artifacts(GeneratedConfig, source_module="app.config")
    second = generate_artifacts(GeneratedConfig, source_module="app.config")
    assert first == second
    assert '"format": "kms-config-contract/v1"' in first.contract
    assert '"language": "python"' in first.contract
    assert first.schema_sha256 in first.contract
    assert "GeneratedConfigStore" in first.binding
    assert '"minimum": 1' in first.schema and '"maximum": 65535' in first.schema

    binding, schema, contract = tmp_path / "generated.py", tmp_path / "schema.json", tmp_path / "contract.json"
    write_artifacts(first, binding=binding, schema=schema, contract=contract)
    write_artifacts(first, binding=binding, schema=schema, contract=contract, check=True)
    binding.write_text("stale", encoding="utf-8")
    with pytest.raises(StaleArtifactsError) as caught:
        write_artifacts(first, binding=binding, schema=schema, contract=contract, check=True)
    assert str(binding) in str(caught.value)


def test_committed_artifacts_are_current() -> None:
    from tests.fixtures.configgen.source import ApplicationConfig

    root = Path(__file__).parent / "fixtures/configgen"
    artifacts = generate_artifacts(
        ApplicationConfig,
        source_module="tests.fixtures.configgen.source",
        source_type="ApplicationConfig",
    )
    write_artifacts(
        artifacts,
        binding=root / "config_generated.py",
        schema=root / "config.schema.json",
        contract=root / "config.contract.json",
        check=True,
    )


def test_committed_generated_binding_is_a_typed_runtime_consumer() -> None:
    from tests.fixtures.configgen.config_generated import (
        CONTRACT, GeneratedConfigStore, ServerConfigView, Snapshot,
    )

    store = GeneratedConfigStore({})
    prepared = store.prepare(ReleaseSnapshot(
        namespace="prod/app", name="runtime", version=1, activation_revision=1,
        schema_id="app", schema_version=1, digest="digest", metadata_json="{}",
        entries=(), parameters={"runtime": '{"debug":true,"port":9000}'},
        secrets={"db_password": Secret(b"canary", version=1)},
    ))
    prepared.commit()
    current: Snapshot = store.current
    view: ServerConfigView = current.server()
    assert view.port == 9000
    assert view.debug is True
    assert str(view.password) == "[REDACTED]"
    assert all(isinstance(entry, ContractEntry) for entry in CONTRACT)
    assert store.defaults_artifact("dev").endswith("\n")


def test_generated_start_and_verify_surfaces(monkeypatch) -> None:
    import tests.fixtures.configgen.config_generated as generated

    store = generated.GeneratedConfigStore({})
    sync_manager, async_manager = object(), object()
    monkeypatch.setattr(generated, "_start_managed_config", lambda *args, **kwargs: sync_manager)
    async def start_async(*args, **kwargs):
        return async_manager
    monkeypatch.setattr(generated, "_start_async_managed_config", start_async)
    assert store.start(object(), release="runtime", callbacks=Callbacks(lambda _: None)) is sync_manager
    assert asyncio.run(store.start_async(
        object(), release="runtime", callbacks=Callbacks(lambda _: None)
    )) is async_manager

    response = type("Response", (), {
        "release_name": "runtime", "release_version": 1, "activation_revision": 2,
        "schema_matches": True,
        "entries": (type("Verdict", (), {"alias": "runtime", "verdict": "match"})(),),
        "unverified_count": 0,
    })()
    class Client:
        def verify_release_defaults(self, **kwargs):
            return response
    class AsyncClient:
        async def verify_release_defaults(self, **kwargs):
            return response
    assert store.verify_defaults(Client(), namespace="dev/app").passed
    assert asyncio.run(store.verify_defaults_async(AsyncClient(), namespace="dev/app")).passed


class PortableNested(BaseModel):
    model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
    label: str


class PortableConfig(BaseModel):
    model_config = ConfigDict(frozen=True, strict=True, extra="forbid", arbitrary_types_allowed=True)
    count: Annotated[int, Field(ge=-10, le=10), Parameter("portable")] = 0
    payload: Annotated[bytes, Parameter("portable")] = b""
    timeout: Annotated[dt.timedelta, Parameter("portable")] = dt.timedelta()
    nested: Annotated[PortableNested | None, Parameter("portable")] = None
    password: Annotated[Secret, SecretField("password")]


def test_portable_schema_exactly_describes_runtime_wire_codecs() -> None:
    artifacts = generate_artifacts(PortableConfig, source_module=__name__)
    schema = json.loads(artifacts.schema)
    properties = schema["properties"]["portable"]["properties"]
    assert properties["count"] == {"type": "integer", "minimum": -10, "maximum": 10}
    assert properties["payload"] == {"type": "string", "format": "kms-base64"}
    assert properties["timeout"] == {"type": "string", "format": "go-duration"}
    assert "$ref" not in artifacts.schema

    from kms_paramstore.configstore import ConfigBinding
    binding = ConfigBinding(PortableConfig, {})
    prepared = binding.prepare(ReleaseSnapshot(
        namespace="dev/app", name="runtime", version=1, activation_revision=1,
        schema_id="app", schema_version=1, digest="digest", metadata_json="{}", entries=(),
        parameters={"portable": '{"count":5,"nested":{"label":"ok"},"payload":"AAE=","timeout":"1s"}'},
        secrets={"password": Secret(b"secret", version=1)},
    ))
    prepared.commit()
    encoded = json.loads(binding.encode_parameter_groups()["portable"])
    assert encoded == {"count": 5, "nested": {"label": "ok"}, "payload": "AAE=", "timeout": "1s"}
