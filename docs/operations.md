# Operations

This is the production runbook: starting the service unattended, TLS/mTLS,
backup and restore, disaster recovery, key rotation, and what to monitor.
For the encryption and authorization design behind these procedures, see
[`security.md`](security.md). For the HTTP API the frontend uses, see
[`http-api.md`](http-api.md).

> **Status note.** Every command below (`serve`, `init`, `migrate`, `check`,
> `backup`, `restore`, `create-admin`, `rotate-kek`, `import`, `put-secret`,
> `get-secret`, `put-parameter`, `list`) is implemented and documented
> exactly as written. `make build` produces a working `bin/parameter-store`,
> and `serve` opens both the gRPC and HTTP listeners
> (`cmd/parameter-store/main.go` wires `cli.GRPCFactory` to
> `internal/server/grpcserver`) — verified with a live `put-secret`/
> `get-secret` round trip against a running server.

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
| `rotate-kek` | `--db`, `--key-file` (current key, omit to use the current passphrase), `--new-key-file` (new key, omit to enter a new passphrase) | Unseals with the current key, generates or loads the new key, and calls the same `Service.RotateKEK` used internally — rewrapping every secret version's DEK under the new KEK in one transaction. |
| `import` | `--from` (JSON file or SuhaibParameterStore SQLite export), `--namespace` (required), `--db` (required unless `--dry-run`), `--master-key-file`, `--dry-run`, `--report FILE` | Maps flat keys to namespaced paths (`slug(key)`, e.g. `TWILIO_SID` → `<namespace>/twilio-sid`), writes each as a new secret with a freshly generated per-secret access token, and emits a mapping report (old key → new path → token, written once). `--dry-run` reports the path mapping without writing or minting tokens. See [`migration.md`](migration.md) for the full gradethis walkthrough. |

`init`, `check`, `rotate-kek`, and `import` all go through the same
master-key acquisition path as `serve` (key file → `KMS_MASTER_PASSPHRASE`
→ interactive prompt) via the shared `unseal` helper, so the same no-TTY
fail-fast behavior applies.

### Convenience commands (talk to a running server over gRPC)

These operate over gRPC against `--endpoint` (default `localhost:8443`), not
directly on the database file, so they need a server with the gRPC listener
open (the default when running `serve`). They share a common set of
connection flags: `--endpoint`, `--token` (identity bearer
token; env `KMS_TOKEN`), `--insecure` (skip TLS, development only), `--ca`,
`--cert`/`--key` (mTLS).

| Command | Extra flags | Purpose |
|---|---|---|
| `put-secret PATH` | `--value-file` (default: read stdin), `--content-type` (default `text/plain`), `--client-bound`, `--generate-token`, `--secret-token` (for client-bound updates) | Stores a new secret version. Prints the minted access token once when `--generate-token` is set. |
| `get-secret PATH` | `--version`, `--label`, `--secret-token`, `--show` (allow printing to a terminal), `--out FILE` (write to a file instead) | Fetches a secret value. Refuses to print raw secret bytes to an interactive terminal unless `--show` is passed or output is piped — `--out FILE` or piping (`\| cat`) works without `--show`. |
| `put-parameter PATH VALUE` | `--content-type` (default `string`) | Stores a new parameter version. |
| `list [PREFIX]` | — | Lists parameters and secrets (metadata only) under a prefix as a table: type, path, current version, content-type/client-bound note. |

A literal `--` ends flag parsing: everything after it is taken as
positional arguments verbatim, even if it begins with `-`. Use it when a
value itself looks like a flag, e.g. `parameter-store put-parameter
/prod/gradethis/flag -- -5`.

## Startup sequence and readiness

On `serve`, the process (`internal/cli/serve.go`):

1. Loads and validates config, logs the redacted summary.
2. Opens SQLite (`storage.Open`) — this also runs pending migrations.
3. Constructs the core service (not yet ready — no keyring attached).
4. **Acquires the master key** (below) and attaches it to the service,
   which is what flips readiness on.
5. Starts the watch hub (change-log tailer / subscriber registry).
6. Starts the gRPC listener (via `cli.GRPCFactory`, wired in
   `cmd/parameter-store/main.go` to `internal/server/grpcserver`), then the
   HTTP listener.
7. Blocks on `SIGINT`/`SIGTERM`, then shuts down: gRPC graceful stop, HTTP
   graceful shutdown (20s timeout), stop the watch hub, close the store.

