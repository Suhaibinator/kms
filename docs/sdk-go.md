# Go SDK (`sdk/go/paramstore`)

The Go SDK hides gRPC plumbing behind a small surface: direct reads
(`GetParameter`/`GetSecret`), declarative config fields (`SecretValue`/
`ParameterValue`) resolved with one `Client.Resolve` call, and hot reload of
non-secret parameters over the `Subscribe` stream. Secret plaintext never
appears in logs, errors, or the default string/JSON representation of any
type in this package — reaching it always requires an explicit call.

Package import path: `github.com/Suhaibinator/kms/sdk/go/paramstore`.

This document describes the public API as implemented; every symbol below
exists in `sdk/go/paramstore/*.go`. For the wire-level HTTP contract used by
the frontend, see [`http-api.md`](http-api.md); for path/namespace
conventions and a worked example, see [`migration.md`](migration.md).

## Installing

```bash
go get github.com/Suhaibinator/kms/sdk/go/paramstore
```

## Connecting

```go
client, err := paramstore.NewClient(paramstore.Config{
    Endpoint: "parameter-store.prod.internal:8443",
    Token:    os.Getenv("PARAM_STORE_TOKEN"), // per-client identity token
    TLS:      paramstore.MTLSFromFiles("client.crt", "client.key", "ca.crt"),
    CacheTTL: time.Minute,
})
if err != nil {
    return err
}
defer client.Close()
```

`Config` fields (`sdk/go/paramstore/client.go`):

