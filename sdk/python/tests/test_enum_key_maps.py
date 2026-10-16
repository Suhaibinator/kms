from __future__ import annotations

import json
from enum import Enum, Flag, IntEnum, IntFlag
from pathlib import Path
from typing import Annotated, Mapping, get_type_hints

import pytest
from pydantic import BaseModel, ConfigDict, Field, create_model

from kms_paramstore.configgen import generate_artifacts
from kms_paramstore.configstore import CandidateError, ConfigBinding, ConfigSpec, Parameter
from kms_paramstore.configstore.codecs import decode_value, encode_value
from kms_paramstore.configstore.enum_keys import enum_keys
from kms_paramstore.release import ReleaseSnapshot
from tests.fixtures.configgen.enums import Role
from tests.fixtures.configgen.source import Transport


def snapshot(document: str) -> ReleaseSnapshot:
    return ReleaseSnapshot(
        namespace="dev/app", name="runtime", version=1, activation_revision=1,
        schema_version=1, digest="digest", metadata_json="{}", entries=(),
        parameters={"runtime": document}, secrets={},
    )


class EnumConfig(BaseModel):
    model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
    entries: Annotated[dict[Role, bytes], Parameter("runtime")] = {Role.UNSPECIFIED: b""}
    names: Annotated[Mapping[Transport, str] | None, Parameter("runtime")] = None


def test_managed_round_trip_defaults_and_defensive_copies() -> None:
    binding = ConfigBinding(EnumConfig, {})
    assert json.loads(binding.encode_defaults_groups()["runtime"]) == {
        "entries": {"UNSPECIFIED": ""}, "names": None,
    }
    original = EnumConfig(entries={Role.ADMIN_ALIAS: b"a"}, names={Transport.HTTP_ALIAS: "a"})
    assert json.loads(ConfigBinding(EnumConfig, original).encode_defaults_groups()["runtime"]) == {
        "entries": {"ADMIN": "YQ=="}, "names": {"http": "a"},
    }
    document = '{"entries":{"LOW":"YQ==","ADMIN":"Yg=="},"names":{"http":"a","grpc":"b"}}'
    binding.prepare(snapshot(document)).commit()
    actual = binding.current.config()
    assert actual.entries == {Role.LOW: b"a", Role.ADMIN: b"b"}
    assert actual.names == {Transport.HTTP: "a", Transport.GRPC: "b"}
    assert all(type(key) is Role for key in actual.entries)
    assert json.loads(binding.encode_parameter_groups()["runtime"]) == json.loads(document)
    actual.entries[Role.UNSPECIFIED] = b"changed"
    assert Role.UNSPECIFIED not in binding.current.config().entries


def test_recursive_maps_and_mapping_annotations() -> None:
    annotation = list[Mapping[Role, dict[Transport, int]] | None]
    wire = [{"ADMIN": {"http": 2}}, None, {}]
    value = decode_value(annotation, wire)
    assert value == [{Role.ADMIN: {Transport.HTTP: 2}}, None, {}]
    assert encode_value(annotation, value) == wire


@pytest.mark.parametrize("bad", ["ADMIN_ALIAS", "admin", "0", "PRIVATE_UNKNOWN_KEY"])
def test_unknown_and_noncanonical_wire_keys_are_redacted(bad: str) -> None:
    binding = ConfigBinding(EnumConfig, {})
    with pytest.raises(CandidateError) as caught:
        binding.prepare(snapshot(json.dumps({"entries": {bad: ""}, "names": None})))
    assert bad not in str(caught.value)


@pytest.mark.parametrize("bad", [0, "UNSPECIFIED", Transport.HTTP])
def test_encode_rejects_bare_and_foreign_keys_including_defaults(bad: object) -> None:
    with pytest.raises(ValueError, match="declared enum member"):
        encode_value(dict[Role, str], {bad: "a"})
    binding = ConfigBinding(EnumConfig, {"entries": {bad: b""}})
    with pytest.raises(ValueError, match="declared enum member"):
        binding.encode_defaults_groups()


def test_string_enum_encode_requires_members_and_uses_values() -> None:
    assert encode_value(dict[Transport, int], {Transport.HTTP_ALIAS: 1}) == {"http": 1}
    with pytest.raises(ValueError, match="declared enum member"):
        encode_value(dict[Transport, int], {"http": 1})
    with pytest.raises(ValueError, match="unknown enum map key"):
        decode_value(dict[Transport, int], {"HTTP": 1})


def test_local_store_requires_enum_members_and_preserves_types() -> None:
    local = ConfigBinding._from_local(EnumConfig, {"entries": {Role.UNSPECIFIED: b""}})
    assert all(type(key) is Role for key in local.current.config().entries)
    for bad in (0, "UNSPECIFIED"):
        with pytest.raises(CandidateError):
            ConfigBinding._from_local(EnumConfig, {"entries": {bad: b""}})


