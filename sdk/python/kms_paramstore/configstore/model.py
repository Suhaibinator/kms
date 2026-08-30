"""Pydantic model metadata used by managed configuration and configgen."""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Annotated, Any, Literal, Mapping, get_args, get_origin

try:
    from pydantic import BaseModel
    from pydantic.fields import PydanticUndefined
except ImportError as error:  # pragma: no cover - dependency guard for broken installs
    raise ImportError("kms_paramstore.configstore requires pydantic>=2.13,<3") from error

from ..secret import Secret
from .contract import ContractEntry

__all__ = [
    "ConfigSpec",
    "Inline",
    "Parameter",
    "SecretField",
    "Unmanaged",
]

ReloadPolicy = Literal["hot", "restart"]
_NAME = re.compile(r"^[A-Za-z][A-Za-z0-9_-]{0,63}$")


@dataclass(frozen=True)
class Parameter:
    """Map one root field into a JSON parameter group."""

    group: str
    json_name: str = ""
    reload: ReloadPolicy = "hot"
    views: tuple[str, ...] = ()


@dataclass(frozen=True)
class SecretField:
    """Map one required :class:`Secret` root field to a release alias."""

    alias: str
    reload: ReloadPolicy = "hot"
    views: tuple[str, ...] = ()


@dataclass(frozen=True)
class Inline:
    """Document that a nested Pydantic value is encoded inline in its group."""


@dataclass(frozen=True)
class Unmanaged:
    """Exclude a root field from the generated KMS release contract."""


@dataclass(frozen=True)
class ParameterField:
    property: str
    json_name: str
    group: str
    reload: ReloadPolicy
    views: tuple[str, ...]
    annotation: Any


@dataclass(frozen=True)
class ManagedSecretField:
    property: str
    alias: str
    reload: ReloadPolicy
    views: tuple[str, ...]


@dataclass(frozen=True)
class ConfigSpec:
    model: type[BaseModel]
    parameters: tuple[ParameterField, ...]
    secrets: tuple[ManagedSecretField, ...]
    unmanaged: tuple[str, ...]

    @classmethod
    def from_model(cls, model: type[BaseModel]) -> "ConfigSpec":
        if not isinstance(model, type) or not issubclass(model, BaseModel):
            raise TypeError("configstore: configuration root must be a Pydantic model class")
        config = model.model_config
        if config.get("frozen") is not True or config.get("strict") is not True:
            raise TypeError("configstore: configuration model must set frozen=True and strict=True")
        if config.get("extra") != "forbid":
            raise TypeError("configstore: configuration model must set extra='forbid'")

        parameters: list[ParameterField] = []
        secrets: list[ManagedSecretField] = []
        unmanaged: list[str] = []
        for property_name, field in model.model_fields.items():
            managed = [item for item in field.metadata if isinstance(item, (Parameter, SecretField, Unmanaged))]
            if len(managed) != 1:
                raise TypeError(
                    f"configstore: field {property_name} must declare exactly one Parameter, SecretField, or Unmanaged marker"
                )
            marker = managed[0]
            if isinstance(marker, Unmanaged):
                unmanaged.append(property_name)
                continue
            if isinstance(marker, Parameter):
                _validate_name(marker.group, f"field {property_name} group")
                json_name = marker.json_name or field.serialization_alias or field.alias or property_name
                _validate_name(json_name, f"field {property_name} JSON name")
                _validate_policy(marker.reload, property_name)
                _validate_views(marker.views, property_name)
                if field.default is PydanticUndefined and field.default_factory is None:
                    raise TypeError(f"configstore: non-secret field {property_name} must declare a source default")
                parameters.append(
                    ParameterField(
                        property_name, json_name, marker.group, marker.reload,
                        marker.views, _validation_annotation(field.annotation, field.metadata),
                    )
                )
                continue

            _validate_name(marker.alias, f"field {property_name} secret alias")
            _validate_policy(marker.reload, property_name)
            _validate_views(marker.views, property_name)
            if field.default is not PydanticUndefined or field.default_factory is not None:
                raise TypeError(f"configstore: secret field {property_name} must be required and default-free")
            if not _accepts_secret(field.annotation):
                raise TypeError(f"configstore: secret field {property_name} must be typed as Secret")
            secrets.append(ManagedSecretField(property_name, marker.alias, marker.reload, marker.views))

        if not parameters and not secrets:
            raise TypeError("configstore: model must contain at least one managed field")
        aliases = [item.group for item in parameters] + [item.alias for item in secrets]
        if len(set(aliases)) != len(set(item.group for item in parameters)) + len(secrets):
            raise TypeError("configstore: parameter group and secret aliases must be unique")
        group_json_names: dict[str, set[str]] = {}
        for item in parameters:
            names = group_json_names.setdefault(item.group, set())
            if item.json_name in names:
                raise TypeError(f"configstore: duplicate JSON field {item.group}.{item.json_name}")
            names.add(item.json_name)
        return cls(
            model,
            tuple(sorted(parameters, key=lambda item: (item.group.encode(), item.json_name.encode()))),
            tuple(sorted(secrets, key=lambda item: item.alias.encode())),
            tuple(sorted(unmanaged)),
        )

    @property
    def contract(self) -> tuple[ContractEntry, ...]:
        groups = sorted({field.group for field in self.parameters}, key=lambda value: value.encode())
        entries = [ContractEntry(alias, "parameter", "json") for alias in groups]
        entries.extend(ContractEntry(field.alias, "secret", "") for field in self.secrets)
        return tuple(sorted(entries, key=lambda item: item.alias.encode()))

    def group_fields(self) -> Mapping[str, tuple[ParameterField, ...]]:
        result: dict[str, list[ParameterField]] = {}
        for field in self.parameters:
            result.setdefault(field.group, []).append(field)
        return {group: tuple(fields) for group, fields in result.items()}


def _validate_name(value: str, label: str) -> None:
    if not isinstance(value, str) or _NAME.fullmatch(value) is None:
        raise TypeError(f"configstore: {label} must be canonical")


def _validate_policy(value: str, property_name: str) -> None:
    if value not in ("hot", "restart"):
        raise TypeError(f"configstore: field {property_name} reload must be hot or restart")


def _validate_views(views: tuple[str, ...], property_name: str) -> None:
    if not isinstance(views, tuple) or len(set(views)) != len(views):
        raise TypeError(f"configstore: field {property_name} views must be a unique tuple")
    for view in views:
        _validate_name(view, f"field {property_name} view")


def _accepts_secret(annotation: Any) -> bool:
    return annotation is Secret or Secret in get_args(annotation) or get_origin(annotation) is Secret


def _validation_annotation(annotation: Any, metadata: list[Any]) -> Any:
    validation = tuple(
        item for item in metadata
        if not isinstance(item, (Parameter, SecretField, Unmanaged, Inline))
    )
    if not validation:
        return annotation
    return Annotated[(annotation, *validation)]
