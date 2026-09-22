from __future__ import annotations

import asyncio
from typing import Annotated, Any

import pytest
from pydantic import BaseModel, ConfigDict, Field, model_validator

from kms_paramstore import Secret
from kms_paramstore.configstore import (
    AsyncConfigManager, AsyncManagedConfigManager,
    Callbacks, CandidateError, ConfigBinding, ConfigManager,
    ManagedConfigManager, Parameter, SecretField,
)
from tests.fixtures.configgen.config_generated import create_local_store, create_local_store_async
from tests.fixtures.configgen.source import ApplicationConfig


# Also checked by mypy to verify lifecycle compatibility with managed handles.
def managed_lifecycle(manager: ManagedConfigManager[ApplicationConfig]) -> ConfigManager:
    return manager


def async_managed_lifecycle(manager: AsyncManagedConfigManager[ApplicationConfig]) -> AsyncConfigManager:
    return manager


def test_generated_local_store_lifecycle_and_views() -> None:
    store, local = create_local_store({"port": 9000, "password": Secret(b"canary", bind_key="binding-canary")})
    manager: ConfigManager = local
    manager.wait_until_ready(timeout=0)
    assert store.current.server().port == 9000
    assert store.current.password.value == b"canary"
    assert store.current.password.bind_key == ""
    status = manager.status()
    assert (status.source, status.state, status.ready) == ("local", "applied", True)
    assert status.applied.is_zero and status.observed.is_zero and store.current.release.is_zero
    assert not status.default_divergent and status.reconnects == 0
    stats = manager.stats()
    assert (stats.candidates, stats.applied, stats.reconnects) == (1, 1, 0)
    assert not stats.rejected and not stats.default_divergent and stats.applied_release_version == 0
    for _ in range(2):
        manager.stop()
        manager.wait(timeout=0)
    assert store.current.port == 9000
    with pytest.raises(RuntimeError, match="local configuration cannot be replaced"):
        store.prepare(object())
    with pytest.raises(RuntimeError, match="only be started once"):
        store.start(object(), release="runtime", callbacks=Callbacks(lambda _: None))
    assert "canary" not in repr(store.current) + repr(manager.status()) + repr(store.current.password)


def test_async_local_lifecycle() -> None:
    async def run() -> None:
        store, local = await create_local_store_async(ApplicationConfig(port=9001))
        manager: AsyncConfigManager = local
        await manager.wait_until_ready_async()
        for _ in range(2):
            await manager.stop_async()
            await manager.wait_async()
        assert store.current.port == 9001
        assert manager.status().source == "local"
        with pytest.raises(RuntimeError, match="only be started once"):
            await store.start_async(object(), release="runtime", callbacks=Callbacks(lambda _: None))
    asyncio.run(run())


def test_strict_revalidation_and_redacted_errors() -> None:
    invalid = ApplicationConfig.model_construct(port="canary", password=Secret())
    values: list[Any] = [invalid, {"port": "canary"}, {"password": "canary"}, {"unknown-canary": 1}]
    for value in values:
        with pytest.raises(CandidateError) as caught:
            create_local_store(value)
        assert "canary" not in str(caught.value)
    store, _ = create_local_store({})
    assert store.current.password.value == b""  # optional secret remains valid
    assert store.current.password.bind_key == ""  # even default declarations are stripped


retained: list[MutableConfig] = []


class Nested(BaseModel):
    count: int


class MutableConfig(BaseModel):
    model_config = ConfigDict(arbitrary_types_allowed=True, strict=True, frozen=True, extra="forbid")
    labels: Annotated[list[str], Parameter("runtime")] = Field(default_factory=list)
    nested: Annotated[Nested, Parameter("runtime")] = Field(default_factory=lambda: Nested(count=1))
    password: Annotated[Secret, SecretField("password")]

    @model_validator(mode="after")
    def validate_config(self):
        if not self.password.value:
            raise ValueError("required secret canary")
        self.labels.append("validated")
        object.__setattr__(self, "password", Secret(self.password.value, bind_key="validator-canary"))
        retained.append(self)
        return self


def test_clone_before_and_after_validation_and_nested_revalidation() -> None:
    retained.clear()
    supplied: dict[str, Any] = {"labels": ["input"], "nested": Nested(count=1), "password": Secret(b"secret-canary")}
    store = ConfigBinding._from_local(MutableConfig, supplied)
    supplied["labels"].append("mutated")
    retained[-1].labels.append("mutated")
    object.__setattr__(retained[-1], "password", Secret(b"changed"))
    copied = store.current.config()
    copied.labels.append("changed")
    copied.nested.count = 99
    assert store.current.config().labels == ["input", "validated"]
    assert store.current.config().nested.count == 1
    assert store.current.config().password.value == b"secret-canary"
    assert store.current.config().password.bind_key == ""
    for payload in [
        {"nested": Nested.model_construct(count="invalid"), "password": Secret(b"x")},
        {"nested": Nested(count=1), "password": Secret()},
    ]:
        with pytest.raises(CandidateError):
            ConfigBinding._from_local(MutableConfig, payload)


class InvalidDefault(ApplicationConfig):
    port: Annotated[int, Parameter("runtime", views=("server",))] = "invalid"  # type: ignore[assignment]


def test_local_defaults_are_validated_even_without_validate_default() -> None:
    with pytest.raises(CandidateError):
        ConfigBinding._from_local(InvalidDefault, {})
