# kms_paramstore — Python SDK

Python client for the KMS parameter store and secret management service. It hides
gRPC boilerplate, supports TLS/mTLS, caches reads, redacts secrets in logs and
errors, resolves declarative config fields, and hot-reloads parameters over the
watch stream.

Requires Python 3.10+.

## Install

```bash
pip install kms-paramstore          # or: pip install -e sdk/python
```

Runtime dependencies: `grpcio` and `protobuf` (>= 6.30). The gRPC stubs are
vendored under `kms_paramstore/_gen/`, so no `protoc` is needed to use the SDK.

## Quick start

```python
from kms_paramstore import Client

with Client("parameter-store.prod.internal:8443", token="<client-token>") as client:
    db_password = client.get_secret("/prod/gradethis/postgres-password")
    print(db_password)            # -> [REDACTED]
    connect(db_password.value)    # explicit access to plaintext bytes

    rate = client.get_parameter("/prod/gradethis/rate-limit")
```

`get_secret` returns a `Secret` that renders as `[REDACTED]` in `str`, `repr`,
f-strings, `%`-formatting, and logging. Plaintext is only reachable through the
explicit `.value` (bytes) / `.string_value` (str) properties.

## TLS / mTLS

```python
from kms_paramstore import Client, mtls_from_files

client = Client(
    "parameter-store.prod.internal:8443",
    token="<client-token>",
    tls=mtls_from_files("client.crt", "client.key", "ca.crt"),
)
```

Use `tls_from_files(ca_cert)` for server-only TLS, `mtls_from_files(cert, key, ca)`
for mutual TLS, or `tls_from_bytes(ca_cert=..., client_cert=..., client_key=...)`
to build credentials from in-memory PEM. `tls` also accepts a
`TLSConfig(ca=..., cert=..., key=...)` dataclass (paths or raw PEM bytes) if you'd
rather hold the sources as data and call `.to_credentials()` yourself. With no
`tls` the channel is insecure (development only).

## Caching

Set `cache_ttl` (seconds) to cache `get_parameter` / `get_secret` reads. Cached
entries are invalidated by writes through the client and by watch events when a
subscription is active.

```python
client = Client("host:8443", token="...", cache_ttl=60)
```

## Declarative config (descriptors)

Declare store-backed fields as class attributes and resolve them all with one
call — the Python equivalent of the Go SDK's `SecretValue` / `ParameterValue` /
`Resolve` idiom.

```python
from kms_paramstore import Client, SecretValue, ParameterValue

class AppConfig:
    stripe_key = SecretValue("/prod/gradethis/stripe-api-key", token="<per-secret-token>")
    openai_key = SecretValue("/prod/gradethis/openai-api-key", env_var="OPENAI_API_KEY")
    rate_limit = ParameterValue("/prod/gradethis/rate-limit", dynamic=True, default="100")

cfg = AppConfig()
client.resolve(cfg)   # walks the object (and nested config objects) concurrently

cfg.stripe_key            # -> a redacting Secret
cfg.stripe_key.value      # -> bytes (explicit access only)
print(cfg.stripe_key)     # -> [REDACTED]
cfg.rate_limit.get()      # -> latest value, hot-reloaded when dynamic
```

`SecretValue` resolves to a `Secret`; `ParameterValue` resolves to a handle with
`.get()` and `.on_change(fn)`.

Resolution order per field: environment override (`env_var`), then a store
fetch, then `default`, otherwise a fail-fast error naming the path.

- `default` is used only when the store affirmatively reports the value **absent**
  (not found). Connectivity/auth errors fail startup, so a process never boots on
  a dev default just because the store was briefly unreachable. Pass
  `Client(..., fallback_to_defaults_on_error=True)` to opt into permissive
  fallback (a user-reviewed decision, matching the Go SDK).
- `resolve` is idempotent; already-resolved fields are left untouched, and each
  config instance holds its own resolved values.

## Hot reload

`ParameterValue(..., dynamic=True)` registers the field on the client's watch
subscription; `.get()` always returns the latest value with no RPC.

```python
cfg.rate_limit.on_change(lambda old, new: pool.resize(int(new)))
```

For lower-level use, subscribe to a path or prefix directly:

```python
def on_event(ev):
    print(ev.type, ev.path, ev.value)

stop = client.watch("/prod/gradethis/*", on_event)
# ... later
stop()
```

The SDK owns the connection lifecycle: it subscribes on startup, acks
heartbeats, reconnects with exponential backoff + jitter (1s base, 60s cap)
resuming from the last seen revision, and reconciles every 5 minutes via a full
sync. Events are applied idempotently by revision. Callbacks run on a single
dedicated dispatch thread with a bounded queue (a full queue drops
notifications, never values; a raising callback is swallowed and logged), so a
slow or failing callback never stalls the stream. Env-var-overridden values are
pinned and do not hot-reload.

## Errors

All SDK errors derive from `ParamStoreError`. gRPC status codes map to
`NotFoundError`, `PermissionDeniedError`, `UnauthenticatedError`, and
`FailedPreconditionError`; other codes surface as a generic `ParamStoreError`.
`ConfigError` signals bad SDK usage (a missing endpoint, an unconfigured
`SecretValue`/`ParameterValue`); `NotInitializedError` is raised when a
declarative field is read before `Client.resolve` has run. No exception (or its
message) ever contains secret plaintext.

## Development

```bash
cd sdk/python
python -m venv .venv && .venv/bin/pip install -e '.[dev]'
.venv/bin/pytest        # runs the suite against an in-process fake server
.venv/bin/mypy          # type-checks kms_paramstore (config in pyproject.toml)
./gen.sh                # regenerate the vendored gRPC stubs from proto/kms/v1/kms.proto
```
