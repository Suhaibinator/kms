"""Deterministic, secret-free application defaults artifacts."""

from __future__ import annotations

import json
import os
import re
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Mapping, TextIO, TypeVar

from pydantic import BaseModel

from .contract import ContractEntry, validate_contract
from .runtime import ConfigBinding

DEFAULTS_ARTIFACT_FORMAT = "kms-config-defaults/v1"
MAX_DEFAULTS_ARTIFACT_BYTES = 4 * 1024 * 1024
MAX_DEFAULT_PARAMETER_VALUE_BYTES = 1024 * 1024
_SHA256 = re.compile(r"^[0-9a-f]{64}$")
_PROFILE = re.compile(r"^\S(?:.*\S)?$", re.DOTALL)
T = TypeVar("T", bound=BaseModel)

__all__ = [
    "DEFAULTS_ARTIFACT_FORMAT",
    "DefaultsArtifact",
    "DefaultsArtifactError",
    "DefaultsParameter",
    "encode_defaults_artifact",
    "export_defaults",
    "parse_defaults_artifact",
]


class DefaultsArtifactError(ValueError):
    def __init__(self, message: str) -> None:
        super().__init__(f"configstore defaults: {message}")


@dataclass(frozen=True)
class DefaultsParameter:
    alias: str
    content_type: str
    value: str


@dataclass(frozen=True)
class DefaultsArtifact:
    format: str
    profile: str
    schema_sha256: str
    contract: tuple[ContractEntry, ...]
    parameters: tuple[DefaultsParameter, ...]


def encode_defaults_artifact(
    *, profile: str, schema_sha256: str, contract: tuple[ContractEntry, ...],
    parameters: Mapping[str, str],
) -> str:
    _validate_profile(profile)
    if _SHA256.fullmatch(schema_sha256) is None:
        raise DefaultsArtifactError("schema SHA-256 is invalid")
    normalized = tuple(sorted(validate_contract(contract), key=lambda item: item.alias.encode()))
    expected = {entry.alias: entry for entry in normalized if entry.kind == "parameter"}
    if set(parameters) != set(expected):
        raise DefaultsArtifactError("parameters do not exactly match the parameter contract")
    wire_parameters: list[dict[str, str]] = []
    for alias in sorted(parameters, key=lambda item: item.encode()):
        value = parameters[alias]
        if not isinstance(value, str) or len(value.encode()) > MAX_DEFAULT_PARAMETER_VALUE_BYTES:
            raise DefaultsArtifactError("parameter value is invalid or too large")
        wire_parameters.append({"alias": alias, "content_type": expected[alias].content_type, "value": value})
    document = json.dumps(
        {
            "format": DEFAULTS_ARTIFACT_FORMAT,
            "profile": profile,
            "schema_sha256": schema_sha256,
            "contract": [
                {"alias": item.alias, "kind": item.kind, "content_type": item.content_type}
                for item in normalized
            ],
            "parameters": wire_parameters,
        },
        ensure_ascii=False,
        separators=(",", ":"),
    ).replace("\u2028", "\\u2028").replace("\u2029", "\\u2029") + "\n"
    if len(document.encode()) > MAX_DEFAULTS_ARTIFACT_BYTES:
        raise DefaultsArtifactError("artifact exceeds 4 MiB")
    return document


