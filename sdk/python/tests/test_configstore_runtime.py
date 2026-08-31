from __future__ import annotations

import json
import datetime as dt
import asyncio
from typing import Annotated

import pytest
from pydantic import AliasChoices, AliasPath, BaseModel, ConfigDict, Field
from pydantic import create_model

from kms_paramstore.configstore import (
    Callbacks,
    AsyncManagedConfigManager,
    CandidateError,
    ConfigBinding,
    ConfigSpec,
    ManagedConfigManager,
    Parameter,
    SecretField,
    Unmanaged,
    ContractEntry,
    validate_manifest,
    ConfigSnapshot,
    Duration,
    start_async_managed_config,
)
from kms_paramstore.release import ReleaseSnapshot
from kms_paramstore.secret import Secret


class RuntimeConfig(BaseModel):
    model_config = ConfigDict(frozen=True, strict=True, extra="forbid", arbitrary_types_allowed=True)

    port: Annotated[int, Parameter("runtime", reload="restart", views=("server",))] = 8080
    labels: Annotated[list[str], Parameter("runtime", json_name="label_set")] = Field(default_factory=list)
    enabled: Annotated[bool, Parameter("features")] = True
    password: Annotated[Secret, SecretField("db_password", views=("server",))]
    local_name: Annotated[str, Unmanaged()] = "app"


def snapshot(*, port: object = 8080, enabled: object = True, secret_version: int = 1) -> ReleaseSnapshot:
    return ReleaseSnapshot(
        namespace="prod/app", name="runtime", version=secret_version,
        activation_revision=secret_version, schema_id="app", schema_version=1,
        digest=f"digest-{secret_version}", metadata_json="{}", entries=(),
        parameters={
            "runtime": json.dumps({"port": port, "label_set": ["a"]}),
            "features": json.dumps({"enabled": enabled}),
        },
        secrets={"db_password": Secret(b"canary", env="prod", app="app", key="db", version=secret_version)},
    )


def test_spec_and_contract_follow_annotated_model() -> None:
    spec = ConfigSpec.from_model(RuntimeConfig)
    assert [(item.alias, item.kind, item.content_type) for item in spec.contract] == [
        ("db_password", "secret", ""), ("features", "parameter", "json"),
        ("runtime", "parameter", "json"),
    ]


def test_prepare_is_atomic_strict_and_defensive() -> None:
    binding = ConfigBinding(RuntimeConfig, {})
    prepared = binding.prepare(snapshot(port=9000))
    assert prepared.restart_required_fields == ()
    assert [item.path for item in prepared.default_differences] == ["runtime.label_set", "runtime.port"]
    prepared.commit()
    first = binding.current
    with pytest.raises(AttributeError, match="immutable"):
        first.release = first.release  # type: ignore[misc]
    with pytest.raises(AttributeError, match="immutable"):
        first._config = first.config()  # type: ignore[misc]
    assert first.get("port") == 9000
    labels = first.get("labels")
    labels.append("mutated")
    assert first.get("labels") == ["a"]

    reloaded = binding.prepare(snapshot(port=9001, secret_version=2))
    assert reloaded.restart_required_fields == ("runtime.port",)
    assert [(item.path, item.previous, item.current) for item in reloaded.changed] == [
        ("runtime.port", 9000, 9001), ("db_password", None, None),
    ]
    assert binding.current.get("port") == 9000


def test_snapshot_and_validation_own_independent_secret_clones() -> None:
    source = snapshot()
    fetched = source.secrets["db_password"]
    binding = ConfigBinding(RuntimeConfig, {})
    prepared = binding.prepare(source)
    fetched._value = b"mutated-before-commit"
    prepared.commit()
    first = binding.current.get("password")
    assert first.value == b"canary"
    first._value = b"mutated-after-read"
    assert binding.current.get("password").value == b"canary"


@pytest.mark.parametrize(
    "document",
    [
        '{"port":8080,"port":9000,"label_set":[]}',
        '{"port":8080,"label_set":[],"unknown":1}',
        '{"port":8080}',
    ],
)
def test_group_decode_rejects_duplicates_unknown_and_missing(document: str) -> None:
    candidate = snapshot()
    bad = ReleaseSnapshot(**{**candidate.__dict__, "parameters": {**candidate.parameters, "runtime": document}})
    with pytest.raises(CandidateError, match="config_decode_failed"):
        ConfigBinding(RuntimeConfig, {}).prepare(bad)


