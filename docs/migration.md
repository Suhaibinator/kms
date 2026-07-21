# Migrating gradethis from SuhaibParameterStore

This service exists to replace SuhaibParameterStore. The migration is done
when gradethis builds and runs with no dependency on
`SuhaibParameterStoreClient` (plan §33).

## Concept mapping

A namespace is a fixed `(env, app)` pair. Flat, prefixed keys become a
namespace plus a **relative** key; the old `/prod/gradethis/...` path string
survives as a display form and optional client-side SDK/CLI convenience, never
as a server-side wire or storage field.

| SuhaibParameterStore | This service |
|---|---|
| flat key, e.g. `gradethis_TWILIO_ACCOUNT_SID` | namespace `prod/gradethis` + relative key `gradethis-twilio-account-sid` (`slug` lowercases and replaces `_` with `-`; it does not strip prefixes) |
| `ParameterStoreKey` | `SecretValue.Key` / `ParameterValue.Key` — a **relative** key (or an absolute `/env/app/key` for cross-namespace reads) |
| `ParameterStoreSecret` (per-key access secret) | `SecretValue.Token` (per-secret access token) |
| `EnvironmentVariableKey` | `SecretValue.EnvVar` / `ParameterValue.EnvVar` |
| `ParameterStoreValue` (dev default) | `SecretValue.Default` / `ParameterValue.Default` |
| application-managed `config/release` manifest | native immutable configuration release + atomic activation revision + Go/Python `ReleaseLoader` |
| master password entered at startup | master key file, or passphrase (argon2id) at unseal |
| — (client just held the store secret) | **client identity credential**: a per-client certificate bundle (mTLS, recommended) or a bearer token, presented on connect |
| — (keys existed as bare paths) | **namespace registration**: ordinary writes require the `(env, app)` namespace to exist; the offline importer creates it if absent |

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
            os.Getenv("PARAM_STORE_SERVER_CA_CERT"), // trusts the operator-provided server cert
        ),
    })
    if err != nil {
        return nil, err
    }
    cfg := &Config{
        StripeAPIKey: paramstore.SecretValue{
            Key:     "gradethis-stripe-api-key",    // importer preserves the source prefix
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

- **Keys are relative.** `Key: "gradethis-stripe-api-key"` resolves inside the client's
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

## Replacing an application-managed `config/release` manifest

If the application currently watches a parameter such as `config/release`
whose JSON body points to versions of other keys, migrate that orchestration to
a native release. Do not include the old manifest parameter itself in the new
release.

1. Inventory every alias and exact parameter/secret version. Creation can
   accept a label, but KMS resolves it before persistence; explicit versions
   make the migration artifact easiest to review.
2. Optionally register a Draft 2020-12 schema for the alias-keyed **parameter**
   object. Secrets are excluded from that object.
3. Create and validate the immutable release without activating it.
4. Deploy release-aware replicas with their existing, locally distributed
   per-secret tokens, then activate with compare-and-swap.
5. Monitor per-instance `applied` state and remove the old manifest watcher
   only after the dual-run window succeeds.

```yaml
# runtime-release.yaml
namespace: prod/gradethis
name: runtime
schema_id: gradethis/runtime
schema_version: 1
entries:
  - {alias: permissions, kind: parameter, key: config/groups/permissions, version: 12}
  - {alias: rate_limits, kind: parameter, key: config/groups/rate-limits, version: 8}
  - {alias: db_password, kind: secret, key: db-password, version: 3}
  - {alias: session_signing_key, kind: secret, key: session-token-secret, version: 7}
```

```bash
parameter-store release create runtime-release.yaml \
  --endpoint "$PARAM_STORE_ENDPOINT" --token "$ADMIN_TOKEN" \
  --ca "$PARAM_STORE_SERVER_CA_CERT"
parameter-store release validate prod/gradethis runtime 1 \
  --endpoint "$PARAM_STORE_ENDPOINT" --token "$ADMIN_TOKEN" \
  --ca "$PARAM_STORE_SERVER_CA_CERT"
parameter-store release activate prod/gradethis runtime 1 \
  --expected-current-version 0 \
  --endpoint "$PARAM_STORE_ENDPOINT" --token "$ADMIN_TOKEN" \
  --ca "$PARAM_STORE_SERVER_CA_CERT"
```

The Go process replaces manifest watching, parallel ad hoc reads, and apply
bookkeeping with one loader:

```go
loader, err := paramstore.NewReleaseLoader(client, paramstore.ReleaseLoaderConfig{
    Name: "runtime",
    SecretTokenProvider: func(alias, path string) (string, bool) {
        token, ok := bootstrapSecretTokens[alias]
        return token, ok
    },
})
if err != nil { return err }
return loader.Run(ctx, func(ctx context.Context, snapshot paramstore.ReleaseSnapshot) (
    paramstore.PreparedRelease, error,
) {
    return decodeValidateAndPrepare(ctx, snapshot)
})
```

Python uses `ReleaseLoader(client, ReleaseLoaderConfig(name="runtime", ...))`
and synchronous `loader.run(prepare)` with the same resolution, cancellation,
prepare/commit/abort, last-known-good, and acknowledgement guarantees. Both
SDKs decode explicitly; native releases do not use reflection or generate
schemas from application types.

Roll back by reactivating any retained immutable version:

```bash
parameter-store release rollback prod/gradethis runtime 1 \
  --endpoint "$PARAM_STORE_ENDPOINT" --token "$ADMIN_TOKEN" \
  --ca "$PARAM_STORE_SERVER_CA_CERT"
```

Use the Releases frontend or `parameter-store release subscribers
prod/gradethis runtime` until every expected instance reports the target as
`applied`. Replicas apply independently—version 1 has no fleet-wide barrier.
An activation racing immediately after a loader's final active read is handled
as the next candidate, so do not treat activation as a distributed commit.

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
   #   parameter-store admin identity create gradethis-be \
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
   (`gradethis_STRIPE_API_KEY` → `gradethis-stripe-api-key`):
   ```bash
   parameter-store import --from parameterstore.db \
       --env prod --app gradethis --dry-run
   parameter-store import --from parameterstore.db \
       --env prod --app gradethis --report tokens.csv
   ```
   A dry-run report contains only the old-key → display-path mapping. A real
   import additionally includes each freshly minted per-secret access token;
   store that report securely, distribute the tokens into gradethis config,
   then delete it. (Import flags: [`operations.md`](operations.md).)
4. **Dual-run**: point gradethis dev/staging at the new SDK; keep the old
   store running. Verify every value matches (compare tool / staging boot).
5. **Cut over production**, then decommission SuhaibParameterStore.

## Acceptance checklist (plan §33.4)

- [ ] gradethis builds and boots with only the new SDK.
- [ ] The `prod/gradethis` namespace exists and gradethis's identity is bound
      to it (app config is endpoint + credential only).
- [ ] All imported secrets resolve to identical values during dual-run.
- [ ] Env-var overrides and dev defaults behave exactly as before.
