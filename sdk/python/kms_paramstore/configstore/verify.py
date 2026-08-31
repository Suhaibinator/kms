"""Hash-only defaults verification against an active KMS release."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Mapping

from .canonical import parameter_hash
from .contract import ContractEntry

_VERDICTS = (
    "match", "differs", "missing_in_release", "unknown_alias",
    "secret_alias", "unsupported_content_type",
)


@dataclass(frozen=True)
class VerifyEntryResult:
    alias: str
    content_type: str
    verdict: str


@dataclass(frozen=True)
class VerifyResult:
    namespace: str
    release_name: str
    release_version: int
    activation_revision: int
    schema_matches: bool
    entries: tuple[VerifyEntryResult, ...]
    unverified: int = 0

    @property
    def passed(self) -> bool:
        return self.schema_matches and all(entry.verdict == "match" for entry in self.entries)

    def failures(self) -> tuple[VerifyEntryResult, ...]:
        return tuple(sorted((entry for entry in self.entries if entry.verdict != "match"), key=lambda item: item.alias.encode()))

    def report(self) -> str:
        rows = [("VERDICT", "ALIAS", "CONTENT_TYPE")] + [
            (entry.verdict, entry.alias, entry.content_type)
            for entry in sorted(self.entries, key=lambda item: item.alias.encode())
        ]
        widths = [max(len(row[index]) for row in rows) + 2 for index in range(2)]
        lines = [f"{self.namespace} {self.release_name}@{self.release_version}#{self.activation_revision}  schema: {'match' if self.schema_matches else 'differs'}"]
        lines.extend(f"{row[0]:<{widths[0]}}{row[1]:<{widths[1]}}{row[2]}" for row in rows)
        counts = {verdict: sum(entry.verdict == verdict for entry in self.entries) for verdict in _VERDICTS}
        lines.append("summary: " + " ".join(f"{name}={counts[name]}" for name in _VERDICTS) + f" unverified={self.unverified}")
        lines.append("result: active release " + ("matches" if self.passed else "differs from") + " source defaults")
        return "\n".join(lines) + "\n"


def verify_defaults(
    client: object, *, namespace: str, schema_sha256: str,
    contract: tuple[ContractEntry, ...], groups: Mapping[str, str],
    release: str = "", profile: str = "", **options: Any,
) -> VerifyResult:
    if not namespace.strip():
        raise TypeError("configstore: verify requires namespace")
    entries, content_types = [], {}
    for item in contract:
        if item.kind != "parameter":
            continue
        if item.alias not in groups:
            raise ValueError(f"configstore: verify: missing encoded parameter group {item.alias}")
        entries.append({"alias": item.alias, "content_type": item.content_type, "sha256": parameter_hash(item.content_type, groups[item.alias])})
        content_types[item.alias] = item.content_type
    method = getattr(client, "verify_release_defaults")
    response = method(namespace=namespace, release=release, profile=profile, schema_sha256=schema_sha256, entries=entries, **options)
    results = tuple(VerifyEntryResult(item.alias, content_types.get(item.alias, ""), item.verdict) for item in response.entries)
    return VerifyResult(namespace, response.release_name, response.release_version, response.activation_revision, response.schema_matches, results, response.unverified_count)


async def verify_defaults_async(client: object, **kwargs: Any) -> VerifyResult:
    namespace, schema_sha256 = kwargs.pop("namespace"), kwargs.pop("schema_sha256")
    contract, groups = kwargs.pop("contract"), kwargs.pop("groups")
    if not isinstance(namespace, str) or not namespace.strip():
        raise TypeError("configstore: verify requires namespace")
    entries, content_types = [], {}
    for item in contract:
        if item.kind == "parameter":
            if item.alias not in groups:
                raise ValueError(
                    f"configstore: verify: missing encoded parameter group {item.alias}"
                )
            entries.append({"alias": item.alias, "content_type": item.content_type, "sha256": parameter_hash(item.content_type, groups[item.alias])})
            content_types[item.alias] = item.content_type
    response = await getattr(client, "verify_release_defaults")(namespace=namespace, schema_sha256=schema_sha256, entries=entries, **kwargs)
    results = tuple(VerifyEntryResult(item.alias, content_types.get(item.alias, ""), item.verdict) for item in response.entries)
    return VerifyResult(namespace, response.release_name, response.release_version, response.activation_revision, response.schema_matches, results, response.unverified_count)