def test_pydantic_strictness_rejects_coercion_and_nonfinite() -> None:
    with pytest.raises(CandidateError, match="config_decode_failed"):
        ConfigBinding(RuntimeConfig, {}).prepare(snapshot(port="8080"))
    with pytest.raises(CandidateError, match="config_decode_failed"):
        ConfigBinding(RuntimeConfig, {}).prepare(snapshot(enabled=1))


def test_model_contract_rejects_mutable_or_permissive_roots() -> None:
    class Bad(BaseModel):
        value: Annotated[int, Parameter("runtime")] = 1

    with pytest.raises(TypeError, match="frozen=True"):
        ConfigSpec.from_model(Bad)


def test_managed_timedelta_is_rejected_in_favor_of_nanosecond_duration() -> None:
    class NestedDuration(BaseModel):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
        delay: dt.timedelta = dt.timedelta()

    class BadDuration(BaseModel):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
        nested: Annotated[NestedDuration, Parameter("runtime")] = NestedDuration()

    with pytest.raises(TypeError, match="use configstore.Duration"):
        ConfigSpec.from_model(BadDuration)


def test_non_string_validation_aliases_are_rejected_at_root_and_nested() -> None:
    class BadRootAlias(BaseModel):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
        value: Annotated[int, Parameter("runtime")] = Field(
            1, validation_alias=AliasChoices("value", "legacy_value")
        )

    with pytest.raises(TypeError, match="non-string validation alias"):
        ConfigSpec.from_model(BadRootAlias)

    class NestedAlias(BaseModel):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
        value: int = Field(validation_alias=AliasPath("payload", "value"))

    class BadNestedAlias(BaseModel):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
        nested: Annotated[NestedAlias, Parameter("runtime")] = NestedAlias.model_construct(value=1)

    with pytest.raises(TypeError, match="nested.value.*non-string validation alias"):
        ConfigSpec.from_model(BadNestedAlias)


@pytest.mark.parametrize("view", ["class", "start", "verify-defaults"])
def test_view_names_reject_keywords_and_generated_api_collisions(view: str) -> None:
    class BadView(BaseModel):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
        value: Annotated[int, Parameter("runtime", views=(view,))] = 1

    with pytest.raises(TypeError, match="view"):
        ConfigSpec.from_model(BadView)


def test_view_normalization_collisions_are_rejected() -> None:
    class BadViews(BaseModel):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
        one: Annotated[int, Parameter("runtime", views=("foo-bar",))] = 1
        two: Annotated[int, Parameter("runtime", views=("foo_bar",))] = 2

    with pytest.raises(TypeError, match="normalize"):
        ConfigSpec.from_model(BadViews)

    class BadViewClasses(BaseModel):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
        one: Annotated[int, Parameter("runtime", views=("foo",))] = 1
        two: Annotated[int, Parameter("runtime", views=("Foo",))] = 2

    with pytest.raises(TypeError, match="same Python class"):
        ConfigSpec.from_model(BadViewClasses)

    class BadFieldView(BaseModel):
        model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
        server_view: Annotated[int, Parameter("runtime")] = 1
        value: Annotated[int, Parameter("runtime", views=("server-view",))] = 2

    with pytest.raises(TypeError, match="snapshot field"):
        ConfigSpec.from_model(BadFieldView)


def test_release_contract_is_bounded_to_256_entries() -> None:
    fields = {
        f"field_{index}": (Annotated[int, Parameter(f"group_{index}")], index)
        for index in range(257)
    }
    Huge = create_model(
        "Huge", __config__=ConfigDict(frozen=True, strict=True, extra="forbid"), **fields
    )
    with pytest.raises(TypeError, match="maximum is 256"):
        ConfigSpec.from_model(Huge)


def test_manifest_mismatch_is_classified_before_resolution() -> None:
    with pytest.raises(CandidateError, match="config_contract_mismatch") as caught:
        validate_manifest((ContractEntry("runtime", "parameter", "json"),), {})
    assert caught.value.release_rejection_category == "config_contract_mismatch"


def test_default_mismatch_callback_is_required_and_callable() -> None:
    with pytest.raises(TypeError, match="required"):
        Callbacks(None)  # type: ignore[arg-type]


def test_defaults_model_input_omits_required_secret_fields() -> None:
    defaults = RuntimeConfig(password=Secret(b"source-default-must-not-serialize"))
    binding = ConfigBinding(RuntimeConfig, defaults)
    encoded = "".join(binding.encode_defaults_groups().values())
    assert "source-default-must-not-serialize" not in encoded
    assert json.loads(binding.encode_defaults_groups()["runtime"])["port"] == 8080


