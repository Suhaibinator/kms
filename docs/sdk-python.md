# Python SDK (`kms_paramstore`)

The Python SDK mirrors the Go SDK's ergonomics ([`sdk-go.md`](sdk-go.md)):
direct reads (`get_parameter`/`get_secret`), declarative config fields
(`SecretValue`/`ParameterValue`) resolved with one `client.resolve(cfg)`
call, and hot reload of non-secret parameters over the `Subscribe` stream.
Secret plaintext never appears in logs, errors, or the default string
representation of any type in this package — reaching it always requires an
explicit property access.

Distribution name: `kms-paramstore` (PyPI). Import name: `kms_paramstore`.
Source: `sdk/python/kms_paramstore/`. Requires Python 3.10+.

This document describes the public API as implemented and verified: every
symbol below was checked against `sdk/python/kms_paramstore/*.py`, the
package's own test suite (`sdk/python/tests/`, 40 tests, all passing), and a
live round trip against a running `parameter-store` server (put/get a
secret and a parameter, declarative `resolve`, redaction, and hot reload).
For the wire-level contract, see `proto/kms/v1/kms.proto` and
[`http-api.md`](http-api.md); for path/namespace conventions, see
[`migration.md`](migration.md).

## Installing

```bash
pip install kms-paramstore
# or, from a checkout:
pip install -e sdk/python
```

Runtime dependencies are `grpcio` and `protobuf` (>= 6.30); the generated
gRPC stubs are vendored under `kms_paramstore/_gen/`, so `protoc` is not
needed to use the SDK.

## Connecting

```python
from kms_paramstore import Client

with Client("parameter-store.prod.internal:8443", token="<client-token>") as client:
    db_password = client.get_secret("/prod/gradethis/postgres-password")
    print(db_password)           # [REDACTED]
    connect(db_password.value)   # explicit access to plaintext (bytes)
```

`Client` is used as a context manager (`__enter__`/`__exit__` call `close`),
or constructed and closed manually. `Client.__init__` parameters
(`kms_paramstore/client.py`):

