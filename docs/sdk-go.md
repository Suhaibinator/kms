# Go SDK (`sdk/go/kmsclient`)

The Go SDK hides gRPC plumbing behind a small surface: direct reads
(`GetParameter`/`GetSecret`), declarative config fields (`SecretValue`/
`ParameterValue`) resolved with one `Client.Resolve` call, and hot reload of
non-secret parameters over the `Subscribe` stream. Secret plaintext never
appears in logs, errors, or the default string/JSON representation of any
type in this package — reaching it always requires an explicit call.

The client operates in a **namespace** — a fixed `(env, app)` pair such as
`prod/gradethis`. Keys are **relative** to that namespace (`rate-limit`,
`billing/stripe-key`); a leading-slash key is an absolute `/env/app/key`
display path for reaching another namespace. See
[Namespaces and keys](#namespaces-and-keys).

Package import path: `github.com/Suhaibinator/kms/sdk/go/kmsclient`.

This document describes the public API as implemented; every symbol below
exists in `sdk/go/kmsclient/*.go`. For the wire-level HTTP contract used by
the frontend, see [`http-api.md`](http-api.md); for namespace/key conventions
and a worked example, see [`migration.md`](migration.md).

## Installing

The SDK requires Go 1.27 or newer. Its public redaction and managed-
configuration surfaces use the Go 1.27 `encoding/json/jsontext` APIs.

```bash
go get github.com/Suhaibinator/kms/sdk/go/kmsclient
```

## Connecting

If the application does not have credentials yet, follow the
[production mTLS onboarding runbook](operations.md#connect-a-production-application-with-mtls)
to create its namespace and identity and deliver its client cert/key plus the
operator's server CA bundle.

```go
client, err := kmsclient.NewClient(kmsclient.Config{
    Endpoint:  "parameter-store.prod.internal:8443",
    TLS:       kmsclient.MTLSFromFiles("client.crt", "client.key", "server-ca.crt"),
    CacheTTL:  time.Minute,
})
if err != nil {
    return err
}
defer client.Close()
```

The recommended posture is a **client certificate** minted by the KMS's
built-in CA (proof of possession; see [`security.md`](security.md)): the
identity derives from the cert server-side, so `Token` is not needed. Supply a
`Token` only for token-method identities. `server-ca.crt` must trust the
operator-provided **server** certificate. It is not the built-in client CA
returned by `admin ca show`. The example omits `Namespace` because an identity
created with `--namespace prod/gradethis` discovers that binding through
`WhoAmI`; setting it explicitly is also supported.

`Config` fields (`sdk/go/kmsclient/client.go`):

| Field | Meaning |
|---|---|
| `Endpoint` | `host:port` of the server's gRPC listener. Required unless `DialOptions` supplies a custom dialer (as tests do). |
| `Namespace` | The client's home namespace in `"env/app"` form (e.g. `"prod/gradethis"`). Relative keys resolve against it. Leave empty to discover it from the identity at first use (see [Namespaces and keys](#namespaces-and-keys)). A malformed value fails `NewClient`. |
| `Token` | Per-client identity token, sent as `authorization: Bearer <token>` on every RPC. **Optional** when `TLS` carries a client certificate (the cert proves identity); required only for token-method identities. Empty is also allowed against an unauthenticated/dev server. |
| `TLS` | `*tls.Config`. Build one with `TLSFromFiles`/`TLSConfig` (server-auth only) or `MTLSFromFiles`/`MTLSConfig` (client cert + server auth). The `*FromFiles` variants panic on error and are meant for inline use in a `Config` literal; the non-panicking variants return an error. When `TLS` is `nil`, the client requires either `Insecure: true` or explicit custom transport credentials in `DialOptions`. |
| `Insecure` | Explicitly opts into a cleartext connection for local development. Defaults to `false` and is mutually exclusive with `TLS`; never enable it across an untrusted network. |
| `CacheTTL` | Enables an in-memory read cache for `GetParameter` when `> 0`. Secret plaintext is never cached. Parameter entries are invalidated automatically by watch events once a subscription is active, and on writes. |
| `FallbackToDefaultsOnError` | When `false` (default), a declarative field's `Default` is used only when the store affirmatively reports the value **absent** (`ErrNotFound`); every other fetch error fails `Init`/`Resolve`, so a process cannot silently boot on a dev default because the store was briefly unreachable. Set `true` to restore permissive any-error → `Default`. |
| `Timeout` | Default per-RPC deadline applied when the caller's `context.Context` has no earlier deadline. Defaults to 5s. Does not apply to the long-lived `Subscribe` stream. |
| `ClientName` | Identifies this client in the subscription registry (visible on the frontend's Subscribers page). Defaults to `filepath.Base(os.Args[0])`. |
| `Logger` | Receives operational log lines (keys, env var names, connection state, revisions) — never secret plaintext. Defaults to the standard `log` package. Implement the one-method `Logger` interface (`Printf(format string, args ...any)`) to redirect. |
| `DialOptions` | Extra `grpc.DialOption`s appended after the SDK's own options, so advanced callers can override transport credentials or inject a custom dialer (e.g. bufconn in tests). If neither `TLS` nor `Insecure` is set, these options must supply transport credentials; gRPC otherwise fails closed. |

`NewClient` dials immediately (`grpc.NewClient`) and starts a background
goroutine that drains change-callback dispatch; it does not block on
connecting. Client-side keepalive pings every 30s with a 10s timeout,
`PermitWithoutStream: true`. Call `client.Close()` to release the connection
and stop all background goroutines (subscription manager, callback
dispatcher); it is idempotent.

## Namespaces and keys

Every key the SDK accepts — in `GetParameter`/`GetSecret`/`PutParameter`/
`PutSecret`, and in `SecretValue.Key`/`ParameterValue.Key` — is resolved the
same way (`Client.Watch` takes no key, only a namespace; see below):

- A key with a **leading `/`** is an absolute `/env/app/key` display path. The
  SDK splits it into an explicit namespace + key (requiring at least three
  segments; the key may itself contain interior slashes). This is the escape
  hatch for cross-namespace reads.
- **Any other key** is **relative** to the client namespace.

```go
rate, err := client.GetParameter(ctx, "rate-limit")              // relative to prod/gradethis
other, err := client.GetParameter(ctx, "/staging/billing/rate")  // absolute, another namespace
```

### Namespace discovery (`WhoAmI`) and `ErrNoNamespace`

If `Config.Namespace` is empty, the client discovers its namespace from the
identity the first time a relative key needs one (or a namespace-wide
subscribe starts): it calls `AdminService.WhoAmI` once and caches the result
for the client's lifetime. A transient `WhoAmI` failure is not cached and is
retried on the next call; an authoritative "unbound identity" answer is
cached.

If the identity is unbound (no namespace binding) and `Config.Namespace` is
empty, a relative key cannot be resolved and the call fails with
**`ErrNoNamespace`**, naming the key:

```go
_, err := client.GetParameter(ctx, "rate-limit")
if errors.Is(err, kmsclient.ErrNoNamespace) {
    // set Config.Namespace, bind the identity to a namespace, or use an
    // absolute "/env/app/key" path
}
```

Absolute `/env/app/key` keys never trigger discovery and never return
`ErrNoNamespace`.

## Direct reads

```go
value, err := client.GetParameter(ctx, "rate-limit")

secret, err := client.GetSecret(ctx, "stripe-api-key")
// secret prints as [REDACTED]; call secret.Value() ([]byte) or
// secret.StringValue() (string) for plaintext.
```

Both accept `GetOption`s (`sdk/go/kmsclient/options.go`):

- `WithVersion(n uint64)` — pin to an immutable version (takes precedence over `WithLabel`).
- `WithLabel(label string)` — read the version a label points at (e.g. `"current"`, `"previous"`); the server default when neither option is given is `"current"`.
- `WithSecretToken(token string)` — sets the independent per-secret access-token
  field for a token-gated version. The shared option is accepted by
  `GetParameter` for compatibility but is never transmitted there.
- `WithBindingKey(key string)` — supplies the opaque operator-owned key needed
  by a bound version. It is independent of `WithSecretToken`.

`GetParameter` returns `(string, error)`. `GetSecret` returns `(Secret,
error)`. `Secret` (`secret.go`) is a value type carrying plaintext plus
`Path()` (the `/env/app/key` display path), `Version()`, `ContentType()`; its
`String`/`GoString`/`Format`/`MarshalJSON` all print `"[REDACTED]"` regardless
of verb, so it is safe to pass to `fmt.Printf`, a structured logger, or
`json.Marshal`. `Value() ([]byte)` / `StringValue() (string)` are the only way
to read plaintext. `Secret.BindKey` is declaration-only support for generated
managed configuration: `Clone` preserves it while building declarations, but
generated stores extract and clear it before retaining defaults or publishing
snapshots. A `Secret` returned by `GetSecret` always has an empty `BindKey`.

Writes (mainly for tooling, not typical application code):

```go
res, err := client.PutParameter(ctx, "rate-limit", "200",
    kmsclient.WithContentType("integer"))

res, err := client.PutSecret(ctx, "stripe-api-key", []byte("sk_live_..."),
    kmsclient.WithGenerateAccessToken())
// res.AccessToken is set only when WithGenerateAccessToken() was passed,
// and is never retrievable again after this call returns.
```

`PutSecretOption`s are `WithSecretContentType`, `WithSecretMetadataJSON`,
`WithPutBindingKey(key string)`, `WithGenerateAccessToken()`, and
`WithExpiresAt(unixMS int64)`. A non-empty binding key creates a bound version;
empty creates an unbound version even when the preceding version was bound.
Binding keys are opaque valid UTF-8 of at least 32 bytes. Access-token
generation is independent; there is no write-side secret token.

The client also exposes `GetSecretMetadata`, `BindSecret`, `UnbindSecret`,
`RotateSecretBindingKey`, `PreviewSecretBindingCohort`,
`PurgeSecretBindingCohort`, `PreviewSecretUnboundVersions`, and
`PurgeSecretUnboundVersions`. Bind, unbind, and rotation require the current
version observed by the caller and return `SecretVersionTransitionResult`;
each creates one new current version and leaves the source unchanged.
`PurgeSecretBindingCohort` requires the exact `SecretBindingCohortResult`
returned by its preview. Unbound purge likewise requires the exact
`SecretVersionSetResult` returned by its preview. Purge is
irreversible and requires an administrator. The server proves
the supplied current key before rejecting a byte-for-byte unchanged rotation
replacement, preserving the canonical credential-failure boundary.
See [`binding-keys.md`](binding-keys.md).

`SecretMetadata.Bound` summarizes the current-labeled version, while the
top-level `HasAccessToken` reports whether the secret currently has an access
token hash. Use `SecretMetadata.Versions[n].Bound` and `.HasAccessToken` for an
exact pin.

Secret plaintext is never cached. Each call that needs a secret fetches it;
watch notifications remain metadata-only.

## Errors

`sdk/go/kmsclient/errors.go` maps gRPC status codes to sentinel errors
callers test with `errors.Is`:

```go
_, err := client.GetSecret(ctx, "missing")
switch {
case errors.Is(err, kmsclient.ErrNotFound):
    // key, version, or label does not exist
case errors.Is(err, kmsclient.ErrPermissionDenied):
    // authenticated but not authorized
case errors.Is(err, kmsclient.ErrUnauthenticated):
    // missing/invalid/expired identity token
case errors.Is(err, kmsclient.ErrFailedPrecondition):
    // e.g. trying to bind an already-bound current version
}
```

`ErrNoNamespace` is returned when a relative key needs a namespace but none is
available (unbound identity, empty `Config.Namespace`) — see
[Namespace discovery](#namespace-discovery-whoami-and-errnonamespace).
`SecretValue.Value` panics with a descriptive message when read before
resolution; `ParameterValue.Get` returns `""` before resolution. The exported
`ErrNotInitialized` sentinel is reserved for compatibility and is not currently
emitted. None of these errors, nor anything wrapping them, ever carries secret
plaintext. Credential-bearing secret RPCs preserve the structured status or
sentinel while replacing every remote diagnostic with fixed safe text; a buggy
or hostile peer cannot reflect plaintext, an access token, or a binding key
into the returned error. For non-secret RPCs, errors outside the mapped set
(for example `Unavailable`, `DeadlineExceeded`, or `Internal`) remain the
original gRPC status error.

`ErrPurgeCleanupPending` is the exceptional purge outcome: the logical purge
transaction committed, but active SQLite/WAL cleanup is still pending. Do not
retry a bound-cohort purge with the retired binding key, or an unbound-version
purge as though its preview were still live. gRPC cannot return a mutation
result alongside an error, so the returned purge result is zero in this case.

## Declarative config: `SecretValue` and `ParameterValue`

Declare store-backed fields directly in a config struct
(`sdk/go/kmsclient/values.go`):

```go
type Config struct {
    StripeAPIKey kmsclient.SecretValue
    RateLimit    kmsclient.ParameterValue
    LogFormat    kmsclient.ParameterValue
}

cfg := Config{
    StripeAPIKey: kmsclient.SecretValue{
        Key:     "stripe-api-key",
        Token:   os.Getenv("STRIPE_API_KEY_TOKEN"), // per-secret token, if required
        BindKey: os.Getenv("STRIPE_API_KMS_BIND_KEY"), // binding key, if required
        EnvVar:  "STRIPE_API_KEY",                  // env override still wins
        Default: "sk_test_dev_only",                // dev-only fallback
    },
    RateLimit: kmsclient.ParameterValue{
        Key:     "rate-limit",
        Default: "100",
        // hot-reloads by default; read the latest with cfg.RateLimit.Get()
    },
    LogFormat: kmsclient.ParameterValue{
        Key:    "log-format",
        Static: true, // resolve once at Init, never hot-reload
    },
}
if err := client.Resolve(ctx, &cfg); err != nil {
    return err
}
```

**`SecretValue` fields:** `Key` (relative key or absolute `/env/app/key`),
`Token` (per-secret access token), `BindKey` (independent binding key), `EnvVar`
(optional override), and `Default` (dev-only fallback). The key is sent only
for a store read; an environment override or default performs no secret RPC.

**`ParameterValue` fields:** `Key`, `EnvVar`, `Default`, and `Static`.

> **Hot-reload default flip.** In the previous SDK, parameters were static
> unless you set `Dynamic: true`. That field is **removed**. Parameters now
> **hot-reload by default**; set **`Static: true`** to opt out and pin the
> value to its boot-time read. This is a behavioral change: values you declare
> without thinking about it will now track the store at runtime. `Get()` was
> always the documented read pattern, so most code needs no change — but audit
> any field that must not change after startup and mark it `Static: true`.

Both types expose `Init(client *Client) error` (and an `InitContext(ctx,
client)` variant) for explicit per-field initialization, and are safe to use
as plain struct fields — no embedding or interface satisfaction required.
`Init` is idempotent: a second call after success is a no-op.

**Resolution order** (identical for both types, per field):

1. `EnvVar`, if set and the named environment variable is non-empty.
2. Store fetch (`GetParameter`/`GetSecret` on `Key`).
3. `Default`, if the store fetch reported the value **absent** (`ErrNotFound`)
   and `Default` is non-empty. Any other fetch error fails resolution, unless
   `Config.FallbackToDefaultsOnError` is set (then any error falls back to
   `Default`).
4. Otherwise: an error naming the missing key.

An env-var override pins the value — it never hot-reloads, and `Init` logs
that hot reload was skipped for a non-`Static` field resolved from an env var.
A relative `Key` on a client with no namespace fails `Init` with
`ErrNoNamespace`.

### `Client.Resolve`: one call for a whole struct

```go
if err := client.Resolve(ctx, &cfg); err != nil {
    return err
}
```

`Resolve` (`sdk/go/kmsclient/resolve.go`) walks `cfg` via reflection —
including nested structs, non-nil struct pointers and pointer chains (with
cycle detection), and the elements of slices and arrays of those types
(`[]T`, `[]*T`, `[N]T`) — finds every `SecretValue`/`ParameterValue` field
(unexported fields are skipped), and initializes them **concurrently**, one
goroutine per field, since the service exposes no batch-read RPC. Map values,
interface values, and channel/function fields are not walked. It returns the
joined (`errors.Join`) set of every field's error, if any; fields that
already succeeded remain initialized even if a sibling field failed.
`cfg` must be a non-nil pointer to a struct.

### Reading resolved values

```go
// SecretValue
plaintext := cfg.StripeAPIKey.Value()      // panics if read before Init
plaintext := cfg.StripeAPIKey.StringValue() // alias for Value
wrapped   := cfg.StripeAPIKey.Secret()      // wraps Value() in a redacting Secret
ok        := cfg.StripeAPIKey.Initialized()

// ParameterValue
current := cfg.RateLimit.Get()              // never panics; "" before Init
ok      := cfg.RateLimit.Initialized()
```

`SecretValue`'s `String`/`GoString`/`Format`/`MarshalJSON` all redact to
`"[REDACTED]"`, exactly like `Secret`. `ParameterValue.String()` returns the
current value (parameters are not secret).

## Hot reload

Non-`Static` parameters (and namespaces watched directly — see below) are
served over the client's shared `Subscribe` stream. The SDK owns the whole
connection lifecycle — connect, send the namespace subscription, apply
snapshot/changes, ack heartbeats, reconnect with jittered exponential backoff
(base 1s, cap 60s) on stream loss, and resume from the last-applied revision.
Applications only see values and callbacks.

The **namespace `(env, app)` is the unit of subscription**. **Every non-static
`ParameterValue` and every `Watch` share one subscription** to the set of
namespaces they reference — the client's own namespace, plus any extra
namespace a `WatchNamespace` targets. The server streams **every** change in
those namespaces; the SDK routes each incoming change to the matching field by
**exact key** and to every `Watch` on the change's namespace. Registering more
parameters or watchers in an already-subscribed namespace therefore does
**not** reconnect the stream; the stream only reconnects when a new namespace
is added. Static fields resolve once and never subscribe.

```go
// 1. Live handle — always the latest value, safe for concurrent use, no RPC.
rateLimit := cfg.RateLimit.Get()

// 2. Change callback — for values that need explicit reaction.
cfg.RateLimit.OnChange(func(old, new string) {
    pool.Resize(mustAtoi(new))
})

// 3. Namespace-level watch for advanced use. Fires for EVERY key in the
//    namespace; filter inside the callback if you only want a subset.
stop, err := client.Watch(ctx, func(ev kmsclient.Event) {
    if !strings.HasPrefix(ev.Key, "billing/") {
        return
    }
    fmt.Printf("%s %s/%s => %s\n", ev.Type, ev.Namespace, ev.Key, ev.Value)
})
defer stop()
```

`OnChange` callbacks (and `Watch` callbacks) run serially on one dedicated
dispatch goroutine shared by the client. A panic is recovered and logged. A
slow callback cannot stall the subscription stream, but it **does delay later
callbacks**; if the bounded dispatch queue fills, the notification is dropped
and logged (the value was already updated before dispatch).

`Client.Watch(ctx, fn)` subscribes to the client's **whole namespace** and
takes **no key pattern**: it fires for every change in the namespace, so an app
that only cares about a subset filters inside `fn` by its own convention (e.g.
`strings.HasPrefix(ev.Key, "db/")`). Each `Event` is a parameter put/delete
(`EventPut`/`EventDelete`, with `Value` populated for puts) or a secret metadata
change (`EventSecretChange`; no plaintext, re-fetch via `GetSecret` if needed).
`Watch` returns a `stop` function; `stop` is also called automatically if the
supplied `ctx` is cancelled. On a client with no namespace (unbound identity and
empty `Config.Namespace`) `Watch` returns `ErrNoNamespace`. To observe a
different namespace the client is authorized for, use
`Client.WatchNamespace(ctx, "env/app", fn)`.

An `Event` (`sdk/go/kmsclient/watch.go`) carries `Type`, `Namespace`
(`"env/app"`), `Key` (relative), `Value` (for puts), `Version`, `Revision`,
and the raw `ChangeType`. `Event.Path()` returns the `/env/app/key` display
path.

As a safety net, the subscription manager also polls a reconciliation every 5
minutes: it lists each subscribed namespace with an empty key prefix, applies
the values it receives, and infers deletions only after a complete listing.
Enumeration is capped at 100 pages to prevent runaway pagination. If a list
call fails or the cap is reached with another page pending, fetched values are
still applied but deletion inference is skipped. Event delivery is
at-least-once and idempotent by revision, so this never double-applies a
change.

If the store is unreachable, `Get()` keeps returning the last-known value;
the SDK reconnects in the background and reconciles automatically once it
resumes.

### Deleted parameters

When a hot-reloading parameter is deleted and the SDK observes it — either a
snapshot resync (after a reconnect past the replay window) that omits the key,
or a reconciliation fetch that returns not-found — the handle reverts:

- `Get()` returns the field's configured `Default` if one is set. With no
  `Default`, `Get()` keeps the last-known value (it never errors), and
  `OnChange` does not fire.
- The deletion is surfaced through the existing `OnChange(old, new string)`
  callback, invoked as `OnChange(oldValue, Default)` (skipped when `oldValue`
  already equals `Default`). There is no separate deletion event or flag on
  `ParameterValue`.
- A `Client.Watch` callback receives an `Event` with `Type == EventDelete` and
  an empty `Value` for the deleted key.

Deletion is scoped to what a stream actually subscribed to: only known keys
within the subscribed **namespaces** that are absent from a snapshot are
reverted, so a known key in another namespace is never cleared by an unrelated
snapshot.

## Generated managed configuration

`sdk/go/configstore` and `cmd/kms-config-gen` provide an additive high-level
integration for applications that want release-wide atomicity without writing
their own decoder, default comparison, reload policy, atomic generation, and
typed views. The generated binding owns one immutable active generation;
`Current` performs one atomic load and captured scalar getters are ordinary
field access.

The generator emits a separate application binding package that imports the
root configuration package; the root must not import the binding. Generator
package selection and output paths are relative to its working directory. See
the linked guide for reproducible `go:generate`, module-root, and CI commands.

The application remains authoritative for non-secret defaults. A release
that diverges from them is applied and reported, at startup and on every
reload, and stays visibly divergent through `Status`, `OnApplied`, the applied
acknowledgement, and `kmsmetrics`; any change to a restart-bound field rejects
the complete candidate. The generated `VerifyReleaseDefaults` and
`sdk/go/kmsverify` turn the same comparison into a CI test. Generated strict
decoding, declaration tags, supported encodings, snapshot discipline,
secrets, schema/contract artifacts, logging, metrics, verification, testing,
and the operator workflow are in the
[managed Go configuration guide](managed-go-configuration.md).

Generated bindings also expose `GeneratedSchema() []byte`, which returns a
fresh copy of the exact schema artifact. Applications that want schema
registration, defaults import, and app-owned release creation can expose one
command:

```go
os.Exit(configstore.RunManagedConfigCommand(
    os.Args[1:], os.Stdout, os.Stderr,
    configstore.ManagedConfigCommandConfig[appconfig.Profile, appconfig.Config]{
        Application: appconfig.APP_NAME,
        Schema: configkms.GeneratedSchema,
        Defaults: configstore.DefaultsApplierConfig[appconfig.Profile, appconfig.Config]{
            Provider: appconfig.ManagedReleaseDefaults,
            Encoder: configkms.EncodeDefaultsArtifact,
            Namespace: configkms.NamespaceForProfile,
        },
    },
))
```

It exposes `schema upload` (no profile), `defaults apply --profile ...`, and
`release create --profile ...`. Release creation previews by default and uses
`--execute` to persist an inactive immutable release. It requires current
parameters to match the generated defaults, carries the active release's exact
secret pins forward, and never activates. `RunDefaultsApplier` remains
available when only defaults import is wanted.
For custom tooling, `Client.CreateApplicationSchema` accepts an application,
schema bytes, and optional metadata directly. KMS derives the release name and
returns the assigned coordinates and digest; an identical registration wraps
the public `ErrAlreadyExists` sentinel. `Client.CreateApplicationRelease`
exposes the same preview/plan-digest/create workflow used by the composite
runner without exposing parameter values or secret material in its result.

The lower-level APIs below remain supported and are useful when an application
needs a custom preparation model.

## Atomic release loading

Use `ReleaseLoader` when several parameter and secret versions must be
prepared and installed as one candidate. It watches one named release in the
client's home namespace; unlike ordinary `ParameterValue` callbacks, a release
event cannot be permanently lost to callback-queue saturation.

```go
loader, err := kmsclient.NewReleaseLoader(client, kmsclient.ReleaseLoaderConfig{
    Name:              "runtime",
    ReconcileInterval: time.Minute, // default
    MaxConcurrentFetches: 16,       // default; maximum 256
    SecretTokenProvider: func(alias, path string) (string, bool) {
        token, ok := bootstrapSecretTokens[alias]
        return token, ok
    },
    BindingKeys: map[string]string{
        "db_password": os.Getenv("DB_PASSWORD_KMS_BIND_KEY"),
    },
})
if err != nil {
    return err
}

err = loader.Run(ctx, func(ctx context.Context, candidate kmsclient.ReleaseSnapshot) (
    kmsclient.PreparedRelease, error,
) {
    limits, ok := candidate.Parameter("rate_limits")
    if !ok {
        return nil, errors.New("rate_limits alias is missing")
    }
    password, ok := candidate.Secret("db_password")
    if !ok {
        return nil, errors.New("db_password alias is missing")
    }

    // Decode and validate explicitly. Secret plaintext is accessible only
    // through Value/StringValue, just like a direct GetSecret result.
    return prepareRuntime(ctx, limits.Value(), password.Value())
})
```

`ReleaseLoaderConfig.Name` is required. `ReconcileInterval` defaults to one
minute and `MaxConcurrentFetches` to 16. `InstanceID` normally stays empty so
the loader generates one process-lifetime UUID and reuses it across stream
reconnects; set it only when the runtime already owns a stable replica ID. The
client's `Config.ClientName` groups replicas in subscriber views.

`ValidateManifest`, when supplied, receives an immutable `ReleaseManifest`
after release identity/digest and basic entry checks but before any parameter
or secret fetch or token-provider call. It can reject an alias/kind/content
contract without reading an unexpected protected resource. The manifest
contains only release identity and copied entry metadata; no parameter value,
secret plaintext, or token. Generated managed bindings install this hook
automatically.

For each exact secret pin the loader first fetches live metadata and verifies
the response identity, exact version, state, expiry, `Bound`, and
`HasAccessToken`. `SecretTokenProvider(alias, path)` is invoked only when that
version is access-token gated. `BindingKeys` is a defensively copied alias-keyed
map used only when that version is bound. The two credentials are resolved and
sent independently in the pinned `GetSecret` request; neither enters the
release, snapshot formatting, watch event, acknowledgement, metric, or KMS
storage. A missing required credential rejects the whole candidate as
`token_unavailable`; a failed read/wrong credential is `resolution_failed`.

`ReleaseSnapshot` provides `Namespace`, `Name`, `Version`,
`ActivationRevision`, `SchemaVersion`, `Digest`, and
`MetadataJSON`, plus alias-keyed `Entry`/`Entries`, `Parameter`/`Parameters`,
and `Secret`/`Secrets` accessors. Maps returned from plural accessors are
copies. Each entry exposes exact path/version, content type, captured metadata,
and parameter digest. Release entries carry no protection flags; those are
looked up on the exact secret version and are immutable while it remains live,
so the pin implicitly fixes the protection mode. `String`, every
`fmt` verb, and JSON marshaling exclude resolved parameter and secret values;
`Secret` retains its existing `[REDACTED]` formatting.

Application preparation returns:

```go
type PreparedRelease interface {
    Commit() // must be infallible; normally an atomic pointer swap
    Abort()  // frees an uncommitted candidate
}
```

The callback context is canceled when a newer activation supersedes the
candidate. The loader resolves exact pins concurrently, verifies versions and
digests, reports `received`, calls preparation, reports `prepared`, then
fresh-reads the active name/version/revision/digest before `Commit`. It calls
`Abort` exactly once for any successfully prepared candidate that cannot
commit. A failed initial candidate makes `Run` return; after one successful
commit, outages and rejected candidates leave the last-known-good release in
place. A panic in `Commit` is fatal and is never reported as `applied`.

A `PreparedRelease` may also implement `ReleaseDivergenceReporter`:

```go
type ReleaseDivergenceReporter interface {
    ReleaseDivergence() (divergent bool, fieldCount int)
}
```

When it does, the `applied` acknowledgement carries `applied_divergent` and
`divergent_field_count` so subscriber views and readiness findings can show
which replicas run a release that differs from their source defaults. No
field name or value is sent. Generated managed bindings implement it
automatically.

`Client.VerifyReleaseDefaults(ctx, VerifyReleaseDefaultsOptions{Namespace,
Release, Profile, SchemaSHA256, Entries})` is the value-free comparison behind
the CI verify test: each entry is an alias, content type, and the lowercase
hex SHA-256 of the canonical document (`configstore.ParameterHash`), and the
result carries one bounded verdict per alias plus `SchemaMatches` and an
`UnverifiedCount`. It requires the `configuration-release:verify-defaults`
operation and is rate limited per identity; the client maps
`ResourceExhausted` to `ErrRateLimited`, which callers should treat as "wait
for the window" rather than retry. Generated bindings wrap it as
`VerifyReleaseDefaults(ctx, client, root, opts)`; see
[managed defaults](managed-defaults.md#verify-code-defaults-against-a-release-ci).

For an explicit typed decode step without reflection:

```go
err = kmsclient.RunTypedRelease(ctx, loader,
    func(snapshot kmsclient.ReleaseSnapshot) (RuntimeConfig, error) {
        return decodeRuntime(snapshot)
    },
    func(ctx context.Context, cfg RuntimeConfig) (kmsclient.PreparedRelease, error) {
        return prepareTypedRuntime(ctx, cfg)
    },
)
```

`loader.Status()` and `loader.Stats()` return bounded redacted state and
low-cardinality counters: observed/applied version and revision, lifecycle
state, reconnects, timings, and rejection-category counts. They contain no
aliases, paths, values, tokens, or diagnostics.

The final fresh read is a staleness fence, not a distributed lock. A release
activated immediately after it can briefly leave this replica on the prior
version; the newer activation is then handled as the next candidate. Replicas
apply independently—there is no fleet-wide barrier in version 1. Server-side
release/schema semantics are documented in
[`configuration-releases.md`](configuration-releases.md).

## Testing against a fake server

`sdk/go/kmsclient/kmsclienttest` provides an in-process, scriptable fake
of the gRPC services over `bufconn`, used by the SDK's own tests and
available to consumers. Values are addressed by namespace + relative key, with
`*Path` conveniences that take a `/env/app/key` display path:

```go
srv, _ := kmsclienttest.New()
defer srv.Close()
srv.SetParameter("prod/gradethis", "rate-limit", "100")
srv.SetParameterPath("/prod/gradethis/rate-limit", "100") // equivalent
srv.SetSecret("prod/gradethis", "stripe-api-key", []byte("sk_test"))

client, _ := kmsclient.NewClient(kmsclient.Config{
    Namespace:   "prod/gradethis",
    DialOptions: srv.DialOptions(),
})
```

`srv.DialOptions()` includes explicit cleartext credentials for this in-process
test transport. For a standalone plaintext development server, opt in directly
with `Insecure: true`.

Scripting surface:

- **Values:** `SetParameter(ns, key, value)` / `SetParameterPath(displayPath, value)`, `RemoveParameter` / `RemoveParameterPath`, `SetSecret(ns, key, []byte)` / `SetSecretPath`.
- **Errors:** `SetParameterError(ns, key, err)` / `SetParameterErrorPath`, `SetSecretError` / `SetSecretErrorPath`.
- **Identity:** `SetIdentity(name, kind, namespace, authMethod)` sets the `WhoAmI` response — pass an empty `namespace` for an unbound identity — to drive namespace discovery and `ErrNoNamespace`.
- **Inspection:** `LastMetadata(method)` (the incoming gRPC metadata of the most recent call), `PutSecretCalls()`, `Revision()`, `SubscribeCount()`, `SetGetParameterHook(func(displayPath string))` (runs at the start of every `GetParameter`, to inject a concurrent event mid-fetch).
- **Driving the stream:** `WaitForSubscribe(timeout)` returns a `*Subscription` whose requested namespaces are inspectable via `Namespaces`, `HasNamespace(ns)`, and `NamespaceStrings()`. Push events with `PushSnapshot(rev, params...)` (build params with the `kmsclienttest.Param(ns, key, value, version)` / `ParamPath(displayPath, value, version)` helpers), `PushChange(rev, ns, key, changeType, value, version)` / `PushChangePath`, `PushSecretChange(rev, ns, key, changeType, version)` / `PushSecretChangePath`, `SendHeartbeat(rev)`, `WaitAck(timeout)`, and `Kill()` (force a disconnect to exercise reconnect and resume-by-revision).
- **Configuration releases:** store exact values with `SetParameterVersion`
  and `SetSecretVersion`, install the startup release with
  `SetActiveRelease`, and publish later candidates with
  `ActivateConfigurationRelease`. `WaitForReleaseSubscribe` returns a
  `*ReleaseSubscription` for registration inspection, lifecycle
  acknowledgements, scripted events, and forced disconnects.

## What this document does not cover

- The wire-level gRPC/HTTP contract — see the `.proto` at
  `proto/kms/v1/kms.proto` and [`http-api.md`](http-api.md).
- Server-side configuration, deployment, and the CLI — see
  [`operations.md`](operations.md).
- The encryption, authentication (built-in CA, mTLS), and authorization model
  — see [`security.md`](security.md).