def test_secret_values_never_appear_in_errors() -> None:
    binding = ConfigBinding(RuntimeConfig, {})
    candidate = snapshot()
    bad = ReleaseSnapshot(**{**candidate.__dict__, "parameters": {"runtime": "CANARY", "features": '{"enabled":true}'}})
    with pytest.raises(CandidateError) as caught:
        binding.prepare(bad)
    assert "CANARY" not in str(caught.value)
    assert "canary" not in repr(caught.value)


class _Loader:
    def __init__(self, snapshots) -> None:
        self.snapshots = snapshots
        self.rejections = {}

    def run(self, prepare) -> None:
        for item in self.snapshots:
            try:
                candidate = prepare(object(), item)
                candidate.commit()
            except CandidateError as error:
                self.rejections[error.category] = self.rejections.get(error.category, 0) + 1

    def stop(self) -> None:
        pass

    def status(self):
        return type("Status", (), {"state": "stopped", "last_failure_category": "restart_required", "last_failure_unix_ms": 0})()

    def stats(self):
        return type("Stats", (), {"reconnects": 0, "acknowledgements": {"applied": 1}, "rejections": self.rejections})()


def test_manager_applies_divergence_but_rejects_whole_restart_candidate() -> None:
    binding = ConfigBinding(RuntimeConfig, {})
    mismatches, applied, rejected = [], [], []
    manager = ManagedConfigManager(
        _Loader([snapshot(port=9000), snapshot(port=9001, secret_version=2)]),
        binding,
        Callbacks(mismatches.append, applied.append, rejected.append),
    )
    manager.start()
    manager.wait_until_ready(1)
    manager.wait(1)
    assert binding.current.get("port") == 9000
    assert len(mismatches) == 1
    assert len(applied) == 1
    assert [report.category for report in rejected] == ["restart_required"]
    assert manager.status().default_divergent is True


class Nested(BaseModel):
    model_config = ConfigDict(frozen=True, strict=True, extra="forbid")
    name: str


class CodecConfig(BaseModel):
    model_config = ConfigDict(frozen=True, strict=True, extra="forbid", arbitrary_types_allowed=True)

    count: Annotated[int, Field(ge=-5, le=5), Parameter("typed")] = 0
    ratio: Annotated[float, Parameter("typed")] = 1.0
    payload: Annotated[bytes, Parameter("typed")] = b""
    delay: Annotated[Duration, Parameter("typed")] = Duration(0)
    optional: Annotated[int | None, Parameter("typed")] = None
    items: Annotated[list[int], Parameter("typed")] = Field(default_factory=list)
    mapping: Annotated[dict[str, bool], Parameter("typed")] = Field(default_factory=dict)
    nested: Annotated[Nested, Parameter("typed")] = Nested(name="default")
    secret: Annotated[Secret, SecretField("secret")]


def _codec_snapshot(document: str) -> ReleaseSnapshot:
    candidate = snapshot()
    return ReleaseSnapshot(**{
        **candidate.__dict__,
        "parameters": {"typed": document},
        "secrets": {"secret": Secret(b"canary", version=1)},
    })


def test_recursive_codecs_support_portable_managed_types() -> None:
    document = json.dumps({
        "count": 5, "ratio": 1.25, "payload": "AAE=", "delay": "1h2m3s4ms5us",
        "optional": None, "items": [1, 2], "mapping": {"ready": True},
        "nested": {"name": "candidate"},
    })
    binding = ConfigBinding(CodecConfig, {})
    prepared = binding.prepare(_codec_snapshot(document))
    prepared.commit()
    config = binding.current.config()
    assert config.payload == b"\x00\x01"
    assert config.delay == Duration(3_723_004_005_000)
    assert json.loads(binding.encode_parameter_groups(config)["typed"])["payload"] == "AAE="


@pytest.mark.parametrize(
    ("field", "value"),
    [("count", 6), ("ratio", float("nan")), ("payload", "***"), ("delay", "PT1H")],
)
def test_recursive_codecs_reject_ranges_nonfinite_and_noncanonical_encodings(field: str, value: object) -> None:
    values = {
        "count": 0, "ratio": 1.0, "payload": "", "delay": "0", "optional": None,
        "items": [], "mapping": {}, "nested": {"name": "ok"},
    }
    values[field] = value
    document = json.dumps(values, allow_nan=True)
    with pytest.raises(CandidateError):
        ConfigBinding(CodecConfig, {}).prepare(_codec_snapshot(document))


