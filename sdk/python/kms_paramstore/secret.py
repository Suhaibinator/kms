"""The redacting :class:`Secret` value type."""

from __future__ import annotations

__all__ = ["Secret", "REDACTED", "new_secret"]

# What every secret-bearing type prints instead of plaintext.
REDACTED = "[REDACTED]"


class Secret:
    """Holds secret plaintext together with non-sensitive metadata.

    ``Secret`` is deliberately hard to leak: ``repr``, ``str``, and ``format``
    all emit ``"[REDACTED]"``, so it is safe to pass to ``print``, ``logging``,
    f-strings, or ``%``-formatting. Plaintext is only reachable through the
    explicit :meth:`value` / :meth:`string_value` accessors.
    """

    __slots__ = ("_value", "_env", "_app", "_key", "_version", "_content_type")

    def __init__(
        self,
        value: bytes = b"",
        *,
        env: str = "",
        app: str = "",
        key: str = "",
        version: int = 0,
        content_type: str = "",
    ) -> None:
        self._value = bytes(value)
        self._env = env
        self._app = app
        self._key = key
        self._version = version
        self._content_type = content_type

    # --- plaintext accessors (the only way to read the secret) -------------

    @property
    def value(self) -> bytes:
        """The raw secret plaintext as bytes. The only way to read it."""
        return self._value

    @property
    def string_value(self) -> str:
        """The secret plaintext decoded to ``str`` (UTF-8)."""
        return self._value.decode("utf-8")

    # --- non-sensitive metadata --------------------------------------------

    @property
    def env(self) -> str:
        return self._env

    @property
    def app(self) -> str:
        return self._app

    @property
    def key(self) -> str:
        return self._key

    @property
    def namespace(self) -> str:
        return f"{self._env}/{self._app}"

    @property
    def path(self) -> str:
        """Display path ``/env/app/key`` (or the bare key if the namespace is unset)."""
        if self._env and self._app:
            return f"/{self._env}/{self._app}/{self._key}"
        return self._key

    @property
    def version(self) -> int:
        return self._version

    @property
    def content_type(self) -> str:
        return self._content_type

    def is_empty(self) -> bool:
        return len(self._value) == 0

    def clone(self) -> "Secret":
        """Return an independent wrapper with copied plaintext bytes."""
        return Secret(
            bytes(bytearray(self._value)), env=self._env, app=self._app,
            key=self._key, version=self._version, content_type=self._content_type,
        )

    def __len__(self) -> int:
        return len(self._value)

    def __bool__(self) -> bool:
        return len(self._value) > 0

    # --- redaction ----------------------------------------------------------

    def __repr__(self) -> str:  # used by logging, %r, the REPL
        return REDACTED

    def __str__(self) -> str:  # used by print, str(), f-strings
        return REDACTED

    def __format__(self, spec: str) -> str:  # used by format() / f"{x:...}"
        return REDACTED

    # Equality compares plaintext so tests and rotation checks work, but the
    # comparison never renders the value.
    def __eq__(self, other: object) -> bool:
        if isinstance(other, Secret):
            return self._value == other._value
        return NotImplemented

    def __hash__(self) -> int:  # keep hashability disabled to avoid accidental
        raise TypeError("Secret is unhashable to discourage caching by value")


def new_secret(
    value: bytes,
    *,
    env: str = "",
    app: str = "",
    key: str = "",
    version: int = 0,
    content_type: str = "",
) -> Secret:
    """Wrap plaintext in a :class:`Secret` (mainly for tests and tooling)."""
    return Secret(value, env=env, app=app, key=key, version=version, content_type=content_type)
