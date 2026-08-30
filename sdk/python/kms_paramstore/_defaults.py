"""Shared validation for value-free defaults RPCs."""

from __future__ import annotations

import re
from collections.abc import Iterable, Mapping

from . import errors
from ._gen import kms_pb2
from ._refs import parse_namespace, to_proto_namespace
from .models import (
    ApplicationDefaultsApplyEntry,
    ApplicationDefaultsApplyResult,
    VerifyDefaultEntry,
    VerifyDefaultVerdict,
    VerifyReleaseDefaultsResult,
)

_SHA256 = re.compile(r"^[0-9a-f]{64}$")
_VERDICTS = {
    "match", "differs", "missing_in_release", "unknown_alias",
    "secret_alias", "unsupported_content_type",
}
_APPLY_STATUSES = {"create", "unchanged", "update", "blocked"}


def make_verify_request(
    *, namespace: str, release: str, profile: str, schema_sha256: str,
    entries: Iterable[VerifyDefaultEntry | Mapping[str, object]],
) -> tuple[kms_pb2.VerifyReleaseDefaultsRequest, set[str]]:
    ns = parse_namespace(namespace)
    if schema_sha256 and not _SHA256.fullmatch(schema_sha256):
        raise errors.ConfigError("schema_sha256 must be lowercase 64-character hex")
    wire_entries = []
    seen: set[str] = set()
    for index, entry in enumerate(entries):
        if isinstance(entry, VerifyDefaultEntry):
            raw_alias: object = entry.alias
            raw_content_type: object = entry.content_type
            raw_sha256: object = entry.sha256
        elif isinstance(entry, Mapping):
            raw_alias = entry.get("alias")
            raw_content_type = entry.get("content_type", "")
            raw_sha256 = entry.get("sha256")
        else:
            raise errors.ConfigError(f"verify entry {index} must be VerifyDefaultEntry or a mapping")
        if not isinstance(raw_alias, str) or not raw_alias.strip():
            raise errors.ConfigError(f"verify entry {index} has an empty alias")
        alias = raw_alias.strip()
        if alias in seen:
            raise errors.ConfigError(f"verify entry {alias!r} is duplicated")
        if not isinstance(raw_content_type, str):
            raise errors.ConfigError(f"verify entry {alias!r} has an invalid content_type")
        if not isinstance(raw_sha256, str) or not _SHA256.fullmatch(raw_sha256):
            raise errors.ConfigError(f"verify entry {alias!r} has an invalid sha256")
        seen.add(alias)
        wire_entries.append(kms_pb2.VerifyEntry(
            alias=alias, content_type=raw_content_type, sha256=raw_sha256
        ))
    return (
        kms_pb2.VerifyReleaseDefaultsRequest(
            namespace=to_proto_namespace(ns), name=release, profile=profile,
            schema_sha256=schema_sha256, entries=wire_entries,
        ),
        seen,
    )


def verify_result(response, requested: set[str]) -> VerifyReleaseDefaultsResult:
    if response is None:
        raise errors.ParamStoreError("KMS verify response was empty", code="internal")
    if len(response.entries) != len(requested):
        raise errors.ParamStoreError("verify response verdict count mismatch", code="internal")
    seen: set[str] = set()
    entries = []
    tally = {verdict: 0 for verdict in _VERDICTS}
    for entry in response.entries:
        if entry.alias not in requested or entry.alias in seen:
            raise errors.ParamStoreError(
                "verify response names an unknown or duplicated alias", code="internal"
            )
        if entry.verdict not in _VERDICTS:
            raise errors.ParamStoreError("verify response has an invalid verdict", code="internal")
        seen.add(entry.alias)
        tally[entry.verdict] += 1
        entries.append(VerifyDefaultVerdict(entry.alias, entry.verdict))
    counts = {
        "match": response.match_count,
        "differs": response.differs_count,
        "missing_in_release": response.missing_in_release_count,
        "unknown_alias": response.unknown_alias_count,
        "secret_alias": response.secret_alias_count,
        "unsupported_content_type": response.unsupported_content_type_count,
    }
    for verdict, count in counts.items():
        if count != tally[verdict]:
            raise errors.ParamStoreError(
                f"verify response {verdict} count disagrees with verdicts", code="internal"
            )
    return VerifyReleaseDefaultsResult(
        response.name, response.version, response.activation_revision,
        response.schema_matches, tuple(entries), response.match_count,
        response.differs_count, response.missing_in_release_count,
        response.unknown_alias_count, response.secret_alias_count,
        response.unsupported_content_type_count, response.unverified_count,
    )


def make_apply_request(
    *, namespace: str, artifact: bytes | bytearray | str, overwrite: bool,
    execute: bool, plan_digest: str, update_definition: bool,
) -> kms_pb2.ApplyApplicationDefaultsRequest:
    ns = parse_namespace(namespace)
    if isinstance(artifact, str):
        artifact = artifact.encode("utf-8")
    elif isinstance(artifact, bytearray):
        artifact = bytes(artifact)
    if not isinstance(artifact, bytes) or not artifact:
        raise errors.ConfigError("defaults artifact is required")
    return kms_pb2.ApplyApplicationDefaultsRequest(
        namespace=to_proto_namespace(ns), artifact=artifact, overwrite=overwrite,
        execute=execute, plan_digest=plan_digest, update_definition=update_definition,
    )


def apply_result(response, *, expected_execute: bool) -> ApplicationDefaultsApplyResult:
    if response is None or not response.plan_digest:
        raise errors.ParamStoreError("invalid defaults response", code="internal")
    if response.executed != expected_execute:
        raise errors.ParamStoreError("defaults response execution state mismatch", code="internal")
    if response.definition_updated != (expected_execute and response.definition_changed):
        raise errors.ParamStoreError("defaults response definition state mismatch", code="internal")
    entries = []
    for index, entry in enumerate(response.entries):
        if not entry.alias or not entry.key or entry.status not in _APPLY_STATUSES:
            raise errors.ParamStoreError(
                f"defaults response entry {index} is invalid", code="internal"
            )
        entries.append(ApplicationDefaultsApplyEntry(
            entry.alias, entry.key, entry.content_type, entry.status,
            entry.current_version, entry.applied_version, entry.revision,
        ))
    return ApplicationDefaultsApplyResult(
        response.profile, response.schema_sha256, response.artifact_digest,
        response.plan_digest, tuple(entries), tuple(response.missing_secrets),
        response.executed, response.definition_changed, response.definition_updated,
    )
