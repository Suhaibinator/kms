# paramstore — Go SDK for the KMS parameter store

`paramstore` is the Go client for the KMS parameter-store and secret-management
service. It hides gRPC boilerplate behind a small, safe surface: simple reads,
declarative store-backed config fields, and hot reload of dynamic parameters —
with secret plaintext that never leaks into logs, errors, or string/JSON output.

```go
import "github.com/Suhaibinator/kms/sdk/go/paramstore"
```

## Connect

```go
client, err := paramstore.NewClient(paramstore.Config{
    Endpoint: "parameter-store.prod.internal:8443",
    Token:    os.Getenv("KMS_TOKEN"), // per-client identity token
    TLS:      paramstore.MTLSFromFiles("client.crt", "client.key", "ca.crt"),
    CacheTTL: time.Minute, // optional in-memory read cache
})
if err != nil {
    return err
}
defer client.Close()
```

`TLS: nil` uses an insecure connection (development only). Use `TLSFromFiles(ca)`
for one-way TLS or `MTLSFromFiles(cert, key, ca)` for mutual TLS.

## Read parameters and secrets

```go
rate, err := client.GetParameter(ctx, "/prod/payments/rate-limit")

pw, err := client.GetSecret(ctx, "/prod/payments/postgres/password")
db.Connect(pw.Value()) // []byte plaintext; pw itself prints "[REDACTED]"
```

Read options:

```go
client.GetParameter(ctx, path, paramstore.WithVersion(3))
client.GetSecret(ctx, path, paramstore.WithLabel("previous"))
client.GetSecret(ctx, path, paramstore.WithSecretToken(tok)) // token-protected / client-bound
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
order per field: **env override → store fetch → `Default` → error naming the path**.

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
    DBPassword: paramstore.SecretValue{Key: "/prod/payments/postgres/password"},
    StripeKey:  paramstore.SecretValue{Key: "/prod/payments/stripe/api-key", EnvVar: "STRIPE_KEY"},
    RateLimit:  paramstore.ParameterValue{Key: "/prod/payments/rate-limit", Dynamic: true},
}
cfg.Payments.Timeout = paramstore.ParameterValue{Key: "/prod/payments/timeout", Default: "30s"}

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

`ParameterValue{Dynamic: true}` registers on a `Subscribe` stream the SDK owns
end to end: subscribe on startup, heartbeat/ack, reconnect with jittered
backoff, resume by revision, and a 5-minute reconciliation safety net.
Applications only see values and callbacks.

```go
// Always-current handle:
limit := cfg.RateLimit.Get()

// React to changes (runs on a dedicated goroutine; a slow/panicking callback
// can never stall the stream):
cfg.RateLimit.OnChange(func(old, new string) {
    pool.Resize(mustAtoi(new))
})

// Namespace-level watch:
stop, _ := client.Watch(ctx, "/prod/payments/*", func(ev paramstore.Event) {
    log.Printf("%s %s -> %s", ev.Type, ev.Path, ev.Value)
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
`ErrFailedPrecondition`. No error ever contains secret plaintext.

## Testing against a fake

`paramstore/paramstoretest` provides an in-process, scriptable gRPC fake
(bufconn) for your own tests: set values, inject errors, and drive the Subscribe
stream (snapshots, changes, heartbeats, forced disconnects).

```go
srv, _ := paramstoretest.New()
defer srv.Close()
srv.SetParameter("/rate", "100")

client, _ := paramstore.NewClient(paramstore.Config{
    Endpoint:    srv.Target(),
    DialOptions: srv.DialOptions(),
})
```
