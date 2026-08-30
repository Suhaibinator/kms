"""Pydantic-first managed configuration runtime."""

from __future__ import annotations

import asyncio
import copy
import inspect
import json
import logging
import math
import threading
import time
from dataclasses import dataclass
from types import MappingProxyType
from typing import Any, Awaitable, Callable, Generic, Mapping, TypeVar

from pydantic import BaseModel, ValidationError

from ..secret import Secret
from .canonical import canonical_parameter_value
from .codecs import decode_value, encode_value
from .contract import validate_manifest
from .model import ConfigSpec
from .types import (
    AppliedReport,
    CandidateError,
    CandidateRejectionReport,
    ConfigSnapshot,
    DefaultMismatchReport,
    FieldChange,
    FieldDifference,
    ManagedConfigStats,
    ManagedConfigStatus,
    ReleaseIdentity,
)

T = TypeVar("T", bound=BaseModel)

__all__ = [
    "Callbacks",
    "ConfigBinding",
    "ConfigView",
    "ManagedConfigManager",
    "AsyncManagedConfigManager",
    "logging_callbacks",
    "start_managed_config",
    "start_async_managed_config",
]


@dataclass(frozen=True)
class Callbacks:
    on_default_mismatch: Callable[[DefaultMismatchReport], None]
    on_applied: Callable[[AppliedReport], None] | None = None
    on_candidate_rejected: Callable[[CandidateRejectionReport], None] | None = None


class ConfigView:
    """A typed-view building block that always reads from one snapshot."""

    __slots__ = ("_snapshot", "_fields")

    def __init__(self, snapshot: ConfigSnapshot[Any], fields: tuple[str, ...]) -> None:
        self._snapshot = snapshot
        self._fields = fields

    def get(self, field: str) -> Any:
        if field not in self._fields:
            raise AttributeError(field)
        return self._snapshot.get(field)


