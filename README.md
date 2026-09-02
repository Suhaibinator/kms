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
- **Application-owned configuration contracts**: an application sits above its
  environment namespaces and owns one canonical release name, optional schema
  pin, and alias/kind/content-type contract. `dev/payments`, `prod/payments`,
  and `prod-gcp/payments` keep independent values and versions while release
  creation and activation reject shape drift. The Applications dashboard shows
  a cross-environment value matrix and can create independent parameter
  versions in several selected environments from one reviewed action.
- **Envelope encryption at rest**: AES-256-GCM, one Data Encryption Key
  (DEK) per secret version, wrapped by a Key Encryption Key (KEK). Secret
  plaintext never touches SQLite, logs, metrics, or audit records.
- **Opt-in client-bound secrets**: a secret's DEK can be double-wrapped under a
  server-minted, one-time client token, so decryption requires both it and the master key —
  the server alone cannot decrypt it, even with full database and key
  access. No recovery escrow, by design; see
  [`docs/security.md`](docs/security.md#client-bound-secrets-opt-in-double-wrapping).
- **Built-in certificate authority + mTLS**: an embedded CA (Ed25519,
  KEK-wrapped private key) created by `init` mints short-lived client
  certificates (90-day default) so machine clients prove possession of a key
  rather than merely presenting a token. Each namespace records its
  `allowed_auth_methods` (`mtls`/`token`); **new namespaces default to
  mTLS-only**. Administrators additionally need a certificate of their own
  (ECDSA P-256, importable into a browser) alongside their token; theirs is
  issued only offline on the KMS host.
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
  last-known-good release on later failures. The TypeScript SDK provides the
  same exact-version, digest, supersession, acknowledgement, and
  last-known-good lifecycle for Node.js services. Optional immutable Draft 2020-12
  JSON Schemas validate the alias-keyed parameter object; secret values and
  per-secret tokens are never stored in a release or watch event.
- **Generated managed Go configuration**: applications declare an ordinary
  validated root type with storage, reload-policy, and consumer-view tags.
  `kms-config-gen` emits strict decoders, immutable atomic snapshots and typed
  views, a release schema, and a machine contract. Application-owned defaults
  are applied on startup drift while the divergence is surfaced through
  callbacks, status, logs, and metrics; use value-free defaults verification as
  the release gate. See
  [`docs/managed-go-configuration.md`](docs/managed-go-configuration.md).
- **Declarative SDK config** in Go, Python, and TypeScript: `SecretValue`/
  `ParameterValue` fields resolved with one `Resolve`/`resolve` call — env
  override, then store, then dev default, else a startup error naming the
  missing key. See [`docs/sdk-go.md`](docs/sdk-go.md),
  [`docs/sdk-python.md`](docs/sdk-python.md), and
  [`sdk/typescript/README.md`](sdk/typescript/README.md).
- **Embedded Next.js admin UI** (static export, no separate frontend
  server) for application contracts and cross-environment value management,
  namespace/environment management (auth methods), parameters, secrets (with
  an explicit reveal flow), policies, identities (with mTLS certificate
  issuance), audit log browsing, live subscriber visibility, and key
  metadata.
- **KEK rotation** that rewraps every non-destroyed secret version's DEK —
  and the CA's private key — under a new master key without decrypting and
  re-encrypting values, in one transaction.
- **Prometheus metrics on `/metrics`**, on by default: authentication,
  authorization, rate-limit, audit, and release counters plus gRPC/HTTP
  latency, watch fan-out lag, key age, and certificate-expiry gauges. Every
  label is a closed set fixed in code — never a namespace, identity, key,
  client, IP, or request ID — so the unauthenticated endpoint reveals how much
  is happening, never what. A scrape never queries the database. Ships with
  alerting rules ([`deploy/prometheus/alerts.yml`](deploy/prometheus/alerts.yml))
  and a `parameter-store healthcheck` command the container image wires to
  Docker's `HEALTHCHECK`. See
  [`docs/operations.md`](docs/operations.md#prometheus-metrics).
- **Scriptable CLI**: every command accepts `--output json` (or
  `KMS_OUTPUT=json`) and then puts exactly one document on stdout, with status
  lines, warnings, and prompts on stderr. Failures carry classified exit codes
  — `2` usage, `3` unauthenticated, `4` permission denied, `5` not found, `6`
  conflict, `7` failed precondition, `8` unavailable, `9` rate limited — so a
  script branches without parsing messages. Credentials can come from an
  owner-only file (`--token-file`, `--secret-token-file`) instead of an
  argument every local user can read in `ps`. Irreversible commands confirm,
  either by retyping the target or `[y/N]`, and refuse to run on a
  non-interactive stdin without `--yes`. `whoami` reports the identity the
  server resolves from the credential you presented. See
  [`docs/operations.md`](docs/operations.md#global-flags-output-formats-and-exit-codes).
- **Store values in any process's environment**: `parameter-store exec ENV/APP
  -- CMD` resolves a namespace's parameters and secrets, maps them to
  environment variables, and replaces itself with `CMD`, so a workload that
  cannot link an SDK — a shell script, a third-party binary, a container
  entrypoint — still reads from the store. `--release NAME` pins and verifies
  the active release's exact versions and digests; `env` prints the same
  variables as dotenv, `export`, JSON, or YAML for `source <(...)` or a systemd
  `EnvironmentFile=`. See
  [`docs/operations.md`](docs/operations.md#run-any-process-with-store-values).
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
client SDK (Go · Python · TypeScript)
  |
  v
parameter-store — single binary (cmd/parameter-store)
  |
  internal/cli — command dispatch: serve, init, migrate, backup,
  |              restore, create-admin, rotate-admin, rotate-kek, import, ...
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

- Go 1.27+ (see `go.mod`)
- Node.js 20.9+ (required by the pinned Next.js version)
- Node.js 22+ when building or consuming the independently published
  TypeScript SDK

### Build

```bash
make build   # runs the `frontend` target (npm ci && npm run build -> frontend/out)
             # then the `backend` target (go build -> bin/parameter-store)
```

`make frontend` and `make backend` are also available individually. `make
test` runs every Go test with the race detector; `make test-unit` and `make
test-integration` provide the same unit/integration split enforced by CI.
For the Node SDK, `make typescript`, `make test-typescript`, and `make
check-typescript` install from its committed lockfile and run its build,
tests/consumer type checks, or complete release gate respectively.
`make check-frontend` fails if `frontend/out/index.html` is missing (useful in
CI before a release build, so an empty UI never ships silently). See
[`docs/testing.md`](docs/testing.md) for all local and CI regression commands.

### Install a release

Stable releases publish native `parameter-store` and `kms-config-gen` archives,
the Python SDK, the TypeScript SDK, and a multi-platform container image through
GitHub. For example:

```bash
VERSION=0.1.15 # choose a release containing RELEASE_COMPLETE.json

# Container image (Linux amd64/arm64).
docker pull "ghcr.io/suhaibinator/kms:${VERSION}"

# Python SDK wheel from the GitHub Release.
python -m pip install \
  "https://github.com/Suhaibinator/kms/releases/download/v${VERSION}/kms_paramstore-${VERSION}-py3-none-any.whl"
```

GitHub's npm registry requires an authenticated `@suhaibinator` scope before
`npm install @suhaibinator/kms`. See [`docs/releasing.md`](docs/releasing.md)
for binary downloads, checksum and provenance verification, npm setup,
container initialization, and the maintainer release procedure.

### Initialize and run locally

```bash
# Every setting has a flag, a KMS_* variable, and a config key; init and serve
# resolve them the same way, so export the paths once and both commands agree.
export KMS_SQLITE_PATH=./kms.db KMS_KEK_FILE=./master.key

# Create the database, a file-based master key, the built-in CA, and a
# bootstrap admin.
./bin/parameter-store init --admin ops
# -> Initialized database at /abs/path/kms.db (source: env KMS_SQLITE_PATH)
# -> Built-in CA: ready
# -> prints the admin identity's bearer token exactly once; save it.
# Add --cert-dir ./admin-creds to also issue the admin's client certificate,
# which production requires alongside the token (see below).

# Start a plaintext development server on loopback only. Production requires
# TLS; see "Connect a production application with mTLS" below.
KMS_GRPC_ADDR=127.0.0.1:8443 KMS_HTTP_ADDR=127.0.0.1:8080 \
  ./bin/parameter-store serve
```

Flags work just as well as the variables — `init --sqlite-path ./kms.db
--kek-file ./master.key --admin ops` is the same run — and a flag always wins
over the environment and the config file. `parameter-store config show` prints
the effective configuration with the source of every value.

If `init` fails with `unsafe destination spelling ... is group- or
other-writable without the sticky bit`, the directory chain is group-writable —
the `umask 002` default on Debian and Ubuntu. Run `chmod g-w` on each flagged
ancestor, or keep the data outside that tree:

```bash
install -d -m 0700 ~/.local/share/parameter-store
./bin/parameter-store init --sqlite-path ~/.local/share/parameter-store/kms.db \
  --kek-file ~/.local/share/parameter-store/master.key --admin ops
```

See [Preparing a destination directory](docs/operations.md#preparing-a-destination-directory)
for the full rules.

Check readiness: `curl http://localhost:8080/readyz` (`ready`) and
`curl http://localhost:8080/healthz` (`ok`). The embedded web UI is at
`http://localhost:8080/` — log in with the admin token from `init`. Server
logs should show both `gRPC listening` and `HTTP listening`.

This development server also logs `security.admin_require_client_cert cannot
be enforced without TLS`. That is expected: an admin normally needs a **client
certificate in addition to its token**, and without TLS no certificate can
reach the server, so the requirement is relaxed and the token alone signs you
in. On a TLS deployment, issue each admin a certificate with
[`admin-cert issue`](docs/operations.md#admin-credentials-and-browser-setup)
before they can use the console or the `admin` CLI.

### Local development: connect with a token

Namespace, identity, and policy management is an admin operation, done
through the `parameter-store admin` CLI subcommands, the HTTP API (used by
the frontend), or the embedded web UI. For the loopback-only plaintext server
above, create an explicitly token-enabled development namespace and a
token-only identity:

```bash
ADMIN_TOKEN=...   # from `init --admin`; admin flags: --endpoint / --token
                  # (or export KMS_ENDPOINT / KMS_TOKEN and omit both)

./bin/parameter-store admin namespace create --env dev --app gradethis \
  --auth-methods token --endpoint localhost:8443 --insecure --token "$ADMIN_TOKEN"

./bin/parameter-store admin identity create gradethis-local \
  --namespace dev/gradethis --auth token \
  --endpoint localhost:8443 --insecure --token "$ADMIN_TOKEN"
# -> prints the one-time identity token; save it as GRADETHIS_TOKEN
```

A namespace-bound identity may already **read and list within its own
namespace** with no policy (the implicit home-namespace grant), so a read
policy is only needed for cross-namespace access. No policy is needed for this
read-only application walkthrough: seed the value with the admin credential
below. If an application must write, grant its identity an explicit
namespace-wide `{operation, env, app}` rule in the frontend or through the
[HTTP API](docs/http-api.md).

### Put and get a secret

The CLI's convenience commands talk directly to a running server over
gRPC and need no separate client code:

The convenience commands take a `/env/app/key` display path (split
client-side into namespace + key):

```bash
echo -n 'sk_test_123' | ./bin/parameter-store put-secret /dev/gradethis/stripe-api-key \
  --endpoint localhost:8443 --insecure --token "$ADMIN_TOKEN"
# -> Stored /dev/gradethis/stripe-api-key version 1 (revision N)

./bin/parameter-store get-secret /dev/gradethis/stripe-api-key \
  --endpoint localhost:8443 --insecure --token "$GRADETHIS_TOKEN" --show
```

A process that cannot link an SDK can still read from the store: `exec`
resolves the namespace and hands the values to the command as environment
variables (`stripe-api-key` becomes `STRIPE_API_KEY`), and `env` prints the
same set instead of running anything.

```bash
./bin/parameter-store exec dev/gradethis --insecure --token "$GRADETHIS_TOKEN" \
  -- printenv STRIPE_API_KEY
# -> sk_test_123

# In production, pin the active release's exact, digest-verified versions:
parameter-store exec prod/gradethis --release runtime --strict -- ./server
```

The equivalent from a consuming application using the Go SDK is below. This
local example deliberately opts into cleartext and token authentication; do
not carry either setting into a deployed service:

```go
client, err := kmsclient.NewClient(kmsclient.Config{
    Endpoint:  "localhost:8443",
    Namespace: "dev/gradethis",
    Token:     os.Getenv("GRADETHIS_TOKEN"),
    Insecure:  true, // explicit cleartext opt-in for this local server only
})
if err != nil {
    log.Fatal(err)
}
defer client.Close()

secret, err := client.GetSecret(context.Background(), "stripe-api-key") // relative to dev/gradethis
if err != nil {
    log.Fatal(err)
}
// secret prints as [REDACTED]; call secret.Value() for plaintext.
fmt.Println(secret.StringValue())
```

See [`docs/sdk-go.md`](docs/sdk-go.md) for the full Go SDK guide, including
the declarative `SecretValue`/`ParameterValue` pattern most applications
should actually use instead of calling `GetSecret` directly.

### Connect a production application with mTLS

Production uses four distinct certificate roles. Keeping the direction of
trust clear prevents the most common setup mistake:

| Certificate role | Created and stored by | Used for |
|---|---|---|
| **KMS server certificate/key + server trust CA** | The operator obtains the serving certificate from the organization's PKI or another trusted CA, configures `server_cert_file`/`server_key_file`, and distributes a `server-ca.crt` trust bundle to applications. | KMS presents the serving certificate; applications use `server-ca.crt` to verify that they reached the real KMS server. The server private key stays on the KMS host. |
| **KMS built-in client-issuing CA** | `parameter-store init` creates this self-signed CA and stores it in SQLite's `ca_keys` table; the private key is KEK-wrapped. | KMS issues and verifies application client certificates **and administrator certificates**. Applications do **not** use this CA to verify the server. Its public certificate can be exported with `admin ca show` for diagnostics or out-of-band verification only. |
| **Per-application client certificate/key** | KMS creates one when an mTLS identity is enrolled; its serial/fingerprint enrollment record remains in SQLite and the one-time PEM files go to the operator. | The application presents the certificate and proves possession of its private key; KMS maps its `kms://identity/<name>` URI SAN to the enrolled identity. |
| **Administrator certificate/key** | Minted only on the KMS host by `parameter-store admin-cert issue NAME --out DIR` (or `init`/`create-admin` with `--cert-dir`); never issued over the network. | An administrator presents it **in addition to** their bearer token, on the CLI (`--cert`/`--key`) and in the browser (imported as PKCS#12). A stolen admin token alone is useless. |

First [enable server TLS](docs/operations.md#tls-and-mtls) with the
operator-provided serving certificate. Its DNS or IP SAN must match the host
applications use in `KMS_ENDPOINT` (`kms.internal` in this example), because
the SDKs perform normal server-name verification. Then create an mTLS-only
namespace and one identity for the consuming application:

```bash
ADMIN_TOKEN=...                 # the admin's bearer token
ADMIN_CERT=./admin-creds/ops.crt   # from: parameter-store admin-cert issue ops --out ./admin-creds
ADMIN_KEY=./admin-creds/ops.key
KMS_ENDPOINT=kms.internal:8443
SERVER_CA=/etc/parameter-store/trust/server-ca.crt

./bin/parameter-store admin namespace create --env prod --app gradethis \
  --auth-methods mtls --endpoint "$KMS_ENDPOINT" --ca "$SERVER_CA" \
  --cert "$ADMIN_CERT" --key "$ADMIN_KEY" --token "$ADMIN_TOKEN"

# POSIX: create an owner-only output directory.
install -d -m 0700 ./credentials/gradethis-be
# PowerShell, from a trusted current-user directory:
# New-Item -ItemType Directory -Force .\credentials\gradethis-be
./bin/parameter-store admin identity create gradethis-be \
  --namespace prod/gradethis --auth mtls --ttl 90d \
  --out ./credentials/gradethis-be \
  --endpoint "$KMS_ENDPOINT" --ca "$SERVER_CA" \
  --cert "$ADMIN_CERT" --key "$ADMIN_KEY" --token "$ADMIN_TOKEN"
# -> ./credentials/gradethis-be/gradethis-be.crt
# -> ./credentials/gradethis-be/gradethis-be.key (one-time private key)
```

On Windows, the CLI protects the generated private key with a
current-user-only DACL. On every platform, use an output directory controlled
by the account running the command.

Deploy the application certificate and private key through your secret
delivery mechanism, and deploy the operator's server CA bundle as trust
configuration. Restrict the private key to the application account. Do not
deploy the built-in client-issuing CA, the KMS server key, or an admin token to
the application.

All three SDKs take the files in the same order conceptually: application
certificate, application private key, then **server** CA. A namespace-bound
identity can discover `prod/gradethis` through `WhoAmI`, so these examples need
neither a bearer token nor an explicit namespace.

Go ([full guide](docs/sdk-go.md)):

```go
client, err := kmsclient.NewClient(kmsclient.Config{
    Endpoint: os.Getenv("KMS_ENDPOINT"),
    TLS: kmsclient.MTLSFromFiles(
        os.Getenv("KMS_CLIENT_CERT"),
        os.Getenv("KMS_CLIENT_KEY"),
        os.Getenv("KMS_SERVER_CA"),
    ),
})
if err != nil {
    log.Fatal(err)
}
defer client.Close()
```

Python ([full guide](docs/sdk-python.md)):

```python
import os
from kms_paramstore import Client, mtls_from_files

with Client(
    os.environ["KMS_ENDPOINT"],
    tls=mtls_from_files(
        os.environ["KMS_CLIENT_CERT"],
        os.environ["KMS_CLIENT_KEY"],
        os.environ["KMS_SERVER_CA"],
    ),
) as client:
    identity = client.who_am_i()  # verifies TLS, the client cert, and enrollment
```

TypeScript/Node.js ([full guide](sdk/typescript/README.md)):

```ts
import { createClient, mtlsFromFiles } from "@suhaibinator/kms";

const client = createClient({
  endpoint: process.env.KMS_ENDPOINT!,
  credentials: mtlsFromFiles(
    process.env.KMS_CLIENT_CERT!,
    process.env.KMS_CLIENT_KEY!,
    process.env.KMS_SERVER_CA!,
  ),
});

try {
  const identity = await client.whoAmI(); // verifies TLS, the client cert, and enrollment
} finally {
  await client.close();
}
```

The identity's implicit home-namespace grant covers reads and lists. Add a
policy only for writes or cross-namespace access. Certificate renewal is an
overlap operation: issue a replacement, deploy it, then revoke the old serial.
See the complete [application onboarding and rollover runbook](docs/operations.md#connect-a-production-application-with-mtls).

## Configuration

Every setting has exactly three spellings — a YAML key, a `KMS_*` environment
variable, and a flag derived from it (`storage.sqlite_path`,
`KMS_SQLITE_PATH`, `--sqlite-path`) — and they resolve highest first:
**flag, then environment variable, then the config file (`--config FILE` /
`KMS_CONFIG`), then the built-in default**. The same order applies to `serve`
and to every offline command (`init`, `migrate`, `check`, `backup`, `restore`,
…). Run `parameter-store config show` to print the effective configuration
with the source of each value, and `parameter-store config validate` to check
it before a restart. Full reference, including the env var and flag table, is
in [`docs/operations.md`](docs/operations.md#configuration).

```yaml
server:
  grpc_addr: "0.0.0.0:8443"
  http_addr: "0.0.0.0:8080"
  verify_defaults:
    requests_per_hour: 60
    burst: 10
    mismatch_budget_per_hour: 500

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
  # Admins present a built-in-CA client certificate as well as their token.
  # Default true; relaxed with a warning while tls_enabled is false.
  admin_require_client_cert: true

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
- **Administrators present both credentials**: while
  `security.admin_require_client_cert` is enforced (the default whenever TLS
  is on), an admin needs a built-in-CA client certificate **and** its bearer
  token, on the CLI and in the browser alike. Admin certificates are minted
  only on the KMS host (`admin-cert issue`), never over the network, so a
  stolen admin token is useless on its own and cannot mint a replacement.
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
missed = the subscriber is dropped from the registry). The Go, Python, and
TypeScript SDKs own the
whole lifecycle — reconnect with jittered backoff, resume by revision, and a
5-minute reconciliation poll as a safety net — so application code just
reads the current value or registers a change callback. Declarative parameters
hot-reload **by default** (opt out with `Static`); every non-static value in
a namespace shares one namespace-wide subscription. Secret changes are
pushed as metadata-only notifications (no plaintext over the stream); the SDK
re-fetches on request. The frontend's **Subscribers** page shows every live
subscription, its namespaces, and its last acknowledged revision. Revisions
are global, so a subscriber may appear behind because another namespace
changed; use the view as a coarse liveness/lag signal, not proof that a
specific key was applied. See [`docs/sdk-go.md`](docs/sdk-go.md#hot-reload),
[`docs/sdk-python.md`](docs/sdk-python.md), and the
[TypeScript SDK guide](sdk/typescript/README.md#declarative-values-and-hot-reload).

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

The `frontend/` directory is a Next.js static export (`output: "export"`, no
Node server runtime) embedded into the Go binary. It provides the operator
console for applications and environments, parameters and secrets, identities
and policies, releases and schemas, audit events, subscribers, and service
health. Secret plaintext is hidden unless explicitly revealed, and
client-bound secrets cannot be revealed by the server. Dynamic data comes from
the [`/api/v1/*` API](docs/http-api.md); unknown frontend routes fall back to
the exported entry HTML so client-side deep links work on refresh. See the
[`frontend` development guide](frontend/README.md) for local workflows and
[`docs/testing.md`](docs/testing.md#frontend) for test boundaries.

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
  `kms-paramstore`) — equivalent synchronous and asyncio clients and release
  loaders, plus Pydantic-based generated managed configuration, defaults, and
  verification; see [`docs/sdk-python.md`](docs/sdk-python.md) and the
  [`Python managed configuration guide`](sdk/python/MANAGED_CONFIG.md).
- **TypeScript** (`sdk/typescript`, package `@suhaibinator/kms`) — Node.js
  TLS/mTLS client, declarative hot reload, atomic release loading,
  framework-neutral public-policy publishing, generated managed configuration,
  and optional serverful Next.js helpers; see the
  [`sdk/typescript` guide](sdk/typescript/README.md). The core SDK is not
  browser-safe; only its explicit `next/client` public-policy hook belongs in a
  browser bundle.

## Documentation

- [`docs/README.md`](docs/README.md) — documentation map, including active
  references and historical records.
- [`docs/operations.md`](docs/operations.md) — running in production:
  systemd, TLS/mTLS, backup/restore, disaster recovery, KEK rotation,
  monitoring.
- [`docs/security.md`](docs/security.md) — the encryption, authentication,
  authorization, and audit model in depth.
- [`docs/sdk-go.md`](docs/sdk-go.md) — the Go client SDK.
- [`docs/managed-go-configuration.md`](docs/managed-go-configuration.md) —
  generated atomic typed configuration, defaults, and operator workflow.
- [`docs/managed-defaults.md`](docs/managed-defaults.md) — export source-owned
  parameter defaults and atomically preview/apply them to an existing app.
- [`docs/sdk-python.md`](docs/sdk-python.md) — the Python client SDK.
- [`sdk/typescript/README.md`](sdk/typescript/README.md) — TypeScript SDK
  installation, operations, release loading, and serverful Next.js examples.
- [`docs/sdk-typescript-api.md`](docs/sdk-typescript-api.md) — TypeScript public
  API, runtime boundaries, compatibility, and versioning policy.
- [`docs/sdk-typescript-parity.md`](docs/sdk-typescript-parity.md) — the living
  Go-to-TypeScript behavioral parity and release-status ledger.
- [`docs/http-api.md`](docs/http-api.md) — the JSON HTTP API contract used
  by the embedded frontend.
- [`docs/configuration-releases.md`](docs/configuration-releases.md) — release
  and schema gRPC contracts, lifecycle semantics, limits, and operational
  guarantees.
- [`docs/migration.md`](docs/migration.md) — migrating from
  SuhaibParameterStore (gradethis's prior parameter store).
- [`docs/testing.md`](docs/testing.md) — local and CI validation commands.
- [`docs/releasing.md`](docs/releasing.md) — release artifacts, installation,
  verification, and maintainer workflow.
- [`docs/consumer-adoption.md`](docs/consumer-adoption.md) — adopting generated
  managed configuration in a consuming Go service.

## License

MIT — see [`LICENSE`](LICENSE).
