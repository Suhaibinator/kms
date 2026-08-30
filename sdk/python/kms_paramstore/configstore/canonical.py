"""Cross-SDK canonical parameter encoding and hashing."""

from __future__ import annotations

import hashlib
import json
import re
import sys
from dataclasses import dataclass
from typing import Any

__all__ = ["canonical_parameter_value", "parameter_hash"]

_NUMBER = re.compile(r"-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?")
_WS = " \t\r\n"


@dataclass(frozen=True)
class _Number:
    lexeme: str


class _Object(list[tuple[str, Any]]):
    pass


class _Parser:
    def __init__(self, source: str) -> None:
        self.source = source
        self.index = 0

    def parse(self) -> Any:
        self._space()
        value = self._value(0)
        self._space()
        if self.index != len(self.source):
            raise ValueError("trailing data")
        return value

    def _space(self) -> None:
        while self.index < len(self.source) and self.source[self.index] in _WS:
            self.index += 1

    def _value(self, depth: int) -> Any:
        if depth > 1000 or self.index >= len(self.source):
            raise ValueError("invalid JSON")
        character = self.source[self.index]
        if character == '"':
            return self._string()
        if character == "{":
            return self._object(depth)
        if character == "[":
            return self._array(depth)
        for token, value in (("null", None), ("true", True), ("false", False)):
            if self.source.startswith(token, self.index):
                self.index += len(token)
                return value
        match = _NUMBER.match(self.source, self.index)
        if match is None:
            raise ValueError("invalid JSON")
        self.index = match.end()
        return _Number(match.group(0))

    def _string(self) -> str:
        start = self.index
        self.index += 1
        escaped = False
        while self.index < len(self.source):
            character = self.source[self.index]
            if ord(character) < 0x20:
                raise ValueError("invalid JSON string")
            self.index += 1
            if escaped:
                escaped = False
            elif character == "\\":
                escaped = True
            elif character == '"':
                try:
                    decoded = json.loads(self.source[start : self.index])
                except (TypeError, ValueError) as error:
                    raise ValueError("invalid JSON string") from error
                return _replace_surrogates(decoded)
        raise ValueError("unterminated JSON string")

    def _array(self, depth: int) -> list[Any]:
        self.index += 1
        self._space()
        values: list[Any] = []
        if self._take("]"):
            return values
        while True:
            values.append(self._value(depth + 1))
            self._space()
            if self._take("]"):
                return values
            if not self._take(","):
                raise ValueError("invalid JSON array")
            self._space()

    def _object(self, depth: int) -> _Object:
        self.index += 1
        self._space()
        values = _Object()
        seen: set[str] = set()
        if self._take("}"):
            return values
        while True:
            if self.index >= len(self.source) or self.source[self.index] != '"':
                raise ValueError("invalid JSON object")
            name = self._string()
            if name in seen:
                raise ValueError("duplicate object key")
            seen.add(name)
            self._space()
            if not self._take(":"):
                raise ValueError("invalid JSON object")
            self._space()
            values.append((name, self._value(depth + 1)))
            self._space()
            if self._take("}"):
                return values
            if not self._take(","):
                raise ValueError("invalid JSON object")
            self._space()

    def _take(self, token: str) -> bool:
        if self.source.startswith(token, self.index):
            self.index += len(token)
            return True
        return False


def _replace_surrogates(value: str) -> str:
    # Go and JavaScript replace decoded, unpaired JSON surrogate escapes with
    # U+FFFD. Python intentionally preserves them, so normalize explicitly.
    encoded = value.encode("utf-16", "surrogatepass")
    return encoded.decode("utf-16", "replace")


def _quote(value: str) -> str:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


def _write(value: Any) -> str:
    if value is None:
        return "null"
    if value is True:
        return "true"
    if value is False:
        return "false"
    if isinstance(value, _Number):
        return value.lexeme
    if isinstance(value, str):
        return _quote(value)
    if isinstance(value, _Object):
        properties = sorted(value, key=lambda item: item[0].encode("utf-8"))
        return "{" + ",".join(f"{_quote(k)}:{_write(v)}" for k, v in properties) + "}"
    if isinstance(value, list):
        return "[" + ",".join(_write(item) for item in value) + "]"
    raise TypeError("unsupported canonical JSON node")


def canonical_parameter_value(content_type: str, value: str | bytes) -> bytes:
    """Return the byte form shared by KMS, Go, and TypeScript.

    Only the exact lower-case content type ``json`` is canonicalized. Other
    content types pass through byte-for-byte (strings are UTF-8 encoded).
    """
    if not isinstance(content_type, str):
        raise TypeError("configstore: canonical content type must be a string")
    if not isinstance(value, (str, bytes)):
        raise TypeError("configstore: canonical value must be a string or bytes")
    if content_type != "json":
        return value.encode("utf-8") if isinstance(value, str) else bytes(value)
    try:
        source = value if isinstance(value, str) else value.decode("utf-8", "strict")
        source.encode("utf-8", "strict")
        old_limit = sys.getrecursionlimit()
        if old_limit < 5000:
            sys.setrecursionlimit(5000)
        try:
            return _write(_Parser(source).parse()).encode("utf-8")
        finally:
            if old_limit < 5000:
                sys.setrecursionlimit(old_limit)
    except (UnicodeError, ValueError, RecursionError):
        raise ValueError("configstore: canonical json: invalid document") from None


def parameter_hash(content_type: str, value: str | bytes) -> str:
    """Return lower-case SHA-256 of :func:`canonical_parameter_value`."""
    return hashlib.sha256(canonical_parameter_value(content_type, value)).hexdigest()
