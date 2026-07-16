# paramstore — Go SDK for the KMS parameter store

`paramstore` is the Go client for the KMS parameter-store and secret-management
service. It hides gRPC boilerplate behind a small, safe surface: simple reads,
declarative store-backed config fields, and hot reload of parameters — with
secret plaintext that never leaks into logs, errors, or string/JSON output.

```go
import "github.com/Suhaibinator/kms/sdk/go/paramstore"
```

## Connect

```go
client, err := paramstore.NewClient(paramstore.Config{
    Endpoint:  "parameter-store.prod.internal:8443",
    Namespace: "prod/gradethis",                                    // env/app; optional (see below)
    TLS:       paramstore.MTLSFromFiles("client.crt", "client.key", "server-ca.crt"),
    CacheTTL:  time.Minute,                                         // optional in-memory read cache
})
if err != nil {
    return err
}
defer client.Close()
```

The preferred posture is a **client certificate** (proof of possession, minted
by the KMS CA): identity derives from the cert server-side, so `Token` is
optional. `Token` is only required for token-method identities. `TLS: nil` uses
an insecure connection (development only). `server-ca.crt` must trust the
operator-provided server certificate; it is not the built-in client CA shown by
`admin ca show`.

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
client.GetParameter(ctx, key, paramstore.WithVersion(3))
client.GetSecret(ctx, key, paramstore.WithLabel("previous"))
client.GetSecret(ctx, key, paramstore.WithSecretToken(tok)) // token-protected / client-bound
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
    DBPassword paramstore.SecretValue
    StripeKey  paramstore.SecretValue
    RateLimit  paramstore.ParameterValue
    Payments   struct { // nested structs are walked too
        Timeout paramstore.ParameterValue
    }
}

cfg := Config{
    DBPassword: paramstore.SecretValue{Key: "postgres/password"},
    StripeKey:  paramstore.SecretValue{Key: "stripe/api-key", EnvVar: "STRIPE_KEY"},
    RateLimit:  paramstore.ParameterValue{Key: "rate-limit"}, // hot-reloads by default
}
cfg.Payments.Timeout = paramstore.ParameterValue{Key: "timeout", Default: "30s", Static: true}

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
stop, _ := client.Watch(ctx, func(ev paramstore.Event) {
    if !strings.HasPrefix(ev.Key, "billing/") {
        return
    }
    log.Printf("%s %s/%s -> %s", ev.Type, ev.Namespace, ev.Key, ev.Value)
})
defer stop()
```

If the store is unreachable the SDK keeps serving last-known values and
reconnects in the background.

## Errors

Map gRPC codes to sentinels with `errors.Is`:

```go
if errors.Is(err, paramstore.ErrNotFound) { ... }
```

`ErrNotFound`, `ErrPermissionDenied`, `ErrUnauthenticated`,
`ErrFailedPrecondition`, `ErrNoNamespace`. No error ever contains secret
plaintext.

## Testing against a fake

`paramstore/paramstoretest` provides an in-process, scriptable gRPC fake
(bufconn) for your own tests: set values by namespace + relative key (or display
path), inject errors, set the WhoAmI identity, and drive the Subscribe stream
(snapshots, changes, heartbeats, forced disconnects).

```go
srv, _ := paramstoretest.New()
defer srv.Close()
srv.SetParameter("prod/gradethis", "rate-limit", "100")
srv.SetParameterPath("/prod/gradethis/rate-limit", "100") // equivalent

client, _ := paramstore.NewClient(paramstore.Config{
    Endpoint:    srv.Target(),
    Namespace:   "prod/gradethis",
    DialOptions: srv.DialOptions(),
})
```
