# kmsclient — Go SDK for the KMS parameter store

`kmsclient` is the Go client for the KMS parameter-store and secret-management
service. It hides gRPC boilerplate behind a small, safe surface: simple reads,
declarative store-backed config fields, and hot reload of parameters — with
secret plaintext that never leaks into logs, errors, or string/JSON output.

```go
import "github.com/Suhaibinator/kms/sdk/go/kmsclient"
```

## Connect

Follow the
[production mTLS onboarding runbook](../../../docs/operations.md#connect-a-production-application-with-mtls)
to create the application's namespace and identity and deliver its client
cert/key plus the operator's server CA bundle.

```go
client, err := kmsclient.NewClient(kmsclient.Config{
    Endpoint:  "parameter-store.prod.internal:8443",
    TLS:       kmsclient.MTLSFromFiles("client.crt", "client.key", "server-ca.crt"),
    CacheTTL:  time.Minute,                                         // optional in-memory read cache
})
if err != nil {
    return err
}
defer client.Close()
```

The preferred posture is a **client certificate** (proof of possession, minted
by the KMS CA): identity derives from the cert server-side, so `Token` is
optional. `Token` is only required for token-method identities. `server-ca.crt`
must trust the operator-provided server certificate; it is not the built-in
client CA shown by `admin ca show`. A namespace-bound identity discovers its
home namespace through `WhoAmI`; `Config.Namespace` remains available when an
explicit namespace is preferable.

Transport security must be explicit: without `TLS`, `NewClient` fails instead
of silently sending credentials and secret plaintext over cleartext. A local
development server can be reached with `Insecure: true`; do not use that option
across an untrusted network. Low-level callers may instead provide explicit
transport credentials through `DialOptions`.

### Namespaces and keys

A client operates in one **namespace** — a fixed `(env, app)` pair like
`prod/gradethis`. Keys are **relative** to it (`rate-limit`,
`billing/stripe-key`); a key's interior slashes are part of the name, never
namespace structure.

- Set `Config.Namespace` explicitly, **or** leave it empty to **discover** it
  from the identity at first use (one `WhoAmI` call, cached for the client's
  lifetime). A relative key on an unbound identity fails with `ErrNoNamespace`.
- A leading-slash key is an absolute **`/env/app/key` display path**, split in
  the SDK to reach another namespace:

```go
rate, err := client.GetParameter(ctx, "rate-limit")              // relative to prod/gradethis
other, err := client.GetParameter(ctx, "/staging/billing/rate")  // absolute, cross-namespace
```

## Read parameters and secrets

```go
rate, err := client.GetParameter(ctx, "rate-limit")

pw, err := client.GetSecret(ctx, "postgres/password")
db.Connect(pw.Value()) // []byte plaintext; pw itself prints "[REDACTED]"
```

Read options:

```go
client.GetParameter(ctx, key, kmsclient.WithVersion(3))
client.GetSecret(ctx, key, kmsclient.WithLabel("previous"))
client.GetSecret(ctx, key, kmsclient.WithSecretToken(tok)) // token-protected / client-bound
```

## Redaction

`Secret`, `SecretValue`, and anything containing them redact in every common
sink — `fmt` (`%v`, `%s`, `%+v`, `%#v`, `%q`), `Stringer`, and `json.Marshal`:

```go
log.Printf("secret=%v", pw)          // secret=[REDACTED]
json.Marshal(cfg)                    // {"DBPassword":"[REDACTED]", ...}
```

Plaintext is only reachable through explicit accessors: `Secret.Value()`,
`Secret.StringValue()`, `SecretValue.Value()`.

## Declarative config (drop-in pattern)

Declare store-backed fields and resolve the whole struct in one call. Resolution
order per field: **env override → store fetch → `Default` → error naming the key**.

`Default` is a dev-only escape hatch: it is used only when the store
affirmatively reports the value **absent** (`ErrNotFound`). Any other fetch error
— unavailable, timeout, unauthenticated, permission denied — fails
`Init`/`Resolve` even when a `Default` is set, so a process can never silently
boot on a dev default because the store was briefly unreachable. Set
`Config.FallbackToDefaultsOnError: true` to opt into any-error → `Default`.

```go
type Config struct {
    DBPassword kmsclient.SecretValue
    StripeKey  kmsclient.SecretValue
    RateLimit  kmsclient.ParameterValue
    Payments   struct { // nested structs are walked too
        Timeout kmsclient.ParameterValue
    }
}

cfg := Config{
    DBPassword: kmsclient.SecretValue{Key: "postgres/password"},
    StripeKey:  kmsclient.SecretValue{Key: "stripe/api-key", EnvVar: "STRIPE_KEY"},
    RateLimit:  kmsclient.ParameterValue{Key: "rate-limit"}, // hot-reloads by default
}
cfg.Payments.Timeout = kmsclient.ParameterValue{Key: "timeout", Default: "30s", Static: true}

if err := client.Resolve(ctx, &cfg); err != nil { // batches fetches concurrently
    return err
}

pw := cfg.DBPassword.Value()   // plaintext
rl := cfg.RateLimit.Get()      // latest value
```

Prefer explicit initialization? Every value type also has `Init(client)`:

```go
if err := cfg.DBPassword.Init(client); err != nil { return err }
```

`Init` is idempotent. Env-overridden values are pinned and never hot-reload.

## Hot reload

**Parameters hot-reload by default.** A `ParameterValue` tracks the store over a
`Subscribe` stream the SDK owns end to end: subscribe on startup, heartbeat/ack,
reconnect with jittered backoff, resume by revision, and a 5-minute
reconciliation safety net. Every non-static value in a namespace shares one
namespace-wide subscription. Set `Static: true` to pin a value to its boot-time
read (`ParameterValue{Key: "log-format", Static: true}`).

> **Default flip:** values now change at runtime by default. `Get()` was already
> the documented read pattern; use `Static: true` where you need a fixed value.

```go
// Always-current handle:
limit := cfg.RateLimit.Get()

// React to changes (runs on a dedicated goroutine; a slow/panicking callback
// can never stall the stream):
cfg.RateLimit.OnChange(func(old, new string) {
    pool.Resize(mustAtoi(new))
})

// Watch fires for EVERY change in the client's namespace — there is no key
// pattern. Filter inside the callback if you only care about a subset. Use
// client.WatchNamespace(ctx, "env/app", fn) to watch a different namespace.
stop, _ := client.Watch(ctx, func(ev kmsclient.Event) {
    if !strings.HasPrefix(ev.Key, "billing/") {
        return
    }
    log.Printf("%s %s/%s -> %s", ev.Type, ev.Namespace, ev.Key, ev.Value)
})
defer stop()
```

If the store is unreachable the SDK keeps serving last-known values and
reconnects in the background.

## Atomic configuration releases

Use a release loader when related values must be resolved and installed
together rather than through independent key callbacks:

```go
loader, err := kmsclient.NewReleaseLoader(client, kmsclient.ReleaseLoaderConfig{
    Name: "runtime",
    SecretTokenProvider: func(alias, path string) (string, bool) {
        token, ok := localTokens[alias]
        return token, ok
    },
})
if err != nil { return err }

err = loader.Run(ctx, func(ctx context.Context, snapshot kmsclient.ReleaseSnapshot) (
    kmsclient.PreparedRelease, error,
) {
    return decodeValidateAndPrepare(ctx, snapshot)
})
```

The snapshot exposes the release version, activation revision, deterministic
digest, schema pin, and exact alias-keyed resource pins. Resolved maps are
immutable-by-copy and normal formatting excludes values; secret plaintext
still requires explicit `Secret.Value`/`StringValue`. `PreparedRelease.Commit`
must be infallible and normally performs an atomic swap; `Abort` releases any
prepared candidate that becomes stale or fails the final active-release check.
The loader fails startup until one release applies, then retains the
last-known-good state through outages and rejections.

`RunTypedRelease[T]` adds an explicit decode step and uses no reflection.
See [`../../../docs/sdk-go.md`](../../../docs/sdk-go.md#atomic-release-loading)
for lifecycle, acknowledgement, token-provider, and status details.

For an application-specific store with generated strict group decoders,
source-owned default drift checks, hot/restart policy, immutable snapshots,
typed consumer views, and schema/contract generation, use the additive
[`sdk/go/configstore` managed configuration layer and
`cmd/kms-config-gen`](../../../docs/managed-go-configuration.md).
Existing `ReleaseLoader`, `RunTypedRelease`, `ParameterValue`, and
`SecretValue` integrations do not need to change.

## Errors

Map gRPC codes to sentinels with `errors.Is`:

```go
if errors.Is(err, kmsclient.ErrNotFound) { ... }
```

`ErrNotFound`, `ErrPermissionDenied`, `ErrUnauthenticated`,
`ErrFailedPrecondition`, `ErrNoNamespace`. No error ever contains secret
plaintext.

## Testing against a fake

`kmsclient/kmsclienttest` provides an in-process, scriptable gRPC fake
(bufconn) for your own tests: set values by namespace + relative key (or display
path), inject errors, set the WhoAmI identity, and drive the Subscribe stream
(snapshots, changes, heartbeats, forced disconnects).

```go
srv, _ := kmsclienttest.New()
defer srv.Close()
srv.SetParameter("prod/gradethis", "rate-limit", "100")
srv.SetParameterPath("/prod/gradethis/rate-limit", "100") // equivalent

client, _ := kmsclient.NewClient(kmsclient.Config{
    Endpoint:    srv.Target(),
    Namespace:   "prod/gradethis",
    DialOptions: srv.DialOptions(),
})
```

`srv.DialOptions()` includes explicit cleartext transport credentials for the
in-process connection, so no `Insecure` flag is needed in this test setup.
