# kms

A self-hosted parameter store and secret management service: a single Go
binary that persists to SQLite, exposes a gRPC API for consuming
applications, serves an embedded web UI for administration, and encrypts
every secret at rest with envelope encryption. It exists to give internal
services one place to store configuration values and secrets — API keys,
database passwords, OAuth credentials, webhook signing secrets — with
versioning, namespace-scoped access control, audit logging, and hot reload,
without requiring every consumer to understand encryption or key
management.

## Features

- **Namespaces, parameters, and secrets**: a namespace is a first-class
  `(env, app)` pair (e.g. `prod/gradethis`); parameters and secrets are
  addressed by a **relative key** within it (`rate-limit`,
  `billing/stripe-key`), with the `/env/app/key` form surviving only as a
  display path and client-side SDK/CLI convenience. Values are immutably versioned, with movable labels
  (`current`, `previous`) and version promotion/rollback.
- **Envelope encryption at rest**: AES-256-GCM, one Data Encryption Key
  (DEK) per secret version, wrapped by a Key Encryption Key (KEK). Secret
  plaintext never touches SQLite, logs, metrics, or audit records.
- **Opt-in client-bound secrets**: a secret's DEK can be double-wrapped under a
  server-minted, one-time client token, so decryption requires both it and the master key —
  the server alone cannot decrypt it, even with full database and key
  access. No recovery escrow, by design; see
  [`docs/security.md`](docs/security.md#client-bound-secrets-opt-in-double-wrapping).
- **Built-in certificate authority + mTLS**: an embedded CA (Ed25519,
  KEK-wrapped private key) mints short-lived client certificates (90-day
  default) so machine clients prove possession of a key rather than merely
  presenting a token. Each namespace records its `allowed_auth_methods`
  (`mtls`/`token`); **new namespaces default to mTLS-only**.
- **Namespace-native RBAC** with deny precedence: rules are
  `{operation, env, app}` (env/app exact or `*`) over per-operation verbs (`secret:read`, `parameter:write`,
  `admin:key:rotate`, …), plus an **implicit home-namespace grant** — a
  namespace-bound identity may read and list within its own namespace with
  no policy at all. Authorization is namespace-wide; there is no per-key
  policy field.
- **Audit logging** for every secret access, write, admin action, and
  authorization denial — with secret reads failing closed if the audit
  write itself fails.
- **Hot reload, on by default**: consuming applications subscribe to keys in
  their namespace over a streaming gRPC `Subscribe` API and get pushed
  updates, with heartbeats, reconnect/resume-by-revision, and a periodic
  reconciliation poll as a safety net. Declarative SDK parameters hot-reload
  by default (opt out with `Static`). Secrets push metadata-only change
  notifications (notify-then-refetch); plaintext is never pushed over the
  stream.
- **Atomic configuration releases**: an immutable namespace-scoped release
  pins exact parameter and secret versions under stable aliases. One activation
  moves `current`/`previous`, writes one authoritative global revision, and can
  be guarded with compare-and-swap. Go and Python loaders resolve the complete
  candidate, let the application prepare it, fence stale work, and retain the
  last-known-good release on later failures. Optional immutable Draft 2020-12
  JSON Schemas validate the alias-keyed parameter object; secret values and
  per-secret tokens are never stored in a release or watch event.
- **Generated managed Go configuration**: applications declare an ordinary
  validated root type with storage, reload-policy, and consumer-view tags.
  `kms-config-gen` emits strict decoders, immutable atomic snapshots and typed
  views, a release schema, and a machine contract. Application-owned defaults
  fail closed on startup drift while explicit hot emergency overrides remain
  possible. See [`docs/managed-go-configuration.md`](docs/managed-go-configuration.md).
- **Declarative SDK config** in both Go and Python: `SecretValue`/
  `ParameterValue` fields resolved with one `Resolve`/`resolve` call — env
  override, then store, then dev default, else a startup error naming the
  missing key. See [`docs/sdk-go.md`](docs/sdk-go.md) and
  [`docs/sdk-python.md`](docs/sdk-python.md).
- **Embedded Next.js admin UI** (static export, no separate frontend
  server) for namespace management (auth methods), parameters, secrets (with
  an explicit reveal flow), policies, identities (with mTLS certificate
  issuance), audit log browsing, live subscriber visibility, and key
  metadata.
- **KEK rotation** that rewraps every non-destroyed secret version's DEK —
  and the CA's private key — under a new master key without decrypting and
  re-encrypting values, in one transaction.
- **SuhaibParameterStore migration tooling**: `parameter-store import` maps
  flat keys into an `(env, app)` namespace (`--env`/`--app`) and mints fresh
  per-secret tokens with a one-time mapping report. See
  [`docs/migration.md`](docs/migration.md).

## Architecture

```text
Consuming service
  |
  | gRPC over TLS/mTLS
  v
sdk/go/kmsclient (Go SDK)
  |
  v
parameter-store — single binary (cmd/parameter-store)
  |
  internal/cli — command dispatch: serve, init, migrate, backup,
  |              restore, create-admin, rotate-kek, import, ...
  |
  +--- grpcserver ---------------+--- httpserver -------------------+
  |    ParameterService, Secret- |    /api/v1/*  (frontend + admin) |
  |    Service, AdminService,    |    /healthz  /readyz              |
  |    WatchService, Configura-  |    embedded Next.js export        |
  |    tionReleaseService,       |                                   |
  |    ConfigurationSchemaService                                   |
  |    (gRPC clients / SDKs)     |    (frontend/out)                 |
  +-------------------------------+-----------------------------------+
                  |
                  v
  internal/core (Service)
    authN (token / mTLS) · per-namespace method gate · authZ
    (internal/policy) · audit · business logic for parameters, secrets,
    namespaces, identities, policies, KEK rotation
                  |
      +-----------+-----------+-----------------+
      v           v                             v
  internal/crypto  internal/ca            internal/watch (Hub)
    envelope        built-in CA:            change-log tailer, subscriber
    encryption,     Ed25519, issues         registry, heartbeats,
    KEK / keyring,  short-lived mTLS         replay/snapshot
    argon2id        client certs
    unseal,         (key KEK-wrapped)
    client-bound
      |             |                          |
      +-----------+-+--------------------------+
                  v
  internal/storage (SQLite, WAL mode)
    namespaces · parameters · secrets · policies · identities ·
    identity_certs · ca_keys · configuration_releases · configuration_schemas ·
    release subscriber state · audit_events · change_log · key_metadata
```

The master key (KEK) is acquired at startup from a key file or a
passphrase — see [Security model](#security-model) — and is never written
to SQLite. The embedded frontend is a Next.js static export
(`frontend/out`, produced by `npm run build`) compiled into the binary with
Go's `embed` package (`frontend_embed.go`); the Go HTTP server serves it
directly, with client-side routing fallback for deep links.

## Quickstart

### Prerequisites

- Go 1.26.5+ (see `go.mod`)
- Node.js 20.9+ (required by the pinned Next.js version)

### Build

```bash
make build   # runs the `frontend` target (npm ci && npm run build -> frontend/out)
             # then the `backend` target (go build -> bin/parameter-store)
```

`make frontend` and `make backend` are also available individually. `make
test` runs every Go test with the race detector; `make test-unit` and `make
test-integration` provide the same unit/integration split enforced by CI.
`make check-frontend` fails if `frontend/out/index.html` is missing (useful in
CI before a release build, so an empty UI never ships silently). See
[`docs/testing.md`](docs/testing.md) for all local and CI regression commands.

### Initialize and run

```bash
# Create the database and a file-based master key, plus a bootstrap admin.
./bin/parameter-store init --db ./kms.db --master-key-file ./master.key --admin ops
# -> prints the admin identity's bearer token exactly once; save it.

# Start the server (defaults: gRPC :8443, HTTP :8080, both plaintext — fine
# for local development; see docs/operations.md for TLS/mTLS in production).
KMS_KEK_FILE=./master.key KMS_SQLITE_PATH=./kms.db ./bin/parameter-store serve
```

Check readiness: `curl http://localhost:8080/readyz` (`ready`) and
`curl http://localhost:8080/healthz` (`ok`). The embedded web UI is at
`http://localhost:8080/` — log in with the admin token from `init`. Server
logs should show both `gRPC listening` and `HTTP listening`.

### Create a namespace, a client identity, and (optionally) a policy

Namespace, identity, and policy management is an admin operation, done
through the `parameter-store admin` CLI subcommands, the HTTP API (used by
the frontend), or the embedded web UI. First create the namespace the app
lives in, then a client identity bound to it:

```bash
ADMIN_TOKEN=...   # from `init --admin`; admin flags: --endpoint / --token

# Create the namespace. Omitting --auth-methods defaults to mTLS-only (the
# recommended production posture); here we also allow token auth so the
# token-based examples below work over --insecure.
./bin/parameter-store admin namespace create --env prod --app gradethis \
  --auth-methods mtls,token --endpoint localhost:8443 --insecure --token "$ADMIN_TOKEN"

# Mint both credentials for this walkthrough: the token works with the local
# plaintext server, while the certificate is the recommended production
# credential once server TLS is enabled. Save the printed one-time token as
# GRADETHIS_TOKEN; the PEM bundle is written into ./certs.
./bin/parameter-store admin identity create gradethis-be \
  --namespace prod/gradethis --auth both --ttl 2160h --out ./certs \
  --endpoint localhost:8443 --insecure --token "$ADMIN_TOKEN"

# Optional: fetch the built-in CA that issued the client certificate for
# out-of-band inspection. This is NOT the CA bundle clients use to verify the
# operator-provided server certificate.
./bin/parameter-store admin ca show --endpoint localhost:8443 --insecure > kms-client-ca.crt
```

To create a token-only identity over the HTTP API instead of the CLI command
above, use the following request and save the one-time token:

```bash
curl -s -X POST http://localhost:8080/api/v1/identities \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name": "gradethis-be", "kind": "client",
       "namespace": {"env": "prod", "app": "gradethis"}, "auth_methods": ["token"]}'
# -> {"identity": {...}, "token": "kms_..."}  (shown once — save it)
```

A namespace-bound identity may already **read and list within its own
namespace** with no policy (the implicit home-namespace grant), so a read
policy is only needed for cross-namespace access. Writes always need an
explicit rule — grant one with the namespace-wide `{operation, env, app}` shape:

```bash
curl -s -X POST http://localhost:8080/api/v1/policies \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"policy": {"name": "gradethis-write", "subject": "gradethis-be",
       "allow": [{"operation": "secret:write", "env": "prod", "app": "gradethis"}]}}'
```

Full HTTP API contract: [`docs/http-api.md`](docs/http-api.md).

### Put and get a secret

The CLI's convenience commands talk directly to a running server over
gRPC and need no separate client code:

The convenience commands take a `/env/app/key` display path (split
client-side into namespace + key):

```bash
echo -n 'sk_test_123' | ./bin/parameter-store put-secret /prod/gradethis/stripe-api-key \
  --endpoint localhost:8443 --insecure --token "$ADMIN_TOKEN"
# -> Stored /prod/gradethis/stripe-api-key version 1 (revision N)

./bin/parameter-store get-secret /prod/gradethis/stripe-api-key \
  --endpoint localhost:8443 --insecure --token "$GRADETHIS_TOKEN" --show
```

(`--insecure` skips TLS for local development only; see
[`docs/operations.md`](docs/operations.md#tls-and-mtls) for production TLS/
mTLS setup.) The equivalent from a consuming application, using the Go SDK:
the client is bound to a namespace and reads by **relative key**. This local
quickstart uses the token because the server above is plaintext:

```go
client, err := kmsclient.NewClient(kmsclient.Config{
    Endpoint:  "localhost:8443",
    Namespace: "prod/gradethis",
    Token:     os.Getenv("GRADETHIS_TOKEN"),
    Insecure:  true, // explicit cleartext opt-in for this local server only
})
if err != nil {
    log.Fatal(err)
}
defer client.Close()

secret, err := client.GetSecret(context.Background(), "stripe-api-key") // relative to prod/gradethis
if err != nil {
    log.Fatal(err)
}
// secret prints as [REDACTED]; call secret.Value() for plaintext.
fmt.Println(secret.StringValue())
```

In production, enable server TLS and prefer the issued client certificate:
set `TLS` to `kmsclient.MTLSFromFiles("certs/gradethis-be.crt",
"certs/gradethis-be.key", "server-ca.crt")` and omit `Token`. Here
`server-ca.crt` must trust the operator-provided **server** certificate; it is
not the built-in client-issuing CA returned by `admin ca show`.

See [`docs/sdk-go.md`](docs/sdk-go.md) for the full Go SDK guide, including
the declarative `SecretValue`/`ParameterValue` pattern most applications
should actually use instead of calling `GetSecret` directly.

## Configuration

YAML file (`--config FILE` / `KMS_CONFIG`) plus `KMS_*` environment
overrides; see `internal/config/config.go`. Full reference, including the
env var table, is in
[`docs/operations.md`](docs/operations.md#configuration).

```yaml
server:
  grpc_addr: "0.0.0.0:8443"
  http_addr: "0.0.0.0:8080"

storage:
  sqlite_path: "/var/lib/parameter-store/kms.db"

security:
  tls_enabled: true
  # Built-in-CA client certificates work whenever TLS is enabled. This flag
  # only adds an operator-supplied client CA to the gRPC trust pool.
  mtls_enabled: false
  server_cert_file: "/etc/parameter-store/tls/server.crt"
  server_key_file: "/etc/parameter-store/tls/server.key"
  client_ca_file: ""
  trust_proxy_headers: false

encryption:
  kek_file: "/etc/parameter-store/master.key"

frontend:
  enabled: true

audit:
  enabled: true

watch:
  heartbeat_interval: 30s
  retain_duration: 24h
  retain_rows: 10000
  release_retain_duration: 2160h
  release_retain_versions: 100
  release_subscriber_retain_duration: 720h

log:
  level: "info"
```

## Security model

Full detail: [`docs/security.md`](docs/security.md). Summary:

- **Envelope encryption**: every secret version gets its own random DEK,
  AES-256-GCM, bound to a canonical associated-data string
  (`env=…;app=…;key=…;version=…`) so ciphertext can never be replayed into
  the wrong record. The DEK is wrapped by a KEK that never touches SQLite.
- **Master key acquisition**: key file first (fully unattended restarts);
  otherwise a human passphrase, stretched with argon2id, entered via a
  no-echo TTY prompt or `KMS_MASTER_PASSPHRASE`. A stored key-check value
  makes a wrong key fail immediately and loudly at startup. If neither a
  key file nor a passphrase source is available and stdin isn't a TTY, the
  service fails fast instead of hanging a systemd unit on an invisible
  prompt.
- **Client-bound secrets** double-wrap the DEK so the server cannot decrypt
  them without a client-supplied token — defends against offline
  database-plus-key theft, not a live compromise of the running server.
  Losing the master key makes all versions unrecoverable; losing a
  client-bound token permanently loses the versions encrypted under that
  token. There is no escrow.
- **Proof of identity**: machine clients authenticate by mTLS client
  certificate from the built-in CA (identity is the cert's
  `kms://identity/<name>` URI SAN); tokens remain available where a
  namespace allows them. Each namespace's `allowed_auth_methods` gates which
  method admits a caller at all, checked before authorization.
- **Tokens**: high-entropy, server-minted, shown once, stored only as
  SHA-256 hashes (nullable for cert-only identities). Per-client identity
  tokens establish the caller; optional per-secret access tokens
  additionally gate individual secrets.
- **Authorization**: namespace-native RBAC — `{operation, env, app}` rules
  (env/app exact or `*`) over whole namespaces, explicit deny always wins over
  allow, default deny, plus an implicit home-namespace read/list grant. There
  is no per-key authorization.
- **Audit**: every secret read/write/reveal, admin action, and
  authorization denial is recorded; secret reads fail closed if the audit
  write itself fails, rather than serving a secret that couldn't be logged.

## Hot reload

Consuming applications subscribe to one or more whole namespaces over a single
long-lived gRPC `Subscribe` stream. The
server pushes an initial snapshot or a replay from the client's last-seen
revision, then live changes, interleaved with heartbeats (default 30s; 3
missed = the subscriber is dropped from the registry). The Go SDK owns the
whole lifecycle — reconnect with jittered backoff, resume by revision, and a
5-minute reconciliation poll as a safety net — so application code just
reads `value.Get()` or registers `OnChange`. Declarative parameters
hot-reload **by default** (opt out with `Static`); every non-static value in
a namespace shares one namespace-wide subscription. Secret changes are
pushed as metadata-only notifications (no plaintext over the stream); the SDK
re-fetches on request. The frontend's **Subscribers** page shows every live
subscription, its namespaces, and its last acknowledged revision. Revisions
are global, so a subscriber may appear behind because another namespace
changed; use the view as a coarse liveness/lag signal, not proof that a
specific key was applied. See [`docs/sdk-go.md`](docs/sdk-go.md#hot-reload) and
[`plan-namespaces.md`](plan-namespaces.md) §9 for the full design.

For related values that must change together, use a **configuration release**
instead of independent key callbacks. A release-aware client observes the
release version and activation revision, resolves every exact pin, verifies
digests and versions, prepares application resources, then commits only after a
fresh active-release check. The release stream records distinct `received`,
`prepared`, `applied`, and `rejected` states per process instance, grouped by
client name in the UI. This is application apply state; the ordinary namespace
subscriber acknowledgement remains transport receipt only. See
[`docs/configuration-releases.md`](docs/configuration-releases.md) and the SDK
guides. Go applications that want generated strict decoding, source-owned
defaults, typed views, and reload-policy enforcement should use the
[managed configuration layer](docs/managed-go-configuration.md).

## Frontend

The `frontend/` directory is a Next.js app built as a static export
(`output: "export"`, no server runtime) and embedded into the binary. It
covers: login (bearer token), a dashboard, namespace management (create with
`(env, app)` and allowed auth methods, per-namespace parameter/secret
counts), parameter create/edit/version history, secret
create/update/promote/disable/destroy with an explicit reveal flow (secret
values are hidden by default and require an explicit action, with
client-bound secrets showing no reveal option at all — the server cannot
produce their plaintext), policy management, identity management (namespace
binding, auth methods, one-time mTLS certificate issuance and revocation),
audit log browsing with filters, live subscriber visibility, configuration
release/schema creation, validation, diff, activation, rollback and per-instance
apply status, and a
health/status view. All dynamic data comes from the
`/api/v1/*` JSON API described in [`docs/http-api.md`](docs/http-api.md);
unknown frontend routes fall back to the exported entry HTML so
client-side routing resolves deep links on refresh.

## Client SDKs

- **Go, lower level** (`sdk/go/kmsclient`) — declarative values plus the
  atomic `ReleaseLoader`; see [`docs/sdk-go.md`](docs/sdk-go.md).
- **Go, generated managed configuration** (`sdk/go/configstore` and
  `cmd/kms-config-gen`) — strict typed group decoding, source-owned defaults,
  immutable snapshots, consumer views, and hot/restart policy; see
  [`docs/managed-go-configuration.md`](docs/managed-go-configuration.md) and
  run the self-contained
  [`examples/managed-config`](examples/managed-config) walkthrough.
- **Python** (`sdk/python`, package `kms_paramstore`, distribution
  `kms-paramstore`) — equivalent synchronous release loading with explicit
  decoding; see [`docs/sdk-python.md`](docs/sdk-python.md).

## Documentation

- [`docs/operations.md`](docs/operations.md) — running in production:
  systemd, TLS/mTLS, backup/restore, disaster recovery, KEK rotation,
  monitoring.
- [`docs/security.md`](docs/security.md) — the encryption, authentication,
  authorization, and audit model in depth.
- [`docs/sdk-go.md`](docs/sdk-go.md) — the Go client SDK.
- [`docs/managed-go-configuration.md`](docs/managed-go-configuration.md) —
  generated atomic typed configuration, defaults, and operator workflow.
- [`docs/sdk-python.md`](docs/sdk-python.md) — the Python client SDK.
- [`docs/http-api.md`](docs/http-api.md) — the JSON HTTP API contract used
  by the embedded frontend.
- [`docs/configuration-releases.md`](docs/configuration-releases.md) — release
  and schema gRPC contracts, lifecycle semantics, limits, and operational
  guarantees.
- [`docs/migration.md`](docs/migration.md) — migrating from
  SuhaibParameterStore (gradethis's prior parameter store).
- [`plan.md`](plan.md) — the original requirements/design record (historical;
  current contracts live in `README.md` and `docs/`).
- [`plan-namespaces.md`](plan-namespaces.md) — the completed namespace-native
  rewrite record.

## License

MIT — see [`LICENSE`](LICENSE).