| Field | Meaning |
|---|---|
| `Endpoint` | `host:port` of the server's gRPC listener. Required unless `DialOptions` supplies a custom dialer (as tests do). |
| `Token` | Per-client identity token, sent as `authorization: Bearer <token>` on every RPC. Empty is allowed against an unauthenticated/dev server. |
| `TLS` | `*tls.Config`. `nil` dials insecure (development only). Build one with `TLSFromFiles`/`TLSConfig` (server-auth only) or `MTLSFromFiles`/`MTLSConfig` (client cert + server auth). The `*FromFiles` variants panic on error and are meant for inline use in a `Config` literal; the non-panicking variants return an error. |
| `CacheTTL` | Enables an in-memory read cache for `GetParameter`/`GetSecret` when `> 0`. Cache entries are invalidated automatically by watch events once a subscription is active (see Hot reload below), and unconditionally on every write. |
| `Timeout` | Default per-RPC deadline applied when the caller's `context.Context` has no earlier deadline. Defaults to 5s. Does not apply to the long-lived `Subscribe` stream. |
| `ClientName` | Identifies this client in the subscription registry (visible on the frontend's Subscribers page). Defaults to `filepath.Base(os.Args[0])`. |
| `Logger` | Receives operational log lines (paths, env var names, connection state, revisions) — never secret plaintext. Defaults to the standard `log` package. Implement the one-method `Logger` interface (`Printf(format string, args ...any)`) to redirect. |
| `DialOptions` | Extra `grpc.DialOption`s appended after the SDK's own defaults, so they can override transport credentials or inject a custom dialer (e.g. bufconn in tests). |

`NewClient` dials immediately (`grpc.NewClient`) and starts a background
goroutine that drains change-callback dispatch; it does not block on
connecting. Client-side keepalive pings every 30s with a 10s timeout,
`PermitWithoutStream: true`. Call `client.Close()` to release the connection
and stop all background goroutines (subscription manager, callback
dispatcher); it is idempotent.

## Direct reads

```go
value, err := client.GetParameter(ctx, "/prod/gradethis/rate-limit")

secret, err := client.GetSecret(ctx, "/prod/gradethis/stripe-api-key")
// secret prints as [REDACTED]; call secret.Value() ([]byte) or
// secret.StringValue() (string) for plaintext.
```

Both accept `GetOption`s (`sdk/go/paramstore/options.go`):

- `WithVersion(n uint64)` — pin to an immutable version (takes precedence over `WithLabel`).
- `WithLabel(label string)` — read the version a label points at (e.g. `"current"`, `"previous"`); the server default when neither option is given is `"current"`.
- `WithSecretToken(token string)` — sets the `x-kms-secret-token` metadata; required for token-protected and client-bound secrets.

`GetParameter` returns `(string, error)`. `GetSecret` returns `(Secret,
error)`. `Secret` (`secret.go`) is a value type carrying plaintext plus
`Path()`, `Version()`, `ContentType()`; its `String`/`GoString`/`Format`/
`MarshalJSON` all print `"[REDACTED]"` regardless of verb, so it is safe to
pass to `fmt.Printf`, a structured logger, or `json.Marshal`. `Value()
([]byte)` / `StringValue() (string)` are the only way to read plaintext.

Writes (mainly for tooling, not typical application code):

```go
res, err := client.PutParameter(ctx, "/prod/gradethis/rate-limit", "200",
    paramstore.WithContentType("integer"))

res, err := client.PutSecret(ctx, "/prod/gradethis/stripe-api-key", []byte("sk_live_..."),
    paramstore.WithGenerateAccessToken())
// res.AccessToken is set only when WithGenerateAccessToken() was passed,
// and is never retrievable again after this call returns.
```

`PutSecretOption`s: `WithSecretContentType`, `WithSecretMetadataJSON`,
`WithClientBound()` (opt in to client-bound double wrapping — only honored
on first creation), `WithGenerateAccessToken()`, `WithExpiresAt(unixMS
int64)`, `WithPutSecretToken(token string)` (supply the existing token when
updating a token-protected or client-bound secret).

## Errors

`sdk/go/paramstore/errors.go` maps gRPC status codes to sentinel errors
callers test with `errors.Is`:

```go
_, err := client.GetSecret(ctx, "/prod/missing")
switch {
case errors.Is(err, paramstore.ErrNotFound):
    // path, version, or label does not exist
case errors.Is(err, paramstore.ErrPermissionDenied):
    // authenticated but not authorized
case errors.Is(err, paramstore.ErrUnauthenticated):
    // missing/invalid/expired identity token
case errors.Is(err, paramstore.ErrFailedPrecondition):
    // e.g. client_bound mode mismatch on an existing secret
}
```

`ErrNotInitialized` is returned (or panicked with, from `SecretValue.Value`/
`ParameterValue` before `Init`) when a declarative value is read before
resolution. None of these errors, nor anything wrapping them, ever carries
secret plaintext. Errors outside this mapped set (e.g. `Unavailable`,
`DeadlineExceeded`, `Internal`) are returned as the original gRPC status
error.

## Declarative config: `SecretValue` and `ParameterValue`

Declare store-backed fields directly in a config struct
(`sdk/go/paramstore/values.go`):

```go
type Config struct {
    StripeAPIKey paramstore.SecretValue
    RateLimit    paramstore.ParameterValue
}

cfg := Config{
    StripeAPIKey: paramstore.SecretValue{
        Key:     "/prod/gradethis/stripe-api-key",
        Token:   os.Getenv("STRIPE_API_KEY_TOKEN"), // per-secret token, if required
        EnvVar:  "STRIPE_API_KEY",                  // env override still wins
        Default: "sk_test_dev_only",                // dev-only fallback
    },
    RateLimit: paramstore.ParameterValue{
        Key:     "/prod/gradethis/rate-limit",
        Default: "100",
        Dynamic: true, // hot-reloads; read with cfg.RateLimit.Get()
    },
}
if err := client.Resolve(ctx, &cfg); err != nil {
    return err
}
```

**`SecretValue` fields:** `Key` (store path), `Token` (per-secret access
token; for client-bound secrets this is also the client key share — see
[`security.md`](security.md)), `EnvVar` (optional override), `Default`
(dev-only fallback).

**`ParameterValue` fields:** `Key`, `EnvVar`, `Default`, `Dynamic` (register
for hot reload).

Both types expose `Init(client *Client) error` (and an `InitContext(ctx,
client)` variant) for explicit per-field initialization, and are safe to use
as plain struct fields — no embedding or interface satisfaction required.
`Init` is idempotent: a second call after success is a no-op.

**Resolution order** (identical for both types, per field):

1. `EnvVar`, if set and the named environment variable is non-empty.
2. Store fetch (`GetParameter`/`GetSecret` on `Key`).
3. `Default`, if the store fetch failed and `Default` is non-empty.
4. Otherwise: an error naming the missing path.

An env-var override pins the value — it never hot-reloads, and `Init` logs
that hot reload was skipped for a `Dynamic: true` field resolved from an
env var.

### `Client.Resolve`: one call for a whole struct

```go
if err := client.Resolve(ctx, &cfg); err != nil {
    return err
}
```

`Resolve` (`sdk/go/paramstore/resolve.go`) walks `cfg` via reflection —
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

Parameters marked `Dynamic: true` (or watched directly — see below) are
registered on the client's shared `Subscribe` stream. The SDK owns the whole
connection lifecycle — connect, send registration, apply snapshot/changes,
ack heartbeats, reconnect with jittered exponential backoff (base 1s, cap
60s) on stream loss, and resume from the last-applied revision. Applications
only see values and callbacks.

```go
// 1. Live handle — always the latest value, safe for concurrent use, no RPC.
rateLimit := cfg.RateLimit.Get()

// 2. Change callback — for values that need explicit reaction.
cfg.RateLimit.OnChange(func(old, new string) {
    pool.Resize(mustAtoi(new))
})

// 3. Namespace-level watch for advanced use.
stop, err := client.Watch(ctx, "/prod/gradethis/*", func(ev paramstore.Event) {
    fmt.Printf("%s %s => %s\n", ev.Type, ev.Path, ev.Value)
})
defer stop()
```

`OnChange` callbacks (and `Watch` callbacks) run on a single dedicated
dispatch goroutine shared by the client; a panicking or slow callback is
recovered/logged and cannot stall the stream or other callbacks (a full
dispatch queue drops the notification — values are already updated by then
— and logs a warning rather than blocking).

`Client.Watch(ctx, pattern, fn)` subscribes to an exact path or a `"*"`
prefix pattern (e.g. `"/prod/gradethis/*"`) and delivers every matching
`Event` — parameter puts/deletes (`EventPut`/`EventDelete`, with `Value`
populated for puts) and secret metadata changes (`EventSecretChange`; no
plaintext, re-fetch via `GetSecret` if needed). It returns a `stop` function;
`stop` is also called automatically if the supplied `ctx` is cancelled.

As a safety net, the subscription manager also polls a full reconciliation
every 5 minutes (registered parameter paths and any `"*"`-suffixed watch
prefixes), applying any drift the event stream might have missed — event
delivery is at-least-once and idempotent by revision, so this never
double-applies a change.

If the store is unreachable, `Get()` keeps returning the last-known value;
the SDK reconnects in the background and reconciles automatically once it
resumes.

### Deleted parameters

When a dynamic parameter is deleted and the SDK observes it — either a
snapshot resync (after a reconnect past the replay window) that omits the path,
or a reconciliation fetch that returns not-found — the handle reverts:

- `Get()` returns the field's configured `Default` if one is set. With no
  `Default`, `Get()` keeps the last-known value (it never errors), and
  `OnChange` does not fire.
- The deletion is surfaced through the existing `OnChange(old, new string)`
  callback, invoked as `OnChange(oldValue, Default)` (skipped when `oldValue`
  already equals `Default`). There is no separate deletion event or flag on
  `ParameterValue`.
- A `Client.Watch` callback receives an `Event` with `Type == EventDelete` and
  an empty `Value` for the deleted path.

Deletion is scoped to the paths a stream actually subscribed to: only paths
under the registered patterns that are absent from a snapshot are reverted, so
an unrelated known path is never cleared by another watch's snapshot.

## Testing against a fake server

`sdk/go/paramstore/paramstoretest` provides an in-process, scriptable fake
of the gRPC services over `bufconn`, used by the SDK's own tests and
available to consumers:

```go
srv, _ := paramstoretest.New()
defer srv.Close()
srv.SetParameter("/prod/gradethis/rate-limit", "100")
srv.SetSecret("/prod/gradethis/stripe-api-key", []byte("sk_test"))

client, _ := paramstore.NewClient(paramstore.Config{
    DialOptions: srv.DialOptions(),
})
```

It supports scripting per-path errors (`SetParameterError`,
`SetSecretError`), inspecting the metadata a call was made with
(`LastMetadata`), recording `PutSecret` calls, and driving the `Subscribe`
stream by hand (`WaitForSubscribe`, `Subscription.PushSnapshot`/
`PushChange`/`PushSecretChange`/`SendHeartbeat`/`WaitAck`/`Kill`) to exercise
reconnect and resume-by-revision behavior.

## What this document does not cover

- The wire-level gRPC/HTTP contract — see the `.proto` at
  `proto/kms/v1/kms.proto` and [`http-api.md`](http-api.md).
- Server-side configuration, deployment, and the CLI — see
  [`operations.md`](operations.md).
- The encryption and authorization model — see [`security.md`](security.md).