def parse_defaults_artifact(document: str | bytes) -> DefaultsArtifact:
    try:
        source = document.decode("utf-8", "strict") if isinstance(document, bytes) else document
    except UnicodeDecodeError:
        raise DefaultsArtifactError("artifact must be valid UTF-8") from None
    if not isinstance(source, str) or len(source.encode()) > MAX_DEFAULTS_ARTIFACT_BYTES:
        raise DefaultsArtifactError("artifact is invalid or too large")
    def pairs(items: list[tuple[str, object]]) -> dict[str, object]:
        result: dict[str, object] = {}
        for key, value in items:
            if key in result:
                raise DefaultsArtifactError("artifact contains a duplicate property")
            result[key] = value
        return result
    try:
        root = json.loads(source, object_pairs_hook=pairs, parse_constant=lambda _: (_ for _ in ()).throw(ValueError()))
    except DefaultsArtifactError:
        raise
    except (TypeError, ValueError, json.JSONDecodeError):
        raise DefaultsArtifactError("artifact is not valid JSON") from None
    if not isinstance(root, dict) or set(root) != {"format", "profile", "schema_sha256", "contract", "parameters"}:
        raise DefaultsArtifactError("artifact structure is invalid")
    if root["format"] != DEFAULTS_ARTIFACT_FORMAT:
        raise DefaultsArtifactError("format is unsupported")
    profile = root["profile"]
    digest = root["schema_sha256"]
    _validate_profile(profile)
    if not isinstance(digest, str) or _SHA256.fullmatch(digest) is None:
        raise DefaultsArtifactError("schema SHA-256 is invalid")
    raw_contract, raw_parameters = root["contract"], root["parameters"]
    if not isinstance(raw_contract, list) or not isinstance(raw_parameters, list):
        raise DefaultsArtifactError("artifact structure is invalid")
    contract: list[ContractEntry] = []
    previous = ""
    for item in raw_contract:
        if not isinstance(item, dict) or set(item) != {"alias", "kind", "content_type"}:
            raise DefaultsArtifactError("contract entry is invalid")
        if not all(isinstance(item[name], str) for name in ("alias", "kind", "content_type")):
            raise DefaultsArtifactError("contract entry is invalid")
        entry = ContractEntry(item["alias"], item["kind"], item["content_type"])
        if previous and previous.encode() >= entry.alias.encode():
            raise DefaultsArtifactError("contract is not sorted by alias")
        previous = entry.alias
        contract.append(entry)
    try:
        normalized = validate_contract(contract)
    except ValueError as error:
        raise DefaultsArtifactError("contract is invalid") from error
    expected = {entry.alias: entry for entry in normalized if entry.kind == "parameter"}
    parameters: list[DefaultsParameter] = []
    previous = ""
    for item in raw_parameters:
        if not isinstance(item, dict) or set(item) != {"alias", "content_type", "value"}:
            raise DefaultsArtifactError("parameter entry is invalid")
        alias, content_type, value = item["alias"], item["content_type"], item["value"]
        if not all(isinstance(v, str) for v in (alias, content_type, value)):
            raise DefaultsArtifactError("parameter entry is invalid")
        if previous and previous.encode() >= alias.encode():
            raise DefaultsArtifactError("parameters are not sorted by alias")
        previous = alias
        if alias not in expected or expected[alias].content_type != content_type:
            raise DefaultsArtifactError("parameters do not exactly match the parameter contract")
        if len(value.encode()) > MAX_DEFAULT_PARAMETER_VALUE_BYTES:
            raise DefaultsArtifactError("parameter value is too large")
        parameters.append(DefaultsParameter(alias, content_type, value))
    if {item.alias for item in parameters} != set(expected):
        raise DefaultsArtifactError("parameters do not exactly match the parameter contract")
    return DefaultsArtifact(DEFAULTS_ARTIFACT_FORMAT, profile, digest, normalized, tuple(parameters))


def export_defaults(
    *, profile: str, schema_sha256: str, binding: ConfigBinding[T], output: str | os.PathLike[str] | TextIO,
) -> None:
    """Encode a binding's parameter-only defaults and write atomically."""
    document = encode_defaults_artifact(
        profile=profile, schema_sha256=schema_sha256, contract=binding.spec.contract,
        parameters=binding.encode_defaults_groups(),
    )
    if hasattr(output, "write"):
        output.write(document)  # type: ignore[union-attr]
        return
    destination = Path(output)
    destination.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=".kms-defaults-", dir=destination.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="") as stream:
            stream.write(document)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, destination)
    except BaseException:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise


def _validate_profile(profile: object) -> None:
    if not isinstance(profile, str) or not profile or profile != profile.strip():
        raise DefaultsArtifactError("profile is invalid")
