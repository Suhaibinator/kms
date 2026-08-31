"""Cross-SDK canonical parameter encoding and hashing."""

from __future__ import annotations

import hashlib
import json
import re
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
        root: list[Any] = [None]
        tasks: list[tuple[Any, ...]] = [("value", root, 0)]
        while tasks:
            task = tasks.pop()
            kind = task[0]
            if kind == "value":
                holder, depth = task[1], task[2]
                if depth > 1000:
                    raise ValueError("invalid JSON")
                self._space()
                if self.index >= len(self.source):
                    raise ValueError("invalid JSON")
                character = self.source[self.index]
                if character == "[":
                    self.index += 1
                    value: Any = []
                    holder[0] = value
                    tasks.append(("array", value, depth, True))
                elif character == "{":
                    self.index += 1
                    value = _Object()
                    holder[0] = value
                    tasks.append(("object", value, set(), depth, True))
                else:
                    holder[0] = self._scalar()
            elif kind == "array":
                array, depth, allow_close = task[1], task[2], task[3]
                self._space()
                if allow_close and self._take("]"):
                    continue
                holder = [None]
                tasks.extend((("array_after", array, holder, depth), ("value", holder, depth + 1)))
            elif kind == "array_after":
                array, holder, depth = task[1], task[2], task[3]
                array.append(holder[0])
                self._space()
                if self._take("]"):
                    continue
                if not self._take(","):
                    raise ValueError("invalid JSON array")
                tasks.append(("array", array, depth, False))
            elif kind == "object":
                obj, seen, depth, allow_close = task[1], task[2], task[3], task[4]
                self._space()
                if allow_close and self._take("}"):
                    continue
                if self.index >= len(self.source) or self.source[self.index] != '"':
                    raise ValueError("invalid JSON object")
                name = self._string()
                if name in seen:
                    raise ValueError("duplicate object key")
                seen.add(name)
                self._space()
                if not self._take(":"):
                    raise ValueError("invalid JSON object")
                holder = [None]
                tasks.extend((("object_after", obj, seen, name, holder, depth), ("value", holder, depth + 1)))
            elif kind == "object_after":
                obj, seen, name, holder, depth = task[1:]
                obj.append((name, holder[0]))
                self._space()
                if self._take("}"):
                    continue
                if not self._take(","):
                    raise ValueError("invalid JSON object")
                tasks.append(("object", obj, seen, depth, False))
        self._space()
        if self.index != len(self.source):
            raise ValueError("trailing data")
        return root[0]

    def _scalar(self) -> Any:
        character = self.source[self.index]
        if character == '"':
            return self._string()
        for token, value in (("null", None), ("true", True), ("false", False)):
            if self.source.startswith(token, self.index):
                self.index += len(token)
                return value
        match = _NUMBER.match(self.source, self.index)
        if match is None:
            raise ValueError("invalid JSON")
        self.index = match.end()
        return _Number(match.group(0))

    def _space(self) -> None:
        while self.index < len(self.source) and self.source[self.index] in _WS:
            self.index += 1

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
    output: list[str] = []
    tasks: list[tuple[str, Any]] = [("value", value)]
    while tasks:
        kind, item = tasks.pop()
        if kind == "text":
            output.append(item)
        elif item is None:
            output.append("null")
        elif item is True:
            output.append("true")
        elif item is False:
            output.append("false")
        elif isinstance(item, _Number):
            output.append(item.lexeme)
        elif isinstance(item, str):
            output.append(_quote(item))
        elif isinstance(item, _Object):
            properties = sorted(item, key=lambda pair: pair[0].encode("utf-8"))
            tasks.append(("text", "}"))
            for index in range(len(properties) - 1, -1, -1):
                name, child = properties[index]
                tasks.append(("value", child))
                tasks.append(("text", ":"))
                tasks.append(("text", _quote(name)))
                if index:
                    tasks.append(("text", ","))
            tasks.append(("text", "{"))
        elif isinstance(item, list):
            tasks.append(("text", "]"))
            for index in range(len(item) - 1, -1, -1):
                tasks.append(("value", item[index]))
                if index:
                    tasks.append(("text", ","))
            tasks.append(("text", "["))
        else:
            raise TypeError("unsupported canonical JSON node")
    return "".join(output)


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
        return _write(_Parser(source).parse()).encode("utf-8")
    except (UnicodeError, ValueError, RecursionError):
        raise ValueError("configstore: canonical json: invalid document") from None


def parameter_hash(content_type: str, value: str | bytes) -> str:
    """Return lower-case SHA-256 of :func:`canonical_parameter_value`."""
    return hashlib.sha256(canonical_parameter_value(content_type, value)).hexdigest()