`/healthz` is liveness (process is up). `/readyz` and the gRPC standard
health service (`grpc.health.v1.Health`, `internal/server/grpcserver`)
report **not ready** until the store is reachable and the master key has
been acquired and verified — `Service.Ready()` checks exactly this
(`internal/core/service.go`). The gRPC/HTTP listeners can be up and
answering "not ready" while a human is still at the passphrase prompt; they
never silently accept traffic before the key is verified.

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
  mtls_enabled: true
  server_cert_file: "/etc/parameter-store/tls/server.crt"
  server_key_file: "/etc/parameter-store/tls/server.key"
  client_ca_file: "/etc/parameter-store/tls/client-ca.crt"
  trust_proxy_headers: false
```

`tls_enabled` alone terminates TLS on both listeners with the server
certificate (`Config.BuildServerTLS`, minimum TLS 1.2). `mtls_enabled`
additionally requires and verifies a client certificate against
`client_ca_file` — but **only for gRPC clients**. The embedded HTTP/frontend
listener explicitly clones the TLS config and disables client-certificate
enforcement for browsers (`serve.go`: `httpTLS.ClientAuth =
tls.NoClientCert`), since human users authenticate with a bearer token
through the login flow, not a client certificate. If you want mTLS in front
of the web UI too, put a TLS-terminating reverse proxy in front of the HTTP
listener rather than relying on the server's own mTLS gate.

`mtls_enabled` requires `tls_enabled`; both cert/key files (and the CA
bundle, for mTLS) are checked to exist at config validation time, so a
typo'd path fails at startup rather than on the first connection attempt.

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
ciphertext, policy, identity, and audit metadata — but a stolen copy on its
own cannot be decrypted). The master key file is the crown jewel: back it up
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
| **Master key lost, database intact** | **All non-client-bound secrets are permanently unrecoverable.** There is no escrow, no recovery mechanism, no support path. Parameters (never encrypted) and metadata are unaffected; every secret value is gone. This is why the key backup procedure above exists and must never be skipped. |
| **Client-bound secret's client token lost** (secret created with `client_bound: true`) | That specific secret is **permanently unrecoverable**, even with the master key and the database both intact. This is by design (plan §10.7.3) — client-bound mode exists precisely so the KMS operator cannot single-handedly recover it. Document this trade-off to anyone opting a secret into client-bound mode; the frontend and CLI require explicit acknowledgment at creation time for exactly this reason. |
| Wrong master key / passphrase supplied | Startup fails immediately at the key-check step (`VerifyKeyCheck`) with an actionable error — the service will not start in a half-unsealed state. |

## KEK rotation

Rotation rewraps every secret's `encrypted_dek` under a freshly generated
KEK, without decrypting and re-encrypting the underlying value ciphertext,
and commits the metadata swap and all rewraps in a single storage
transaction (`Service.RotateKEK`, `internal/core/admin.go`) — so rotation
either completes fully or leaves the previous KEK active, never a partially
rewrapped state. Historical secret versions keep their original `kek_id`
recorded, so retired KEKs must be retained (in the keyring) as long as any
version still references them; rotation does not delete old key material,
it demotes it to "retired" and keeps it available for decryption.

For client-bound secrets, rotation **only touches the outer (KEK) layer**.
The inner, client-token-derived layer is untouched — rotating the master
key never requires contacting any client or invalidating any client token
(plan §11.4.4).

```bash
parameter-store rotate-kek --db /var/lib/parameter-store/kms.db \
    --key-file /etc/parameter-store/master.key \
    --new-key-file /etc/parameter-store/master-new.key
# Then, once satisfied, replace the old key file with the new one and
# restart the server — the database now expects the new key.
```

Omit `--key-file` to unseal the current key from a passphrase instead of a
file; omit `--new-key-file` to be prompted for a new passphrase rather than
generating a new key file. Every rotation is audited as `key.rotate` with
the count of rewrapped versions.

## Monitoring and readiness

- `GET /healthz` — liveness (plain text, no auth).
- `GET /readyz` — readiness: store reachable, migrations applied, master
  key acquired and verified. Alert on this being unready for longer than a
  normal restart window — in interactive/passphrase mode a long "not ready"
  period may simply mean nobody has entered the passphrase yet.
- gRPC standard health service (`grpc.health.v1.Health`) mirrors the same
  readiness signal per registered service name
  (`kms.v1.ParameterService`, `kms.v1.SecretService`, `kms.v1.WatchService`),
  refreshed on a 5s interval by default
  (`grpcserver.Config.HealthRefreshInterval`).
- `GET /api/v1/health` (authenticated-exempt) returns
  `{"healthy","ready","version","current_revision"}` — `current_revision`
  is useful as a coarse "is anything moving" signal.
- The frontend's **Subscribers** page (`/subscribers`, backed by `GET
  /api/v1/subscribers`) is the operational way to confirm a configuration
  change actually propagated: it lists every live-subscribed application,
  its watched paths, last heartbeat, and last-acked revision compared
  against the server's current revision.
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
