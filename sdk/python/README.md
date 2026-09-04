# kms_paramstore — Python SDK

Python client for the KMS parameter store and secret management service. It hides
gRPC boilerplate, supports TLS/mTLS, caches parameter reads, redacts secrets in logs and
errors, resolves declarative config fields, and hot-reloads parameters over the
watch stream.

Requires Python 3.10+.

## Install

```bash
pip install -e sdk/python
# Published releases are wheel assets on GitHub (replace both versions):
python -m pip install \
  https://github.com/Suhaibinator/kms/releases/download/v0.3.0/kms_paramstore-0.3.0-py3-none-any.whl
```

Runtime dependencies are `grpcio>=1.83.1`, `protobuf>=7.35.1,<8`, and
`pydantic>=2.13,<3`. The gRPC stubs are vendored under
`kms_paramstore/_gen/`, so no `protoc` is needed to use the SDK.

## Quick start

```python
from kms_paramstore import Client, mtls_from_files

with Client(
    "parameter-store.prod.internal:8443",
    tls=mtls_from_files("app.crt", "app.key", "server-ca.crt"),
) as client:
    db_password = client.get_secret("postgres-password")   # relative to the namespace
    print(db_password)            # -> [REDACTED]
    connect(db_password.value)    # explicit access to plaintext bytes

    rate = client.get_parameter("rate-limit")
```

