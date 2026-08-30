"""Generated release contract types and validation."""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Iterable, Mapping

__all__ = ["ContractEntry", "validate_contract", "validate_manifest"]

_ALIAS = re.compile(r"^[A-Za-z][A-Za-z0-9_-]{0,63}$")


@dataclass(frozen=True)
class ContractEntry:
    alias: str
    kind: str
    content_type: str = ""


def validate_contract(entries: Iterable[ContractEntry]) -> tuple[ContractEntry, ...]:
    result = tuple(entries)
    if not result:
        raise ValueError("configstore: contract must contain at least one entry")
    seen: set[str] = set()
    for entry in result:
        if not isinstance(entry, ContractEntry) or not _ALIAS.fullmatch(entry.alias):
            raise ValueError("configstore: contract aliases must be non-empty and canonical")
        if entry.alias in seen:
            raise ValueError("configstore: contract contains a duplicate alias")
        seen.add(entry.alias)
        if entry.kind not in ("parameter", "secret"):
            raise ValueError("configstore: contract entry kind is invalid")
        if not isinstance(entry.content_type, str):
            raise ValueError("configstore: contract content type is invalid")
        try:
            entry.content_type.encode("utf-8", "strict")
        except UnicodeError:
            raise ValueError("configstore: contract content type is invalid") from None
        if entry.content_type != entry.content_type.strip():
            raise ValueError("configstore: contract content type is invalid")
        if entry.kind == "parameter" and not entry.content_type:
            raise ValueError("configstore: parameter contract entries require a content type")
    return result


def validate_manifest(
    contract: Iterable[ContractEntry], entries: Mapping[str, object] | Iterable[object]
) -> None:
    """Validate a release manifest before any resource is fetched."""
    from .types import CandidateError
    try:
        expected = {entry.alias: entry for entry in validate_contract(contract)}
        if isinstance(entries, Mapping):
            actual = dict(entries)
        else:
            actual = {str(getattr(entry, "alias", "")): entry for entry in entries}
        if set(actual) != set(expected):
            raise ValueError("release aliases do not match generated contract")
        for alias, wanted in expected.items():
            got = actual[alias]
            if getattr(got, "alias", alias) != alias or getattr(got, "kind", "") != wanted.kind:
                raise ValueError("release entry does not match generated contract")
            if wanted.content_type and getattr(got, "content_type", "") != wanted.content_type:
                raise ValueError("release content type does not match generated contract")
    except CandidateError:
        raise
    except Exception as error:
        raise CandidateError("config_contract_mismatch", error) from error