class ConfigBinding(Generic[T]):
    """Generated-binding runtime shared by sync and async managers."""

    def __init__(self, model: type[T], defaults: Mapping[str, Any] | T) -> None:
        self.spec = ConfigSpec.from_model(model)
        self.model = model
        self._source_defaults = self._normalize_defaults(defaults)
        self._active: ConfigSnapshot[T] | None = None
        self._lock = threading.Lock()

    @property
    def current(self) -> ConfigSnapshot[T]:
        with self._lock:
            if self._active is None:
                raise RuntimeError("configstore: managed configuration is not ready")
            return self._active

    def view(self, name: str) -> ConfigView:
        fields = tuple(field.property for field in self.spec.parameters if name in field.views)
        fields += tuple(field.property for field in self.spec.secrets if name in field.views)
        if not fields:
            raise KeyError(name)
        return ConfigView(self.current, fields)

    def encode_parameter_groups(self, config: T | None = None) -> Mapping[str, str]:
        source = config or self.current.config()
        groups: dict[str, str] = {}
        for group, fields in self.spec.group_fields().items():
            document = {
                field.json_name: encode_value(field.annotation, getattr(source, field.property))
                for field in fields
            }
            raw = json.dumps(document, ensure_ascii=False, separators=(",", ":"), allow_nan=False)
            groups[group] = canonical_parameter_value("json", raw).decode("utf-8")
        return MappingProxyType(groups)

    def encode_defaults_groups(self) -> Mapping[str, str]:
        # Secrets are required for whole-model validation, but parameter group
        # encoding is deliberately independent and never materializes them.
        groups: dict[str, str] = {}
        for group, fields in self.spec.group_fields().items():
            values: dict[str, Any] = {}
            for field in fields:
                value = self._source_defaults[field.property]
                values[field.json_name] = encode_value(field.annotation, value)
            raw = json.dumps(values, ensure_ascii=False, separators=(",", ":"), allow_nan=False)
            groups[group] = canonical_parameter_value("json", raw).decode("utf-8")
        return MappingProxyType(groups)

    def prepare(self, snapshot: object) -> "_PreparedCandidate[T]":
        identity = ReleaseIdentity.from_candidate(snapshot)
        try:
            parameters = getattr(snapshot, "parameters")
            secrets = getattr(snapshot, "secrets")
            payload = copy.deepcopy(self._source_defaults)
            for group, fields in self.spec.group_fields().items():
                if group not in parameters:
                    raise CandidateError("config_decode_failed", paths=(group,))
                values = _strict_group(parameters[group], tuple(field.json_name for field in fields))
                for field in fields:
                    try:
                        payload[field.property] = decode_value(field.annotation, values[field.json_name])
                    except (TypeError, ValueError) as error:
                        raise CandidateError(
                            "config_decode_failed", error,
                            paths=(f"{field.group}.{field.json_name}",),
                        ) from error
            for secret_field in self.spec.secrets:
                secret = secrets.get(secret_field.alias)
                if not isinstance(secret, Secret):
                    raise CandidateError("config_decode_failed", paths=(secret_field.alias,))
                payload[secret_field.property] = secret
            try:
                candidate = self.model.model_validate(payload, strict=True)
            except ValidationError as error:
                raise CandidateError("config_validation_failed", error) from error
            _require_finite(candidate, tuple(field.property for field in self.spec.parameters))

            effective_payload = copy.deepcopy(self._source_defaults)
            for secret_field in self.spec.secrets:
                effective_payload[secret_field.property] = secrets[secret_field.alias]
            try:
                effective = self.model.model_validate(effective_payload, strict=True)
            except ValidationError as error:
                raise CandidateError("config_validation_failed", error) from error
            _require_finite(effective, tuple(field.property for field in self.spec.parameters))

            differences: list[FieldDifference] = []
            for field in self.spec.parameters:
                expected = getattr(effective, field.property)
                actual = getattr(candidate, field.property)
                if not _same(expected, actual):
                    differences.append(
                        FieldDifference(f"{field.group}.{field.json_name}", copy.deepcopy(expected), copy.deepcopy(actual))
                    )

            with self._lock:
                previous_snapshot = self._active
            changes: list[FieldChange] = []
            restart: list[str] = []
            if previous_snapshot is not None:
                previous = previous_snapshot.config()
                for field in self.spec.parameters:
                    old, new = getattr(previous, field.property), getattr(candidate, field.property)
                    if not _same(old, new):
                        path = f"{field.group}.{field.json_name}"
                        changes.append(FieldChange(path, copy.deepcopy(old), copy.deepcopy(new)))
                        if field.reload == "restart":
                            restart.append(path)
                for secret_field in self.spec.secrets:
                    old = getattr(previous, secret_field.property)
                    new = getattr(candidate, secret_field.property)
                    if _secret_identity(old) != _secret_identity(new):
                        changes.append(FieldChange(secret_field.alias, None, None))
                        if secret_field.reload == "restart":
                            restart.append(secret_field.alias)

            next_snapshot = ConfigSnapshot(candidate, identity)
            groups = self.encode_parameter_groups(candidate)
            return _PreparedCandidate(
                self,
                next_snapshot,
                tuple(differences),
                tuple(sorted(restart)),
                tuple(changes),
                groups,
            )
        except CandidateError:
            raise
        except Exception as error:
            raise CandidateError("config_decode_failed", error) from error

    def _normalize_defaults(self, defaults: Mapping[str, Any] | T) -> dict[str, Any]:
        raw = defaults.model_dump(round_trip=True) if isinstance(defaults, BaseModel) else dict(defaults)
        allowed = {field.property for field in self.spec.parameters} | set(self.spec.unmanaged)
        unknown = set(raw) - allowed
        if unknown:
            raise TypeError("configstore: defaults contain unknown or secret fields")
        for field in self.spec.parameters:
            if field.property not in raw:
                info = self.model.model_fields[field.property]
                raw[field.property] = info.get_default(call_default_factory=True)
        for property_name in self.spec.unmanaged:
            if property_name not in raw:
                info = self.model.model_fields[property_name]
                if info.is_required():
                    raise TypeError(f"configstore: unmanaged field {property_name} requires a default")
                raw[property_name] = info.get_default(call_default_factory=True)
        return copy.deepcopy(raw)


