# Python SDK (`kms_paramstore`)

The Python SDK mirrors the Go SDK's ergonomics ([`sdk-go.md`](sdk-go.md)): a
**namespaced** client that resolves relative keys, direct reads
(`get_parameter`/`get_secret`), declarative config fields
(`SecretValue`/`ParameterValue`) resolved with one `client.resolve(cfg)`
call, and hot reload of non-secret parameters over the `Subscribe` stream.
Secret plaintext never appears in logs, errors, or the default string
representation of any type in this package — reaching it always requires an
explicit property access.

Distribution name: `kms-paramstore`. Import name: `kms_paramstore`. Source:
`sdk/python/kms_paramstore/`. Requires Python 3.10+. The documented install
path below does not assume a published package release.

This document describes the public API as implemented in
`sdk/python/kms_paramstore/*.py`. For the wire-level contract, see
`proto/kms/v1/kms.proto` and [`http-api.md`](http-api.md); for the
namespace/key model, see [`migration.md`](migration.md).

## Installing

```bash
pip install -e sdk/python
# For a published release, the package name is:
# pip install kms-paramstore
```

Runtime dependencies are `grpcio>=1.81.1` and `protobuf>=6.33.5,<7`; the generated
gRPC stubs are vendored under `kms_paramstore/_gen/`, so `protoc` is not
needed to use the SDK.

## Namespaces and keys

A namespace is a fixed `(env, app)` pair, written as the string `"env/app"`
(for example `"prod/gradethis"`). The client is scoped to one namespace, and
every key you pass is resolved SDK-side:

- a **relative** key — `"rate-limit"`, `"billing/stripe-key"` — is looked up
  in the client's namespace. Interior slashes are just part of the name, not
  namespace structure.
- an **absolute** display path — `"/env/app/key"` — addresses another
  namespace directly. This is the cross-namespace escape hatch (subject to
  policy on the server).

The server never parses a path string; the SDK splits an absolute path into
explicit `(env, app, key)` fields before it hits the wire, and sends the
namespace as structured fields for a relative key. **Key resolution is done
client-side; the server is authoritative on naming.** The SDK checks only
structure — that a namespace string has both halves and an absolute path has
three segments — and defers the character set of `env`, `app`, and keys to
the server, so it will not reject a name that a newer server would accept.
This matches the Go SDK.

The namespace can be set explicitly or discovered:

```python
# Explicit — no discovery round trip.
Client("host:8443", namespace="prod/gradethis", token="...")

# Discovered — the client calls WhoAmI once and caches the result.
Client("host:8443", tls=mtls_from_files("app.crt", "app.key", "server-ca.crt"))
```