| Parameter | Meaning |
|---|---|
| `endpoint` | `host:port` of the server's gRPC listener. Required unless `channel` is supplied. First positional argument. |
| `token` | Per-client identity token, sent as `authorization: Bearer <token>` on every RPC. Empty is allowed against an unauthenticated/dev server. Second positional argument, or keyword. |
| `tls` | `TLSConfig` (see below) or a raw `grpc.ChannelCredentials`. `None` (default) dials insecure — development only. |
| `cache_ttl` | Seconds to cache `get_parameter`/`get_secret` reads; `0` (default) disables caching. Cache entries are invalidated by writes through the client and, once a subscription is active, by watch events. |
| `timeout` | Default per-RPC deadline in seconds, used when a call doesn't pass its own `timeout`. Defaults to 5.0. Does not apply to the long-lived `Subscribe` stream. |
| `client_name` | Identifies this client in the subscription registry (visible on the frontend's Subscribers page). Defaults to `os.path.basename(sys.argv[0])`. |
| `fallback_to_defaults_on_error` | Controls when a declarative field's `default` is used — see [Declarative config](#declarative-config-secretvalue-and-parametervalue) below. Defaults to `False`. |
| `logger` | A `logging.Logger` for operational messages (paths, env var names, connection state) — never secret plaintext. Defaults to `logging.getLogger("kms_paramstore")`. |
| `channel_options` | Extra gRPC channel options (`list[tuple[str, object]]`), appended after the SDK's own keepalive defaults. |
| `channel` | A pre-built `grpc.Channel` to use directly (mainly for tests); when set, `endpoint`/`tls`/`channel_options` are ignored and the SDK does not own (and will not close) the channel. |

Client-side keepalive pings every 30s with a 10s timeout,
`grpc.keepalive_permit_without_calls=1`. `close()` releases the connection
and stops all background threads (the watch subscription manager and the
callback dispatch thread); it is idempotent.

## Direct reads

```python
value = client.get_parameter("/prod/gradethis/rate-limit")

secret = client.get_secret("/prod/gradethis/stripe-api-key")
# secret prints as [REDACTED]; use secret.value (bytes) or
# secret.string_value (str) for plaintext.
```

Both accept keyword-only options:

- `get_parameter(path, *, version=0, label="", timeout=None)`
- `get_secret(path, *, version=0, label="", secret_token="", timeout=None)`

`version` pins the read to a specific immutable version; `label` reads the
version a label points at (server default is `"current"` when neither is
given). `secret_token` sets the `x-kms-secret-token` metadata, required for
token-protected and client-bound secrets.

`get_secret` returns a `Secret` (`kms_paramstore/secret.py`): an immutable,
`__slots__`-based value type carrying plaintext plus read-only `path`,
`version`, and `content_type` properties. Its `__str__`, `__repr__`, and
`__format__` all return `"[REDACTED]"` regardless of format spec, so it is
safe to pass to `print`, `logging`, f-strings, and `%`-formatting — a
`Secret` is not JSON-serializable at all (`json.dumps` raises `TypeError`
rather than silently leaking). `value` (bytes) and `string_value` (str,
UTF-8-decoded) are the only way to read plaintext. Equality compares
plaintext without rendering it; `Secret` is deliberately unhashable
(`hash(secret)` raises `TypeError`) to discourage accidentally caching by
value. `new_secret(value, *, path="", version=0, content_type="")` wraps
plaintext in a `Secret` directly — mainly useful for tests and tooling.

Writes (mainly for tooling, not typical application code):

```python
result = client.put_parameter("/prod/gradethis/rate-limit", "200",
                               content_type="integer")

result = client.put_secret("/prod/gradethis/stripe-api-key", b"sk_live_...",
                            generate_access_token=True)
# result.access_token is set only when generate_access_token=True was
# passed, and is never retrievable again after this call returns.
```

- `put_parameter(path, value, *, content_type="", metadata_json="", timeout=None) -> PutResult`
- `put_secret(path, value, *, content_type="", metadata_json="", client_bound=False, generate_access_token=False, expires_at_unix_ms=0, secret_token="", timeout=None) -> PutSecretResult` — `value` may be `bytes` or `str` (a `str` is UTF-8-encoded); `client_bound=True` opts into client-bound double wrapping (only honored on first creation); `secret_token` supplies the existing token when updating a token-protected or client-bound secret.
- `list_parameters(prefix="", *, page_size=0, page_token="") -> (list[Parameter], next_page_token)`
- `delete_parameter(path, *, timeout=None) -> int` (returns the revision)
- `get_secret_metadata(path, *, timeout=None) -> SecretInfo` (metadata only, never plaintext)
- `delete_secret(path, *, timeout=None) -> int` (returns the revision)

`PutResult`, `PutSecretResult`, `Parameter`, `SecretInfo`, `SecretVersion`
(`kms_paramstore/models.py`) are plain dataclasses decoupling callers from
the generated protobuf messages.

## Errors

`kms_paramstore/errors.py` maps gRPC status codes to exception types, all
deriving from `ParamStoreError`:

```python
from kms_paramstore import NotFoundError, PermissionDeniedError

try:
    client.get_secret("/prod/missing")
except NotFoundError:
    ...           # path, version, or label does not exist
except PermissionDeniedError:
    ...           # authenticated but not authorized
```

| Exception | Meaning |
|---|---|
| `NotFoundError` | The path/version/label does not exist. |
| `PermissionDeniedError` | Authenticated but not authorized for the path/operation. |
| `UnauthenticatedError` | Missing, invalid, or expired identity token. |
| `FailedPreconditionError` | Well-formed request the server state forbids (e.g. a `client_bound` mode mismatch, a disabled version). |
| `NotInitializedError` | A declarative `SecretValue` field was read before `resolve` ran. |
| `ConfigError` | The SDK itself was misconfigured (missing endpoint, no key/env_var/default on a declarative field, ...). |

gRPC status codes outside this mapped set surface as a generic
`ParamStoreError` carrying the status code name and message. No exception,
or its message, ever contains secret plaintext — the SDK maps errors using
only the gRPC status code and the server's own (non-secret) status message.

## TLS / mTLS

```python
from kms_paramstore import Client, TLSConfig, tls_from_files, mtls_from_files

# Dataclass form, accepts file paths or raw PEM bytes:
client = Client("host:8443", token="...", tls=TLSConfig(ca="ca.crt"))
client = Client("host:8443", token="...",
                 tls=TLSConfig(ca="ca.crt", cert="client.crt", key="client.key"))

# Function form, equivalent, returns grpc.ChannelCredentials directly:
creds = mtls_from_files("client.crt", "client.key", "ca.crt")
client = Client("host:8443", token="...", tls=creds)
```

`tls=` accepts either a `TLSConfig` (`ca`/`cert`/`key`, each a file path or
raw `bytes`; `to_credentials()` builds the `grpc.ChannelCredentials`) or a
`grpc.ChannelCredentials` built directly with `tls_from_files(ca_cert)`
(server-only TLS), `mtls_from_files(client_cert, client_key, ca_cert)`
(mutual TLS), or `tls_from_bytes(ca_cert=None, client_cert=None,
client_key=None)` (in-memory PEM bytes, e.g. from a secrets manager rather
than a file). With no `tls=`, the channel is insecure — development only.

## Declarative config: `SecretValue` and `ParameterValue`

Declare store-backed fields directly as class attributes
(`kms_paramstore/values.py`), Python descriptor style:

```python
from kms_paramstore import Client, SecretValue, ParameterValue

class AppConfig:
    stripe_key = SecretValue("/prod/gradethis/stripe-api-key",
                              token="<per-secret-token>")         # per-secret token, if required
    openai_key = SecretValue("/prod/gradethis/openai-api-key",
                              env_var="OPENAI_API_KEY")            # env override still wins
    rate_limit = ParameterValue("/prod/gradethis/rate-limit",
                                 default="100", dynamic=True)      # hot-reloads

cfg = AppConfig()
client.resolve(cfg)   # walks the object (and nested config objects) concurrently

cfg.stripe_key              # -> a Secret (redacts as [REDACTED])
cfg.stripe_key.value        # -> bytes plaintext (explicit access only)
cfg.rate_limit.get()        # -> latest value, hot-reloaded when dynamic
```

**`SecretValue(key="", *, token=None, env_var=None, default=None)`** —
resolves to a `Secret` object: accessing the attribute on a resolved
instance (`cfg.stripe_key`) returns the `Secret` directly (so
`isinstance(cfg.stripe_key, Secret)` is `True`), which redacts exactly like
any other `Secret`. Accessing it before `resolve` has run raises
`NotInitializedError`.

**`ParameterValue(key="", *, env_var=None, default=None, dynamic=False)`**
— resolves to a `ParameterHandle` (`.get()`, `.value` property, `.on_change()`,
`.initialized` property — see [Hot reload](#hot-reload) below).

**Resolution order** (identical for both types, per field): `env_var` (if
set and the named environment variable is non-empty) → store fetch → the
field's `default` → otherwise `ConfigError` naming the missing path.

`default` is used only when the store fetch fails with an **affirmative
not-found** (`NotFoundError`), unless the client was constructed with
`fallback_to_defaults_on_error=True` — a connectivity error, timeout, or
auth failure fails resolution outright by default, so a process cannot
silently boot on a dev default merely because the store was briefly
unreachable. This is stricter than the Go SDK, which falls back to
`Default` on any fetch error; set `fallback_to_defaults_on_error=True` on
the `Client` to opt into the permissive (Go-like) behavior.

### `Client.resolve`: one call for a whole object graph

```python
client.resolve(cfg)
```

`resolve(config_obj, *, timeout=None)` walks `config_obj`'s class hierarchy
(via `__mro__`) for `SecretValue`/`ParameterValue` descriptors, recurses
into any instance attribute whose type also declares such descriptors
(nested config objects), and initializes every field found **concurrently**
via a thread pool (`ThreadPoolExecutor(max_workers=min(16, n))`), since the
service exposes no batch-read RPC. It raises the first error encountered
after every in-flight fetch has settled; fields that already resolved
successfully stay resolved. `resolve` is idempotent per field — re-calling
it on an already-initialized field is a no-op, even if the stored value has
since changed (see `test_init_is_idempotent` in the SDK's own test suite).

There is no separate explicit per-field `.init(client)` call in this SDK
(unlike the Go SDK's `SecretValue.Init(client)`) — `client.resolve(cfg)` is
the only supported resolution path.

## Hot reload

`ParameterValue(..., dynamic=True)` fields register on the client's shared
`Subscribe` stream. The SDK owns the whole connection lifecycle — connect,
send registration, apply snapshot/changes, ack heartbeats, reconnect with
jittered exponential backoff (base 1s, cap 60s) on stream loss, resume from
the last-applied revision, and a periodic full reconciliation poll (default
every 5 minutes) as a safety net. Applications only see values and
callbacks.

```python
# 1. Live handle — always the latest value, no RPC.
rate_limit = cfg.rate_limit.get()      # equivalently: cfg.rate_limit.value

# 2. Change callback — for values that need explicit reaction, fired on a
#    dedicated dispatch thread.
cfg.rate_limit.on_change(lambda old, new: pool.resize(int(new)))

# 3. Namespace-level watch for advanced use.
def on_event(ev):
    print(ev.type, ev.path, ev.value)

stop = client.watch("/prod/gradethis/*", on_event)
# ... later
stop()
```

`ParameterHandle` (returned by accessing a resolved `ParameterValue` field):
`get()` and the `value` property are equivalent and both return `""` before
resolution; `on_change(fn)` registers `fn(old, new)`, called on the shared
dispatch thread — a callback on a non-dynamic or env-pinned field is
accepted but never fires; `initialized` reports whether resolution has run.
An env-var override pins the value (it never hot-reloads), and `resolve`
logs that hot reload was skipped for a `dynamic=True` field resolved from
an environment variable.

`Client.watch(pattern, callback) -> stop`: subscribes to an exact path or a
`"*"`-suffixed prefix pattern (e.g. `"/prod/gradethis/*"`); `callback`
receives an `Event` (`type: EventType`, `path`, `value` — populated for
puts —, `version`, `revision`, `change_type`) for every matching change,
dispatched on the shared callback thread so a slow or raising callback
cannot stall the stream or other callbacks. `EventType` is `PUT`, `DELETE`,
or `SECRET_CHANGE` (metadata-only; re-fetch via `get_secret` if needed —
plaintext is never pushed over the stream). The returned `stop` function
unregisters the watcher.

`Client.current_revision` is a read-only property: the last revision the
active subscription has applied, or `0` if nothing is being watched.

## What this document does not cover

- The wire-level gRPC contract — see `proto/kms/v1/kms.proto`.
- Server-side configuration, deployment, and the CLI — see
  [`operations.md`](operations.md).
- The encryption and authorization model — see [`security.md`](security.md).
- The SDK's own internal test fixtures (`sdk/python/tests/_fake_server.py`)
  are private test infrastructure, not a public testing utility — unlike
  the Go SDK's `paramstoretest` package, they are not exported by
  `kms_paramstore` and are not part of its public API.