class _PreparedCandidate(Generic[T]):
    def __init__(
        self,
        binding: ConfigBinding[T],
        snapshot: ConfigSnapshot[T],
        differences: tuple[FieldDifference, ...],
        restart: tuple[str, ...],
        changes: tuple[FieldChange, ...],
        groups: Mapping[str, str],
    ) -> None:
        self.binding = binding
        self.snapshot = snapshot
        self.default_differences = differences
        self.restart_required_fields = restart
        self.changed = changes
        self.groups = groups
        self._done = False

    def commit(self) -> None:
        if self._done:
            return
        with self.binding._lock:
            self.binding._active = self.snapshot
        self._done = True

    publish = commit

    def abort(self) -> None:
        self._done = True

    def release_divergence(self) -> tuple[bool, int]:
        """Bounded source-default drift carried by release acknowledgements."""
        return bool(self.default_differences), len(self.default_differences)


class ManagedConfigManager(Generic[T]):
    def __init__(self, loader: object, binding: ConfigBinding[T], callbacks: Callbacks) -> None:
        self.loader, self.binding, self.callbacks = loader, binding, callbacks
        self._ready = threading.Event()
        self._done = threading.Event()
        self._error: BaseException | None = None
        self._observed = ReleaseIdentity()
        self._applied = ReleaseIdentity()
        self._divergent = False
        self._last_mismatch_key = ""
        self._last_rejection_key = ""
        self._thread: threading.Thread | None = None

    def start(self) -> None:
        if self._thread is not None:
            raise RuntimeError("configstore: manager is already started")
        self._thread = threading.Thread(target=self._run, name="kms-configstore", daemon=True)
        self._thread.start()

    def wait_until_ready(self, timeout: float | None = None) -> None:
        deadline = None if timeout is None else time.monotonic() + timeout
        while not self._ready.wait(0.01):
            if self._done.is_set():
                if self._error is not None:
                    raise self._error
                raise RuntimeError("configstore: release loader stopped before initial publication")
            if deadline is not None and time.monotonic() >= deadline:
                raise TimeoutError("configstore: initial configuration was not published")

    def stop(self) -> None:
        getattr(self.loader, "stop")()

    def wait(self, timeout: float | None = None) -> None:
        if not self._done.wait(timeout):
            raise TimeoutError("configstore: manager did not stop")
        if self._error is not None:
            raise self._error

    def status(self) -> ManagedConfigStatus:
        status = getattr(self.loader, "status")()
        return ManagedConfigStatus(
            state=getattr(status, "state", "idle"), ready=self._ready.is_set(),
            observed=self._observed, applied=self._applied,
            default_divergent=self._divergent,
            last_rejection_category=getattr(status, "last_failure_category", ""),
            last_failure_unix_ms=getattr(status, "last_failure_unix_ms", 0),
            reconnects=getattr(getattr(self.loader, "stats")(), "reconnects", 0),
        )

    def stats(self) -> ManagedConfigStats:
        stats = getattr(self.loader, "stats")()
        rejections = getattr(stats, "rejections", {})
        return ManagedConfigStats(
            candidates=sum(getattr(stats, "acknowledgements", {}).values()),
            applied=getattr(stats, "acknowledgements", {}).get("applied", 0),
            rejected=MappingProxyType(dict(rejections)), reconnects=getattr(stats, "reconnects", 0),
            default_divergent=self._divergent,
            applied_release_version=self._applied.version,
            applied_activation_revision=self._applied.activation_revision,
        )

    def _run(self) -> None:
        try:
            getattr(self.loader, "run")(self._prepare)
        except BaseException as error:
            self._error = error
        finally:
            self._done.set()

    def _prepare(self, *args: object) -> _PreparedCandidate[T]:
        snapshot = args[-1]
        identity = ReleaseIdentity.from_candidate(snapshot)
        self._observed = identity
        startup = not self._ready.is_set()
        try:
            candidate = self.binding.prepare(snapshot)
            if not startup and candidate.restart_required_fields:
                candidate.abort()
                error = CandidateError("restart_required", paths=candidate.restart_required_fields)
                raise error
            if candidate.default_differences:
                key = _identity_key(identity)
                if key != self._last_mismatch_key:
                    self._last_mismatch_key = key
                    _safe_call(self.callbacks.on_default_mismatch, DefaultMismatchReport(
                        "startup" if startup else "runtime", identity, candidate.default_differences
                    ))
            original_commit = candidate.commit
            def commit() -> None:
                original_commit()
                self._applied = identity
                self._divergent = bool(candidate.default_differences)
                if not self._divergent:
                    self._last_mismatch_key = ""
                self._ready.set()
                if self.callbacks.on_applied is not None:
                    _safe_call(self.callbacks.on_applied, AppliedReport(
                        "startup" if startup else "runtime", identity, self._divergent,
                        () if startup else candidate.changed, candidate.groups,
                    ))
            candidate.commit = commit  # type: ignore[method-assign]
            candidate.publish = commit  # type: ignore[method-assign]
            return candidate
        except CandidateError as error:
            self._rejected(identity, error)
            raise

    def _rejected(self, identity: ReleaseIdentity, error: CandidateError) -> None:
        key = _identity_key(identity)
        if key == self._last_rejection_key:
            return
        self._last_rejection_key = key
        if self.callbacks.on_candidate_rejected is not None:
            _safe_call(self.callbacks.on_candidate_rejected, CandidateRejectionReport(
                error.category, identity, error.paths
            ))


