from __future__ import annotations

from pathlib import Path
from typing import Annotated
import asyncio
import json

import pytest
from pydantic import BaseModel, ConfigDict, Field

from kms_paramstore.configgen import StaleArtifactsError, generate_artifacts, write_artifacts
from kms_paramstore.configstore import Duration, Parameter, SecretField
from kms_paramstore.configstore import Callbacks, ContractEntry
from kms_paramstore.release import ReleaseSnapshot
from kms_paramstore.secret import Secret


class GeneratedConfig(BaseModel):
    model_config = ConfigDict(frozen=True, strict=True, extra="forbid", arbitrary_types_allowed=True)

    port: Annotated[int, Field(ge=1, le=65535), Parameter("runtime", views=("server",))] = 8080
    password: Annotated[Secret, SecretField("password", reload="restart", views=("server",))] = Secret(
        bind_key="generator-binding-key-canary"
    )


def test_generation_is_deterministic_and_contract_matches_other_sdks(tmp_path: Path) -> None:
    first = generate_artifacts(GeneratedConfig, source_module="app.config")
    second = generate_artifacts(GeneratedConfig, source_module="app.config")
    assert first == second
    assert '"format": "kms-config-contract/v1"' in first.contract
    assert '"language": "python"' in first.contract
    assert first.schema_sha256 in first.contract
    assert "GeneratedConfigStore" in first.binding
    assert '"minimum": 1' in first.schema and '"maximum": 65535' in first.schema
    assert "generator-binding-key-canary" not in first.binding + first.schema + first.contract

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
        schema_version=1, digest="digest", metadata_json="{}",
        entries=(), parameters={"runtime": '{"debug":true,"port":9000}'},
        secrets={"db_password": Secret(b"canary", version=1)},
    ))
    prepared.commit()
    current: Snapshot = store.current
    view: ServerConfigView = current.server()
    assert view.port == 9000
    assert view.debug is True
    assert str(view.password) == "[REDACTED]"
    assert view.password.bind_key == ""
    assert "fixture-binding-key-never-generated" not in repr(store)
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
    timeout: Annotated[Duration, Parameter("portable")] = Duration(0)
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
        schema_version=1, digest="digest", metadata_json="{}", entries=(),
        parameters={"portable": '{"count":5,"nested":{"label":"ok"},"payload":"AAE=","timeout":"1s"}'},
        secrets={"password": Secret(b"secret", version=1)},
    ))
    prepared.commit()
    encoded = json.loads(binding.encode_parameter_groups()["portable"])
    assert encoded == {"count": 5, "nested": {"label": "ok"}, "payload": "AAE=", "timeout": "1s"}


class ConstrainedNested(BaseModel):
    model_config = ConfigDict(
        frozen=True, strict=True, extra="forbid", populate_by_name=False,
    )
    required_name: Annotated[str, Field(alias="required-name", min_length=2, max_length=8, pattern="^[a-z]+$")]
    retry_ratio: Annotated[float, Field(alias="retry-ratio", gt=0, le=1, multiple_of=0.1)] = 0.5
    labels: Annotated[list[str], Field(min_length=1, max_length=3)] = ["default"]
    weights: Annotated[dict[str, int], Field(min_length=1, max_length=2)] = {"one": 1}
    nickname: Annotated[str | None, Field(min_length=2, max_length=4)] = None


class AliasedConfig(BaseModel):
    model_config = ConfigDict(
        frozen=True, strict=True, extra="forbid", arbitrary_types_allowed=True,
        populate_by_name=False,
    )
    port: Annotated[int, Parameter("runtime")] = Field(8080, alias="wire-port")
    nested: Annotated[ConstrainedNested, Parameter("runtime")] = Field(
        default=ConstrainedNested.model_validate({"required-name": "okay"}),
        alias="wire-nested",
    )
    precise: Annotated[Duration, Parameter("runtime")] = Duration(1)
    password: Annotated[Secret, SecretField("password")]


def test_schema_constraints_aliases_and_nested_defaults_match_runtime() -> None:
    artifacts = generate_artifacts(AliasedConfig, source_module=__name__)
    properties = json.loads(artifacts.schema)["properties"]["runtime"]["properties"]
    nested = properties["wire-nested"]
    assert nested["required"] == ["required-name"]
    assert nested["properties"]["required-name"] == {
        "type": "string", "minLength": 2, "maxLength": 8, "pattern": "^[a-z]+$",
    }
    assert nested["properties"]["retry-ratio"] == {
        "type": "number", "minimum": -float.fromhex("0x1.fffffffffffffp+1023"),
        "maximum": 1, "exclusiveMinimum": 0, "multipleOf": 0.1,
    }
    assert nested["properties"]["labels"]["minItems"] == 1
    assert nested["properties"]["labels"]["maxItems"] == 3
    assert nested["properties"]["weights"]["minProperties"] == 1
    assert nested["properties"]["weights"]["maxProperties"] == 2
    assert nested["properties"]["nickname"] == {
        "anyOf": [
            {"type": "string", "minLength": 2, "maxLength": 4},
            {"type": "null"},
        ]
    }
    assert properties["precise"] == {"type": "string", "format": "go-duration"}

    from kms_paramstore.configstore import ConfigBinding
    binding = ConfigBinding(AliasedConfig, {})
    prepared = binding.prepare(ReleaseSnapshot(
        namespace="dev/app", name="runtime", version=1, activation_revision=1,
        schema_version=1, digest="digest", metadata_json="{}", entries=(),
        parameters={"runtime": json.dumps({
            "wire-port": 9000,
            "wire-nested": {"required-name": "valid", "retry-ratio": 0.7,
                            "labels": ["x"], "weights": {"x": 1}},
            "precise": "1ns",
        })},
        secrets={"password": Secret(b"secret", version=1)},
    ))
    prepared.commit()
    assert binding.current.config().port == 9000
    assert binding.current.config().nested.required_name == "valid"
    encoded = json.loads(binding.encode_parameter_groups()["runtime"])
    assert set(encoded) == {"wire-port", "wire-nested", "precise"}
    assert encoded["wire-nested"]["required-name"] == "valid"
    assert encoded["precise"] == "1ns"


def test_source_model_named_snapshot_does_not_collide_with_generated_snapshot() -> None:
    class Snapshot(BaseModel):
        model_config = ConfigDict(
            frozen=True, strict=True, extra="forbid", arbitrary_types_allowed=True,
        )
        value: Annotated[int, Parameter("runtime")] = 1
        password: Annotated[Secret, SecretField("password")]

    binding = generate_artifacts(Snapshot, source_module=__name__).binding
    assert "_RootConfig = _source.Snapshot" in binding
    assert "Snapshot = _source.Snapshot" not in binding
    compile(binding, "<generated>", "exec")


def test_bytes_length_constraints_are_rejected_instead_of_misstated() -> None:
    class BytesConfig(BaseModel):
        model_config = ConfigDict(
            frozen=True, strict=True, extra="forbid", arbitrary_types_allowed=True,
        )
        payload: Annotated[bytes | None, Field(min_length=2), Parameter("runtime")] = None
        password: Annotated[Secret, SecretField("password")]

    with pytest.raises(TypeError, match="decoded byte length constraints"):
        generate_artifacts(BytesConfig, source_module=__name__)
