"""Strict JSON/Python codecs for managed Pydantic fields."""

from __future__ import annotations

import base64
import binascii
import datetime as dt
import re
import types
from decimal import Decimal, InvalidOperation
from dataclasses import dataclass
from typing import Annotated, Any, Mapping, Union, get_args, get_origin

from pydantic import BaseModel, TypeAdapter

_DURATION_PART = re.compile(r"((?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))(ns|us|µs|μs|ms|s|m|h)")
_DURATION_SCALE = {
    "ns": Decimal(1), "us": Decimal(1_000), "µs": Decimal(1_000), "μs": Decimal(1_000),
    "ms": Decimal(1_000_000), "s": Decimal(1_000_000_000),
    "m": Decimal(60_000_000_000), "h": Decimal(3_600_000_000_000),
}
_MIN_DURATION_NS = -(1 << 63)
_MAX_DURATION_NS = (1 << 63) - 1


@dataclass(frozen=True, order=True)
class Duration:
    """A Go-compatible duration retaining signed int64 nanosecond precision."""

    nanoseconds: int

    def __post_init__(self) -> None:
        if isinstance(self.nanoseconds, bool) or not isinstance(self.nanoseconds, int):
            raise TypeError("duration nanoseconds must be an integer")
        if not (_MIN_DURATION_NS <= self.nanoseconds <= _MAX_DURATION_NS):
            raise ValueError("duration exceeds signed int64 nanoseconds")

    def to_timedelta(self) -> dt.timedelta:
        if self.nanoseconds % 1000:
            raise ValueError("duration is below Python timedelta precision")
        return dt.timedelta(microseconds=self.nanoseconds // 1000)

    def __str__(self) -> str:
        return _format_duration_ns(self.nanoseconds)


def decode_value(annotation: Any, value: Any) -> Any:
    """Decode one JSON node without Python-style type coercion."""
    core = _core(annotation)
    origin = get_origin(core)
    args = get_args(core)
    if value is None:
        return TypeAdapter(annotation).validate_python(None, strict=True)
    if core is bytes:
        if not isinstance(value, str):
            raise TypeError("expected base64 string")
        try:
            decoded: Any = base64.b64decode(value, validate=True)
        except (ValueError, binascii.Error) as error:
            raise ValueError("invalid base64") from error
        if base64.b64encode(decoded).decode("ascii") != value:
            raise ValueError("base64 is not canonical")
    elif core in (dt.timedelta, Duration):
        duration = _parse_duration(value)
        decoded = duration.to_timedelta() if core is dt.timedelta else duration
    elif origin in (list, set, frozenset):
        if not isinstance(value, list):
            raise TypeError("expected array")
        decoded = origin(decode_value(args[0], item) for item in value)
    elif origin is tuple:
        if not isinstance(value, list):
            raise TypeError("expected array")
        if len(args) == 2 and args[1] is Ellipsis:
            decoded = tuple(decode_value(args[0], item) for item in value)
        else:
            if len(value) != len(args):
                raise ValueError("wrong array length")
            decoded = tuple(decode_value(item_type, item) for item_type, item in zip(args, value))
    elif origin in (dict, Mapping):
        if not isinstance(value, dict) or any(not isinstance(key, str) for key in value):
            raise TypeError("expected string-keyed object")
        decoded = {key: decode_value(args[1], item) for key, item in value.items()}
    elif origin in (Union, types.UnionType):
        failures: list[Exception] = []
        for member in args:
            try:
                return TypeAdapter(annotation).validate_python(decode_value(member, value), strict=True)
            except (TypeError, ValueError) as error:
                failures.append(error)
        raise ValueError("value does not match union") from failures[-1]
    elif isinstance(core, type) and issubclass(core, BaseModel):
        model_type = core
        if not isinstance(value, dict):
            raise TypeError("expected object")
        decoded_fields: dict[str, Any] = {}
        for name, field in model_type.model_fields.items():
            wire_name = field.serialization_alias or field.alias or name
            input_name = field.validation_alias if isinstance(field.validation_alias, str) else field.alias or name
            if wire_name in value:
                nested = Annotated[(field.annotation, *field.metadata)] if field.metadata else field.annotation
                decoded_fields[input_name] = decode_value(nested, value[wire_name])
        if set(value) - {
            (field.serialization_alias or field.alias or name)
            for name, field in model_type.model_fields.items()
        }:
            raise ValueError("unknown nested field")
        decoded = model_type.model_validate(decoded_fields, strict=True)
    else:
        # validate_json is important here: JSON integers remain strict integers
        # while strings cannot be coerced into numerics or booleans.
        import json
        decoded = TypeAdapter(annotation).validate_json(
            json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False),
            strict=True,
        )
        if core is int and not (-(1 << 63) <= decoded <= (1 << 63) - 1):
            raise ValueError("integer is outside the portable int64 range")
        return decoded
    return TypeAdapter(annotation).validate_python(decoded, strict=True)


