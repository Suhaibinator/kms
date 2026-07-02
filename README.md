# kms

A self-hosted parameter store and secret management service: a single Go
binary that persists to SQLite, exposes a gRPC API for consuming
applications, serves an embedded web UI for administration, and encrypts
every secret at rest with envelope encryption. It exists to give internal
services one place to store configuration values and secrets — API keys,
database passwords, OAuth credentials, webhook signing secrets — with
versioning, path-based access control, audit logging, and hot reload,
without requiring every consumer to understand encryption or key
management.

## Features

- **Parameters and secrets**, both path-addressed (`/prod/gradethis/rate-limit`)
  and immutably versioned, with movable labels (`current`, `previous`) and
  version promotion/rollback.
- **Envelope encryption at rest**: AES-256-GCM, one Data Encryption Key
  (DEK) per secret version, wrapped by a Key Encryption Key (KEK). Secret
  plaintext never touches SQLite, logs, metrics, or audit records.
- **Opt-in client-bound secrets**: a secret's DEK can be double-wrapped so
  decryption requires both the master key *and* a client-supplied token —
  the server alone cannot decrypt it, even with full database and key
  access. No recovery escrow, by design; see
  [`docs/security.md`](docs/security.md#client-bound-secrets-opt-in-double-wrapping).
- **Path-based RBAC** with deny precedence, exact and prefix (`/prod/*`)
  path patterns, and per-operation policy rules
  (`secret:read`, `parameter:write`, `admin:key:rotate`, …).
- **Bearer-token authentication**: per-client identity tokens plus optional
  per-secret access tokens; only token hashes are ever stored.
- **Audit logging** for every secret access, write, admin action, and
  authorization denial — with secret reads failing closed if the audit
  write itself fails.
- **Hot reload**: consuming applications subscribe to parameter paths over
  a streaming gRPC `Subscribe` API and get pushed updates, with heartbeats,
  reconnect/resume-by-revision, and a periodic reconciliation poll as a
  safety net. Secrets push metadata-only change notifications
  (notify-then-refetch); plaintext is never pushed over the stream.
- **Declarative SDK config** in both Go and Python: `SecretValue`/
  `ParameterValue` fields resolved with one `Resolve`/`resolve` call — env
  override, then store, then dev default, else a startup error naming the
  missing path. See [`docs/sdk-go.md`](docs/sdk-go.md) and
  [`docs/sdk-python.md`](docs/sdk-python.md).
- **Embedded Next.js admin UI** (static export, no separate frontend
  server) for namespaces, parameters, secrets (with an explicit reveal
  flow), policies, identities, audit log browsing, live subscriber
  visibility, and key metadata.
- **KEK rotation** that rewraps every secret's DEK under a new master key
  without decrypting and re-encrypting values, in one transaction.
- **SuhaibParameterStore migration tooling**: `parameter-store import` maps
  flat keys into namespaced paths and mints fresh per-secret tokens with a
  one-time mapping report. See [`docs/migration.md`](docs/migration.md).

## Architecture

```text
Consuming service
  |
  | gRPC over TLS/mTLS
  v
sdk/go/paramstore (Go SDK)
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
  |    WatchService              |    embedded Next.js export        |
  |    (gRPC clients / SDKs)     |    (frontend/out)                 |
  +-------------------------------+-----------------------------------+
                  |
                  v
  internal/core (Service)
    authN/authZ (internal/policy) · audit · business logic for
    parameters, secrets, namespaces, identities, policies, KEK rotation
                  |
      +-----------+-----------+
      v                       v
  internal/crypto         internal/watch (Hub)
    envelope encryption,    change-log tailer, subscriber registry,
    KEK / keyring, argon2id heartbeats, replay/snapshot
    unseal, client-bound
      |                       |
      +-----------+-----------+
                  v
  internal/storage (SQLite, WAL mode)
    parameters · secrets · policies · identities ·
    audit_events · change_log · key_metadata
```

The master key (KEK) is acquired at startup from a key file or a
passphrase — see [Security model](#security-model) — and is never written
to SQLite. The embedded frontend is a Next.js static export
(`frontend/out`, produced by `npm run build`) compiled into the binary with
Go's `embed` package (`frontend_embed.go`); the Go HTTP server serves it
directly, with client-side routing fallback for deep links.

## Quickstart

### Prerequisites

- Go 1.26+ (see `go.mod`)
- Node.js (to build the frontend export; see `frontend/package.json`)

### Build

```bash
make build   # runs the `frontend` target (npm ci && npm run build -> frontend/out)
             # then the `backend` target (go build -> bin/parameter-store)
```

`make frontend` and `make backend` are also available individually; `make
test` runs the full test suite with the race detector, and
`make check-frontend` fails if `frontend/out/index.html` is missing (useful
in CI before a release build, so an empty UI never ships silently).

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

### Create a client identity and a policy

Identity and policy management is an admin operation, done through the
HTTP API (used by the frontend) or the embedded web UI. Using the admin
token from `init` above:

```bash
ADMIN_TOKEN=...   # from `init --admin`

# Create a client identity for a consuming application.
curl -s -X POST http://localhost:8080/api/v1/identities \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name": "gradethis-be", "kind": "client"}'
# -> {"identity": {...}, "token": "kms_..."}  (shown once — save it)

# Grant it read access to its namespace.
curl -s -X POST http://localhost:8080/api/v1/policies \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"policy": {"name": "gradethis-read", "subject": "gradethis-be",
       "allow": [{"operation": "secret:read", "path": "/prod/gradethis/*"},
                 {"operation": "parameter:read", "path": "/prod/gradethis/*"}]}}'
```

Full HTTP API contract: [`docs/http-api.md`](docs/http-api.md).

### Put and get a secret

The CLI's convenience commands talk directly to a running server over
gRPC and need no separate client code:

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

```go
client, err := paramstore.NewClient(paramstore.Config{
    Endpoint: "localhost:8443",
    Token:    os.Getenv("GRADETHIS_TOKEN"), // the "gradethis-be" token from above
})
if err != nil {
    log.Fatal(err)
}
defer client.Close()

secret, err := client.GetSecret(context.Background(), "/prod/gradethis/stripe-api-key")
if err != nil {
    log.Fatal(err)
}
// secret prints as [REDACTED]; call secret.Value() for plaintext.
fmt.Println(secret.StringValue())
```

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
  mtls_enabled: true
  server_cert_file: "/etc/parameter-store/tls/server.crt"
  server_key_file: "/etc/parameter-store/tls/server.key"
  client_ca_file: "/etc/parameter-store/tls/client-ca.crt"
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

log:
  level: "info"
```

## Security model

Full detail: [`docs/security.md`](docs/security.md). Summary:

- **Envelope encryption**: every secret version gets its own random DEK,
  AES-256-GCM, bound to a canonical associated-data string
  (`namespace/path/version`) so ciphertext can never be replayed into the
  wrong record. The DEK is wrapped by a KEK that never touches SQLite.
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
  Losing the master key or a client-bound secret's token is a **permanent,
  unrecoverable loss by design** — there is no escrow.
- **Tokens**: high-entropy, server-minted, shown once, stored only as
  SHA-256 hashes. Per-client identity tokens establish the caller; optional
  per-secret access tokens additionally gate individual secrets.
- **Authorization**: path-based RBAC, exact or `/*`-prefix path patterns,
  explicit deny always wins over allow, default deny.
- **Audit**: every secret read/write/reveal, admin action, and
  authorization denial is recorded; secret reads fail closed if the audit
  write itself fails, rather than serving a secret that couldn't be logged.

## Hot reload

Consuming applications subscribe to parameter paths (exact or `/prefix/*`)
over a single long-lived gRPC `Subscribe` stream. The server pushes an
initial snapshot or a replay from the client's last-seen revision, then
live changes, interleaved with heartbeats (default 30s; 3 missed = the
subscriber is dropped from the registry). The Go SDK owns the whole
lifecycle — reconnect with jittered backoff, resume by revision, and a
5-minute reconciliation poll as a safety net — so application code just
reads `value.Get()` or registers `OnChange`. Secret changes are pushed as
metadata-only notifications (no plaintext over the stream); the SDK
re-fetches on request. The frontend's **Subscribers** page shows every live
application, what it watches, and whether it has applied the latest
revision — the operational way to confirm a config change actually
propagated. See [`docs/sdk-go.md`](docs/sdk-go.md#hot-reload) and
plan.md §8.3–§8.4 for the full design.

## Frontend

The `frontend/` directory is a Next.js app built as a static export
(`output: "export"`, no server runtime) and embedded into the binary. It
covers: login (bearer token), a dashboard, namespace browsing, parameter
create/edit/version history, secret create/update/promote/disable/destroy
with an explicit reveal flow (secret values are hidden by default and
require an explicit action, with client-bound secrets showing no reveal
option at all — the server cannot produce their plaintext), policy and
identity management, audit log browsing with filters, live subscriber
visibility, and a health/status view. All dynamic data comes from the
`/api/v1/*` JSON API described in [`docs/http-api.md`](docs/http-api.md);
unknown frontend routes fall back to the exported entry HTML so
client-side routing resolves deep links on refresh.

## Client SDKs

- **Go** (`sdk/go/paramstore`) — see [`docs/sdk-go.md`](docs/sdk-go.md).
- **Python** (`sdk/python`, package `kms_paramstore`, distribution
  `kms-paramstore`) — see [`docs/sdk-python.md`](docs/sdk-python.md).

## Documentation

- [`docs/operations.md`](docs/operations.md) — running in production:
  systemd, TLS/mTLS, backup/restore, disaster recovery, KEK rotation,
  monitoring.
- [`docs/security.md`](docs/security.md) — the encryption, authentication,
  authorization, and audit model in depth.
- [`docs/sdk-go.md`](docs/sdk-go.md) — the Go client SDK.
- [`docs/sdk-python.md`](docs/sdk-python.md) — the Python client SDK.
- [`docs/http-api.md`](docs/http-api.md) — the JSON HTTP API contract used
  by the embedded frontend.
- [`docs/migration.md`](docs/migration.md) — migrating from
  SuhaibParameterStore (gradethis's prior parameter store).
- [`plan.md`](plan.md) — the full requirements and design document this
  project implements.

## License

MIT — see [`LICENSE`](LICENSE).
