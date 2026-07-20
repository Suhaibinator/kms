# Operations

This is the production runbook: starting the service unattended, TLS/mTLS,
backup and restore, disaster recovery, key rotation, and what to monitor.
For the encryption and authorization design behind these procedures, see
[`security.md`](security.md). For the HTTP API the frontend uses, see
[`http-api.md`](http-api.md).

> **Status note.** Every command below matches the CLI implemented in
> `internal/cli`. The offline commands (`init`, `migrate`, `check`, `backup`,
> `restore`, `create-admin`, `rotate-kek`, `import`) operate directly on the
> SQLite file; the `admin` command group and the convenience commands
> (`put-secret`, `get-secret`, `put-parameter`, `list`, `release`) talk to a running
> server over gRPC. `make build` produces a working `bin/parameter-store`,
> and `serve` opens both the gRPC and HTTP listeners
> (`cmd/parameter-store/main.go` wires `cli.GRPCFactory` to
> `internal/server/grpcserver`).

## Configuration

The server reads a YAML config file (`--config FILE` or `KMS_CONFIG` env
var), applies `KMS_*` environment overrides on top, then validates the
result. Defaults come from `internal/config.Default()`.

```yaml
server:
  grpc_addr: "0.0.0.0:8443"
  http_addr: "0.0.0.0:8080"

storage:
  sqlite_path: "/var/lib/parameter-store/kms.db"

security:
  tls_enabled: true
  # Built-in-CA client certificates work whenever TLS is enabled. Set this
  # only to add a separate operator-supplied client CA to the gRPC trust pool.
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
  release_retain_duration: 2160h       # 90 days
  release_retain_versions: 100
  release_subscriber_retain_duration: 720h  # 30 days

log:
  level: "info"
```

