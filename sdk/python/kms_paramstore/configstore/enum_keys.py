"""Declared enum members and their portable JSON object key names."""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum, Flag
from typing import Any


@dataclass(frozen=True)
class EnumKeys:
    enum_type: type[Enum]
    members: tuple[tuple[str, Enum], ...]
    integer: bool

    def decode(self, key: str) -> Enum:
        for name, member in self.members:
            if name == key:
                return member
        raise ValueError("unknown enum map key")

    def encode(self, key: Any) -> str:
        # Enum subclasses of int/str compare equal to their primitive values.
        # Require the actual declared member before comparing identities.
        if type(key) is self.enum_type:
            for name, member in self.members:
                if key is member:
                    return name
        raise ValueError("map key must be a declared enum member")

    def schema(self) -> dict[str, Any]:
        result: dict[str, Any] = {"type": "string", "enum": [name for name, _ in self.members]}
        if self.integer:
            result["x-kms-enum-numbers"] = {name: member.value for name, member in self.members}
        return result


def enum_keys(annotation: Any) -> EnumKeys | None:
    if not isinstance(annotation, type) or not issubclass(annotation, Enum):
        return None
    if issubclass(annotation, Flag):
        raise TypeError("enum map keys cannot use Flag or IntFlag")
    declared = tuple(annotation.__members__.values())
    if not declared:
        raise TypeError("enum map keys must declare at least one member")
    kinds = {type(member.value) for member in declared}
    if kinds not in ({int}, {str}):
        raise TypeError("enum map keys must have homogeneous integer or string values")
    integer = kinds == {int}
    members: list[tuple[str, Enum]] = []
    seen: set[int | str] = set()
    for member in declared:
        if integer and not (-(1 << 63) <= member.value <= (1 << 63) - 1):
            raise TypeError("enum map key integer is outside the portable int64 range")
        if member.value in seen:
            continue
        seen.add(member.value)
        members.append((member.name if integer else member.value, member))
    if integer:
        members.sort(key=lambda item: item[1].value)
    return EnumKeys(annotation, tuple(members), integer)
