# Migrating gradethis from SuhaibParameterStore

This service exists to replace SuhaibParameterStore. The migration is done
when gradethis builds and runs with no dependency on
`SuhaibParameterStoreClient` (plan §33).

## Concept mapping

| SuhaibParameterStore | This service |
|---|---|
| flat key, e.g. `gradethis_TWILIO_ACCOUNT_SID` | namespaced path, e.g. `/prod/gradethis/twilio-account-sid` |
| `ParameterStoreKey` | `SecretValue.Key` / `ParameterValue.Key` (path) |
| `ParameterStoreSecret` (per-key access secret) | `SecretValue.Token` (per-secret access token) |
| `EnvironmentVariableKey` | `SecretValue.EnvVar` / `ParameterValue.EnvVar` |
| `ParameterStoreValue` (dev default) | `SecretValue.Default` / `ParameterValue.Default` |
| master password entered at startup | master key file, or passphrase (argon2id) at unseal |

The new SDK is not API-compatible with `ParameterStoreConfig` by design; the
rewrite is mechanical but the API stands on its own merits.

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

After:

```go
type Config struct {
    StripeAPIKey paramstore.SecretValue
    RateLimit    paramstore.ParameterValue
}

func Load(ctx context.Context) (*Config, error) {
    client, err := paramstore.NewClient(paramstore.Config{
        Endpoint: os.Getenv("PARAM_STORE_ENDPOINT"),
        Token:    os.Getenv("PARAM_STORE_TOKEN"), // per-client identity token
    })
    if err != nil {
        return nil, err
    }
    cfg := &Config{
        StripeAPIKey: paramstore.SecretValue{
            Key:     "/prod/gradethis/stripe-api-key",
            Token:   os.Getenv("STRIPE_API_KEY_TOKEN"), // per-secret token, from the import mapping report
            EnvVar:  "STRIPE_API_KEY",                  // env override still wins
            Default: "sk_test_dev_only",                // dev only
        },
        RateLimit: paramstore.ParameterValue{
            Key:     "/prod/gradethis/rate-limit",
            Default: "100",
            Dynamic: true, // hot-reloads; read with cfg.RateLimit.Get()
        },
    }
    if err := client.Resolve(ctx, cfg); err != nil { // hydrates every field
        return nil, err
    }
    return cfg, nil
}
```

Resolution order per field is unchanged from the old behavior: env var
override → store → default → startup error naming the missing path.

## Steps

1. **Stand up the service** and initialize it:
   ```bash
   parameter-store init --db /var/lib/parameter-store/kms.db \
       --master-key-file /etc/parameter-store/master.key --admin bootstrap
   parameter-store serve --config /etc/parameter-store/config.yaml
   ```
2. **Import** existing data (dry-run first):
   ```bash
   parameter-store import --from parameterstore.db \
       --namespace /prod/gradethis --dry-run
   parameter-store import --from parameterstore.db \
       --namespace /prod/gradethis --report tokens.csv
   ```
   The report maps every old key to its new path and freshly minted
   per-secret access token. Tokens appear only in this report — store it
   securely, distribute the tokens into gradethis config, then delete it.
3. **Create the gradethis client identity + policy**:
   ```bash
   parameter-store create-admin --db ... --name ops   # if not done at init
   # via UI or API: identity "gradethis-be" (kind client), policy allowing
   # secret:read + parameter:read on /prod/gradethis/*
   ```
4. **Dual-run**: point gradethis dev/staging at the new SDK; keep the old
   store running. Verify every value matches (compare tool / staging boot).
5. **Cut over production**, then decommission SuhaibParameterStore.

## Acceptance checklist (plan §33.4)

- [ ] gradethis builds and boots with only the new SDK.
- [ ] All imported secrets resolve to identical values during dual-run.
- [ ] Env-var overrides and dev defaults behave exactly as before.
