# Migrating gradethis from SuhaibParameterStore

This service exists to replace SuhaibParameterStore. The migration is done
when gradethis builds and runs with no dependency on
`SuhaibParameterStoreClient` (plan §33).

## Concept mapping

A namespace is a fixed `(env, app)` pair. Flat, prefixed keys become a
namespace plus a **relative** key; the old `/prod/gradethis/...` path string
survives only as a display form, never as something you configure.

| SuhaibParameterStore | This service |
|---|---|
| flat key, e.g. `gradethis_TWILIO_ACCOUNT_SID` | namespace `prod/gradethis` + relative key `twilio-account-sid` |
| `ParameterStoreKey` | `SecretValue.Key` / `ParameterValue.Key` — a **relative** key (or an absolute `/env/app/key` for cross-namespace reads) |
| `ParameterStoreSecret` (per-key access secret) | `SecretValue.Token` (per-secret access token) |
| `EnvironmentVariableKey` | `SecretValue.EnvVar` / `ParameterValue.EnvVar` |
| `ParameterStoreValue` (dev default) | `SecretValue.Default` / `ParameterValue.Default` |
| master password entered at startup | master key file, or passphrase (argon2id) at unseal |
| — (client just held the store secret) | **client identity credential**: a per-client certificate bundle (mTLS, recommended) or a bearer token, presented on connect |
| — (keys existed as bare paths) | **namespace registration**: the `(env, app)` namespace must exist before any parameter/secret is written into it |

The new SDK is not API-compatible with `ParameterStoreConfig` by design; the
rewrite is mechanical but the API stands on its own merits. The two new rows
are the substantive additions: a machine client now proves its identity with
a certificate (or token) rather than possessing a per-key secret, and
resources live inside a namespace that has to be created first.

## Before / after: a typical gradethis config field

Before (`gradethis/be/config/config.go`):

```go
type Config struct {
    StripeAPIKey parameterstoreconfig.ParameterStoreConfig
}

func (c *Config) Init() {
    c.StripeAPIKey = parameterstoreconfig.ParameterStoreConfig{
        ParameterStoreKey:      "gradethis_STRIPE_API_KEY",
        ParameterStoreSecret:   os.Getenv("GRADETHIS_STRIPE_API_KEY_SECRET"),
        EnvironmentVariableKey: "STRIPE_API_KEY",
        ParameterStoreValue:    "sk_test_dev_only",
    }
    c.StripeAPIKey.Init(c.CommonConfig.ParamStoreClient)
}
```

After — the recommended posture is a **cert-only** client. The certificate
proves possession, and the server derives the identity (and, because the
identity is namespace-bound, the namespace) from it, so application config is
just the endpoint plus the credential — no token and no namespace string in
code:

```go
type Config struct {
    StripeAPIKey paramstore.SecretValue
    RateLimit    paramstore.ParameterValue
}

func Load(ctx context.Context) (*Config, error) {
    client, err := paramstore.NewClient(paramstore.Config{
        Endpoint: os.Getenv("PARAM_STORE_ENDPOINT"),
        // Cert-only identity (plan §7): the client certificate authenticates,
        // and the namespace is discovered from the bound identity via WhoAmI.
        // No Token, no Namespace needed here.
        TLS: paramstore.MTLSFromFiles(
            os.Getenv("PARAM_STORE_CLIENT_CERT"),
            os.Getenv("PARAM_STORE_CLIENT_KEY"),
            os.Getenv("PARAM_STORE_CA_CERT"),
        ),
    })
    if err != nil {
        return nil, err
    }
    cfg := &Config{
        StripeAPIKey: paramstore.SecretValue{
            Key:     "stripe-api-key",              // relative to prod/gradethis
            Token:   os.Getenv("STRIPE_API_KEY_TOKEN"), // per-secret token if the secret requires one (from the import report)
            EnvVar:  "STRIPE_API_KEY",              // env override still wins
            Default: "sk_test_dev_only",            // dev only
        },
        RateLimit: paramstore.ParameterValue{
            Key:     "rate-limit",                  // relative
            Default: "100",
            // Hot-reloads by default; read with cfg.RateLimit.Get().
            // Set Static: true for a boot-time-only read.
        },
    }
    if err := client.Resolve(ctx, cfg); err != nil { // hydrates every field
        return nil, err
    }
    return cfg, nil
}
```