class AsyncManagedConfigManager(ManagedConfigManager[T]):
    """Event-loop-owned counterpart of :class:`ManagedConfigManager`."""

    def __init__(self, loader: object, binding: ConfigBinding[T], callbacks: Callbacks) -> None:
        super().__init__(loader, binding, callbacks)
        self._task: asyncio.Task[None] | None = None
        self._async_ready = asyncio.Event()

    async def start_async(self) -> None:
        if self._task is not None:
            raise RuntimeError("configstore: manager is already started")
        self._task = asyncio.create_task(self._run_async(), name="kms-configstore")

    async def wait_until_ready_async(self) -> None:
        ready = asyncio.create_task(self._async_ready.wait())
        assert self._task is not None
        try:
            done, _ = await asyncio.wait((ready, self._task), return_when=asyncio.FIRST_COMPLETED)
            if ready in done:
                return
            await self._task
            raise RuntimeError("configstore: release loader stopped before initial publication")
        finally:
            if not ready.done():
                ready.cancel()
            await asyncio.gather(ready, return_exceptions=True)

    async def stop_async(self) -> None:
        result = getattr(self.loader, "stop")()
        if inspect.isawaitable(result):
            await result

    async def wait_async(self) -> None:
        if self._task is None:
            raise RuntimeError("configstore: manager is not started")
        await self._task

    async def _run_async(self) -> None:
        try:
            run = getattr(self.loader, "run")
            result = run(self._prepare_async)
            if inspect.isawaitable(result):
                await result
        except BaseException as error:
            self._error = error
            raise
        finally:
            self._done.set()

    async def _prepare_async(self, *args: object) -> _PreparedCandidate[T]:
        candidate = self._prepare(*args)
        original = candidate.commit
        def commit() -> None:
            original()
            self._async_ready.set()
        candidate.commit = commit  # type: ignore[method-assign]
        candidate.publish = commit  # type: ignore[method-assign]
        return candidate