@pytest.mark.parametrize("payload", ["AB==", "AAE", "AAE===", "A A E="])
def test_base64_requires_canonical_round_trip(payload: str) -> None:
    values = {
        "count": 0, "ratio": 1.0, "payload": payload, "delay": "0",
        "optional": None, "items": [], "mapping": {}, "nested": {"name": "ok"},
    }
    with pytest.raises(CandidateError):
        ConfigBinding(CodecConfig, {}).prepare(_codec_snapshot(json.dumps(values)))


def test_go_duration_allows_only_one_leading_sign() -> None:
    base = {
        "count": 0, "ratio": 1.0, "payload": "", "optional": None,
        "items": [], "mapping": {}, "nested": {"name": "ok"},
    }
    binding = ConfigBinding(CodecConfig, {})
    prepared = binding.prepare(_codec_snapshot(json.dumps({**base, "delay": "-1h2m"})))
    prepared.commit()
    assert binding.current.get("delay") == Duration(-3_720_000_000_000)
    assert json.loads(binding.encode_parameter_groups()["typed"])["delay"] == "-1h2m0s"
    for invalid in ("1h-2m", "1h+2m", "--1h", "+-1h"):
        with pytest.raises(CandidateError):
            ConfigBinding(CodecConfig, {}).prepare(_codec_snapshot(json.dumps({**base, "delay": invalid})))


def test_go_duration_retains_nanoseconds_and_uses_canonical_spellings() -> None:
    from kms_paramstore.configstore.codecs import decode_value, encode_value
    from kms_paramstore.configstore import parameter_hash

    assert decode_value(Duration, "1ns") == Duration(1)
    assert encode_value(Duration, decode_value(Duration, "1μs")) == "1µs"
    assert encode_value(Duration, decode_value(Duration, "1500ns")) == "1.5µs"
    assert encode_value(Duration, decode_value(Duration, "1h2m")) == "1h2m0s"
    assert encode_value(Duration, Duration(0)) == "0s"
    hashes = {
        parameter_hash("json", json.dumps({"delay": encode_value(Duration, decode_value(Duration, value))}))
        for value in ("1us", "1µs", "1μs")
    }
    assert len(hashes) == 1
    for invalid in ("9223372036854775808ns", "-9223372036854775809ns"):
        with pytest.raises(ValueError, match="int64"):
            decode_value(Duration, invalid)


class _AsyncLoader:
    def __init__(self, candidate=None, error=None) -> None:
        self.candidate, self.error = candidate, error

    async def run(self, prepare) -> None:
        if self.error:
            raise self.error
        prepared = await prepare(asyncio.Event(), self.candidate)
        prepared.commit()

    def stop(self) -> None:
        pass


def test_async_manager_uses_event_loop_lifecycle_and_records_failure() -> None:
    async def scenario() -> None:
        binding = ConfigBinding(RuntimeConfig, {})
        manager = AsyncManagedConfigManager(
            _AsyncLoader(snapshot()), binding, Callbacks(lambda _report: None)
        )
        await manager.start_async()
        await manager.wait_until_ready_async()
        await manager.wait_async()
        assert binding.current.get("port") == 8080
        assert manager._done.is_set()

        failed = AsyncManagedConfigManager(
            _AsyncLoader(error=ValueError("failed")),
            ConfigBinding(RuntimeConfig, {}), Callbacks(lambda _report: None),
        )
        await failed.start_async()
        with pytest.raises(ValueError, match="failed"):
            await failed.wait_until_ready_async()
        assert failed._done.is_set()

    asyncio.run(scenario())


def test_cancelled_async_start_stops_loader_and_releases_binding_claim(monkeypatch) -> None:
    import kms_paramstore.async_release as async_release

    async def scenario() -> None:
        running = asyncio.Event()
        stopped = asyncio.Event()

        class BlockingLoader:
            def __init__(self, client, config) -> None:
                pass

            async def run(self, prepare) -> None:
                running.set()
                await stopped.wait()

            async def stop(self) -> None:
                stopped.set()

        monkeypatch.setattr(async_release, "AsyncReleaseLoader", BlockingLoader)
        binding = ConfigBinding(RuntimeConfig, {})
        task = asyncio.create_task(start_async_managed_config(
            object(), release="runtime", binding=binding,
            callbacks=Callbacks(lambda _report: None),
        ))
        await running.wait()
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert stopped.is_set()
        binding._claim_start()
        binding._release_start_claim()

    asyncio.run(scenario())