When `namespace` is omitted, the client discovers its namespace from the
identity on first use, via a single `WhoAmI` call (see
[Identity and discovery](#identity-and-discovery)). An **unbound** identity
(one not tied to a namespace) combined with a **relative** key raises
`NoNamespaceError` — the message names the offending key. Give the client a
`namespace=`, or use an absolute `/env/app/key`.

## Connecting

```python
from kms_paramstore import Client, mtls_from_files

# Recommended: cert-only identity. No token; the namespace is discovered.
with Client("parameter-store.prod.internal:8443",
            tls=mtls_from_files("app.crt", "app.key", "server-ca.crt")) as client:
    db_password = client.get_secret("postgres-password")  # relative to the namespace
    print(db_password)           # [REDACTED]
    connect(db_password.value)   # explicit access to plaintext (bytes)
```

`Client` is used as a context manager (`__enter__`/`__exit__` call `close`),
or constructed and closed manually. `Client.__init__` parameters
(`kms_paramstore/client.py`):

| Parameter | Meaning |
|---|---|
| `endpoint` | `host:port` of the server's gRPC listener. Required unless `channel` is supplied. First positional argument. |
| `token` | Per-client identity token, sent as `authorization: Bearer <token>` on every RPC. **Optional** — an mTLS client certificate authenticates on its own, and a dev server may need no credential at all. When both a token and a cert are present, the token is still sent. Second positional argument, or keyword. |
| `namespace` | The client's namespace as `"env/app"`. Keyword-only, `None` by default. When `None`, the namespace is discovered from the identity via `WhoAmI` on first use. A malformed string fails fast with `ConfigError` at construction. |
| `tls` | `TLSConfig` (see below) or a raw `grpc.ChannelCredentials`. `None` (default) dials insecure — development only. |
| `cache_ttl` | Seconds to cache `get_parameter`/`get_secret` reads; `0` (default) disables caching. Cache entries are invalidated by writes through the client and, once a subscription is active, by watch events. |
| `timeout` | Default per-RPC deadline in seconds, used when a call doesn't pass its own `timeout`. Defaults to 5.0. Does not apply to the long-lived `Subscribe` stream. |
| `client_name` | Identifies this client in the subscription registry (visible on the frontend's Subscribers page). Defaults to `os.path.basename(sys.argv[0])`. |
| `fallback_to_defaults_on_error` | Controls when a declarative field's `default` is used — see [Declarative config](#declarative-config-secretvalue-and-parametervalue) below. Defaults to `False`. |
| `logger` | A `logging.Logger` for operational messages (keys, env var names, connection state) — never secret plaintext. Defaults to `logging.getLogger("kms_paramstore")`. |
| `channel_options` | Extra gRPC channel options (`list[tuple[str, object]]`). They are passed before the SDK's fixed keepalive entries; avoid supplying duplicate keepalive keys because gRPC's duplicate-option precedence is not an SDK contract. |
| `channel` | A pre-built `grpc.Channel` to use directly (mainly for tests); when set, `endpoint`/`tls`/`channel_options` are ignored and the SDK does not own (and will not close) the channel. |

Client-side keepalive pings every 30s with a 10s timeout,
`grpc.keepalive_permit_without_calls=1`. `close()` releases the connection
and stops all background threads (the watch subscription manager and the
callback dispatch thread); it is idempotent.

## Identity and discovery

The recommended posture (plan §7) is a **client certificate** minted by the
KMS's built-in CA: it proves possession — a stolen bearer token alone is
useless where a namespace requires mTLS — and the server derives the
identity, and thus the namespace, from the certificate. Build the transport
with `mtls_from_files(...)` (see [TLS / mTLS](#tls--mtls)) and leave `token`
unset. A bearer token is still supported, and is admitted only where the
namespace's `allowed_auth_methods` includes `"token"`.

```python
me = client.who_am_i()
me.name          # identity name
me.kind          # "client" | "admin"
me.namespace     # "env/app", or None when the identity is unbound
me.auth_method   # "mtls" | "token" | ""
```

`who_am_i(*, timeout=None) -> WhoAmI` returns the identity the server sees
for this connection. It is callable by any authenticated identity (no policy
check) and is the SDK's namespace-discovery mechanism. `WhoAmI`
(`kms_paramstore/client.py`) is a small dataclass with the fields shown
above. Discovery happens at most once per client: the result is cached for
the client's lifetime, and an explicitly configured `namespace=` skips the
call entirely.

## Direct reads

```python
value = client.get_parameter("rate-limit")               # relative to the namespace
secret = client.get_secret("stripe-api-key")             # relative
other = client.get_parameter("/staging/billing/rate")    # absolute, cross-namespace
# secret prints as [REDACTED]; use secret.value (bytes) or
# secret.string_value (str) for plaintext.
```

Both accept keyword-only options:

- `get_parameter(key, *, version=0, label="", timeout=None)`
- `get_secret(key, *, version=0, label="", secret_token="", timeout=None)`

`version` pins the read to a specific immutable version; `label` reads the
version a label points at (server default is `"current"` when neither is
given). `secret_token` sets the `x-kms-secret-token` metadata, required for
token-protected and client-bound secrets.

`get_secret` returns a `Secret` (`kms_paramstore/secret.py`): an immutable,
`__slots__`-based value type carrying plaintext plus read-only `env`, `app`,
`key`, `namespace` (`"env/app"`), `path` (`"/env/app/key"`), `version`, and
`content_type` properties. Its `__str__`, `__repr__`, and `__format__` all
return `"[REDACTED]"` regardless of format spec, so it is safe to pass to
`print`, `logging`, f-strings, and `%`-formatting — a `Secret` is not
JSON-serializable at all (`json.dumps` raises `TypeError` rather than
silently leaking). `value` (bytes) and `string_value` (str, UTF-8-decoded)
are the only way to read plaintext. Equality compares plaintext without
rendering it; `Secret` is deliberately unhashable (`hash(secret)` raises
`TypeError`) to discourage accidentally caching by value.
`new_secret(value, *, env="", app="", key="", version=0, content_type="")`
wraps plaintext in a `Secret` directly — mainly useful for tests and
tooling.

Writes (mainly for tooling, not typical application code):

```python
result = client.put_parameter("rate-limit", "200", content_type="integer")

result = client.put_secret("stripe-api-key", b"sk_live_...",
                           generate_access_token=True)
# result.access_token is set only when generate_access_token=True was
# passed, and is never retrievable again after this call returns.
```

- `put_parameter(key, value, *, content_type="", metadata_json="", timeout=None) -> PutResult`
- `put_secret(key, value, *, content_type="", metadata_json="", client_bound=False, generate_access_token=False, expires_at_unix_ms=0, secret_token="", timeout=None) -> PutSecretResult` — `value` may be `bytes` or `str` (a `str` is UTF-8-encoded). A new client-bound secret requires `client_bound=True, generate_access_token=True`; retain the returned one-time token. Updates require `client_bound=True, secret_token=current_token`; also setting `generate_access_token=True` rotates the token for the new version.
- `list_parameters(namespace=None, key_prefix="", *, page_size=0, page_token="") -> (list[Parameter], next_page_token)` — listing is namespace-scoped; `namespace` accepts an `"env/app"` string (or `None` for the client's own namespace) and `key_prefix` filters by relative-key prefix.
- `delete_parameter(key, *, timeout=None) -> int` (returns the revision)
- `get_secret_metadata(key, *, timeout=None) -> SecretInfo` (metadata only, never plaintext)
- `delete_secret(key, *, timeout=None) -> int` (returns the revision)

`PutResult`, `PutSecretResult`, `Parameter`, `SecretInfo`, `SecretVersion`
(`kms_paramstore/models.py`) are plain dataclasses decoupling callers from
the generated protobuf messages. `Parameter` and `SecretInfo` carry explicit
`env`, `app`, and `key` fields, plus `namespace` (`"env/app"`) and `path`
(`"/env/app/key"`) display properties.

## Errors

`kms_paramstore/errors.py` maps gRPC status codes to exception types, all
deriving from `ParamStoreError`:

```python
from kms_paramstore import NotFoundError, PermissionDeniedError

try:
    client.get_secret("missing")
except NotFoundError:
    ...           # key, version, or label does not exist
except PermissionDeniedError:
    ...           # authenticated but not authorized
```

| Exception | Meaning |
|---|---|
| `NotFoundError` | The key/version/label does not exist. |
| `PermissionDeniedError` | Authenticated but not authorized for the resource/operation (includes a namespace that forbids the caller's auth method). |
| `UnauthenticatedError` | Missing, invalid, or expired credential. |
| `FailedPreconditionError` | Well-formed request the server state forbids (e.g. a `client_bound` mode mismatch, a disabled version). |
| `NotInitializedError` | A declarative `SecretValue` field was read before `resolve` ran. |
| `ConfigError` | The SDK itself was misconfigured (missing endpoint, malformed namespace, no key/env_var/default on a declarative field, ...). |
| `NoNamespaceError` | A **subclass of `ConfigError`**: a relative key was used on a client with no namespace (an unbound identity and no `namespace=`). The message names the key. |

gRPC status codes outside this mapped set surface as a generic
`ParamStoreError` carrying the status code name and message. No exception,
or its message, ever contains secret plaintext — the SDK maps errors using
only the gRPC status code and the server's own (non-secret) status message.

## TLS / mTLS

```python
from kms_paramstore import Client, TLSConfig, tls_from_files, mtls_from_files

# Recommended: cert-only mutual TLS, identity + namespace derived from the cert.
client = Client("host:8443",
                tls=mtls_from_files("app.crt", "app.key", "server-ca.crt"))

# Dataclass form, accepts file paths or raw PEM bytes:
client = Client("host:8443", token="...", tls=TLSConfig(ca="server-ca.crt"))
client = Client("host:8443",
                tls=TLSConfig(ca="server-ca.crt", cert="app.crt", key="app.key"))
```

`tls=` accepts either a `TLSConfig` (`ca`/`cert`/`key`, each a file path or
raw `bytes`; `to_credentials()` builds the `grpc.ChannelCredentials`) or a
`grpc.ChannelCredentials` built directly with `tls_from_files(ca_cert)`
(server-only TLS), `mtls_from_files(client_cert, client_key, ca_cert)`
(mutual TLS), or `tls_from_bytes(ca_cert=None, client_cert=None,
client_key=None)` (in-memory PEM bytes, e.g. from a secrets manager rather
than a file). Supplying `cert`/`key` presents a client certificate, which is
the recommended way to authenticate — `token` then becomes optional. The CA
argument must trust the operator-provided **server** certificate; the built-in
client CA returned by `admin ca show` is not a server trust root. With no
`tls=`, the channel is insecure — development only.

## Declarative config: `SecretValue` and `ParameterValue`

Declare store-backed fields directly as class attributes
(`kms_paramstore/values.py`), Python descriptor style. Keys are relative to
the client namespace (or absolute `/env/app/key`):

```python
from kms_paramstore import Client, SecretValue, ParameterValue

class AppConfig:
    stripe_key = SecretValue("stripe-api-key",
                              token="<per-secret-token>")   # per-secret token, if required
    openai_key = SecretValue("openai-api-key",
                              env_var="OPENAI_API_KEY")      # env override still wins
    rate_limit = ParameterValue("rate-limit", default="100") # hot-reloads (default)
    log_format = ParameterValue("log-format", static=True)   # read once at resolve

cfg = AppConfig()
client.resolve(cfg)   # walks the object (and nested config objects) concurrently

cfg.stripe_key              # -> a Secret (redacts as [REDACTED])
cfg.stripe_key.value        # -> bytes plaintext (explicit access only)
cfg.rate_limit.get()        # -> latest value, hot-reloaded
```

**`SecretValue(key="", *, token=None, env_var=None, default=None)`** —
resolves to a `Secret` object: accessing the attribute on a resolved
instance (`cfg.stripe_key`) returns the `Secret` directly (so
`isinstance(cfg.stripe_key, Secret)` is `True`), which redacts exactly like
any other `Secret`. Accessing it before `resolve` has run raises
`NotInitializedError`.

**`ParameterValue(key="", *, env_var=None, default=None, static=False)`** —
resolves to a `ParameterHandle` (`.get()`, `.value` property, `.on_change()`,
`.initialized` property — see [Hot reload](#hot-reload) below).

**Resolution order** (identical for both types, per field): `env_var` (if
set and the named environment variable is non-empty) → store fetch → the
field's `default` → otherwise `ConfigError` naming the missing key. An
`env_var` override resolves without touching the store, so a namespace is
**not** required to satisfy an env-pinned field. A relative key on a client
with no namespace fails with `NoNamespaceError` (naming the key) rather than
falling back to `default`.

`default` is used only when the store fetch fails with an **affirmative
not-found** (`NotFoundError`), unless the client was constructed with
`fallback_to_defaults_on_error=True` — a connectivity error, timeout, or
auth failure fails resolution outright by default, so a process cannot
silently boot on a dev default merely because the store was briefly
unreachable. This matches the Go SDK. Set
`fallback_to_defaults_on_error=True` on the `Client` to opt into permissive
any-error fallback in either SDK.

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

**Hot reload is on by default.** A `ParameterValue` field tracks the client's
shared `Subscribe` stream and always returns the latest value with no RPC;
updates propagate to every subscribed process. This is a change from earlier
versions, where a field had to opt in with `dynamic=True` — **that keyword is
gone**. To read a value once at resolution time and never track changes, pass
`static=True`.

The **namespace `(env, app)` is the unit of subscription.** All non-static
fields in a namespace share that namespace's **one** subscription on the
shared stream, rather than a per-key registration, so many hot-reloading
fields cost a single subscription. A `static=True` field opens no
subscription at all. An `env_var`-pinned field is never registered (it can
never change at runtime), and `resolve` logs that hot reload was skipped for
it.

The SDK owns the whole connection lifecycle — connect, send registration,
apply snapshot/changes, ack heartbeats, reconnect with jittered exponential
backoff (base 1s, cap 60s) on stream loss, resume from the last-applied
revision, and a periodic reconciliation poll (default every 5 minutes) as a
safety net. The poll applies listed values and infers deletions only after a
complete enumeration. It is capped at 1,000 pages; on a list failure or a cap
with another page pending, fetched values are applied but deletion inference is
skipped. Applications only see values and callbacks.

```python
# 1. Live handle — always the latest value, no RPC.
rate_limit = cfg.rate_limit.get()      # equivalently: cfg.rate_limit.value

# 2. Change callback — for values that need explicit reaction, fired on a
#    dedicated dispatch thread.
cfg.rate_limit.on_change(lambda old, new: pool.resize(int(new)))

# 3. Namespace watch for advanced use — fires for EVERY change in the
#    namespace; there is no key pattern. Filter inside the callback.
def on_event(ev):
    if not ev.key.startswith("billing/"):
        return                                          # this app's own convention
    print(ev.type, ev.namespace, ev.key, ev.value)      # ev.path -> "/env/app/key"

stop = client.watch(on_event)                           # the client's namespace
# ... later
stop()
```

`ParameterHandle` (returned by accessing a resolved `ParameterValue` field):
`get()` and the `value` property are equivalent and both return `""` before
resolution; `on_change(fn)` registers `fn(old, new)`, called on the shared
dispatch thread — a callback on a static or env-pinned field is accepted but
never fires; `initialized` reports whether resolution has run.

`Client.watch(callback) -> stop`: subscribes to the client's whole
namespace. It takes **no key pattern** — the namespace is the unit of
subscription, so `callback` fires for **every** change in the namespace; an
application interested in only some keys filters by its own convention inside
the callback (e.g. `if ev.key.startswith("billing/"): ...`). `callback` is
dispatched serially on the shared callback thread. A slow or raising callback
cannot stall the stream, and exceptions are logged, but a slow callback does
delay later callbacks. `Client.watch_namespace(namespace,
callback) -> stop` does the same for another namespace the client is
authorized for (`namespace` is an `"env/app"` string, or `None` for the
client's own). `Event` (`kms_paramstore/watch.py`) carries
`type: EventType`, `namespace` (`"env/app"`), `key` (relative), `value`
(populated for puts), `version`, `revision`, and `change_type`; its `path`
property renders the absolute display path `"/env/app/key"` for logging.
`EventType` is `PUT`, `DELETE`, or `SECRET_CHANGE` (metadata-only; re-fetch
via `get_secret` if needed — plaintext is never pushed over the stream). The
returned `stop` function unregisters the watcher.

`Client.current_revision` is a read-only property: the last revision the
active subscription has applied, or `0` if nothing is being watched.

## Atomic release loading

Use `ReleaseLoader` when related parameter and secret versions must be
prepared and installed as one candidate. It uses the dedicated release stream,
not the ordinary namespace callback queue.

```python
import threading
from kms_paramstore import (
    ReleaseLoader, ReleaseLoaderConfig, ReleaseSnapshot,
)

loader = ReleaseLoader(client, ReleaseLoaderConfig(
    name="runtime",
    reconcile_interval=60.0,       # default
    max_concurrent_fetches=16,     # default; maximum 256
    secret_token_provider=lambda alias, path: local_tokens.get(alias),
))

class PreparedRuntime:
    def __init__(self, next_runtime):
        self.next_runtime = next_runtime

    def commit(self) -> None:
        # Must be infallible; normally an atomic reference swap.
        runtime_ref.set(self.next_runtime)

    def abort(self) -> None:
        self.next_runtime.close()

def prepare(cancel: threading.Event, snapshot: ReleaseSnapshot) -> PreparedRuntime:
    limits_json = snapshot.parameters["rate_limits"]
    password = snapshot.secrets["db_password"]
    # Decode and validate explicitly. Secret plaintext is available only via
    # password.value / password.string_value.
    return PreparedRuntime(build_runtime(cancel, limits_json, password.value))

loader.run(prepare)  # blocks until loader.stop() or the optional stop_event
```

`ReleaseLoaderConfig.name` is required. `namespace=None` uses the client's
namespace/discovery; set a namespace string or `NamespaceRef` only when a
loader should be explicitly scoped. `client_name=None` uses the client's
name. A process-wide UUID is used as `instance_id` and reused across
reconnects unless one is supplied. Reconciliation defaults to 60 seconds,
resolution concurrency to 16, reconnect backoff to 0.25–30 seconds, and RPC
deadlines to the client's default unless `request_timeout` is set.

The token provider receives `(alias, absolute_path)` and may return a token
string, `(token, bool)`, or `None`. It is called only for a release entry
captured as token-protected or client-bound. Tokens remain local and are sent
only with the corresponding pinned secret read; they never enter KMS release
storage, snapshots, watch events, lifecycle acknowledgements, logs, or metrics.

`ReleaseSnapshot` is a frozen dataclass with namespace, release version,
activation revision, schema ID/version, deterministic digest, metadata,
immutable entry tuple, and read-only alias-keyed parameter/secret mappings.
Each `ReleaseEntry` contains exact path/version, content type, captured
metadata, parameter digest, and non-sensitive secret protection flags. Snapshot
`str`/`repr`/formatting omit resolved values, and every `Secret` remains
`[REDACTED]` unless `.value` or `.string_value` is used explicitly.

Preparation receives a cancellation event set when a newer activation
supersedes the candidate. The loader resolves exact pins concurrently,
verifies versions and digests, reports `received`, calls preparation, reports
`prepared`, and fresh-reads active name/version/revision/digest before
`commit`. It calls `abort` exactly once for every successfully prepared object
that does not commit. An initial failure raises `ReleaseStartupError`; after a
successful commit, outages and rejected candidates retain the last-known-good
release. An exception from `commit` raises fatal `ReleaseCommitError` and is
never acknowledged as applied. A `ReleaseLoader` may be run only once.

For explicit typed decoding without descriptors, dataclass reflection, or
schema generation:

```python
from kms_paramstore import run_typed_release

run_typed_release(
    loader,
    decode=lambda snapshot: decode_runtime(snapshot),
    prepare=lambda cancel, cfg: prepare_runtime(cancel, cfg),
    stop_event=shutdown,
)
```

`loader.status()` and `loader.stats()` are thread-safe, frozen, redacted views
of observed/applied versions and revisions, state, bounded timing/reconnect
counters, and bounded acknowledgement/rejection counts. They contain no
aliases, paths, values, tokens, or diagnostics.

The final active read is a staleness fence, not a distributed lock. An
activation immediately after it can briefly leave this replica on the prior
release; the next activation event prepares the newer candidate. Replicas
apply independently—version 1 has no fleet-wide barrier. See
[`configuration-releases.md`](configuration-releases.md) for server semantics.

## Parity with the Go SDK

The two SDKs are intentionally close; the naming differs where each
language's conventions do (see [`sdk-go.md`](sdk-go.md)):

| Concept | Go | Python |
|---|---|---|
| Namespace config | `Config.Namespace` | `Client(namespace=...)` |
| Hot-reload opt-out | `ParameterValue{Static: true}` | `ParameterValue(static=True)` |
| Unbound + relative key | `ErrNoNamespace` | `NoNamespaceError` (a `ConfigError`) |
| Identity discovery | `WhoAmI` | `client.who_am_i()` / `WhoAmI` dataclass |
| Cross-namespace key | leading-`/` display path | leading-`/` display path |
| Atomic release loader | `ReleaseLoader.Run` | `ReleaseLoader.run` |
| Explicit typed helper | `RunTypedRelease[T]` | `run_typed_release` |

## What this document does not cover

- The wire-level gRPC contract — see `proto/kms/v1/kms.proto`.
- Server-side configuration, deployment, and the CLI — see
  [`operations.md`](operations.md).
- The encryption and authorization model, the built-in CA, and per-namespace
  auth methods — see [`security.md`](security.md).
- The SDK's own internal test fixtures (`sdk/python/tests/_fake_server.py`)
  are private test infrastructure, not a public testing utility — unlike
  the Go SDK's `paramstoretest` package, they are not exported by
  `kms_paramstore` and are not part of its public API.