def start_managed_config(
    client: object, *, release: str, binding: ConfigBinding[T], callbacks: Callbacks,
    namespace: str | None = None, **loader_options: Any,
) -> ManagedConfigManager[T]:
    from ..release import ReleaseLoader, ReleaseLoaderConfig
    kwargs = dict(name=release, namespace=namespace, **loader_options)
    if "validate_manifest" in inspect.signature(ReleaseLoaderConfig).parameters:
        kwargs["validate_manifest"] = lambda *args: validate_manifest(
            binding.spec.contract, args[-1].entries
        )
    manager = ManagedConfigManager(ReleaseLoader(client, ReleaseLoaderConfig(**kwargs)), binding, callbacks)  # type: ignore[arg-type]
    manager.start()
    manager.wait_until_ready()
    return manager


async def start_async_managed_config(
    client: object, *, release: str, binding: ConfigBinding[T], callbacks: Callbacks,
    namespace: str | None = None, **loader_options: Any,
) -> AsyncManagedConfigManager[T]:
    from ..async_release import AsyncReleaseLoader, AsyncReleaseLoaderConfig
    kwargs = dict(name=release, namespace=namespace, **loader_options)
    if "validate_manifest" in inspect.signature(AsyncReleaseLoaderConfig).parameters:
        kwargs["validate_manifest"] = lambda *args: validate_manifest(
            binding.spec.contract, args[-1].entries
        )
    manager = AsyncManagedConfigManager(
        AsyncReleaseLoader(client, AsyncReleaseLoaderConfig(**kwargs)), binding, callbacks
    )
    await manager.start_async()
    await manager.wait_until_ready_async()
    return manager


def logging_callbacks(logger: logging.Logger | None = None) -> Callbacks:
    target = logger or logging.getLogger("kms_paramstore.configstore")
    return Callbacks(
        on_default_mismatch=lambda report: target.error("kms config diverges from source defaults", extra={
            "phase": report.phase, "release": str(report.release),
            "fields": [item.path for item in report.fields()],
        }),
        on_applied=lambda report: target.info("kms config applied", extra={
            "phase": report.phase, "release": str(report.release),
            "default_divergent": report.default_divergent,
            "changed": [item.path for item in report.changed()],
        }),
        on_candidate_rejected=lambda report: target.error("kms config candidate rejected", extra={
            "category": report.category, "release": str(report.release), "fields": list(report.paths()),
        }),
    )


def _strict_group(document: str, expected: tuple[str, ...]) -> dict[str, Any]:
    if not isinstance(document, str):
        raise CandidateError("config_decode_failed")
    def pairs(values: list[tuple[str, Any]]) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for name, value in values:
            if name in result:
                raise ValueError("duplicate property")
            result[name] = value
        return result
    try:
        value = json.loads(document, object_pairs_hook=pairs, parse_constant=lambda _: (_ for _ in ()).throw(ValueError()))
    except (TypeError, ValueError, json.JSONDecodeError) as error:
        raise CandidateError("config_decode_failed", error) from error
    if not isinstance(value, dict) or set(value) != set(expected):
        raise CandidateError("config_decode_failed")
    return value


def _same(left: Any, right: Any) -> bool:
    try:
        return left == right
    except Exception:
        return False


def _secret_identity(secret: Secret) -> tuple[str, str, str, int, str]:
    return (secret.env, secret.app, secret.key, secret.version, secret.content_type)


def _require_finite(model: BaseModel, fields: tuple[str, ...]) -> None:
    def visit(value: Any) -> None:
        if isinstance(value, float) and not math.isfinite(value):
            raise CandidateError("config_validation_failed")
        if isinstance(value, BaseModel):
            for child in value.__class__.model_fields:
                visit(getattr(value, child))
        elif isinstance(value, Mapping):
            for child in value.values():
                visit(child)
        elif isinstance(value, (list, tuple, set, frozenset)):
            for child in value:
                visit(child)
    for field in fields:
        visit(getattr(model, field))


def _safe_call(callback: Callable[[Any], None], value: Any) -> None:
    try:
        untyped: Any = callback
        result = untyped(value)
        if inspect.iscoroutine(result):
            result.close()
    except BaseException:
        pass


def _identity_key(identity: ReleaseIdentity) -> str:
    return "\0".join((
        identity.namespace, identity.name, str(identity.version),
        str(identity.activation_revision), identity.digest,
    ))