Notes:

- **Keys are relative.** `Key: "stripe-api-key"` resolves inside the client's
  namespace. Use an absolute `Key: "/staging/gradethis/rate-limit"` only to
  reach another namespace (subject to policy).
- **Token is optional.** A token-authenticated client instead sets
  `Token: os.Getenv("PARAM_STORE_TOKEN")` and no `TLS` cert — but that only
  works where the namespace's allowed auth methods include `"token"` (new
  namespaces are mTLS-only by default; see below).
- **Namespace is optional in config.** Leave `Config.Namespace` empty to
  discover it from the identity; set it explicitly (`Namespace:
  "prod/gradethis"`) if the identity is unbound or you want to skip the
  `WhoAmI` round trip. A relative key on a client with neither a namespace
  nor a bound identity fails fast with `ErrNoNamespace`, naming the key.
- **Hot reload is on by default.** `ParameterValue` fields track the store at
  runtime; there is no `Dynamic` flag anymore. Opt out per field with
  `Static: true`.

Resolution order per field is unchanged from the old behavior: env var
override → store → default → startup error naming the missing key.

The Python SDK is the mirror image of this (`namespace="prod/gradethis"`
optional, `SecretValue("stripe-api-key")`, `ParameterValue("rate-limit")`
hot-reloading by default) — see [`sdk-python.md`](sdk-python.md).

## Steps

1. **Stand up the service** and initialize it:
   ```bash
   parameter-store init --db /var/lib/parameter-store/kms.db \
       --master-key-file /etc/parameter-store/master.key --admin bootstrap
   parameter-store serve --config /etc/parameter-store/config.yaml
   ```
2. **Create the namespace and the gradethis client identity.** The namespace
   must exist before anything is written into it, and new namespaces default
   to **mTLS-only**. Create `prod/gradethis`, then a client identity **bound
   to it** with a certificate bundle:
   ```bash
   # namespace prod/gradethis (mTLS-only by default), then a bound client
   # identity "gradethis-be" issued a cert bundle (key returned once).
   #   parameter-store admin namespace create --env prod --app gradethis
   #   parameter-store admin identity create --name gradethis-be \
   #       --namespace prod/gradethis --auth mtls --ttl 90d --out ./gradethis-be/
   ```
   Because the identity is bound to the namespace, it may read secrets and
   parameters there with **no policy** (the implicit home-namespace grant), so
   gradethis config stays "endpoint + credential" only. Writes and any
   cross-namespace access still require explicit policy. Add `"token"` to the
   namespace's allowed methods only if a token-authenticated client is
   genuinely required. (Command detail lives in [`operations.md`](operations.md).)
3. **Import** existing data (dry-run first). Import now maps flat keys into a
   namespace via `--env`/`--app`; each old key becomes a relative key
   (`gradethis_STRIPE_API_KEY` → `stripe-api-key`):
   ```bash
   parameter-store import --from parameterstore.db \
       --env prod --app gradethis --dry-run
   parameter-store import --from parameterstore.db \
       --env prod --app gradethis --report tokens.csv
   ```
   The report maps every old key to its new relative key and freshly minted
   per-secret access token. Tokens appear only in this report — store it
   securely, distribute the tokens into gradethis config, then delete it.
   (Import flags and mapping-file support: [`operations.md`](operations.md).)
4. **Dual-run**: point gradethis dev/staging at the new SDK; keep the old
   store running. Verify every value matches (compare tool / staging boot).
5. **Cut over production**, then decommission SuhaibParameterStore.

## Acceptance checklist (plan §33.4)

- [ ] gradethis builds and boots with only the new SDK.
- [ ] The `prod/gradethis` namespace exists and gradethis's identity is bound
      to it (app config is endpoint + credential only).
- [ ] All imported secrets resolve to identical values during dual-run.
- [ ] Env-var overrides and dev defaults behave exactly as before.