Follow the
[production mTLS onboarding runbook](../../docs/operations.md#connect-a-production-application-with-mtls)
to create the application's namespace and identity. `app.crt`/`app.key` are
the per-application credentials issued by KMS; `server-ca.crt` comes from the
operator and trusts the KMS serving certificate. A namespace-bound identity
discovers its namespace through `WhoAmI`, so no token or explicit namespace is
needed here.

`get_secret` returns a `Secret` that renders as `[REDACTED]` in `str`, `repr`,
f-strings, `%`-formatting, and logging. Plaintext is only reachable through the
explicit `.value` (bytes) / `.string_value` (str) properties.

## Namespaces and keys

A namespace is a fixed `(env, app)` pair, written `"env/app"`. Set it once on the
client; every key you pass is then resolved SDK-side:

- a **relative** key (`"rate-limit"`, `"billing/stripe-key"`) is looked up in the
  client's namespace — interior slashes are just part of the name;
- an **absolute** `"/env/app/key"` addresses another namespace directly (the
  cross-namespace escape hatch).

`namespace` is optional. When omitted, the client discovers it once via `WhoAmI`
from a namespace-bound identity (mTLS cert or token). An unbound identity plus a
relative key raises `NoNamespaceError` — pass `namespace=` or use an absolute
path. `client.who_am_i()` returns the identity the server sees
(`identity`, `kind`, `namespace`, `auth_method`); `name` remains a deprecated
v0.1 alias for `identity`.

## TLS / mTLS

The recommended posture is a **client certificate** minted by the KMS's built-in
CA: it proves possession (a stolen token alone is useless where a namespace
requires mTLS), and the server derives the identity — and thus the namespace —
from the cert, so `token` is optional. `server-ca.crt` must trust the
operator-provided server certificate; it is not the built-in client CA shown by
`admin ca show`.

```python
from kms_paramstore import Client, mtls_from_files

# Cert-only: no token, namespace discovered via WhoAmI.
client = Client(
    "parameter-store.prod.internal:8443",
    tls=mtls_from_files("app.crt", "app.key", "server-ca.crt"),
)
```

`token` is still accepted (and sent alongside a cert when both are present); it is
required only for token-method identities, and admitted only where the namespace's
`allowed_auth_methods` includes `"token"`. Use `tls_from_files(ca_cert)` for
server-only TLS, `mtls_from_files(cert, key, ca)` for mutual TLS, or
`tls_from_bytes(ca_cert=..., client_cert=..., client_key=...)` to build credentials
from in-memory PEM. Pass the resulting `grpc.ChannelCredentials` through `tls=`.

Transport choice is fail-closed: if `tls` is omitted, construction fails unless
you pass a pre-built `channel` or explicitly set `insecure=True`. The latter is
for local development only and must not be used across an untrusted network:

```python
client = Client("localhost:8443", namespace="dev/app", insecure=True)
```

## Caching

Set `cache_ttl` (seconds) to cache `get_parameter` reads. Secret plaintext is
never cached. Parameter entries are invalidated by writes through the client
and by watch events when a subscription is active.

```python
client = Client(
    "host:8443",
    token="...",
    tls=tls_from_files("server-ca.crt"),
    cache_ttl=60,
)
```

## Declarative config (descriptors)

Declare store-backed fields as class attributes and resolve them all with one
call — the Python equivalent of the Go SDK's `SecretValue` / `ParameterValue` /
`Resolve` idiom.

```python
import os

from kms_paramstore import Client, SecretValue, ParameterValue

class AppConfig:
    stripe_key = SecretValue(
        "stripe-api-key",
        token="<per-secret-access-token>",
        bind_key=os.environ["STRIPE_KMS_BINDING_KEY"],
    )
    openai_key = SecretValue("openai-api-key", env_var="OPENAI_API_KEY")
    rate_limit = ParameterValue("rate-limit", default="100")       # hot-reloads
    log_format = ParameterValue("log-format", static=True)         # boot-time only

cfg = AppConfig()
client.resolve(cfg)   # walks the object (and nested config objects) concurrently

cfg.stripe_key            # -> a redacting Secret
cfg.stripe_key.value      # -> bytes (explicit access only)
print(cfg.stripe_key)     # -> [REDACTED]
cfg.rate_limit.get()      # -> latest value, hot-reloaded unless static=True
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

**Hot reload is on by default.** A `ParameterValue` tracks the client's watch
subscription; `.get()` always returns the latest value with no RPC, and updates
propagate to every subscribed process. This is a change from earlier versions,
where the field had to opt in with `dynamic=True` — that keyword is gone.

Pass `static=True` for a boot-time-only read (no subscription):

```python
port = ParameterValue("listen-port", static=True)   # read once at resolve
```

The **namespace `(env, app)` is the unit of subscription.** All non-static
fields in a namespace share that namespace's **single** subscription on the
shared stream, so many hot-reloading fields cost one registration.

```python
cfg.rate_limit.on_change(lambda old, new: pool.resize(int(new)))
```

For lower-level use, watch the client's whole namespace. `watch` takes **no
key pattern** — it fires for every change in the namespace, and an app that
cares about only some keys filters inside the callback:

```python
def on_event(ev):
    if not ev.key.startswith("billing/"):
        return                                        # this app's own convention
    print(ev.type, ev.namespace, ev.key, ev.value)    # ev.path -> "/env/app/key"

stop = client.watch(on_event)                         # the client's namespace
# client.watch_namespace("other/svc", on_event)       # another authorized namespace
# ... later
stop()
```

The SDK owns the connection lifecycle: it subscribes on startup, acks
heartbeats, reconnects with exponential backoff + jitter (1s base, 60s cap)
resuming from the last seen revision, and reconciles every 5 minutes by listing
the subscribed namespace. Enumeration is capped at 1,000 pages; if listing
fails or the cap is reached with more pages pending, fetched values are applied
but deletions are not inferred. Events are applied idempotently by revision.
Callbacks run serially on a single dedicated dispatch thread with a bounded
queue (a full queue drops notifications, never values; an exception is logged),
so a slow callback does not stall the stream but does delay later callbacks.
Env-var-overridden values are pinned and do not hot-reload.

## Atomic configuration releases

Use the synchronous, thread-safe release loader when related values must be
resolved and installed together. Asyncio applications use `AsyncReleaseLoader`
with `AsyncReleaseLoaderConfig`; it owns independent event-loop state and an
async gRPC stream.

```python
import os

from kms_paramstore import ReleaseLoader, ReleaseLoaderConfig

loader = ReleaseLoader(client, ReleaseLoaderConfig(
    name="runtime",
    secret_token_provider=lambda alias, path: local_tokens.get(alias),
    binding_keys={"openai_api_key": os.environ["OPENAI_KMS_BINDING_KEY"]},
    validate_manifest=lambda cancel, manifest: validate_contract(manifest),
))

def prepare(cancel, snapshot):
    return decode_validate_and_prepare(cancel, snapshot)

loader.run(prepare)  # blocks; call loader.stop() from another thread to stop
```

Snapshots are frozen/redacting and expose release version, activation
revision, digest, schema pin, exact entries, parameters, and `Secret` values by
stable alias. A prepared object's `commit()` must be infallible and normally
performs an atomic reference swap; `abort()` releases stale or failed prepared
work. Startup fails until one release applies. Later outages and rejections
retain the last-known-good state. Manifest validation runs before resource
fetches and credential lookup. Protection is live exact-version metadata, not a
release-entry flag: access tokens come from `secret_token_provider`, while
binding keys come from the defensive-copied alias map. Missing credentials
reject the whole candidate as `token_unavailable`; wrong credentials reject it
as `resolution_failed`. `ClassifiedReleaseError` propagates an allow-listed,
value-free rejection category (including `restart_required`) without sending
its local message. Applied acknowledgements can carry a bounded divergence
count from a prepared object's optional `release_divergence()` method.

`run_typed_release` provides an explicit no-reflection decode step. See
[`../../docs/sdk-python.md`](../../docs/sdk-python.md#atomic-release-loading)
for lifecycle, cancellation, token-provider, and status details.

## Generated managed configuration

The optional Pydantic v2 layer generates a typed store, Draft 2020-12 schema,
and release contract; it also provides atomic snapshots, hot/restart policy,
defaults export, and value-free defaults verification. See
[`MANAGED_CONFIG.md`](MANAGED_CONFIG.md). Its generator rejects schemas larger
than 256 KiB; the KMS server and Go generator permit up to 1 MiB.

## Errors

All SDK errors derive from `ParamStoreError`. gRPC status codes map to
`NotFoundError`, `PermissionDeniedError`, `UnauthenticatedError`, and
`FailedPreconditionError`; other codes surface as a generic `ParamStoreError`.
`ConfigError` signals bad SDK usage (a missing endpoint, a malformed namespace,
an unconfigured `SecretValue`/`ParameterValue`); its subclass `NoNamespaceError`
is raised when a relative key is used on a client with no namespace (unbound
identity and no `namespace=`). `NotInitializedError` is raised when a declarative
field is read before `Client.resolve` has run. No exception (or its message) ever
contains secret plaintext.

## Development

```bash
cd sdk/python
python -m venv .venv && .venv/bin/pip install -e '.[dev]'
.venv/bin/pytest        # runs the suite against an in-process fake server
.venv/bin/mypy          # type-checks kms_paramstore (config in pyproject.toml)
./gen.sh                # regenerate the vendored gRPC stubs from proto/kms/v1/kms.proto
```
