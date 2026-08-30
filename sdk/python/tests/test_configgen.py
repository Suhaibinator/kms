from __future__ import annotations

from pathlib import Path
from typing import Annotated

import pytest
from pydantic import BaseModel, ConfigDict, Field

from kms_paramstore.configgen import StaleArtifactsError, generate_artifacts, write_artifacts
from kms_paramstore.configstore import Parameter, SecretField
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
    from tests.fixtures.configgen.config_generated import GeneratedConfigStore, ServerConfigView

    store = GeneratedConfigStore({})
    prepared = store.prepare(ReleaseSnapshot(
        namespace="prod/app", name="runtime", version=1, activation_revision=1,
        schema_id="app", schema_version=1, digest="digest", metadata_json="{}",
        entries=(), parameters={"runtime": '{"debug":true,"port":9000}'},
        secrets={"db_password": Secret(b"canary", version=1)},
    ))
    prepared.commit()
    view: ServerConfigView = store.server()
    assert view.port == 9000
    assert view.debug is True
    assert str(view.password) == "[REDACTED]"