def test_generated_schema_contract_and_types_describe_enum_maps() -> None:
    from tests.fixtures.configgen import config_generated as generated
    from tests.fixtures.configgen.source import ApplicationConfig

    artifacts = generate_artifacts(ApplicationConfig)
    properties = json.loads(artifacts.schema)["properties"]["runtime"]["properties"]
    assert properties["weights"]["propertyNames"] == {
        "type": "string", "enum": ["LOW", "UNSPECIFIED", "ADMIN"],
        "x-kms-enum-numbers": {"LOW": -1, "UNSPECIFIED": 0, "ADMIN": 3},
    }
    assert properties["weights"]["additionalProperties"]["type"] == "integer"
    assert properties["transports"]["anyOf"][0]["propertyNames"] == {
        "type": "string", "enum": ["http", "grpc"],
    }
    fields = {field["json_name"]: field for field in json.loads(artifacts.contract)["fields"]}
    assert fields["weights"]["encoding"] == "enum-key-map"
    assert fields["transports"]["encoding"] == "nullable<enum-key-map>"
    assert get_type_hints(generated.Snapshot.weights.fget)["return"] == dict[Role, int]
    assert get_type_hints(generated.ServerConfigView.weights.fget)["return"] == dict[Role, int]
    assert get_type_hints(generated.Snapshot.transports.fget)["return"] == Mapping[Transport, str] | None


def test_map_size_constraints_and_enum_renumbering_change_schema() -> None:
    class Constrained(BaseModel):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
        entries: Annotated[dict[Role, int], Parameter("runtime"), Field(min_length=1, max_length=2)] = {Role.ADMIN: 1}

    props = json.loads(generate_artifacts(Constrained).schema)["properties"]["runtime"]["properties"]["entries"]
    assert props["minProperties"] == 1 and props["maxProperties"] == 2
    with pytest.raises(CandidateError):
        ConfigBinding(Constrained, {}).prepare(snapshot('{"entries":{}}'))

    def schema_for(number: int) -> str:
        key = IntEnum("VersionedKey", {"READY": number})
        model = create_model(
            "VersionedConfig", __config__=ConfigDict(frozen=True, strict=True, extra="forbid"),
            entries=(Annotated[dict[key, int], Parameter("runtime")], {}),
        )
        # Only the schema is needed; dynamic enum types have an importable
        # module/name solely for rendering the unused generated binding.
        return generate_artifacts(model).schema_sha256

    assert schema_for(1) != schema_for(2)


@pytest.mark.parametrize("key_type", [
    Enum("Empty", {}), Enum("Mixed", {"A": 1, "B": "b"}),
    Enum("Booleans", {"A": True}), Enum("Floats", {"A": 1.5}),
    IntEnum("TooLarge", {"A": 1 << 63}), Flag("Flags", {"A": 1}),
    IntFlag("IntFlags", {"A": 1}),
])
def test_invalid_enum_definitions_are_rejected(key_type: type[Enum]) -> None:
    with pytest.raises(TypeError):
        enum_keys(key_type)


def test_primitive_integer_map_keys_remain_unsupported() -> None:
    class BadConfig(BaseModel):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
        entries: Annotated[dict[int, str], Parameter("runtime")] = {}

    with pytest.raises(TypeError, match="string or enum keys"):
        generate_artifacts(BadConfig)
    with pytest.raises(TypeError, match="string or enum keys"):
        encode_value(dict[int, str], {0: "a"})


def test_enum_value_extraction_is_rejected_for_root_and_nested_map_owners() -> None:
    class BadRoot(EnumConfig):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid", use_enum_values=True)

    with pytest.raises(TypeError, match="use_enum_values=False"):
        ConfigSpec.from_model(BadRoot)

    class BadNested(BaseModel):
        model_config = ConfigDict(use_enum_values=True)
        entries: dict[Role, int] = {}

    class Root(BaseModel):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
        nested: Annotated[BadNested, Parameter("runtime")] = BadNested()

    # Resolve the local nested class explicitly under postponed annotations.
    Root.model_rebuild(_types_namespace={"BadNested": BadNested})
    with pytest.raises(TypeError, match="use_enum_values=False"):
        ConfigSpec.from_model(Root)


_CORPUS = json.loads((Path(__file__).parents[2] / "testdata/enum-key-maps.json").read_text())["cases"]


@pytest.mark.parametrize("case", _CORPUS, ids=lambda case: case["name"])
def test_cross_sdk_enum_map_corpus(case: dict) -> None:
    if case["key_kind"] == "integer":
        key_type = IntEnum("WireKey", {member["name"]: member["number"] for member in case["members"]})
    else:
        key_type = Enum("WireKey", {f"KEY_{index}": member["name"] for index, member in enumerate(case["members"])})
    model = create_model(
        "CorpusConfig", __config__=ConfigDict(frozen=True, strict=True, extra="forbid"),
        entries=(Annotated[dict[key_type, str] | None, Parameter("runtime")], None),
    )
    binding = ConfigBinding(model, {})
    if not case["valid"]:
        with pytest.raises(CandidateError):
            binding.prepare(snapshot(case["document"]))
        return
    binding.prepare(snapshot(case["document"])).commit()
    assert json.loads(binding.encode_parameter_groups()["runtime"]) == json.loads(case["document"])
