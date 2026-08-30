"""Deterministic artifacts generated from annotated Pydantic v2 models."""

from __future__ import annotations

import hashlib
import json
import os
import tempfile
import datetime as dt
import collections.abc
from dataclasses import dataclass
from pathlib import Path
import types
from typing import Annotated, Any, Union, get_args, get_origin

from pydantic import BaseModel

from ..configstore.model import ConfigSpec
from ..secret import Secret

CONTRACT_FORMAT = "kms-config-contract/v1"
MAX_SCHEMA_BYTES = 256 * 1024


@dataclass(frozen=True)
class GeneratedArtifacts:
    binding: str
    schema: str
    contract: str
    schema_sha256: str


class StaleArtifactsError(RuntimeError):
    def __init__(self, paths: list[str]) -> None:
        super().__init__("configgen: generated configuration artifacts are stale: " + ", ".join(sorted(paths)))
        self.paths = tuple(sorted(paths))


def generate_artifacts(
    model: type[BaseModel], *, source_module: str | None = None, source_type: str | None = None,
) -> GeneratedArtifacts:
    spec = ConfigSpec.from_model(model)
    module = source_module or model.__module__
    type_name = source_type or model.__name__
    groups: dict[str, Any] = {}
    for alias, fields in spec.group_fields().items():
        properties: dict[str, Any] = {}
        for field in fields:
            properties[field.json_name] = _schema_for(field.annotation)
        groups[alias] = {
            "type": "object", "additionalProperties": False,
            "required": [field.json_name for field in fields], "properties": properties,
        }
    schema_object = {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object", "additionalProperties": False,
        "required": list(groups), "properties": groups,
    }
    compact_schema = json.dumps(schema_object, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    schema_sha256 = hashlib.sha256(compact_schema.encode()).hexdigest()
    schema = _pretty(schema_object)
    if len(schema.encode()) > MAX_SCHEMA_BYTES:
        raise ValueError("configgen: generated schema exceeds 256 KiB")

    contract_object = {
        "format": CONTRACT_FORMAT,
        "source": {"language": "python", "module": module, "type": type_name},
        "schema_sha256": schema_sha256,
        "groups": [
            {"alias": alias, "kind": "parameter", "content_type": "json", "fields": [field.json_name for field in fields]}
            for alias, fields in spec.group_fields().items()
        ],
        "fields": [
            {
                "group": field.group, "json_name": field.json_name,
                "python_name": field.property, "python_path": field.property,
                "reload": field.reload, "encoding": _encoding(field.annotation),
                "views": list(field.views),
            }
            for field in spec.parameters
        ],
        "secrets": [
            {
                "alias": field.alias, "kind": "secret", "python_name": field.property,
                "python_path": field.property, "reload": field.reload,
                "encoding": "secret", "views": list(field.views),
            }
            for field in spec.secrets
        ],
        "views": [
            {"name": view, "method": _identifier(view), "fields": sorted(paths)}
            for view, paths in _views(spec).items()
        ],
    }
    contract = _pretty(contract_object)
    binding = _render_binding(module, type_name, schema_sha256, contract_object, spec)
    return GeneratedArtifacts(binding, schema, contract, schema_sha256)


def write_artifacts(
    artifacts: GeneratedArtifacts, *, binding: str | os.PathLike[str],
    schema: str | os.PathLike[str], contract: str | os.PathLike[str], check: bool = False,
) -> None:
    outputs = {Path(binding): artifacts.binding, Path(schema): artifacts.schema, Path(contract): artifacts.contract}
    identities = [path.resolve() for path in outputs]
    if len(set(identities)) != len(identities):
        raise ValueError("configgen: output paths must be distinct")
    if check:
        stale = [str(path) for path, content in outputs.items() if not path.is_file() or path.read_text(encoding="utf-8") != content]
        if stale:
            raise StaleArtifactsError(stale)
        return
    for path, content in outputs.items():
        path.parent.mkdir(parents=True, exist_ok=True)
        descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
        try:
            with os.fdopen(descriptor, "w", encoding="utf-8", newline="") as stream:
                stream.write(content)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, path)
        except BaseException:
            try:
                os.unlink(temporary)
            except FileNotFoundError:
                pass
            raise