| Env var | Overrides |
|---|---|
| `KMS_CONFIG` | Default `--config` path |
| `KMS_GRPC_ADDR` | `server.grpc_addr` |
| `KMS_HTTP_ADDR` | `server.http_addr` |
| `KMS_SQLITE_PATH` | `storage.sqlite_path` |
| `KMS_KEK_FILE` | `encryption.kek_file` |
| `KMS_SERVER_CERT_FILE` / `KMS_SERVER_KEY_FILE` / `KMS_CLIENT_CA_FILE` | `security.*` |
| `KMS_TLS_ENABLED` / `KMS_MTLS_ENABLED` | `security.tls_enabled` / `security.mtls_enabled` (parsed with `strconv.ParseBool`) |
| `KMS_TRUST_PROXY_HEADERS` | `security.trust_proxy_headers` (parsed with `strconv.ParseBool`) — honor `X-Forwarded-For` for the rate-limit key and audit source IP; enable only behind a trusted reverse proxy (see [TLS and mTLS](#tls-and-mtls)) |
| `KMS_FRONTEND_ENABLED` | `frontend.enabled` |
| `KMS_AUDIT_ENABLED` | `audit.enabled` |
| `KMS_LOG_LEVEL` | `log.level` |
| `KMS_MASTER_PASSPHRASE` | Supplies the master passphrase without a TTY prompt (see below) |

`Config.Validate()` enforces: both listen addresses set; `sqlite_path` set;
`mtls_enabled` requires `tls_enabled`; `tls_enabled` requires
`server_cert_file`/`server_key_file` to exist; `mtls_enabled` requires
`client_ca_file` to exist. `Config.Redacted()` is what the server logs at
startup — addresses, paths, and feature flags, deliberately never a
wholesale dump of the file (so nothing sensitive that might end up in the
YAML by mistake gets logged).

Logs are structured JSON emitted by [Uber zap](https://github.com/uber-go/zap):
each line carries `ts` (ISO8601, millisecond precision), a lowercase `level`
(`debug`/`info`/`warn`/`error`), `msg`, and typed fields. `log.level` /
`KMS_LOG_LEVEL` sets the minimum level (default `info`). Secret plaintext,
tokens, and key material never appear in a log line at any level.

## Administrative CLI reference

These commands are implemented in `internal/cli` and operate directly on the
SQLite file passed via `--db` — they do not require a running server (except
`create-admin`'s underlying identity, which a running `serve` process reads
immediately via its shared database). All flags are the standard library
`flag` package's double-dash-or-single-dash form (e.g. `--db` or `-db`).

| Command | Flags | Purpose |
|---|---|---|
| `init` | `--db` (default `./kms.db`), `--master-key-file` (omit for a passphrase prompt), `--admin NAME` (optional) | Creates/migrates the database and the master key (generating a key file, or prompting for a new passphrase with confirmation). With `--admin`, also creates a bootstrap admin identity and prints its token once. |
| `migrate` | `--db` | Opens the database, applying any pending migrations, and exits. |
| `check` | `--db`, `--key-file` (optional) | Verifies the database is reachable and, if a key source is available (file, `KMS_MASTER_PASSPHRASE`, or TTY), verifies the master key against the stored key-check value. Never prints key material. |
| `backup` | `--db`, `--out` (required, must not already exist) | Writes a consistent online backup via `store.Backup`. Prints a reminder that the master key is not included. |
| `restore` | `--db` (destination), `--in` (required, source backup), `--force` (overwrite an existing destination) | Validates the input is a real SQLite file (checks the file header), copies it into place, removes stale `-wal`/`-shm` sidecars, then opens (and migrates) the restored copy to confirm it's usable. |
| `create-admin` | `--db`, `--name` (required) | Creates an admin identity directly against the database file and prints its token once. Uses WAL mode's concurrent-reader support, but coordinating this against a live `serve` process is the operator's responsibility. |
| `rotate-kek` | `--db`, `--key-file` (current key, omit to use the current passphrase), `--new-key-file` (new key, omit to enter a new passphrase) | Unseals with the current key, generates or loads the new key, and calls the same `Service.RotateKEK` used internally — rewrapping every **non-destroyed** secret version's DEK and every built-in CA key under the new KEK in one transaction. Prints both counts (`N secret versions and M CA keys rewrapped`). Run with `serve` stopped; a live process retains the old keyring. |
| `import` | `--from` (JSON file or SuhaibParameterStore SQLite export), `--namespace env/app` **or** `--env`/`--app`, `--db` (default `./kms.db`), `--master-key-file`, `--dry-run`, `--report FILE` | Maps flat source keys to **relative slug keys** (`slug(key)`, e.g. `TWILIO_SID` → `twilio-sid`) in the destination namespace, **auto-creates the namespace** if it does not exist, writes each as a new secret via a ref-based `PutSecret` with a freshly minted per-secret access token, and emits a mapping report (old key → `/env/app/key` display path → token, written once). Distinct source keys that slug to the same key are reported as a collision rather than silently overwriting. `--dry-run` reports the mapping without writing or minting tokens. Pass either `--namespace` or `--env`/`--app`, not both. See [`migration.md`](migration.md) for the full gradethis walkthrough. |

The v1→v2 database migration adds content type, client-bound mode, and
token-required state to each immutable secret-version row. Because v1 retained
only the latest shared secret attributes, legacy versions are backfilled from
those values; every version created after migration stores its own attributes.

`init`, `check`, `rotate-kek`, and `import` all go through the same
master-key acquisition path as `serve` (key file → `KMS_MASTER_PASSPHRASE`
→ interactive prompt) via the shared `unseal` helper, so the same no-TTY
fail-fast behavior applies.

### Management commands (`admin` group, over gRPC)

The `admin` command group manages namespaces, identities, and the built-in
CA on a **running** server over gRPC (unlike the offline `--db` commands
above). Every admin command shares the connection flags `--endpoint`
(default `localhost:8443`), `--token` (admin bearer token; env `KMS_TOKEN`),
`--insecure` (skip TLS, development only), `--ca`, and `--cert`/`--key`
(mTLS). `admin ca show` needs no credential — the CA certificate is public.

| Command | Args / flags | Purpose |
|---|---|---|
| `admin namespace create` | `--env ENV`, `--app APP`, `--description`, `--auth-methods mtls,token` (default: mTLS-only) | Create a namespace. Environment/application are flags, not a positional `ENV/APP` argument. |
| `admin namespace update` | `--env ENV`, `--app APP`, `--description`, `--auth-methods` | **Full replace** of the description and allowed auth methods. |
| `admin namespace delete` | `--env ENV`, `--app APP` | Delete an **empty** namespace (no parameters, secrets, or bound identities). |
| `admin namespace list` | — | Table of namespaces with allowed auth methods and parameter/secret counts. |
| `admin identity create NAME` | `--kind client\|admin` (default `client`), `--namespace env/app`, `--auth mtls\|token\|both` (default `mtls`), `--ttl 90d` (or e.g. `720h`), `--out DIR` | Create an identity. Mints a token and/or a one-time PEM cert bundle per `--auth`; with `--out DIR` writes `NAME.crt` (0644) and `NAME.key` (0600), otherwise prints them once. |
| `admin identity issue-cert NAME` | `--ttl`, `--out DIR` | Mint an **additional** client certificate for an existing identity (for overlap rollover). |
| `admin identity revoke-cert NAME` | `--serial` (required) | Revoke one certificate by serial. |
| `admin identity rotate NAME` | — | Rotate a token identity's bearer token (printed once). |
| `admin identity revoke NAME` | — | Disable an identity; **all** of its certificates become invalid. |
| `admin identity list` | — | Table: name, kind, namespace, has-token, cert count, disabled. |
| `admin ca show` | `--out FILE` | Print (or write) the public built-in **client-issuing** CA certificate for inspection or out-of-band verification of KMS-issued client certificates. This is not the SDK's server-trust CA. |

`--ttl` accepts a Go duration (`720h`) or a bare day count (`90d`); omitting
it uses the server's 90-day default. `--auth-methods` and `--auth` values are
`mtls` and/or `token`. Tokens and certificate private keys are shown exactly
once and are never retrievable again. `admin namespace`/`identity` map onto
the `AdminService` RPCs; see the [built-in CA runbook](#built-in-ca-and-client-certificates)
below for the certificate lifecycle these commands drive.

### Convenience commands (talk to a running server over gRPC)

These operate over gRPC against `--endpoint` (default `localhost:8443`), not
directly on the database file, so they need a server with the gRPC listener
open (the default when running `serve`). They share the same connection flags
as the `admin` group: `--endpoint`, `--token` (identity bearer token; env
`KMS_TOKEN`), `--insecure` (skip TLS, development only), `--ca`, `--cert`/
`--key` (mTLS). Secrets and parameters are addressed by a **`/env/app/key`
display path**, which the CLI splits into an explicit namespace + relative
key client-side; `list` takes a bare `ENV/APP` namespace.

| Command | Extra flags | Purpose |
|---|---|---|
| `put-secret /env/app/key` | `--value-file` (default: read stdin), `--content-type` (default `text/plain`), `--client-bound`, `--generate-token`, `--secret-token` (for client-bound updates) | Stores a new secret version. A new client-bound secret requires `--client-bound --generate-token`; the server-minted token is its one-time client key share. Existing client-bound secrets require `--client-bound --secret-token`, and adding `--generate-token` rotates the token for the new version. |
| `get-secret /env/app/key` | `--version`, `--label`, `--secret-token`, `--show` (allow printing to a terminal), `--out FILE` (write to a file instead, mode 0600) | Fetches a secret value. Refuses to print raw secret bytes to an interactive terminal unless `--show` is passed or output is piped — `--out FILE` or piping (`\| cat`) works without `--show`. |
| `put-parameter /env/app/key VALUE` | `--content-type` (default `string`) | Stores a new parameter version. |
| `list ENV/APP` | `--prefix` (relative key prefix within the namespace) | Lists parameters and secrets (metadata only) in a namespace as a table: type, `/env/app/key`, current version, content-type/client-bound note. Pages through the full result set. |

A literal `--` ends flag parsing: everything after it is taken as
positional arguments verbatim, even if it begins with `-`. Use it when a
value itself looks like a flag, e.g. `parameter-store put-parameter
/prod/gradethis/flag -- -5`.

### Configuration release commands

The `release` command group uses the same gRPC connection flags as the other
convenience commands. A release definition is strict JSON or YAML:

```yaml
namespace: prod/gradethis
name: runtime
schema_id: gradethis/runtime
schema_version: 1
entries:
  - alias: rate_limits
    kind: parameter
    key: config/rate-limits       # relative to the release namespace
    label: current                # resolved before the release is stored
  - alias: db_password
    kind: secret
    key: db-password
    version: 3
```

An absolute `key`, such as `/shared/platform/feature-flags`, creates a
cross-namespace reference subject to independent resource authorization.
Specify `version` or `label`, not both; omitting both resolves `current`.

```bash
parameter-store release schema create gradethis/runtime runtime.schema.json \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure
parameter-store release schema show gradethis/runtime 1 \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure
parameter-store release schema list gradethis/runtime \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure

parameter-store release create runtime-release.yaml \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure
parameter-store release validate prod/gradethis runtime 1 \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure
parameter-store release show prod/gradethis runtime 1 \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure
parameter-store release list prod/gradethis runtime \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure
parameter-store release diff prod/gradethis runtime 1 2 \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure
```

Activate with compare-and-swap to avoid overwriting an activation you did not
observe. `0` means “expect no active release”:

```bash
parameter-store release activate prod/gradethis runtime 1 \
  --expected-current-version 0 \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure

# Defaults to the active release's previous version and includes a CAS guard.
parameter-store release rollback prod/gradethis runtime \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure

# Or reactivate any retained immutable version directly.
parameter-store release rollback prod/gradethis runtime 1 \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure
```

`show` and `diff` print aliases, references, exact versions, content types,
and parameter digests only. They never read or render secret plaintext or
tokens. `subscribers` is admin-only and pivots the per-state rows into one line
per process instance:

```bash
parameter-store release subscribers prod/gradethis runtime \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure
# CLIENT  INSTANCE  RECEIVED  PREPARED  APPLIED  REJECTED  LAG  CONNECTED
```

The embedded **Releases** page offers equivalent create, schema, validate,
diff, activate, rollback, and subscriber views. Its secret entries are
metadata-only. Full behavior is in
[`configuration-releases.md`](configuration-releases.md).

## Startup sequence and readiness

On `serve`, the process (`internal/cli/serve.go`):

1. Loads and validates config, logs the redacted summary.
2. Opens SQLite (`storage.Open`) — this also runs pending migrations.
3. Constructs the core service (not yet ready — no keyring attached).
4. **Acquires the master key** (below) and attaches it to the service,
   which is what flips readiness on.
5. **Bootstraps the built-in CA** (`Service.BootstrapCA`): on a fresh
   database it generates the CA and stores it KEK-wrapped; on later starts it
   loads and decrypts the existing CA into memory (see
   [Built-in CA](#built-in-ca-and-client-certificates)).
6. Starts the watch hub (change-log tailer / subscriber registry).
7. Starts the gRPC listener (via `cli.GRPCFactory`, wired in
   `cmd/parameter-store/main.go` to `internal/server/grpcserver`), then the
   HTTP listener.
8. Blocks on `SIGINT`/`SIGTERM`, then shuts down: gRPC graceful stop, HTTP
   graceful shutdown (20s timeout), stop the watch hub, close the store.

`/healthz` is liveness (process is up). `/readyz` and the gRPC standard
health service (`grpc.health.v1.Health`, `internal/server/grpcserver`)
report **not ready** until the store is reachable and the master key has
been acquired and verified — `Service.Ready()` checks exactly this
(`internal/core/service.go`). Startup acquires the key and bootstraps the CA
**before** either listener starts, so an interactive passphrase prompt appears
as connection failure, not a listening-but-unready service. After startup, an
already-running listener reports not ready if the database becomes unreachable.

## Master key acquisition (unattended vs. interactive)

Every start (not just first-time init) resolves the master key in this
order (`internal/crypto/unseal.go`, `internal/cli/unseal.go`):

1. **Key file** — `encryption.kek_file` / `KMS_KEK_FILE`. Raw key material
   (32 bytes, 64 hex chars, or base64). No human interaction. **This is the
   only fully unattended mode for a systemd-managed restart.**
2. **`KMS_MASTER_PASSPHRASE` environment variable** — if set, used as the
   passphrase without prompting. Still unattended, but the passphrase sits
   in the process environment (visible to anything that can read `/proc` for
   that PID, or to your secrets-injection tooling) — prefer the key file for
   production unless you have a specific reason to use a passphrase.
3. **Interactive TTY prompt** — no-echo (`term.ReadPassword`), entered twice
   on first initialization (confirmation), once on subsequent unlocks.

**If stdin is not a TTY and neither a key file nor
`KMS_MASTER_PASSPHRASE` is available, startup fails immediately** with an
actionable error (`crypto.ErrNoKeySource`) instead of hanging. This is the
behavior that matters for systemd: a unit with no controlling terminal that
is misconfigured (no key file, no env passphrase) fails fast at start
rather than hanging indefinitely on an invisible prompt.

Whichever mode created the database is sticky: a database initialized with
a file-based key cannot later be unlocked with a passphrase (and vice
versa) — `unlock()` returns an error naming the mismatch rather than
attempting one against the other's key-check value.

## Running under systemd

Use a file-based master key for unattended restarts. The unit needs no
controlling terminal, so `encryption.kek_file` (or `KMS_KEK_FILE`) must
point at a real, readable key file — otherwise `serve` will fail fast per
the no-TTY behavior above.

```ini
# /etc/systemd/system/parameter-store.service
[Unit]
Description=Parameter Store
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=parameter-store
Group=parameter-store
ExecStart=/usr/local/bin/parameter-store serve --config /etc/parameter-store/config.yaml
Restart=on-failure
RestartSec=5s
# No controlling TTY under systemd — encryption.kek_file in config.yaml
# (or KMS_KEK_FILE below) must be set, or KMS_MASTER_PASSPHRASE must be
# supplied via EnvironmentFile. Otherwise the process fails fast at start
# rather than hanging on a prompt it can never receive.
# Environment=KMS_MASTER_PASSPHRASE=...   # only if not using a key file
# EnvironmentFile=/etc/parameter-store/env  # prefer this over inline Environment=
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/parameter-store
StateDirectory=parameter-store

[Install]
WantedBy=multi-user.target
```

Key file permissions matter: the master key file should be owned by the
service user and mode `0600` (this is exactly what
`crypto.WriteKEKMaterialFile` produces when the service creates one itself).

## TLS and mTLS

Set in `security:`:

```yaml
security:
  tls_enabled: true
  mtls_enabled: false  # built-in client CA is added automatically
  server_cert_file: "/etc/parameter-store/tls/server.crt"
  server_key_file: "/etc/parameter-store/tls/server.key"
  client_ca_file: ""   # set only for an additional operator client CA
  trust_proxy_headers: false
```

The **server certificate is operator-provided** (`server_cert_file` /
`server_key_file`); the built-in CA signs client certificates only, never the
server's serving certificate. `tls_enabled` alone terminates TLS on both
listeners with that certificate (`Config.BuildServerTLS`, minimum TLS 1.2).

**Client-certificate authentication uses the built-in CA and does not require
`mtls_enabled`.** Whenever TLS is on, the gRPC listener adds the built-in CA
to its client-CA pool and switches to `tls.VerifyClientCertIfGiven`
(`grpcServerTLS`, `serve.go`) — so a client presenting a certificate the
built-in CA issued (fetch the trust root with `admin ca show`) authenticates
by mTLS, while token-only clients, presenting no certificate, still connect.
The TLS layer verifies any certificate offered; the per-namespace
`allowed_auth_methods` gate, not the handshake, decides who is admitted.

`mtls_enabled` (which requires `tls_enabled` and a `client_ca_file`, both
checked to exist at config-validation time) adds an **operator-supplied** CA to
the gRPC TLS trust pool alongside the built-in one. Certificate presentation
stays optional either way (`VerifyClientCertIfGiven`, not require-and-verify).
However, TLS trust alone does not create a KMS identity: certificate auth also
requires the `kms://identity/<name>` SAN and a matching `identity_certs` row.
The current public issuance API creates those rows only for built-in-CA
certificates, so an external CA is not independently usable for identity auth
without a separate provisioning mechanism. Do not enable `mtls_enabled` merely
to use the built-in CA.

The embedded HTTP/frontend listener **never** requires a client certificate:
it clones the TLS config and sets `tls.NoClientCert` (`serve.go`), since human
users authenticate with a bearer token through the login flow. For mTLS in
front of the web UI, put a TLS-terminating reverse proxy ahead of the HTTP
listener.

**Running securely: don't ship TLS disabled on a networked bind.** If
`tls_enabled` is `false`, `serve` logs a startup warning; if in addition
either listen address is non-loopback (`0.0.0.0`, `::`, an explicit
non-loopback IP, or an unrecognized hostname — `isNonLoopbackBind`,
`internal/cli/serve.go`), the warning escalates to make the risk explicit:
**bearer tokens and secret values will travel in cleartext** to anything
that can observe the connection. For production, do one of:

- Set `security.tls_enabled: true` with a real certificate (the normal
  path), or
- Bind both listeners to loopback (`127.0.0.1` / `localhost`) and
  terminate TLS at a trusted reverse proxy in front.

A loopback bind with TLS disabled still logs a quieter reminder, since
even loopback traffic is unencrypted.

**Trusted proxy headers (`trust_proxy_headers`).** By default the HTTP
server uses the real TCP peer address — never a client-supplied header —
for both the login/failed-auth rate-limit key and the source IP recorded
in audit events (`clientIP`, `internal/server/httpserver/server.go`).
Setting `security.trust_proxy_headers: true` makes it honor
`X-Forwarded-For` instead (the first address in a comma-separated list).
Enable this **only** when the HTTP listener sits behind a trusted reverse
proxy that sets `X-Forwarded-For` on every request and cannot itself be
bypassed by a direct connection to the KMS process — otherwise a client
can put an arbitrary address in that header to dodge the login throttle
and forge the source IP attached to its own actions in the audit log (see
[`security.md`](security.md#login-and-failed-authentication-rate-limiting)).
This setting only affects the HTTP listener; gRPC always uses the TCP peer
address. Default: `false`.

## Built-in CA and client certificates

Machine clients authenticate by mTLS certificates minted by a certificate
authority embedded in the KMS. There is nothing to provision: the CA is
**bootstrapped during the first `serve` startup after unseal** and lives inside
the same database.

**Lifecycle.**

- On the first `serve` after the master key is acquired,
  `Service.BootstrapCA` generates the CA — an Ed25519 key pair and a
  long-lived (10-year), self-signed CA certificate that signs client leaves
  only. On every later start it loads and decrypts the existing CA.
- The **CA private key is stored KEK-wrapped** in the `ca_keys` table
  (enveloped exactly like a secret version: its own DEK wraps the key, and the
  active KEK wraps that DEK); the plaintext signing key remains in server
  memory for the process lifetime so certificates can be issued. A stolen
  database without the master key yields no usable CA
  key — see [`security.md`](security.md#proof-of-identity-the-built-in-ca-and-mtls).
- **KEK rotation rewraps the CA key** alongside every secret DEK, in the same
  transaction (`rotate-kek` prints the CA-key count). Rotation never reissues
  or invalidates any client certificate: the identity↔namespace binding lives
  in the database, not in the certificate, and issued leaves are unaffected.
- The CA certificate is **public**: `admin ca show [--out FILE]` (or the
  unauthenticated `GET /api/v1/ca`) prints it for inspection or out-of-band
  verification of KMS-issued client certificates. The gRPC server loads this
  client-CA directly from the database. SDK clients instead need a CA bundle
  that trusts the separately operator-provided **server** certificate.

**Certificate rollover runbook.** Issued client certificates default to a
**90-day** TTL (`--ttl` overrides). Because an identity may hold several
concurrently-valid certificates, rollover is zero-downtime — issue the
replacement *before* the current one expires:

```bash
# 1. Mint an additional cert for the existing identity (old cert still valid).
parameter-store admin identity issue-cert gradethis-be --ttl 90d --out ./certs \
    --endpoint kms.internal:8443 --ca server-ca.crt --cert admin.crt --key admin.key
#    -> writes certs/gradethis-be.crt and certs/gradethis-be.key (note the serial)

# 2. Deploy the new cert/key to the app and roll it (both certs verify meanwhile).

# 3. Once all instances present the new cert, revoke the old one by serial.
parameter-store admin identity revoke-cert gradethis-be --serial <old-serial> \
    --endpoint kms.internal:8443 --ca server-ca.crt --cert admin.crt --key admin.key
```

A certificate's serial is printed when it is issued and is listed per
identity on the frontend **Identities** page; `admin identity list` shows the
per-identity cert *count*. Revocation is a per-serial database check in the
auth interceptor (no CRL/OCSP). To retire an identity wholesale,
`admin identity revoke NAME` disables it and invalidates **all** of its
certificates at once. There is no automatic renewal — track expiry and
re-issue ahead of the 90-day default (a monitoring reminder keyed off the
certs' `not_after` is worthwhile).

### Per-namespace auth methods

Each namespace records which authentication methods admit a caller
(`allowed_auth_methods`). **New namespaces default to mTLS-only** — the
strongest posture. Allow token auth explicitly (e.g. for a service that
cannot yet present a certificate) with:

```bash
parameter-store admin namespace update --env prod --app gradethis --auth-methods mtls,token \
    --endpoint kms.internal:8443 --ca server-ca.crt --cert admin.crt --key admin.key
```

The method gate runs **before** authorization on every namespaced operation
(parameter reads included): a client whose method is not in the target
namespace's list is refused with `PermissionDenied` and audited, regardless
of policy. It applies to **client-kind** identities only — **admin-kind**
identities (the human management plane, and the browser login, which is
token-based) bypass the method gate but remain fully audited. Changing a
namespace's auth methods is itself an audited admin action
(`admin:namespace:update`).

## Backups

**Backups must separate the encrypted database from the master key.** This
is the single most important operational fact about this system:

```text
SQLite backup WITHOUT the master key:  cannot decrypt any secret.
SQLite backup WITH the master key:     can decrypt every non-client-bound
                                        secret (client-bound secrets still
                                        also need each secret's client token).
```

Treat the two backups as different security tiers. A database backup alone
is safe to store somewhere less tightly controlled than the key itself
(still access-controlled — it contains parameter plaintext, secret
ciphertext, the KEK-wrapped CA key, policy, identity, and audit metadata —
but a stolen copy on its own cannot be decrypted). The built-in CA needs no
separate backup: its key lives inside the database, KEK-wrapped like the
secrets, so a database backup already includes it. The master key file is the crown jewel: back it up
separately, encrypt it at rest wherever it's stored, and restrict who can
read it.

Recommended layout:

```text
/var/backups/parameter-store/db/     periodic SQLite backups
/var/backups/parameter-store/key/    master key backup — separate storage,
                                      separate access control, ideally a
                                      different physical/logical location
```

Take the backup online, without stopping the server, with:

```bash
parameter-store backup --db /var/lib/parameter-store/kms.db \
    --out /var/backups/parameter-store/db/kms-$(date +%Y%m%dT%H%M%S).db
```

`backup` refuses to overwrite an existing output file, so each invocation
needs a fresh path (a timestamp, as above, is the simplest scheme). It
prints an explicit reminder that the master key is not included.

## Restore

Restoring requires **both** pieces: the database backup and the matching
master key (the same key file, or the same passphrase + salt from
`key_metadata`, whichever mode the backed-up database was initialized
with). A database restored without its key is exactly as unreadable as a
stolen one.

```bash
systemctl stop parameter-store
parameter-store restore --db /var/lib/parameter-store/kms.db \
    --in /var/backups/parameter-store/db/kms-20260701T120000.db
# --force is required if the destination file already exists
systemctl start parameter-store
```

`restore` validates that `--in` is actually a SQLite file (checks the
16-byte file header) before copying it into place, removes any stale
`-wal`/`-shm` sidecar files left over from the previous database so the
restored copy is self-consistent, then opens (and migrates) the restored
file to confirm it's usable — all before you start the server against it.
Ensure the master key file (or passphrase) matches what the restored
database expects: `key_metadata.source` in the restored database records
which mode it was, and the key-check canary (`crypto.VerifyKeyCheck`) will
fail loudly and immediately at the next `serve` (or `check`) if the wrong
key or passphrase is supplied, rather than surfacing as scattered
decryption errors later. Confirm `/readyz` reports ready after starting.

## Disaster recovery

| Scenario | Outcome |
|---|---|
| Database lost, key intact, backup exists | Restore the backup; secrets decrypt normally. |
| Database corrupted, no backup | Data loss. This is single-node embedded storage — back it up. |
| **Master key lost, database intact** | **All secret versions are permanently unrecoverable, including client-bound versions** (which require the master key plus their client token). There is no escrow, recovery mechanism, or support path. Parameters and metadata are unaffected. The KEK-wrapped CA private key is also unrecoverable, so the old instance cannot start; a replacement instance bootstraps a new CA and all client certificates must be re-issued. This is why the key backup procedure above must never be skipped. |
| **A client-bound version's client token is lost** | Every version encrypted under that token is **permanently unrecoverable**, even with the master key and database intact. Versions written under other retained tokens remain readable. This is by design (plan §10.7.3); the frontend requires explicit acknowledgment at creation time. |
| Wrong master key / passphrase supplied | Startup fails immediately at the key-check step (`VerifyKeyCheck`) with an actionable error — the service will not start in a half-unsealed state. |

## KEK rotation

Rotation rewraps every **non-destroyed** secret version's `encrypted_dek` —
**and every built-in CA key** (`ca_keys`) — under a freshly generated KEK, without decrypting and
re-encrypting the underlying value ciphertext, and commits the metadata swap
and all rewraps in a single storage transaction (`Service.RotateKEK`,
`internal/core/admin.go`) — so rotation either completes fully or leaves the
previous KEK active, never a partially rewrapped state. It rewraps the CA key
without reissuing any certificate. Every rewrapped row receives the new
`kek_id`; destroyed versions have already had their ciphertext and DEK nulled.
No readable secret or CA row depends on the retired KEK after the transaction.

For client-bound secrets, rotation **only touches the outer (KEK) layer**.
The inner, client-token-derived layer is untouched — rotating the master
key never requires contacting any client or invalidating any client token
(plan §11.4.4).

```bash
parameter-store rotate-kek --db /var/lib/parameter-store/kms.db \
    --key-file /etc/parameter-store/master.key \
    --new-key-file /etc/parameter-store/master-new.key
# -> KEK rotated: 128 secret versions and 1 CA keys rewrapped under kek-<id>
# The old key can no longer decrypt after this rotation; point any running
# server at the new master key and restart it.
```

Stop `serve` before running this offline command; otherwise the live process
continues with its old in-memory keyring until restarted. The command prints
the count of rewrapped secret versions and CA keys. Omit
`--key-file` to unseal the current key from a passphrase instead of a file;
omit `--new-key-file` to be prompted for a new passphrase rather than
generating a new key file. Every rotation is audited as `key.rotate`.

## Monitoring and readiness

- `GET /healthz` — liveness (plain text, no auth).
- `GET /readyz` — readiness: store reachable, migrations applied, master
  key acquired and verified. Alert on this being unready for longer than a
  normal restart window. During an interactive passphrase prompt the listeners
  have not started, so probes see a connection failure rather than `not ready`.
- gRPC standard health service (`grpc.health.v1.Health`) mirrors the same
  readiness signal per registered service name
  (`kms.v1.ParameterService`, `kms.v1.SecretService`, `kms.v1.WatchService`,
  `kms.v1.ConfigurationReleaseService`, and
  `kms.v1.ConfigurationSchemaService`),
  refreshed on a 5s interval by default
  (`grpcserver.Config.HealthRefreshInterval`).
- `GET /api/v1/health` (authenticated-exempt) returns
  `{"healthy","ready","version","current_revision"}` — `current_revision`
  is useful as a coarse "is anything moving" signal.
- The frontend's **Subscribers** page (`/subscribers`, backed by `GET
  /api/v1/subscribers`) is the operational way to confirm a configuration
  subscription: it lists every live-subscribed application, its watched
  namespaces, last heartbeat, and last-acked revision. The comparison uses the
  server's global revision, so unrelated namespace changes can make a healthy
  subscriber appear behind; it is not proof that a specific key was applied.
- The frontend's **Releases** page and `parameter-store release subscribers`
  show application lifecycle separately from transport receipt. Rows are per
  process instance and grouped by client name, with latest received, prepared,
  applied, and rejected release identities, current connection state, and lag
  against that release name's active activation revision. Registration is
  visible immediately, before the first lifecycle acknowledgement.
- The **Audit** page / `GET /api/v1/audit` is the record of every secret
  access, write, admin action, and authorization denial — see
  [`security.md`](security.md#audit-guarantees) for exactly what is and
  isn't recorded there.

## Change-log retention

The watch replay journal (`change_log` table) is pruned on a background
timer (`watch.Hub`, default every 5 minutes) to the configured retention
window: `watch.retain_duration` (default 24h) and `watch.retain_rows`
(default 10000), whichever is more restrictive. A subscriber reconnecting
with a `last_seen_revision` older than the retained window receives a full
snapshot instead of a replay — this is transparent to SDK clients (the
subscription manager handles both cases identically) but is worth knowing
when tuning retention: a longer window means more reconnect scenarios can
replay cheaply instead of falling back to a snapshot, at the cost of a
larger `change_log` table.

Configuration release replay and history have additional guards. By default,
KMS retains at least the newest 100 inactive release versions and 90 days of
history (`watch.release_retain_versions`,
`watch.release_retain_duration`). It never prunes current or previous releases,
their schema dependencies, or versions required by retained activation replay.
Disconnected per-instance lifecycle state is pruned after 30 days by default
(`watch.release_subscriber_retain_duration`). These three settings must be
positive. Release activation rows are filtered out of the existing namespace
resource stream; release loaders use their dedicated stream.