def encode_value(annotation: Any, value: Any) -> Any:
    """Encode bytes/durations recursively with the cross-SDK representations."""
    core = _core(annotation)
    origin = get_origin(core)
    args = get_args(core)
    if value is None:
        return None
    if core is bytes:
        return base64.b64encode(value).decode("ascii")
    if core is dt.timedelta:
        return _format_duration_ns(_timedelta_nanoseconds(value))
    if core is Duration:
        return _format_duration_ns(value.nanoseconds)
    if origin in (list, set, frozenset):
        return [encode_value(args[0], item) for item in value]
    if origin is tuple:
        item_types = [args[0]] * len(value) if len(args) == 2 and args[1] is Ellipsis else args
        return [encode_value(item_type, item) for item_type, item in zip(item_types, value)]
    if origin in (dict, Mapping):
        return {key: encode_value(args[1], item) for key, item in value.items()}
    if origin in (Union, types.UnionType):
        for member in args:
            try:
                TypeAdapter(member).validate_python(value, strict=True)
                return encode_value(member, value)
            except (TypeError, ValueError):
                continue
    if isinstance(value, BaseModel):
        output: dict[str, Any] = {}
        for name, field in value.__class__.model_fields.items():
            wire_name = field.serialization_alias or field.alias or name
            nested = Annotated[(field.annotation, *field.metadata)] if field.metadata else field.annotation
            output[wire_name] = encode_value(nested, getattr(value, name))
        return output
    return TypeAdapter(annotation).dump_python(value, mode="json", round_trip=True)


def _core(annotation: Any) -> Any:
    return get_args(annotation)[0] if get_origin(annotation) is Annotated else annotation


def _parse_duration(value: Any) -> Duration:
    if not isinstance(value, str) or not value:
        raise TypeError("expected Go duration string")
    if value == "0":
        return Duration(0)
    sign = 1
    if value.startswith(("+", "-")):
        sign = -1 if value[0] == "-" else 1
        value = value[1:]
    if not value:
        raise ValueError("invalid duration")
    index, total = 0, Decimal(0)
    try:
        while index < len(value):
            match = _DURATION_PART.match(value, index)
            if match is None:
                raise ValueError("invalid duration")
            total += Decimal(match.group(1)) * _DURATION_SCALE[match.group(2)]
            index = match.end()
    except InvalidOperation as error:
        raise ValueError("invalid duration") from error
    if total != total.to_integral_value():
        raise ValueError("duration is below nanosecond precision")
    nanoseconds = sign * int(total)
    return Duration(nanoseconds)


def _timedelta_nanoseconds(value: dt.timedelta) -> int:
    nanoseconds = (
        ((value.days * 86400 + value.seconds) * 1_000_000) + value.microseconds
    ) * 1000
    if not (_MIN_DURATION_NS <= nanoseconds <= _MAX_DURATION_NS):
        raise ValueError("duration exceeds signed int64 nanoseconds")
    return nanoseconds


def _format_duration_ns(nanoseconds: int) -> str:
    Duration(nanoseconds)
    if nanoseconds == 0:
        return "0s"
    sign = "-" if nanoseconds < 0 else ""
    value = abs(nanoseconds)
    if value < 1_000:
        return f"{sign}{value}ns"
    if value < 1_000_000:
        return sign + _format_decimal_unit(value, 1_000, 3, "µs")
    if value < 1_000_000_000:
        return sign + _format_decimal_unit(value, 1_000_000, 6, "ms")

    hours, remainder = divmod(value, 3_600_000_000_000)
    minutes, remainder = divmod(remainder, 60_000_000_000)
    seconds, fraction = divmod(remainder, 1_000_000_000)
    parts: list[str] = []
    if hours:
        parts.append(f"{hours}h")
    if hours or minutes:
        parts.append(f"{minutes}m")
    rendered_seconds = str(seconds)
    if fraction:
        rendered_seconds += "." + f"{fraction:09d}".rstrip("0")
    parts.append(rendered_seconds + "s")
    return sign + "".join(parts)


def _format_decimal_unit(value: int, scale: int, precision: int, suffix: str) -> str:
    whole, remainder = divmod(value, scale)
    rendered = str(whole)
    if remainder:
        rendered += "." + f"{remainder:0{precision}d}".rstrip("0")
    return rendered + suffix