def _pretty(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n"


def _encoding(annotation: Any) -> str:
    core = get_args(annotation)[0] if get_origin(annotation) is Annotated else annotation
    origin = get_origin(core)
    if core is bool:
        return "boolean"
    if core is str:
        return "string"
    if core is int:
        return "int64"
    if core is float:
        return "float64"
    if core is bytes:
        return "base64"
    if core is dt.timedelta:
        return "go-duration"
    if origin in (list, tuple):
        return "array"
    if origin in (dict, collections.abc.Mapping):
        return "record"
    if origin in (Union, types.UnionType) and type(None) in get_args(core):
        member = next(item for item in get_args(core) if item is not type(None))
        return f"nullable<{_encoding(member)}>"
    if isinstance(core, type) and issubclass(core, BaseModel):
        return "object"
    raise TypeError(f"configgen: unsupported portable field type {core!r}")


def _schema_for(annotation: Any) -> dict[str, Any]:
    core = get_args(annotation)[0] if get_origin(annotation) is Annotated else annotation
    metadata = get_args(annotation)[1:] if get_origin(annotation) is Annotated else ()
    origin, args = get_origin(core), get_args(core)
    if core is bool:
        schema: dict[str, Any] = {"type": "boolean"}
    elif core is str:
        schema = {"type": "string"}
    elif core is int:
        minimum, maximum = -(1 << 63), (1 << 63) - 1
        for item in metadata:
            if getattr(item, "ge", None) is not None:
                minimum = max(minimum, item.ge)
            if getattr(item, "gt", None) is not None:
                minimum = max(minimum, item.gt + 1)
            if getattr(item, "le", None) is not None:
                maximum = min(maximum, item.le)
            if getattr(item, "lt", None) is not None:
                maximum = min(maximum, item.lt - 1)
        schema = {"type": "integer", "minimum": minimum, "maximum": maximum}
    elif core is float:
        float_maximum = float.fromhex("0x1.fffffffffffffp+1023")
        schema = {"type": "number", "minimum": -float_maximum, "maximum": float_maximum}
    elif core is bytes:
        schema = {"type": "string", "format": "kms-base64"}
    elif core is dt.timedelta:
        schema = {"type": "string", "format": "go-duration"}
    elif origin in (list, set, frozenset):
        schema = {"type": "array", "items": _schema_for(args[0])}
    elif origin is tuple:
        if len(args) == 2 and args[1] is Ellipsis:
            schema = {"type": "array", "items": _schema_for(args[0])}
        else:
            schema = {
                "type": "array", "prefixItems": [_schema_for(item) for item in args],
                "minItems": len(args), "maxItems": len(args),
            }
    elif origin in (dict, collections.abc.Mapping):
        if args[0] is not str:
            raise TypeError("configgen: mappings must use string keys")
        schema = {"type": "object", "additionalProperties": _schema_for(args[1])}
    elif origin in (Union, types.UnionType):
        members = tuple(item for item in args if item is not type(None))
        if len(members) != 1 or len(members) == len(args):
            raise TypeError("configgen: only optional unions are portable")
        schema = {"anyOf": [_schema_for(members[0]), {"type": "null"}]}
    elif isinstance(core, type) and issubclass(core, BaseModel):
        properties: dict[str, Any] = {}
        required: list[str] = []
        for name, field in core.model_fields.items():
            json_name = field.serialization_alias or field.alias or name
            nested = Annotated[(field.annotation, *field.metadata)] if field.metadata else field.annotation
            properties[json_name] = _schema_for(nested)
            required.append(json_name)
        schema = {
            "type": "object", "additionalProperties": False,
            "required": required, "properties": properties,
        }
    else:
        raise TypeError(f"configgen: unsupported portable field type {core!r}")
    return schema


def _views(spec: ConfigSpec) -> dict[str, list[str]]:
    result: dict[str, list[str]] = {}
    for field in spec.parameters:
        for view in field.views:
            result.setdefault(view, []).append(f"{field.group}.{field.json_name}")
    for secret_field in spec.secrets:
        for view in secret_field.views:
            result.setdefault(view, []).append(secret_field.alias)
    return dict(sorted(result.items()))


def _identifier(value: str) -> str:
    return value.replace("-", "_")


def _render_binding(module: str, type_name: str, digest: str, contract: object, spec: ConfigSpec) -> str:
    lines = [
        '"""Generated by kms-config-gen-py; DO NOT EDIT."""',
        "from __future__ import annotations", "", "from typing import Any, Mapping, cast", "",
        f"import {module} as _source",
        "from kms_paramstore import Secret",
        "from kms_paramstore.configstore import (",
        "    AsyncManagedConfigManager, Callbacks, ConfigBinding, ConfigSnapshot,",
        "    ConfigView, ContractEntry, ManagedConfigManager, VerifyResult,",
        "    encode_defaults_artifact as _encode_defaults_artifact,",
        "    export_defaults as _export_defaults,",
        "    start_async_managed_config as _start_async_managed_config,",
        "    start_managed_config as _start_managed_config,",
        "    verify_defaults as _verify_defaults,",
        "    verify_defaults_async as _verify_defaults_async,",
        ")", "",
        f"{type_name} = _source.{type_name}", "",
        f'SCHEMA_SHA256 = "{digest}"',
        "CONTRACT = (",
    ]
    for entry in spec.contract:
        lines.append(
            f"    ContractEntry({entry.alias!r}, {entry.kind!r}, {entry.content_type!r}),"
        )
    lines.append(")")
    views = _view_fields(spec)
    for view, fields in views.items():
        class_name = _view_class(view)
        lines.extend(["", f"class {class_name}(ConfigView):"])
        for property_name, annotation in fields:
            rendered = _type_expr(annotation, module)
            lines.extend([
                "    @property", f"    def {property_name}(self) -> {rendered}:",
                f"        return cast({rendered}, ConfigView.get(self, {property_name!r}))", "",
            ])
        if lines[-1] == "":
            lines.pop()
    lines.extend(["", f"class Snapshot(ConfigSnapshot[{type_name}]):"])
    all_fields = [(field.property, field.annotation) for field in spec.parameters]
    all_fields.extend((field.property, Secret) for field in spec.secrets)
    for property_name, annotation in sorted(all_fields):
        rendered = _type_expr(annotation, module)
        lines.extend([
            "    @property", f"    def {property_name}(self) -> {rendered}:",
            f"        return cast({rendered}, ConfigSnapshot.get(self, {property_name!r}))", "",
        ])
    for view, fields in views.items():
        class_name = _view_class(view)
        method = _identifier(view)
        properties = tuple(field[0] for field in fields)
        lines.extend([
            f"    def {method}(self) -> {class_name}:",
            f"        return {class_name}(self, {properties!r})", "",
        ])
    if lines[-1] == "":
        lines.pop()
    lines.extend([
        "", f"class GeneratedConfigStore(ConfigBinding[{type_name}]):",
        f"    def __init__(self, defaults: Mapping[str, Any] | {type_name}) -> None:",
        f"        super().__init__({type_name}, defaults, snapshot_type=Snapshot)",
        "",
        "    @property",
        "    def current(self) -> Snapshot:",
        "        return cast(Snapshot, super().current)",
        "",
        f"    def start(self, client: object, *, release: str, callbacks: Callbacks, namespace: str | None = None, **options: Any) -> ManagedConfigManager[{type_name}]:",
        "        return _start_managed_config(client, release=release, binding=self, callbacks=callbacks, namespace=namespace, **options)",
        "",
        f"    async def start_async(self, client: object, *, release: str, callbacks: Callbacks, namespace: str | None = None, **options: Any) -> AsyncManagedConfigManager[{type_name}]:",
        "        return await _start_async_managed_config(client, release=release, binding=self, callbacks=callbacks, namespace=namespace, **options)",
        "",
        "    def defaults_artifact(self, profile: str) -> str:",
        "        return _encode_defaults_artifact(profile=profile, schema_sha256=SCHEMA_SHA256, contract=CONTRACT, parameters=self.encode_defaults_groups())",
        "",
        "    def export_defaults(self, profile: str, output: Any) -> None:",
        "        _export_defaults(profile=profile, schema_sha256=SCHEMA_SHA256, binding=self, output=output)",
        "",
        "    def verify_defaults(self, client: object, *, namespace: str, release: str = '', profile: str = '', **options: Any) -> VerifyResult:",
        "        return _verify_defaults(client, namespace=namespace, release=release, profile=profile, schema_sha256=SCHEMA_SHA256, contract=CONTRACT, groups=self.encode_defaults_groups(), **options)",
        "",
        "    async def verify_defaults_async(self, client: object, *, namespace: str, release: str = '', profile: str = '', **options: Any) -> VerifyResult:",
        "        return await _verify_defaults_async(client, namespace=namespace, release=release, profile=profile, schema_sha256=SCHEMA_SHA256, contract=CONTRACT, groups=self.encode_defaults_groups(), **options)",
    ])
    return "\n".join(lines) + "\n"


def _view_fields(spec: ConfigSpec) -> dict[str, list[tuple[str, Any]]]:
    result: dict[str, list[tuple[str, Any]]] = {}
    for field in spec.parameters:
        for view in field.views:
            result.setdefault(view, []).append((field.property, field.annotation))
    for secret_field in spec.secrets:
        for view in secret_field.views:
            result.setdefault(view, []).append((secret_field.property, Secret))
    return {view: sorted(fields) for view, fields in sorted(result.items())}


def _view_class(view: str) -> str:
    return "".join(part[:1].upper() + part[1:] for part in view.replace("-", "_").split("_")) + "ConfigView"


def _type_expr(annotation: Any, source_module: str) -> str:
    core = get_args(annotation)[0] if get_origin(annotation) is Annotated else annotation
    origin, args = get_origin(core), get_args(core)
    if core in (bool, str, int, float, bytes):
        return core.__name__
    if getattr(core, "__name__", "") == "Secret" and getattr(core, "__module__", "") == "kms_paramstore.secret":
        return "Secret"
    if origin in (list, set, frozenset):
        return f"{origin.__name__}[{_type_expr(args[0], source_module)}]"
    if origin is dict:
        return f"dict[{_type_expr(args[0], source_module)}, {_type_expr(args[1], source_module)}]"
    if origin is tuple:
        return "tuple[" + ", ".join("..." if item is Ellipsis else _type_expr(item, source_module) for item in args) + "]"
    if origin in (Union, types.UnionType):
        return " | ".join("None" if item is type(None) else _type_expr(item, source_module) for item in args)
    if isinstance(core, type) and core.__module__ == source_module:
        return f"_source.{core.__name__}"
    return "Any"
