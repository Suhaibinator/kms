from __future__ import annotations

import json
from io import StringIO
from typing import Annotated

import pytest
from pydantic import BaseModel, ConfigDict

from kms_paramstore.configstore import (
    ContractEntry,
    DefaultsArtifactError,
    encode_defaults_artifact,
    export_defaults,
    ConfigBinding,
    Parameter,
    SecretField,
    parse_defaults_artifact,
)
from kms_paramstore.secret import Secret


CONTRACT = (
    ContractEntry("db_password", "secret"),
    ContractEntry("runtime", "parameter", "json"),
)


def test_defaults_artifact_is_deterministic_and_secret_free() -> None:
    artifact = encode_defaults_artifact(
        profile="prod", schema_sha256="a" * 64, contract=CONTRACT,
        parameters={"runtime": '{"port":8080}'},
    )
    assert artifact.endswith("\n")
    assert "db_password" in artifact
    assert "secret-value" not in artifact
    parsed = parse_defaults_artifact(artifact)
    assert parsed.profile == "prod"
    assert parsed.parameters[0].value == '{"port":8080}'


@pytest.mark.parametrize(
    "mutation",
    [
        lambda value: value.replace('"format":', '"format":"duplicate","format":'),
        lambda value: value.replace('"profile":"prod"', '"profile":" prod"'),
        lambda value: value + "{}",
        lambda value: value.replace('"alias":"runtime"', '"alias":"unknown"', 1),
    ],
)
def test_defaults_artifact_strictly_rejects_hostile_documents(mutation) -> None:
    artifact = encode_defaults_artifact(
        profile="prod", schema_sha256="a" * 64, contract=CONTRACT,
        parameters={"runtime": "{}"},
    )
    with pytest.raises(DefaultsArtifactError):
        parse_defaults_artifact(mutation(artifact))


def test_exporter_uses_source_defaults_without_materializing_secrets() -> None:
    class Config(BaseModel):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid", arbitrary_types_allowed=True)
        port: Annotated[int, Parameter("runtime")] = 8080
        password: Annotated[Secret, SecretField("password")]

    output = StringIO()
    export_defaults(
        profile="dev", schema_sha256="b" * 64,
        binding=ConfigBinding(Config, {}), output=output,
    )
    artifact = output.getvalue()
    assert "8080" in artifact
    assert "password" in artifact
    assert "REDACTED" not in artifact
