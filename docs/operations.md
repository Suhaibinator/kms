# Operations

This is the production runbook: starting the service unattended, TLS/mTLS,
backup and restore, disaster recovery, key rotation, and what to monitor.
For the encryption and authorization design behind these procedures, see
[`security.md`](security.md). For the HTTP API the frontend uses, see
[`http-api.md`](http-api.md).

> **Status note.** Every command below matches the CLI implemented in
> `internal/cli`. The offline commands (`init`, `check`, `backup`,
> `restore`, `create-admin`, `rotate-admin`, `rotate-kek`, `import`) operate directly on the
> SQLite file; the `admin` command group and the convenience commands
> (`put-secret`, `get-secret`, `put-parameter`, `list`, `exec`, `env`,
> `release`) talk to a running server over gRPC. `make build` produces a working `bin/parameter-store`,
> and `serve` opens both the gRPC and HTTP listeners
> (`cmd/parameter-store/main.go` wires `cli.GRPCFactory` to
> `internal/server/grpcserver`).

## Development mode (`dev`)

`parameter-store dev` is the evaluation path, not an operations one. It is
listed here so nobody mistakes it for a deployment mode.

One command produces a complete, running instance:

```bash
parameter-store dev
```

It creates a **dev store** — a single directory holding the SQLite database,
a file-based master key, a throwaway TLS CA with a server keypair signed by
it, and a marker file — then bootstraps that store exactly as `init` does
(current database baseline, master key, built-in CA), creates a token-only admin identity,
seeds demo content, and starts the same `serve` wiring the production binary
runs: TLS on both listeners, the embedded console, metrics, and hot reload.
When the server answers its own health probe it prints a banner on **stderr**
(stdout stays clean) with the console URL, the gRPC address, the CA
certificate path, the two tokens, and two copy-paste commands. `--output json`
replaces the banner with one JSON document on stdout carrying the same facts.

What it seeds (skip with `--no-seed`): the namespaces `dev/demo` and
`prod/demo`, parameters covering every content type with two versions on one
key, two secrets — one expiring in a few days so the posture and expiry
metrics have something to show — a non-admin identity `demo-app` whose policy
allows reads in `dev/demo` only, and an activated configuration release with
its schema. Everything is written through the same core APIs the `admin` CLI
drives over gRPC, so the store is one a person could have built by hand,
audit trail included. Seeding is idempotent: restarting against the same
`--dir` adds nothing.

**The marker file is the safety interlock.** A dev store carries
`.parameter-store-dev`, written when the directory is created. `dev` refuses a
`--dir` that has contents but no marker — *"not a dev store — refusing to
touch it"* — and refuses it *before writing anything*, so a mistyped path
costs an error message and nothing else. `--reset` erases and re-seeds a
marked directory and is refused on an unmarked one, which is what keeps it
from ever being aimed at a production store. Without `--dir` the store is a
private temporary directory that is deleted when the server stops. Everything
inside is created owner-only (`0600` files under a `0700` directory) through
the same `fileutil` helpers the offline commands use.

**Both listeners bind loopback by default** (`127.0.0.1:8443` for HTTP,
`127.0.0.1:8444` for gRPC; `--http-addr`/`--grpc-addr` override, and a flag,
`KMS_*` variable, or config file entry still wins over those defaults in the
usual order). A non-loopback address is a usage error (exit `2`) unless
`--allow-remote` is given, and then a warning is printed that `--quiet` cannot
suppress. That flag is what the container one-liner in the README uses.

**The printed tokens are dev-only.** They belong to that disposable store and
authenticate nowhere else; the banner labels them so. `dev` also turns
`security.admin_require_client_cert` off — the whole point of the banner is
that a bearer token alone gets you in — which is why `serve` logs its
"a stolen admin token alone grants full administrative access" warning on
every `dev` start. That posture is acceptable for a loopback demo and
unacceptable anywhere else.

**Do not use `dev` for real data.** It is not a supported deployment mode: it
manufactures its own TLS material, weakens the admin credential requirement,
prints credentials to a terminal, and (by default) deletes its database on
exit. A real deployment starts with `init` and `serve` — see
[Configuration](#configuration) and
[Startup sequence and readiness](#startup-sequence-and-readiness).

## Configuration

Every setting has exactly three spellings: a YAML key in the config file, a
`KMS_*` environment variable, and a command-line flag. The flag name is
derived mechanically from the environment variable — strip `KMS_`, lowercase,
replace `_` with `-` — so `storage.sqlite_path`, `KMS_SQLITE_PATH`, and
`--sqlite-path` are the same knob.

They resolve highest-precedence first: **flag, then `KMS_*` environment
variable, then the config file, then the built-in default**. The file is named
by `--config FILE` or `KMS_CONFIG`. This order applies to `serve`, to every
offline command that opens the database (`init`, `check`, `backup`,
`restore`, `create-admin`, `rotate-admin`, `admin-cert`, `rotate-kek`,
`import`), and to `config show` / `config validate`. Defaults come from
`internal/config.Default()`.

A running `serve` re-reads the file with exactly this precedence on `SIGHUP`,
so a setting pinned by a flag or an environment variable cannot be changed by
editing the file — see [Hot reload (SIGHUP)](#hot-reload-sighup) for what a
reload applies and what it only reports.

```yaml
server:
  grpc_addr: "0.0.0.0:8443"
  http_addr: "0.0.0.0:8080"
  # Per-identity budgets for the value-free VerifyReleaseDefaults oracle
  # (see security.md#defaults-verification-oracle). Enforced per instance.
  verify_defaults:
    requests_per_hour: 60          # request bucket refill rate
    burst: 10                      # request bucket capacity
    mismatch_budget_per_hour: 500  # non-match verdicts one identity may obtain per hour (minimum 300)

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
  # Admins must present a built-in-CA client certificate *and* their bearer
  # token. Default true; relaxed at startup with a warning while tls_enabled
  # is false. See "Admin credentials and browser setup".
  admin_require_client_cert: true

encryption:
  kek_file: "/etc/parameter-store/master.key"

frontend:
  enabled: true

audit:
  enabled: true
  retain_duration: 0     # 0 keeps audit rows forever
  archive_dir: ""        # JSONL copy of rows before they are retired

metrics:
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

| Env var | Flag | Config key |
|---|---|---|
| `KMS_CONFIG` | `--config` | — (names the config file itself) |
| `KMS_GRPC_ADDR` | `--grpc-addr` | `server.grpc_addr` |
| `KMS_HTTP_ADDR` | `--http-addr` | `server.http_addr` |
| `KMS_VERIFY_DEFAULTS_REQUESTS_PER_HOUR` | `--verify-defaults-requests-per-hour` | `server.verify_defaults.requests_per_hour` (integer) |
| `KMS_VERIFY_DEFAULTS_BURST` | `--verify-defaults-burst` | `server.verify_defaults.burst` (integer) |
| `KMS_VERIFY_DEFAULTS_MISMATCH_BUDGET_PER_HOUR` | `--verify-defaults-mismatch-budget-per-hour` | `server.verify_defaults.mismatch_budget_per_hour` (integer) |
| `KMS_SQLITE_PATH` | `--sqlite-path` | `storage.sqlite_path` |
| `KMS_KEK_FILE` | `--kek-file` | `encryption.kek_file` |
| `KMS_TLS_ENABLED` | `--tls-enabled` | `security.tls_enabled` (parsed with `strconv.ParseBool`) |
| `KMS_MTLS_ENABLED` | `--mtls-enabled` | `security.mtls_enabled` (parsed with `strconv.ParseBool`) |
| `KMS_SERVER_CERT_FILE` | `--server-cert-file` | `security.server_cert_file` |
| `KMS_SERVER_KEY_FILE` | `--server-key-file` | `security.server_key_file` |
| `KMS_CLIENT_CA_FILE` | `--client-ca-file` | `security.client_ca_file` — the CA the **server** verifies client certificates against, not a client's `--ca` trust bundle |
| `KMS_TRUST_PROXY_HEADERS` | `--trust-proxy-headers` | `security.trust_proxy_headers` (parsed with `strconv.ParseBool`) — honor `X-Forwarded-For` for the rate-limit key and audit source IP; enable only behind a trusted reverse proxy (see [TLS and mTLS](#tls-and-mtls)) |
| `KMS_ADMIN_REQUIRE_CLIENT_CERT` | `--admin-require-client-cert` | `security.admin_require_client_cert` (parsed with `strconv.ParseBool`) — **default `true`**; admins must present a built-in-CA client certificate in addition to their bearer token; relaxed with a warning while `tls_enabled` is false (see [Admin credentials and browser setup](#admin-credentials-and-browser-setup)) |
| `KMS_FRONTEND_ENABLED` | `--frontend-enabled` | `frontend.enabled` |
| `KMS_AUDIT_ENABLED` | `--audit-enabled` | `audit.enabled` — controls general-purpose auditing; binding-management mutation and cohort-preview audits remain mandatory and fail closed |
| `KMS_AUDIT_RETAIN_DURATION` | `--audit-retain-duration` | `audit.retain_duration` (duration) — **default `0`**, which keeps audit rows forever |
| `KMS_AUDIT_ARCHIVE_DIR` | `--audit-archive-dir` | `audit.archive_dir` — directory receiving a JSONL copy of audit rows before they are retired; empty discards them. Requires `audit.retain_duration` above 0 |
| `KMS_METRICS_ENABLED` | `--metrics-enabled` | `metrics.enabled` (parsed with `strconv.ParseBool`) — **default `true`**; serve Prometheus metrics on `/metrics` |
| `KMS_WATCH_HEARTBEAT_INTERVAL` | `--watch-heartbeat-interval` | `watch.heartbeat_interval` (duration) |
| `KMS_WATCH_RETAIN_DURATION` | `--watch-retain-duration` | `watch.retain_duration` (duration) |
| `KMS_WATCH_RETAIN_ROWS` | `--watch-retain-rows` | `watch.retain_rows` (integer) |
| `KMS_WATCH_RELEASE_RETAIN_DURATION` | `--watch-release-retain-duration` | `watch.release_retain_duration` (duration) |
| `KMS_WATCH_RELEASE_RETAIN_VERSIONS` | `--watch-release-retain-versions` | `watch.release_retain_versions` (integer) |
| `KMS_WATCH_RELEASE_SUBSCRIBER_RETAIN_DURATION` | `--watch-release-subscriber-retain-duration` | `watch.release_subscriber_retain_duration` (duration) |
| `KMS_LOG_LEVEL` | `--log-level` | `log.level` |
| `KMS_MASTER_PASSPHRASE` | — | — (supplies the master passphrase without a TTY prompt; see below) |

Durations accept either a Go duration string (`"30s"`, `"24h"`) or a bare
number of seconds, in all three spellings alike. Boolean settings must be
written `--tls-enabled` or `--tls-enabled=false`; `--tls-enabled false` is
rejected (`unexpected argument "false" (boolean flags take the form
--flag=false)`) rather than silently reading as `true` plus a stray argument.

Mistakes are errors, not silent fallbacks, and for every command rather than
just `serve`. A malformed `KMS_*` value fails immediately
(`KMS_TLS_ENABLED="yes" is not a valid boolean (use true/false/1/0)`), and an
unrecognized key in the config file is reported with its line and the nearest
match:

```text
config.yaml:12: unknown key storage.sqlite_pth (did you mean storage.sqlite_path?)
```

`Config.Validate()` enforces: both listen addresses set; `sqlite_path` set;
`mtls_enabled` requires `tls_enabled`; `tls_enabled` requires
`server_cert_file`/`server_key_file` to exist; `mtls_enabled` requires
`client_ca_file` to exist; every `server.verify_defaults.*` budget is
positive and `mismatch_budget_per_hour` is at least 300; and every
`watch.*` duration or row/version count is positive.

`admin_require_client_cert: true` together with `tls_enabled: false` is
deliberately **not** a validation error. The requirement is unenforceable
without TLS — no certificate ever reaches the server — so `serve` relaxes it
at runtime and says so in the startup log, rather than making a development
machine change its configuration to start. `Config.Redacted()` is
the one-line summary the server logs at startup — addresses, paths, and
feature flags, with the key file reported only as `set`/`unset` — so nothing
sensitive that might end up in the YAML by mistake gets logged. For the
complete effective configuration, including where each value came from, use
[`config show`](#inspecting-the-effective-configuration).

Logs are structured JSON emitted by [Uber zap](https://github.com/uber-go/zap):
each line carries `ts` (ISO8601, millisecond precision), a lowercase `level`
(`debug`/`info`/`warn`/`error`), `msg`, and typed fields. `log.level` /
`KMS_LOG_LEVEL` sets the minimum level (default `info`). Accepted values are
`debug`, `info`, `warn`/`warning`, and `error` (case-insensitive); an empty
value means `info`, and anything else is a validation error — `serve` refuses
to start, and a hot reload is rejected as a whole. Secret plaintext, tokens,
and key material never appear in a log line at any level.

The HTTP server uses 10 s read-header, 30 s read, 60 s write, and 120 s idle
timeouts. The release-subscriber SSE endpoint clears its write deadline for
that response and enforces its own five-minute lifetime. Configure reverse
proxies with limits at least this large.

### Inspecting the effective configuration

`config show` resolves the configuration exactly as `serve` (or `init`,
`backup`, `restore`, …) would with the same arguments and environment, then
prints every setting with its value and the source that won. It answers
"which database file is this actually going to open?" without reading three
sources by hand. Paths are printed in full; `KMS_MASTER_PASSPHRASE` is
reported only as `set` or `unset`, never its value.

```text
$ KMS_SQLITE_PATH=/data/kms.db parameter-store config show \
    --config /etc/parameter-store/config.yaml --tls-enabled
config file: /etc/parameter-store/config.yaml (flag --config)

KEY                     VALUE            SOURCE
server.grpc_addr        0.0.0.0:8443     file server.grpc_addr
server.http_addr        0.0.0.0:8080     default
storage.sqlite_path     /data/kms.db     env KMS_SQLITE_PATH
encryption.kek_file     /key/master.key  file encryption.kek_file
security.tls_enabled    true             flag --tls-enabled
...                                      (one row per setting)
log.level               info             default

KMS_MASTER_PASSPHRASE: set
```

The `SOURCE` column is one of `default`, `file <key>`, `env <VAR>`, or
`flag --<name>`; a setting whose effective value is empty prints as `""`.

`config validate` resolves the same way and then runs the same checks `serve`
does, including that the referenced TLS certificate and key files exist:

```text
$ parameter-store config validate --config /etc/parameter-store/config.yaml
configuration OK (/etc/parameter-store/config.yaml)
```

With no file in play it reports `configuration OK (no config file; defaults,
environment, and flags only)`. Both commands accept `--config` and every
server setting flag, so they can be run in CI, or from a systemd
`ExecStartPre=`, to catch a bad file or a typo'd environment variable before a
restart takes the service down.

Every command also documents itself: `parameter-store <command> -h` prints a
usage line, a description, and a flag table that names each setting's
environment variable and config key (`--sqlite-path PATH  SQLite database file
path (env KMS_SQLITE_PATH, config storage.sqlite_path)`), followed by a
reminder of the resolution order.

## Connect a production application with mTLS

Use one KMS identity and one client certificate/key pair per consuming
application. Before issuing anything, distinguish the four certificate roles:

| Certificate role | Created and stored by | Used for |
|---|---|---|
| **KMS server certificate/key + server trust CA** | The operator obtains the serving certificate from the organization's PKI or another trusted CA, configures `server_cert_file`/`server_key_file`, and distributes a `server-ca.crt` trust bundle to applications. | KMS presents the serving certificate; applications use `server-ca.crt` to verify the KMS server. The server private key stays on the KMS host. |
| **KMS built-in client-issuing CA** | `parameter-store init` creates this self-signed CA and stores it in SQLite's `ca_keys` table; the private key is KEK-wrapped. | KMS issues and verifies application client certificates and administrator certificates. Applications do **not** use this CA for server trust. `admin ca show` exports its public certificate for diagnostics or out-of-band verification only. |
| **Per-application client certificate/key** | KMS creates it when an mTLS identity is enrolled. Its serial/fingerprint enrollment remains in `identity_certs`; the one-time PEM files go to the operator. | The application presents the certificate and proves possession of its private key. KMS maps its `kms://identity/<name>` URI SAN to the enrolled identity and checks its serial, fingerprint, expiry, and revocation state. |
| **Administrator certificate/key** | Minted only on the KMS host by `parameter-store admin-cert issue NAME --out DIR` (or `init`/`create-admin` with `--cert-dir`); never issued over the network. | An administrator presents it **in addition to** their bearer token, on the CLI (`--cert`/`--key`) and in the browser (imported as PKCS#12). See [Admin credentials and browser setup](#admin-credentials-and-browser-setup). |

The following secure path assumes the server is already running with
[`security.tls_enabled: true`](#tls-and-mtls), the operator has the CA bundle
that trusts its serving certificate, and an admin credential — token **and**
certificate — is available.
The serving certificate's DNS or IP SAN must match the host applications use
in `KMS_ENDPOINT` (`kms.internal` below), because the SDKs perform normal
server-name verification. The built-in client CA works with
`mtls_enabled: false`; that flag is only for adding a separate
operator-supplied client CA.

### 1. Create the application's namespace

New namespaces default to mTLS-only, but specifying the method makes the
intended posture visible in scripts and reviews:

```bash
ADMIN_TOKEN=...                 # the admin's bearer token
ADMIN_CERT=./admin-creds/ops.crt   # its client certificate, from admin-cert issue
ADMIN_KEY=./admin-creds/ops.key
KMS_ENDPOINT=kms.internal:8443
SERVER_CA=/etc/parameter-store/trust/server-ca.crt

parameter-store admin namespace create --env prod --app gradethis \
    --auth-methods mtls --endpoint "$KMS_ENDPOINT" --ca "$SERVER_CA" \
    --cert "$ADMIN_CERT" --key "$ADMIN_KEY" --token "$ADMIN_TOKEN"
```

An admin needs **both** credentials on every command: the token above and the
certificate pair issued offline with
[`admin-cert issue`](#admin-credentials-and-browser-setup). Exporting
`KMS_TOKEN`, `KMS_CLIENT_CERT_FILE`, and `KMS_CLIENT_KEY_FILE` once lets the
rest of this walkthrough drop the three flags.

For an existing namespace, inspect and update it with the same secure
connection flags:

```bash
parameter-store admin namespace list \
    --endpoint "$KMS_ENDPOINT" --ca "$SERVER_CA" \
    --cert "$ADMIN_CERT" --key "$ADMIN_KEY" --token "$ADMIN_TOKEN"

parameter-store admin namespace update --env prod --app gradethis \
    --auth-methods mtls --endpoint "$KMS_ENDPOINT" --ca "$SERVER_CA" \
    --cert "$ADMIN_CERT" --key "$ADMIN_KEY" --token "$ADMIN_TOKEN"
```

`namespace update` is a full replacement of both the description and auth
method list, so preserve any existing description intentionally.

The embedded console performs the same step from the application page. **Add
environment** creates the namespace with an explicit auth-method choice
(production names are detected and outlined), and can **copy values from**
an existing environment through `POST /api/v1/applications/environments/clone`:
parameters are copied as new versions in the target, keys that already exist
there are left untouched, and secrets are never copied — each is listed as
needing a value so it is provisioned deliberately per environment. Use that
path for the second and later environments of an application; the CLI
commands above remain the scriptable form.

### 2. Enroll one identity and issue its client certificate

Prepare an owner-controlled directory, then create a namespace-bound,
certificate-only identity:

```bash
# POSIX: create an owner-only output directory.
install -d -m 0700 ./credentials/gradethis-be
# PowerShell, from a trusted current-user directory:
# New-Item -ItemType Directory -Force .\credentials\gradethis-be

parameter-store admin identity create gradethis-be \
    --namespace prod/gradethis --auth mtls --ttl 90d \
    --out ./credentials/gradethis-be \
    --endpoint "$KMS_ENDPOINT" --ca "$SERVER_CA" \
    --cert "$ADMIN_CERT" --key "$ADMIN_KEY" --token "$ADMIN_TOKEN"
# writes ./credentials/gradethis-be/gradethis-be.crt (0644)
#        ./credentials/gradethis-be/gradethis-be.key (0600, written only once)
```

On Windows, the CLI applies a protected current-user-only DACL to the private
key. On every platform, the output directory must be controlled by the account
running the command; see [Secure destination paths](#secure-destination-paths).

The private key cannot be retrieved later. If it is lost, issue another
certificate rather than trying to recover it. Do not create one shared
identity for several applications: separate identities make policy, audit,
rollover, and revocation boundaries explicit.

This is the **client-kind** path. `admin identity create --kind admin` mints a
token only — `--auth mtls` is rejected for an admin, and `issue-cert` refuses
an admin target — because an admin certificate is minted only on the server
host with [`admin-cert issue`](#admin-credentials-and-browser-setup).

### 3. Deploy the application credentials and server trust

Deliver these paths to the consuming application through its normal secret
and configuration mechanism:

```text
KMS_ENDPOINT         = kms.internal:8443
KMS_CLIENT_CERT_FILE = /run/credentials/gradethis-be.crt
KMS_CLIENT_KEY_FILE  = /run/credentials/gradethis-be.key
KMS_CA_FILE          = /etc/ssl/kms/server-ca.crt
```

The client certificate is public identity material, but its private key is a
secret and should be readable only by the application account. The server CA
bundle is public trust configuration and must come from the operator or the
organization's PKI. Do **not** deploy the KMS server private key, the built-in
client-issuing CA, or an admin token to the application. In particular, do not
substitute output from `admin ca show` for `KMS_CA_FILE`.

### 4. Configure the SDK

The helpers in all three SDKs take the application certificate, application
private key, and server CA in that conceptual order. Because this identity is
bound to `prod/gradethis`, the SDK can discover the namespace through
`WhoAmI`; no bearer token or explicit namespace is required.

Go ([full guide](sdk-go.md)):

```go
client, err := kmsclient.NewClient(kmsclient.Config{
    Endpoint: os.Getenv("KMS_ENDPOINT"),
    TLS: kmsclient.MTLSFromFiles(
        os.Getenv("KMS_CLIENT_CERT_FILE"),
        os.Getenv("KMS_CLIENT_KEY_FILE"),
        os.Getenv("KMS_CA_FILE"),
    ),
})
if err != nil {
    return err
}
defer client.Close()
```

Python ([full guide](sdk-python.md)):

```python
import os
from kms_paramstore import Client, mtls_from_files

with Client(
    os.environ["KMS_ENDPOINT"],
    tls=mtls_from_files(
        os.environ["KMS_CLIENT_CERT_FILE"],
        os.environ["KMS_CLIENT_KEY_FILE"],
        os.environ["KMS_CA_FILE"],
    ),
) as client:
    identity = client.who_am_i()  # verifies TLS, the client cert, and enrollment
```

TypeScript/Node.js ([full guide](../sdk/typescript/README.md)):

```ts
import { createClient, mtlsFromFiles } from "@suhaibinator/kms";

const client = createClient({
  endpoint: process.env.KMS_ENDPOINT!,
  credentials: mtlsFromFiles(
    process.env.KMS_CLIENT_CERT_FILE!,
    process.env.KMS_CLIENT_KEY_FILE!,
    process.env.KMS_CA_FILE!,
  ),
});

try {
  const identity = await client.whoAmI(); // verifies TLS, the client cert, and enrollment
} finally {
  await client.close();
}
```

The application page's **Connect SDK** panel renders these Go and
TypeScript snippets pre-filled with the server's gRPC address (from
`GET /api/v1/health`), the namespace, the release name, and the first alias,
links to identity creation with the namespace prefilled, and warns when the
server reports `tls_enabled: false`. Its troubleshooting list covers the
three usual failures: an identity that is not bound to the namespace, an
auth method the namespace does not allow, and a loader release name that
differs from the application's `release_name`.

A namespace-bound identity receives an implicit read/list grant in its home
namespace. Create a policy for writes or cross-namespace access; the policy
subject is the identity name (`gradethis-be`). For renewal, follow the
[certificate rollover runbook](#built-in-ca-and-client-certificates): issue a
new certificate while the old one remains valid, deploy it, then revoke the
old serial.

For a loopback-only development server, use a separate namespace that
explicitly allows `token`, a token-only identity, and the SDK's explicit
`insecure` option. Never use that cleartext path on a networked bind. The root
[README local quickstart](../README.md#local-development-connect-with-a-token)
keeps this development workflow separate from production mTLS.

## Administrative CLI reference

These commands are implemented in `internal/cli` and operate directly on the
SQLite file — they do not require a running server (`create-admin`,
`rotate-admin`, and `admin-cert` change credentials, which a running `serve`
process reads immediately via its shared database). They resolve the database
path, the
master key file, and every other setting exactly as `serve` does:
`--sqlite-path` beats `KMS_SQLITE_PATH`, which beats `storage.sqlite_path` in
the `--config` file, which beats the built-in `./kms.db`. A host or container
that already exports `KMS_SQLITE_PATH` and `KMS_KEK_FILE` therefore needs no
path flags at all. All flags are the standard library `flag` package's
double-dash-or-single-dash form (e.g. `--sqlite-path` or `-sqlite-path`), and
boolean flags must be written `--force` or `--force=false`, never
`--force false`.

Because the path can come from three places, each command prints the
**absolute** path it resolved along with the source:

```text
$ parameter-store init --admin ops
Initialized database at /data/kms.db (source: env KMS_SQLITE_PATH)
```

`restore` and `rotate-kek` print `Target database: /abs/path (source: ...)` to
stderr before they touch anything, so a destructive run names its target
first, and then confirm before acting — see
[Global flags, output formats, and exit codes](#global-flags-output-formats-and-exit-codes)
for the confirmation rules, the machine-readable `--output json` mode, the exit
codes every command shares, and the `--token-file` credential form.

| Command | Flags | Purpose |
|---|---|---|
| `init` | `--sqlite-path` (default `./kms.db`), `--kek-file` (omit for a passphrase prompt), `--admin NAME` (optional), `--cert-dir DIR` (optional, requires `--admin`) | Materializes a fresh `0.3.x` baseline database, creates the master key (generating a key file, or prompting for a new passphrase with confirmation), and creates the **built-in CA**. With `--admin`, also creates a bootstrap admin identity and prints its token once; with `--cert-dir`, also issues that admin's client certificate into `DIR/NAME.crt` and `DIR/NAME.key`. A `0.2.x` database is not upgraded in place. |
| `check` | `--sqlite-path`, `--kek-file` (optional) | Verifies the database is reachable and, whenever a key source resolves (a key file from the flag, `KMS_KEK_FILE`, or `encryption.kek_file`; `KMS_MASTER_PASSPHRASE`; or a TTY), verifies the master key against the stored key-check value. Never prints key material. |
| `backup` | `--sqlite-path`, `--out` (required; an existing file is refused with exit `6`) | Writes a consistent online backup through owner-only staging and atomic no-replace publication. Prints a reminder that the master key is not included. |
| `restore` | `--sqlite-path` (destination), `--in` (required, source backup), `--force` (overwrite an existing destination), `--yes` | Validates the input is a current-baseline KMS SQLite backup, stages an owner-only copy, publishes it atomically, removes stale `-wal`/`-shm` sidecars, then opens it to verify the baseline. Without `--force`, publication never replaces an existing or concurrently created entry — and an existing destination is refused before the prompt. **Confirms `[y/N]`** after printing the target; a script needs `--yes`, plus `--force` if the destination exists. |
| `create-admin` | `--sqlite-path`, `--name` (required), `--kek-file`, `--cert-dir DIR` (optional) | Creates an admin identity directly against the database file and prints its token once. With `--cert-dir`, also issues the admin's client certificate into `DIR/NAME.crt` and `DIR/NAME.key` — that path unseals the master key and requires an existing CA. Without it the admin is token-only and no unseal (or passphrase prompt) happens, so it cannot sign in while `admin_require_client_cert` is enforced until `admin-cert issue` runs. Uses WAL mode's concurrent-reader support, but coordinating this against a live `serve` process is the operator's responsibility. |
| `rotate-admin` | `--sqlite-path`, `--name` (required) | Recovery command that directly replaces an existing enabled admin identity's token hash and prints the new token once. The old token becomes invalid immediately; a disabled admin, client identity, missing identity, or identity without a token is rejected without mutation. It does not require the old token, master key, or a running server. If output is lost, rerun the command to mint another replacement. A running server observes the shared-database update immediately, but operators must coordinate concurrent identity administration. |
| `admin-cert issue` | `NAME` (positional), `--out DIR` (**required**), `--ttl` (default 90d), `--sqlite-path`, `--kek-file` | Issues a client certificate for an existing admin identity, offline. Unseals the master key, requires an existing CA, and refuses a non-admin, unknown, or disabled target without writing anything. Writes `DIR/NAME.crt` (`0644`) and `DIR/NAME.key` (`0600`), both created exclusively; the private key is never printed. Audited as `identity.cert.issue` with actor `cli` and `channel: local`. This is the **only** way to mint an admin certificate. |
| `admin-cert revoke` | `NAME` (positional), `--serial SERIAL` (required), `--sqlite-path`, `--yes` | Revokes one of that admin's certificates. Needs no master key. The certificate stops authenticating on the next request; a running server sees the change immediately. **Confirms by retyping `NAME`.** |
| `admin-cert list` | `NAME` (positional), `--sqlite-path` | Prints the admin's certificates as `SERIAL FINGERPRINT STATE EXPIRES ISSUED`, where state is `valid`, `revoked`, or `expired`. Read-only and unaudited. |
| `rotate-kek` | `--sqlite-path`, `--kek-file` (current key, omit to use the current passphrase), `--new-key-file` (new key, omit to enter a new passphrase), `--yes` | **Confirms by retyping the absolute database path**, before the database is opened or any passphrase is prompted for. Unseals with the current key, generates or loads the new key, and calls the same `Service.RotateKEK` used internally — rewrapping every **non-destroyed** secret version's DEK and every built-in CA key under the new KEK in one transaction. Prints both counts (`N secret versions and M CA keys rewrapped`). Run with `serve` stopped; a live process retains the old keyring. |
| `dev` | `--dir DIR`, `--no-seed`, `--reset`, `--allow-remote`, plus `--http-addr`, `--grpc-addr`, `--log-level` | **Evaluation only.** Creates a disposable dev store (database, master key, built-in CA, TLS material, a marker file), bootstraps it the way `init` does, seeds demo namespaces/parameters/secrets/identity/release, and runs the real `serve` wiring on loopback with TLS and the console. Prints a banner on stderr with the console URL, CA path, and two dev-only tokens; `--output json` prints the same facts as one document on stdout. Refuses a `--dir` that has contents but no `.parameter-store-dev` marker, and refuses a non-loopback address without `--allow-remote`. See [Development mode](#development-mode-dev). |
| `healthcheck` | `--ready`, `--timeout` (default `3s`), plus `--http-addr` and `--tls-enabled` | Probes this host's own HTTP listener at `127.0.0.1:<server.http_addr port>/healthz` (`/readyz` with `--ready`) and exits `0` on HTTP 200, `1` otherwise — a connection error is one line on stderr. It resolves the address and the TLS posture through the same flag > env > config file > default order `serve` uses, so a container or unit that already supplies them needs no arguments. The server certificate is **not** verified: this is a loopback liveness check for a container `HEALTHCHECK` or a process supervisor (the image ships one), never a way to check a remote server. Needs no database, master key, or credentials. |
| `audit prune` | `--older-than DURATION` (**required**; `720h`, `90d`, or an RFC 3339 instant), `--archive DIR` (must already exist), `--dry-run`, `--sqlite-path`, `--yes` | Retires audit events older than the cutoff directly from the database file, **archiving before it deletes**: with `--archive`, every row is appended to `DIR/audit-<YYYYMMDD>.jsonl` (0600) and the file is synced before the row is removed, so an unwritable archive means the rows stay. Without `--archive` the rows are discarded outright. Prints `Target database: /abs/path (source: ...)` and then **confirms by retyping the absolute database path**; `--dry-run` counts the matching rows, prints `Would prune N audit events`, and skips both the confirmation and any deletion. Needs no master key and no running server. See [Audit retention and archive](#audit-retention-and-archive). |
| `import` | `--from` (JSON file or SuhaibParameterStore SQLite export), `--namespace env/app` **or** `--env`/`--app`, `--sqlite-path` (default `./kms.db`), `--kek-file`, `--dry-run`, `--report FILE` | Maps flat source keys to **relative slug keys** (`slug(key)`, e.g. `TWILIO_SID` → `twilio-sid`) in the destination namespace, **auto-creates the namespace** if it does not exist, writes each as a new secret via a ref-based `PutSecret` with a freshly minted per-secret access token, and emits a mapping report (old key → `/env/app/key` display path → token, written once). Distinct source keys that slug to the same key are reported as a collision rather than silently overwriting. `--dry-run` reports the mapping without writing or minting tokens. Pass either `--namespace` or `--env`/`--app`, not both. See [`migration.md`](migration.md) for the full gradethis walkthrough. |

`import --from` accepts either a SuhaibParameterStore SQLite database with a
`parameters(key, value)` table, a JSON object such as `{"KEY":"value"}`, or a
JSON array such as `[{"key":"KEY","value":"value"}]`. JSON strings import
as their unquoted text; other JSON values retain their JSON encoding, and all
imported values use secret content type `text/plain`. The report is plain text,
not CSV: each real-import row is `old-key -> /env/app/key -> access-token`.

Import is not an all-or-nothing transaction: namespace creation and each
secret version commit independently, and the one-time token report is written
only after all values succeed. If a later write or report write fails, inspect
the destination before retrying. A retry creates additional versions and may
rotate access tokens; preserve only the final successful report.

This importer is a greenfield bootstrap tool, not a KMS schema-compatibility
path. Its SQLite reader understands only the separate SuhaibParameterStore
`parameters(key, value)` source format; the other accepted source is neutral
JSON. It creates ordinary current-model resources through `PutSecret` in an
already recognized `0.3.x` destination. It neither reads a `0.2.x` KMS schema
nor translates old KMS ciphertext, releases, protobufs, or metadata.

`0.3.x` is a greenfield baseline. Initialize a fresh database and reseed or
import the intended resources; `init` does not repair or mutate a `0.2.x`
database. Old client-bound payloads and old release digests have no
compatibility path. Keep the old database only as a separately controlled
rollback/audit artifact. See [`migration.md`](migration.md#03x-database-cutover).

`init`, `check`, `rotate-kek`, `import`, `admin-cert issue`, and
`create-admin --cert-dir` all go through the same
master-key acquisition path as `serve` (key file → `KMS_MASTER_PASSPHRASE`
→ interactive prompt) via the shared `unseal` helper, so the same no-TTY
fail-fast behavior applies. (`create-admin` without `--cert-dir`,
`admin-cert revoke`, and `admin-cert list` need no key and never prompt.)
The key file is whichever of `--kek-file`,
`KMS_KEK_FILE`, or `encryption.kek_file` wins the usual resolution, so a
container or systemd unit that already supplies one gets the unattended path
without repeating it on the command line — and `check` verifies the master key
whenever a key source resolves that way, not only when a key file is named on
the command line.

### Global flags, output formats, and exit codes

Four flags apply to every command, `version` and `help` included. All four may
precede the subcommand; `--output`, `--yes`, and `--quiet` — short forms
included — are also accepted after it, and a later occurrence wins over an
earlier one.

| Flag | Environment | Effect |
|---|---|---|
| `-o`, `--output table\|json` | `KMS_OUTPUT` | Result format (default `table`). A flag beats the variable. |
| `-y`, `--yes` | — | Answer confirmation prompts. Destructive commands require it on a non-interactive stdin. |
| `-q`, `--quiet` | — | Suppress informational stderr lines. |
| `--config FILE` | `KMS_CONFIG` | Config file path. |

`-o` is always `--output`; a command's own output-directory flag is spelled
`--out` and has no short form, so `--help` lists the pair as `--output FORMAT,
-o` and `--out DIRECTORY`. `--config` after the subcommand is accepted only by
the commands that read server settings (`serve`, `config`, and the offline
database commands); an online command rejects it there. An invalid format —
from either the flag or `KMS_OUTPUT` — is a usage error.

`--quiet` drops progress and advice only. It never suppresses errors,
confirmation prompts, the `release activate` preview, the `Target database: ...`
line, or a one-time token / private-key warning.

#### JSON output

With `--output json` (or `KMS_OUTPUT=json`) **stdout carries exactly one JSON
document** and nothing else; status lines, warnings, and next-steps blocks move
to stderr. Documents are indented two spaces, use `snake_case` keys, spell
timestamps as RFC 3339 in UTC (`2026-11-30T23:18:36.502Z`), and use `null` —
rather than an omitted key — for an absent optional value.

```bash
parameter-store -o json admin namespace list | jq -r '.items[].env'

export KMS_OUTPUT=json          # same, for a whole script
parameter-store admin identity list | jq -r '.items[] | select(.disabled).name'
```

Every list command returns the same envelope, and `items` is never `null`:

```json
{
  "items": []
}
```

`next_page_token` appears only when a listing was truncated. Every list command
in the CLI follows every page itself, so the field is omitted in practice; treat
its presence as the signal to page, not its absence as proof of completeness.

One-time credentials keep their table-mode rules:

- A one-time token — an identity token, a rotated admin token, a per-secret
  access token, an import token — appears in the document exactly once. The
  "shown once" warning stays on stderr, where `--quiet` cannot reach it.
- `import --report FILE --output json` writes the tokens to the report file
  **only**: each entry's `token` is blank and the document carries
  `report_file`, so the same credentials never land in two places (the same
  single-sink rule as `get-secret --out`).
- Certificate bundles are **never** embedded. The files are written to
  `--out`/`--cert-dir` and the document names them (`cert_file`, `key_file`).
  `admin identity create` and `admin identity issue-cert` with `--output json`
  and no `--out` are refused with exit 2 (`--out is required with --output
  json: the one-time private key is written to a file, never to the JSON
  document`). `admin-cert issue` already requires `--out` in both modes.
- `get-secret --output json` keeps the terminal guard: with `--out FILE` the
  bytes go to the file, `value` is `null`, and `out_file` names it; otherwise
  the value is inlined only when `--show` was given or stdout is not a
  terminal, and a bare terminal is refused exactly as in table mode. A value
  that is not valid UTF-8 has no JSON string form and is refused with an
  instruction to use `--out FILE` rather than corrupted.
- `defaults apply --execute --output json` prints only the applied result. The
  preview it runs first stays a human-only step, because stdout may carry only
  one document.

The documents themselves, by command. "items of X" means the list envelope
above with `X` as each element:

| Command | JSON document |
|---|---|
| `dev` | `{console_url, http_addr, grpc_addr, store_dir, ephemeral, ca_file, admin, demo_app, seeded, namespaces, examples}` — `admin` and `demo_app` are `{name, token}`, `demo_app` is `null` with `--no-seed`; printed once the server is ready, in place of the banner |
| `init` | `{sqlite_path, sqlite_path_source, master_key, kek_file, ca, admin}` — `master_key` is `file` or `passphrase`, `kek_file` is absent in passphrase mode, `admin` is `null` without `--admin` |
| `create-admin` | `{name, token, cert}` — the same object `init` nests under `admin`; `cert` is `{cert_file, key_file}` or `null` |
| `check` | `{database, master_key, sqlite_path, sqlite_path_source}` — `master_key` is `ok`, `not_initialized`, or `not_checked` |
| `backup` | `{backup_file, sqlite_path}` |
| `restore` | `{sqlite_path, backup_file}` |
| `rotate-admin` | `{name, token}` |
| `rotate-kek` | `{kek_id, secret_versions_rewrapped, ca_keys_rewrapped, new_key_file}` — `new_key_file` is absent in passphrase mode |
| `import` | `{namespace: {env, app}, dry_run, imported, report_file, entries}`, entry `{key, path, token}` — `token` only on a real import without `--report`; with `--report FILE` the tokens are written to that file, `token` is blank, and `report_file` names it |
| `config show` | `{config_path, config_path_source, passphrase, settings}`, setting `{key, value, source}` — `passphrase` reports `set`/`unset` only |
| `config validate` | `{valid, config_path}` — only the valid case is printed; an invalid configuration exits non-zero with the reason on stderr |
| `version` | `{version}` |
| `whoami` | `{name, kind, namespace, auth_method}` — `kind` `client\|admin`, `namespace` `{env, app}` or `null`, `auth_method` `mtls\|token` |
| `put-secret` | `{key, version, revision, access_token}` — `access_token` only with `--generate-token` |
| `get-secret` | `{key, version, value, content_type, created_at, out_file}` — with `--out` the value went to the file, so `value` is `null` and `out_file` names it; otherwise `out_file` is absent |
| `env` | the `--format json` object `{"NAME": "value", ...}` — with `--out` the assignments went to the file, so stdout carries `{out_file, variables}` instead |
| `put-parameter` | `{key, version, revision}` |
| `list` | items of `{type, path, current, note, bound}` — `type` is `parameter` or `secret` |
| `admin namespace create`, `admin namespace update` | `{env, app, auth_methods}` |
| `admin namespace delete` | `{env, app, deleted}` |
| `admin namespace list` | items of `{env, app, auth_methods, parameter_count, secret_count, description}` |
| `admin identity create` | `{name, kind, namespace, auth_methods, token, cert}` — `token` and `cert` present only when minted; `cert` is `{cert_file, key_file, serial, expires_at}` |
| `admin identity issue-cert`, `admin-cert issue` | `{name, serial, cert}` with the same `cert` object |
| `admin identity rotate` | `{name, token}` |
| `admin identity revoke` | `{name, revoked}` |
| `admin identity revoke-cert`, `admin-cert revoke` | `{name, serial, revoked}` |
| `admin identity list` | items of `{name, kind, namespace, has_token, cert_count, disabled}` |
| `admin-cert list` | items of `{serial, fingerprint, state, expires_at, issued_at}` — `state` is `valid`, `revoked`, or `expired` |
| `admin policy create` | `{name, subject, allow, deny}` |
| `admin policy list` | items of that same object |
| `admin policy delete` | `{name, deleted}` |
| `admin ca show` | `{cert_pem}`, or `{ca_file}` with `--out` |
| `release create` | `{namespace, name, version, digest}` |
| `release validate` | `{valid, errors}`, error `{alias, code, schema_pointer, message}` |
| `release show` | `{namespace, name, version, schema, digest, created_at, entries}` — `schema` is `{id, version}` or `null`; entry `{alias, kind, path, version, content_type, parameter_digest}`. Activation state is not part of a manifest; `release list` reports `current`/`previous` |
| `release list` | items of `{name, version, current, previous, revision, digest, created_at}` |
| `release diff` | `{from: {name, version}, to: {name, version}, added, removed, changed}` — `added`/`removed` are release entries, `changed` is `{alias, from, to}` |
| `release activate`, `release rollback` | `{namespace, name, version, previous_version, revision, changed}` |
| `release subscribers` | items of `{identity, client, instance, received, prepared, applied, rejected, lag, connected}` — each lifecycle state is `{release_version, activation_revision, rejection_category}` or `null` |
| `release verify-defaults` | `{name, version, activation_revision, schema, clean, entries, counts}`, entry `{alias, verdict}`, counts `{match, differs, missing_in_release, unknown_alias, secret_alias, unsupported_content_type, unverified}` |
| `release schema create` | `{application, release_name, version, digest}` |
| `release schema show` | `{application, release_name, version, digest, schema}` — `schema` is the schema document itself, not a string |
| `release schema list` | items of `{application, release_name, version, digest, created_at}` |
| `defaults apply` | `{profile, plan_digest, executed, definition_changed, definition_updated, entries, missing_secrets, counts}`, entry `{status, alias, key, content_type, current_version, applied_version, revision}`, counts `{create, unchanged, update, blocked}` |

#### Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. |
| `1` | An error not classified below, including every local-file problem: a missing or unreadable token file, CA bundle, configuration file, manifest, or `defaults` artifact. |
| `2` | Usage: a bad flag, a stray positional argument (`--yes false` is the usual culprit), a missing or unknown action or positional, a missing required flag, an invalid `ENV/APP` or `VERSION`, or a refused or mistyped confirmation. Nothing is dialed or opened first. |
| `3` | Unauthenticated. |
| `4` | Permission denied. |
| `5` | Not found: the store or server has no such namespace, identity, policy, key, release, or version. A file the CLI itself could not find is `1`, so scripts can tell "no such secret" from "the token file is missing". |
| `6` | Conflict: the resource already exists (`admin identity create` for an existing name, `backup --out` and `restore` onto an existing file without `--force`), or a compare-and-swap lost. |
| `7` | Failed precondition, including an activation that release validation refused. |
| `8` | Server unavailable: unreachable, not ready, or the call deadline expired. |
| `9` | Rate limited (resource exhausted). |

Codes 3–9 mirror the gRPC status the server returned, so an online command's
exit code is the server's own verdict; offline commands map the equivalent
store sentinels the same way.

```bash
parameter-store get-secret /prod/gradethis/db-password --out ./db-password
case $? in
  0) ;;
  5) echo "no such secret" >&2; exit 1 ;;
  4) echo "this credential may not read it" >&2; exit 1 ;;
  8) echo "KMS unreachable; will retry" >&2; exit 75 ;;
  *) exit 1 ;;
esac
```

Three commands keep their own contract:

- `check` is a health probe: `0` healthy, `1` unhealthy. (A bad invocation is
  still `2`.)
- `release verify-defaults` keeps `0` verified, `1` not verified (any non-match
  verdict, a schema mismatch, or an RPC failure), `2` usage.
- `release validate` prints its verdict — in both output modes — and exits `1`
  when the release is invalid.

#### Token files instead of `--token`

`--token` is visible to every local user in `ps` output and
`/proc/PID/cmdline`. Every command that talks to a running server therefore
also accepts a file:

| Flag | Environment | Holds |
|---|---|---|
| `--token-file FILE` | `KMS_TOKEN_FILE` | The identity bearer token. |
| `--secret-token-file FILE` | `KMS_SECRET_TOKEN_FILE` | The per-secret access token for `get-secret`. |

`exec` and `env` read many secrets at once, so they spell the same idea as a
repeatable `--secret-token-file KEY=PATH` with its own
[`KMS_SECRET_TOKEN_<NAME>`](#bound-secrets-and-per-secret-access-tokens)
variables; the single-valued
flag and `KMS_SECRET_TOKEN_FILE` below are `get-secret` only.

Prefer these over `--token`/`--secret-token` anywhere the command line is
observable — a shared host, a CI runner, a container others can `exec` into.
The file must be a regular file owned by the caller with no group or other
access (`0600` or `0400`), under a parent chain that satisfies the
[secure destination-path](#secure-destination-paths) rules. It is opened
read-only and never modified, so a `0400` credential on a read-only mount (a
Kubernetes secret volume, for instance) works. It must hold exactly one token:
a single trailing newline is trimmed, and an empty file or any interior
whitespace is rejected rather than silently turned into an anonymous call.

Supplying both spellings is a usage error rather than a precedence question, so
a stale inline token can never shadow a rotated file:

```text
$ parameter-store whoami --token "$KMS_TOKEN" --token-file ./kms.token
error: --token and --token-file (or KMS_TOKEN and KMS_TOKEN_FILE) are mutually exclusive
```

The check covers the environment: exporting `KMS_TOKEN` and passing
`--token-file` (or the reverse) fails the same way. Note that `--secret-token`
has no environment fallback of its own; only `--secret-token-file` /
`KMS_SECRET_TOKEN_FILE` does. `KMS_SECRET_TOKEN_FILE` is read only by
`get-secret`, the command that accepts the single-valued
`--secret-token-file`: a shell that exports it can still run `put-secret`, `list`, `whoami`,
`exec`, `env`, or any `admin` command without those calls opening — or failing
on — a file they would never use.

#### Confirmations

Destructive commands confirm on stderr, in one of two ways.

**Retype the resource.** The operator types the exact target back, which forces
a second look at *what* is about to be destroyed rather than a reflexive `y`:

| Command | What must be retyped |
|---|---|
| `admin namespace delete` | `ENV/APP`, e.g. `prod/gradethis` |
| `admin identity revoke` | the identity name |
| `admin identity revoke-cert` | the identity name |
| `admin-cert revoke` | the identity name |
| `release rollback` | `ENV/APP` |
| `rotate-kek` | the absolute database path |

```text
$ parameter-store admin namespace delete --env prod --app gradethis
This will delete namespace prod/gradethis. This cannot be undone.
Type "prod/gradethis" to confirm:
```

A mismatch exits `2` and the command does not act — for `admin namespace
delete` the server is never even contacted.

**Yes or no.** `restore`, `release activate`, `secret purge-binding-cohort`,
and `secret purge-unbound-versions` ask `[y/N]`
after printing what they are about to
do. Each purge first prints the preview's exact affected versions and an
explicit irreversible/admin warning. The default is no;
anything but `y`/`yes` aborts with exit `2` and sends no mutation RPC.

`--yes` answers both kinds. On a non-interactive stdin — a pipe, cron, a CI
runner — a command that would prompt refuses before touching anything:

```text
$ echo | parameter-store admin namespace delete --env prod --app gradethis
error: refusing to delete namespace prod/gradethis without --yes on a non-interactive stdin

$ parameter-store --yes admin namespace delete --env prod --app gradethis
Deleted namespace prod/gradethis
```

`defaults apply` is unchanged and asks no interactive question: it keeps its own
preview → `--execute` → `--confirm-production ENV` model.

#### Behavior changes

This repository publishes no changelog file, so the changes an existing script
may notice are recorded here:

- **`restore` now requires `--yes` on a non-interactive stdin.** `--force` keeps
  its separate meaning — replace an existing destination — so a scripted restore
  over an existing database needs **both** `--yes --force`. An existing
  destination without `--force` is now refused *before* the prompt rather than
  after it.
- The same `--yes` requirement applies to `release activate`, `release
  rollback`, `rotate-kek`, `admin namespace delete`, `admin identity revoke`,
  `admin identity revoke-cert`, and `admin-cert revoke`.
- Failures that previously exited `1` now exit `3`–`9` when the server
  classified them, `2` when the command line itself was wrong (a missing
  positional used to be `1`), and `6` when `backup --out` or `restore` names an
  existing file without `--force`. A missing local file — token file, CA
  bundle, manifest, artifact — stays `1`; only a store or server resource is
  `5`. A script that tests `-eq 1` should test `-ne 0` instead.
- Every command now rejects positional arguments it does not take, so
  `--yes false` (which the flag parser reads as `--yes` plus a stray `false`)
  is refused with exit `2` instead of silently confirming. `release create`
  takes the manifest as `FILE` or `--file`, not both.
- `--output json`, `KMS_OUTPUT`, `--quiet`, `--token-file`,
  `--secret-token-file`, and the `whoami` command are new.
- **`0.3.x` replaces client-bound mode.** `put-secret` no longer accepts
  `--client-bound` or a write-side `--secret-token`; set `KMS_BINDING_KEY`
  instead. There is no binding-key file option. New lifecycle commands are
  documented in the convenience-command table above.

### Secure destination paths

Database, backup, restore, generated-key, identity certificate/key-bundle, and
import-report destinations must have a stable parent. KMS resolves parent
symlinks once, uses only the canonical result, and checks every directory in
that chain. On POSIX, each directory must be owned by root or the service user;
group/other write is refused unless the sticky bit protects entries. On macOS,
any allow ACL in the chain is also refused, and ACLs are stripped from private
artifacts. On Windows, the chain must have a trusted owner and a DACL that gives
no untrusted SID path-mutation authority, including reparse-point retarget
writes; private artifacts receive a protected current-user-only DACL. Prepare
destination directories under the same OS account that runs the command.

Private files are created exclusively and owner-only (`0600` files/`0700`
staging directories on POSIX). The identity `.crt` is intentionally public at
`0644`; its `.key` is private, and both entries are reserved before minting.
Import reports are private because successful reports contain one-time access
tokens. Existing files and symlinks are refused except an explicitly opened
existing database that already passes owner-only checks and `restore --force`;
backup and non-force restore use atomic no-replace publication.

> **Existing-database compatibility:** a hardened build fails closed on a
> legacy `0644` database instead of starting with broad access. Stop **all** KMS
> processes first. Verify that the database and its directory are owned by a
> trusted account and that there is no concern about attacker-open handles or
> modified content. On POSIX, set the database to `0600` (and correct ownership
> if necessary); on macOS, also remove unsafe extended ACLs. On Windows, replace
> inherited/broad permissions with a protected DACL granting full access only
> to the account that runs KMS. Then restart. If provenance, content, or handle
> safety cannot be established, do not repair the file in place—restore a
> known-good backup into a fresh secure directory.

On non-Darwin POSIX systems, these checks cover owner UID, mode bits, and
sticky-directory behavior only. They do **not** inspect NFSv4 or other extended
ACL entries; operators must verify separately that no such ACL grants broader
path-mutation or file-access rights.

#### Preparing a destination directory

Every ancestor of the destination is checked, so the whole chain must satisfy
the rules above — a private leaf directory under a group-writable parent is
still refused. Two defaults commonly violate this.

**Group-writable home directories.** Debian and Ubuntu ship `umask 002` with
user-private groups, so every directory an interactive user creates is `0775`.
Running from such a directory fails even though the group has no other members:

```
$ ./parameter-store init
error: opening database: validate database path "./kms.db": unsafe destination
spelling for ./kms.db: /home/you/code is group- or other-writable without the
sticky bit
```

The check reads mode bits only; it does not enumerate group membership. Clear
the group-write bit on every flagged ancestor, or keep KMS data outside the
umask-002 tree:

```bash
chmod g-w ~/code ~/code/kms ~/code/kms/bin   # or:
install -d -m 0700 ~/.local/share/parameter-store
./parameter-store init --sqlite-path ~/.local/share/parameter-store/kms.db
```

**Directories prepared by another account.** Create destination directories
under the account that runs the command. A directory prepared by root for a
service user is refused unless it is owned by root or by that user; `chown` it
to the service account and set `0700` or `0755`.

#### Container and Kubernetes volumes

Inside the published image the chain is short — `/` at `0755 root` and `/data`
at `2755 kms:kms` — so the image works as shipped. What matters is how `/data`
and `/key` are mounted.

| Mount | `/data` as the container sees it | Result |
| --- | --- | --- |
| No volume (image defaults) | `2755 kms:kms` | works |
| Named or anonymous Docker volume | `2755 kms:kms` | works |
| Bind mount, host directory `0775` | `0775 <host uid>` | refused |
| Bind mount, host directory `0755`, `--user` matching its owner | `0755 <host uid>` | works |
| Kubernetes `fsGroup` | `2775 root:<fsGroup>` | refused |
| `emptyDir` | `0777` | refused |

**Named volumes are the supported default.** Docker initializes an empty volume
from the image directory's ownership and mode, so `kms-data` and `kms-key`
inherit `2755 kms:kms` and pass. This is the flow in
[`releasing.md`](releasing.md).

**Bind mounts** expose the host directory's uid and mode directly. The host
directory must be `0755` (not the umask-002 default `0775`) and owned by the
uid the container runs as:

```bash
install -d -m 0755 ./kms-data ./kms-key
docker run --rm --user "$(id -u):$(id -g)" \
  -v "$PWD/kms-data:/data" -v "$PWD/kms-key:/key" \
  ghcr.io/suhaibinator/kms:latest init --admin ops
```

**Kubernetes needs an explicit `chown`.** `fsGroup` grants a non-root container
write access through the group bit, which is exactly what path validation
refuses — so no `fsGroup` value works, and lowering the mode to `0755` leaves
the volume unwritable. Because the check walks every ancestor, moving the
database into a subdirectory does not help either: the volume root is still in
the chain.

Omit `fsGroup` and hand the volume root to the runtime uid with an init
container. The image runs as uid `100`, gid `101`:

```yaml
spec:
  securityContext:
    runAsUser: 100
    runAsGroup: 101
    # No fsGroup: the kubelet would re-add group write on every mount.
  initContainers:
    - name: fix-volume-permissions
      image: busybox:1.38
      securityContext:
        runAsUser: 0
      command: ["sh", "-c", "chown 100:101 /data /key && chmod 0755 /data /key"]
      volumeMounts:
        - { name: kms-data, mountPath: /data }
        - { name: kms-key, mountPath: /key }
  containers:
    - name: parameter-store
      image: ghcr.io/suhaibinator/kms:latest
      volumeMounts:
        - { name: kms-data, mountPath: /data }
        - { name: kms-key, mountPath: /key }
```

If a CSI driver reapplies group write on mount, set
`fsGroupChangePolicy: OnRootMismatch` alongside the init container, or use a
volume type whose root permissions you control. `emptyDir` is `0777` and is
unsuitable for the database regardless.

### Management commands (`admin` group, over gRPC)

The `admin` command group manages namespaces, identities, and the built-in
CA on a **running** server over gRPC (unlike the offline database commands
above). Every admin command shares the connection flags `--endpoint`,
`--token` (admin bearer token), `--insecure` (skip TLS, development only),
`--ca`, and `--cert`/`--key` (mTLS). Each connection flag falls back to an
environment variable when it is omitted, so an operator shell can export the
connection once and drop the flags:

| Flag | Environment fallback |
|---|---|
| `--endpoint` | `KMS_ENDPOINT` (default `localhost:8443`) |
| `--token` | `KMS_TOKEN` |
| `--token-file` | `KMS_TOKEN_FILE` |
| `--ca` | `KMS_CA_FILE` |
| `--cert` | `KMS_CLIENT_CERT_FILE` |
| `--key` | `KMS_CLIENT_KEY_FILE` |

`--token-file` reads the bearer token from an owner-only file instead of the
command line, which `ps` and `/proc/PID/cmdline` expose to every local user;
prefer it wherever the command line is observable. `--token` and `--token-file`
together — from flags or from the environment — are a usage error. See
[Token files instead of `--token`](#token-files-instead-of---token).

**In production, `--cert`/`--key` are not optional for an admin.** While
`security.admin_require_client_cert` is enforced (the default whenever TLS is
on), an admin identity authenticates with its client certificate **and** its
token; either alone is rejected as `unauthenticated` without saying which was
missing. Issue the pair with
[`admin-cert issue`](#admin-credentials-and-browser-setup) and export
`KMS_CLIENT_CERT_FILE`/`KMS_CLIENT_KEY_FILE` alongside `KMS_TOKEN` so the
flags can be dropped. Examples elsewhere in this document that pass only
`--token` assume either a development server with TLS disabled
(`--insecure`), where the requirement is relaxed, or that the certificate
already comes from the environment.

`KMS_CA_FILE` is the **client's** trust bundle for verifying the server. It is
a different thing from the server-side `KMS_CLIENT_CA_FILE`
(`security.client_ca_file`), which is the CA the server verifies client
certificates against. The same fallbacks apply to `release`, `defaults`, and
the convenience commands (`put-secret`, `get-secret`, `put-parameter`,
`list`, `exec`, `env`). The diagnostic `admin ca show` command needs no credential because the
built-in client issuer's certificate is public.

| Command | Args / flags | Purpose |
|---|---|---|
| `admin namespace create` | `--env ENV`, `--app APP`, `--description`, `--auth-methods mtls,token` (default: mTLS-only) | Create a namespace. Environment/application are flags, not a positional `ENV/APP` argument. |
| `admin namespace update` | `--env ENV`, `--app APP`, `--description`, `--auth-methods` | **Full replace** of the description and allowed auth methods. |
| `admin namespace delete` | `--env ENV`, `--app APP`, `--yes` | Delete an **empty** namespace (no parameters, secrets, or bound identities) and retire its environment-scoped release/subscriber history. **Confirms by retyping `ENV/APP`** before the server is contacted. |
| `admin namespace list` | — | Table of namespaces with allowed auth methods and parameter/secret counts. |
| `admin identity create NAME` | `--kind client\|admin` (default `client`), `--namespace env/app`, `--auth mtls\|token\|both` (default `mtls`), `--ttl 90d` (or e.g. `720h`), `--out DIR` | Create an identity. Mints a token and/or a one-time PEM cert bundle per `--auth`; with `--out DIR` writes `NAME.crt` (0644) and `NAME.key` (0600), otherwise prints them once — so `--out` is required whenever `--output json` would mint a certificate. |
| `admin identity issue-cert NAME` | `--ttl`, `--out DIR` | Mint an **additional** client certificate for an existing identity (for overlap rollover). |
| `admin identity revoke-cert NAME` | `--serial` (required), `--yes` | Revoke one certificate by serial. **Confirms by retyping `NAME`.** |
| `admin identity rotate NAME` | — | Rotate a token identity's bearer token (printed once). |
| `admin identity revoke NAME` | `--yes` | Disable an identity; **all** of its certificates become invalid. **Confirms by retyping `NAME`.** |
| `admin identity list` | — | Table: name, kind, namespace, has-token, cert count, disabled. |
| `admin policy create NAME` | `--subject IDENTITY` (or `*`), `--allow OP@ENV/APP` (repeatable), `--deny OP@ENV/APP` (repeatable) | Create a namespace-level policy. Either label may be `*`; a bare `OP` means every namespace. Operations and labels are validated by the server (`policy.ValidateRules`). |
| `admin policy list` | `--page-size` | Table: name, subject, allow rules, deny rules (`op@env/app`). |
| `admin policy delete NAME` | — | Delete a policy. |
| `audit list` | `--env`, `--app`, `--key-prefix`, `--actor`, `--event`, `--decision allow\|deny\|error`, `--since`, `--until`, `--limit` (default 100, at most 1000), `--page-token`, `--follow`, `--interval` (default 5s, minimum 1s) | Table of audit events newest first: `TIME EVENT DECISION ACTOR NAMESPACE KEY REQUEST_ID`, where `TIME` is RFC 3339 in UTC and every column a global event cannot fill prints `-`. `--since`/`--until` take a duration ago (`24h`, `7d`) or an RFC 3339 instant. `--follow` tails the log by polling — the first page is printed oldest-first and later polls print only events the tail has not shown — until SIGINT/SIGTERM; it cannot be combined with `--page-token`. |
| `audit export` | the same filters as `audit list`, plus `--out FILE.jsonl` (**required**) | Streams **every** matching page into `--out` as JSON Lines, one canonical record per line. The file is staged owner-only beside the destination and published atomically, so an interrupted run leaves no truncated file; an existing destination is refused with exit `6` and left untouched. |
| `admin ca show` | `--out FILE` | **Diagnostic/out-of-band only:** print (or write) the public built-in **client-issuing** CA certificate to inspect or independently verify KMS-issued client certificates. This is not the SDK's server-trust CA and is not part of application onboarding. |

`audit list` and `audit export` are spelled without the `admin` prefix —
they are top-level commands — but they belong to this group in every other
respect: the same connection flags, the same admin credential, and the same
`--output json` envelope. They are listed here because reading the audit log
is an administrative operation; the third audit subcommand, `audit prune`,
runs offline and is in the table above. Any identity holding the delegated
`admin:audit:read` operation can run `list` and `export` too, and sees only the
rows its policy and the namespace's authentication-method boundary admit.

`--ttl` accepts a Go duration (`720h`) or a bare day count (`90d`); omitting
it uses the server's 90-day default. `--auth-methods` and `--auth` values are
`mtls` and/or `token`. An admin-kind identity always receives a bearer token;
requesting `mtls` for an admin adds a certificate rather than replacing that
token. Tokens and certificate private keys are shown exactly once and are
never retrievable again. `admin namespace`/`identity` map onto
the `AdminService` RPCs; see the [built-in CA runbook](#built-in-ca-and-client-certificates)
below for the certificate lifecycle these commands drive.

### Audit retention and archive

By default the server keeps audit rows **forever**: `audit.retain_duration` is
`0`, and every start logs one line saying so, so "is this server discarding
audit history?" is answerable from the boot log alone.

```text
{"level":"info","msg":"audit retention","enabled":false,"retain":"forever","archive_dir":""}
{"level":"info","msg":"audit retention","enabled":true,"retain":"2160h0m0s","archive_dir":"/var/lib/parameter-store/audit"}
```

Setting `audit.retain_duration` above `0` starts a background loop that runs
one pass immediately after startup and every five minutes thereafter. It
retires rows older than `now - retain_duration` in batches, and its single
invariant is **archive before delete**: when `audit.archive_dir` names a
directory, a batch is written to its per-day file and `fsync`ed before any of
its rows are removed. An archive that cannot be written is therefore a refused
pass — the rows stay, the failure is logged at `error`, and the next tick
retries — never a silent deletion. Archive failures never stall the watch hub;
the two prune different things on separate loops.

The archive is one file per **UTC day of event creation**, named
`audit-<YYYYMMDD>.jsonl`, created `0600` and opened append-only. Each line is
one record in the same canonical shape `audit export` and `audit list --output
json` produce, so the three can be concatenated and fed to one parser. Two
consequences follow from the retry behavior:

- **Consumers must deduplicate by `id`.** A pass that archives a batch and then
  fails to delete it retries the whole batch, so the same record can appear
  twice. Every record carries its `id` for exactly this purpose.
- **The directory must already exist and be private.** The server does not
  create it — creating it would mean guessing the permissions of a directory
  that is about to hold the only remaining copy of the audit log. `Validate()`
  refuses to start when `audit.archive_dir` is missing, and `audit.archive_dir`
  without a positive `audit.retain_duration` is a configuration error rather
  than a no-op. Give it the same treatment as the database directory (see
  [Preparing a destination directory](#preparing-a-destination-directory)).

Records written by the server's archive, by `audit export`, and by `audit list
--output json` are identical field for field, including the resource's
immutable namespace-incarnation ID (`resource.namespace_id`), so the three can
be concatenated and searched together.

Retiring history offline — before shrinking a database, or on a host where
`serve` is stopped — uses the same code path through `audit prune`:

```bash
# Rehearse: how many rows would a 90-day window retire, and from which file?
parameter-store audit prune --older-than 90d --dry-run
# Target database: /var/lib/parameter-store/kms.db (source: config file storage.sqlite_path)
# Would prune 41027 audit events

# Keep the evidence, then retire it. The directory must already exist.
install -d -m 700 /var/lib/parameter-store/audit
parameter-store audit prune --older-than 90d \
  --archive /var/lib/parameter-store/audit --yes
# Pruned 41027 audit events (archived to /var/lib/parameter-store/audit)
```

Take a full export first if the archive is going somewhere the database backup
does not cover:

```bash
parameter-store audit export --until 90d --out ./audit-through-2026-06.jsonl
```

`audit prune` opens the database directly and works while `serve` is running
(WAL mode allows it), but coordinating that is the operator's responsibility —
the same caveat `create-admin` and `rotate-admin` carry. Pruning **without**
`--archive` deletes the rows outright; see
[`security.md`](security.md#audit-guarantees) for why that is a posture change
rather than a tuning knob.

### Convenience commands (talk to a running server over gRPC)

Except for the local, stdout-only `binding-key generate`, these operate over
gRPC against `--endpoint` (default `localhost:8443`), not directly on the
database file, so they need a server with the gRPC listener open (the default
when running `serve`). They share the same connection flags as the `admin`
group: `--endpoint`, `--token` (identity bearer token; env
`KMS_TOKEN`) or `--token-file` (env `KMS_TOKEN_FILE`), `--insecure` (skip TLS,
development only), `--ca`, `--cert`/`--key` (mTLS). Secrets and parameters are
addressed by a **`/env/app/key` display path**, which the CLI splits into an
explicit namespace + relative key client-side; `list` takes a bare `ENV/APP`
namespace.

| Command | Extra flags | Purpose |
|---|---|---|
| `whoami` | — | Prints the identity the server resolves from the credential this invocation presents: `name`, `kind`, `namespace` (or `(unbound)`), and `auth_method` (`mtls` or `token`). Needs no permission, so it is the first command to run when a token or certificate does not behave as expected. |
| `put-secret /env/app/key` | `--value-file` (default: read stdin), `--content-type` (default `text/plain`), `--generate-token` | Stores a new version. Non-empty `KMS_BINDING_KEY` binds only the new version; empty creates it unbound. `--generate-token` independently creates/rotates the per-secret access token and prints it once. There is no write-side `--secret-token`, `--client-bound`, or binding-key file flag. |
| `get-secret /env/app/key` | `--version`, `--label`, `--secret-token`/`--secret-token-file`, `--show` (allow printing to a terminal), `--out FILE` (write to a file instead, mode 0600) | Fetches one secret. If exact live metadata says the selected version is bound, the key comes from `KMS_BINDING_KEY` or a no-echo terminal prompt. Access-token input remains independent. Refuses terminal plaintext unless `--show`; there is no offline secret-read/export command. |
| `put-parameter /env/app/key VALUE` | `--content-type` (default `string`) | Stores a new parameter version. |
| `list ENV/APP` | `--prefix` (relative key prefix within the namespace) | Lists parameters and secrets (metadata only) in a namespace as a table: type, `/env/app/key`, current version, and content-type/bound note. Pages through the full result set. |
| `binding-key generate` | — | Writes exactly one newly generated 256-bit Base64URL binding key plus newline to stdout, with no other output. |
| `secret bind /env/app/key` | `--expected-current-version` | Clones the unbound current version into one new bound current version using `KMS_BINDING_KEY`. An explicit positive expected version supplies the CAS guard without a metadata read; when omitted, the CLI reads current metadata and uses the observed version. The source remains unchanged as `previous`. |
| `secret unbind /env/app/key` | `--expected-current-version` | Clones the bound current version into one new unbound current version using its current key. An explicit positive expected version supplies the CAS guard without a metadata read; when omitted, the CLI discovers it from current metadata. |
| `binding-key rotate /env/app/key` | `--expected-current-version` | Obtains the old and replacement keys separately (`KMS_BINDING_KEY`, `KMS_NEW_BINDING_KEY`) and submits a CAS guard. An explicit positive expected version avoids a metadata read; when omitted, the CLI reads current metadata. It clones only current into one new version protected by the replacement; historical versions retain the old key. The server proves the old key before rejecting a byte-for-byte unchanged replacement. |
| `secret purge-binding-cohort /env/app/key` | `--version` (`0` = current) | **Irreversible, admin only.** Previews and confirms the exact contiguous compromised cohort, then replays CAS guards and destroys it even if releases pin those versions. |
| `secret purge-unbound-versions /env/app/key` | — | **Irreversible, admin only.** Previews every non-destroyed unbound version (including disabled, expired, and corrupt rows), prints the exact set, confirms, then replays the mandatory revision/version-set guards and destroys it even if releases pin those versions. |
| `exec ENV/APP -- COMMAND [ARGS...]` | `--release NAME`, `--prefix`, `--no-secrets`, `--env-prefix`, `--allow-incomplete-secrets` (namespace mode only), `--secret-token KEY=TOKEN`/`--secret-token-file KEY=PATH` (repeatable), `--preserve-env` | Runs `COMMAND` with the namespace's parameters and secrets injected as environment variables. Resolves every value first, then replaces itself with `COMMAND` (on Unix), so signals and the exit status pass straight through. See [Run any process with store values](#run-any-process-with-store-values). |
| `env ENV/APP` | the same selection and token flags as `exec`, plus `--format dotenv\|export\|json\|yaml`, `--show`, `--out FILE`, `--force` | Prints the same variables instead of running anything, for `source <(...)`, an `EnvironmentFile=`, or a `jq` pipeline. Refuses to print to an interactive terminal unless `--show`, `--out`, or `--no-secrets` is given. |

Binding keys are opaque valid UTF-8 strings of at least 32 bytes. The CLI reads
them only from the exact environment variables named above or from a no-echo
terminal prompt; there is no key-file flag, key-file variable, key directory,
or recovery source. KMS stores no key, hash, fingerprint, or cohort identity.
Bound-cohort purge previews the exact contiguous cryptographic cohort, confirms
it, and replays both revision and affected-version CAS guards. Unbound purge
does the same for the full unbound set without requesting a binding key.
Transitions instead submit the current version observed immediately before the
operation. A purge
whose transaction committed but whose SQLite/WAL cleanup is still pending
returns gRPC `Unavailable` with the fixed message `secret purge committed;
database artifact cleanup is pending` and leaves the service fail-closed; do
not repeat a bound-cohort purge with the retired key or repeat an unbound purge
as though its preview were still live. No purge result accompanies the gRPC
error. See
[`binding-keys.md`](binding-keys.md).

Parameter content types are literal KMS tokens: `string`, `integer`, `float`,
`boolean`, `json`, or `binary`. They are not MIME types. In particular, publish
a managed JSON group with `--content-type json`; `application/json` is rejected.

A literal `--` ends flag parsing: everything after it is taken as
positional arguments verbatim, even if it begins with `-`. Use it when a
value itself looks like a flag, e.g. `parameter-store put-parameter
/prod/gradethis/flag -- -5`.

### Run any process with store values

`exec` and `env` are for workloads that cannot link an SDK: a shell script, a
third-party binary, a container entrypoint, a migration tool. Both resolve the
same selection of parameters and secrets and map them to environment variables.
`exec` then runs a command with them; `env` prints them instead.

```bash
# Run the workload with the active release's exact, digest-verified values.
parameter-store exec prod/gradethis --release runtime -- ./server --port 8080

# The same values, printed for a shell to source.
source <(parameter-store env prod/gradethis --release runtime --format export)
```

#### Prefer `--release NAME` in production

Without `--release` the selection is the namespace's **current** values: every
parameter and secret the caller can list, at whatever version the `current`
label points to at that moment. That is convenient in development and
non-deterministic in production — two replicas started a minute apart can get
different values, and nothing records what either of them saw.

`--release NAME` resolves the namespace's **active** release instead and pins
every entry to the version it recorded. Before any resource read, the CLI
recomputes the complete release digest from the deterministic alias-sorted
manifest projection; an empty or mismatched digest rejects the whole
invocation. Each parameter is then re-fetched by version and verified: same
resource, same version, same content type, and a SHA-256
digest equal to the one the release recorded. Each secret is fetched at its
pinned version and checked the same way, minus the digest (a release never
records one for secret material). Before any exact secret fetch, the CLI reads
live metadata and verifies the response identity, exact version, state, expiry,
and that version's `bound` and `has_access_token` flags. Any mismatch aborts
the invocation before a process is started, so a workload never runs on a mix
of pinned and drifted values. In namespace mode `--prefix db/` narrows the selection to a subtree
exactly as it does for `list`; `--prefix` and `--release` are mutually
exclusive, since a release fixes its own entries.

Release entries are named by **alias**, so variable names come from the
application contract. Both parameter and secret pins must be in the release's
own namespace; cross-namespace release entries are invalid.

#### Variable names

| Source | Rule | Example |
|---|---|---|
| Store key (namespace mode) | uppercase; `/`, `-`, and `.` fold to `_` | `billing/stripe-key` → `BILLING_STRIPE_KEY` |
| Release alias (`--release`) | uppercase; `-` folds to `_` | `stripe-key` → `STRIPE_KEY` |

`--env-prefix APP_` is prepended verbatim to every name (`APP_STRIPE_KEY`). A
name that would start with a digit has no legal spelling as a shell variable
and is refused, naming the flag that fixes it — an `--env-prefix` always begins
with a letter or underscore. Two entries that map to one name are refused as
well; nothing is silently overwritten. Names are sorted, so two runs diff
cleanly.

A value containing a NUL byte, or one that is not valid UTF-8, cannot survive
an environment string. It is base64-encoded under `<NAME>_B64` instead, and the
substitution is reported on stderr:

```text
note: tls/keystore is not text; injected base64-encoded as TLS_KEYSTORE_B64
```

Detection is content-based only: the stored content type carries no signal,
since `application/octet-stream` is the default for every secret.

#### Bound secrets and per-secret access tokens

Bulk `env`/`exec` never accept or request binding keys and never call
`GetSecret` for a bound version. A secret-inclusive invocation that selects a
bound version fails before `env` prints anything or `exec` launches its child;
an empty credential value is never synthesized. Use `--no-secrets` for an
intentional parameter-only invocation, or an SDK release loader when a process
must consume bound secrets.

An unbound secret carrying the independent access-token gate needs its token
before it can be read. Unlike `get-secret`, which takes one single-valued
`--secret-token`, these commands may read many, so the flags are keyed and
repeatable:

| Source | Form | Notes |
|---|---|---|
| `--secret-token` | `KEY=TOKEN`, repeatable | Visible in `ps`; prefer the file form. |
| `--secret-token-file` | `KEY=PATH`, repeatable | One token per file, owner-only, same rules as [`--token-file`](#token-files-instead-of---token). |
| `KMS_SECRET_TOKEN_<NAME>` | environment | `<NAME>` is the variable the secret maps to, **without** `--env-prefix` and without `_B64`. |

Either flag beats the environment. `KEY` is any spelling of the secret: its
`/env/app/key` display path, its relative key when the secret is in the
selected namespace, or — with `--release` — its alias. Name a secret once: the
same `KEY` in both flags is a usage error (exit `2`), and naming one secret
under two different spellings is refused as ambiguous (exit `1`) even when the
tokens agree. A flag token is also refused when it names a secret that is not
in the selection or does not need one (exit `1`): a stale token or a typo that
lands on the wrong secret is almost certainly an operator error.
`KMS_SECRET_TOKEN_<NAME>` variables are ambient and may
be leftovers, so they are read only for a secret that needs a token and never
cause a refusal. `KMS_SECRET_TOKEN_FILE`, which only `get-secret` reads, is not
consulted here.

The token travels only in that unbound secret's `GetSecret` protobuf request;
no other call carries it. By default, a gated unbound secret whose token was
not supplied fails the complete invocation before any environment output or
child launch. The same fail-closed rule applies to a selected bound secret.
This makes the ordinary secret-inclusive mode atomic: it never silently turns
a missing credential into a partial runtime configuration.

Namespace mode has one explicit availability-oriented escape hatch:
`--allow-incomplete-secrets`. It emits parameters and successfully resolved
unbound secrets while omitting bound secrets and gated secrets that lack a
token. Every omission produces a warning that `--quiet` cannot suppress:

```text
warning: omitted unavailable secret /prod/gradethis/stripe-key: it requires a per-secret token and none was supplied (--allow-incomplete-secrets)
```

Incomplete mode never creates an empty secret value. With `exec`, both the
plain mapped name and its possible `_B64` form are removed from the inherited
environment before launch, including under `--preserve-env`, so an omitted
secret cannot fall through to a stale parent credential. With `env`, omission
cannot unset a variable in the shell that consumes its output: source into a
clean environment, or explicitly unset the omitted names first. This mode is
rejected with `--release`, because a release is an atomic configuration unit.

`--no-secrets` intentionally selects parameters only and therefore succeeds
even when the namespace or release contains unavailable secrets. It makes any
`--secret-token`/`--secret-token-file` an error, since the token can no longer
apply to anything. `--allow-incomplete-secrets` and `--no-secrets` are mutually
exclusive. The former opt-in `--strict` flag no longer exists; fail-closed is
the default.

#### The command's environment

`exec` merges the store's variables into the environment it inherited:

- The **injected value wins** over an existing variable of the same name. That
  is the point: a stale `DB_HOST` left in a unit file or a container image must
  not shadow the store.
- `--preserve-env` inverts it for ordinary resolved values — the parent's value
  is kept — and every shadowed name is reported, so the difference is visible
  rather than assumed. Unavailable secret names in explicit incomplete mode
  are removed before this merge and cannot be preserved. For an ordinary
  resolved value the diagnostic is:
  `note: DB_HOST is already set and kept (--preserve-env); the store's value was not injected`.
- Every `KMS_SECRET_TOKEN_*` variable is **removed** from the command's
  environment (`KMS_SECRET_TOKEN_FILE` shares that prefix and goes too). They
  are inputs to the CLI, not credentials the workload should inherit.
- The exact `KMS_BINDING_KEY` and `KMS_NEW_BINDING_KEY` variables are also
  removed. Near-miss names are left alone; there is no binding-key file or
  directory variable.
- `KMS_TOKEN`, `KMS_TOKEN_FILE`, `KMS_ENDPOINT`, and the rest of the connection
  settings **are** inherited: the workload may legitimately call the store
  itself. Unset them explicitly
  (`/usr/bin/env -u KMS_TOKEN parameter-store exec ...`) if it should not.

One `NAME=value` entry may not exceed 128 KiB on Unix or 32 KiB on Windows —
the platform's own limit — and the whole injected set may not exceed 2 MiB.
Over either, the command names the offending variable instead of letting the
kernel refuse the `exec` with a bare `E2BIG`.

#### Who can read the values

An environment variable is not a secure channel. On Linux a process's full
environment is readable through `/proc/PID/environ` by its owner and by root,
it is inherited by every descendant, and it routinely reaches crash dumps and
container inspection APIs (`docker inspect`, `kubectl describe pod` for
anything in the pod spec). `ps eww` shows it to the same accounts. Prefer an
SDK where you can; where you cannot, run the workload as its own user and treat
the values as visible to anything running as that user or as root.

The command line is worse than the environment: `--secret-token KEY=TOKEN` and
`--token TOKEN` are visible to **every** local user in `ps` and
`/proc/PID/cmdline` for as long as the CLI runs. Use `--secret-token-file`,
`--token-file`, or the `KMS_SECRET_TOKEN_<NAME>` variables in production.
Binding keys are never accepted as command-line values.

#### `env` output

`--format` selects `dotenv` (the default), `export`, `json`, or `yaml`. The
global `-o json` selects `json`; combining it with a different `--format` is a
usage error. Each format quotes for its own reader: `export` uses POSIX single
quotes, `dotenv` leaves unambiguous values bare (so `set -a; . file` works) and
JSON-quotes the rest, and `yaml` double-quotes every value.

Because the output *is* the secret material, `env` refuses to write it to an
interactive terminal:

```text
$ parameter-store env prod/gradethis
error: refusing to print secrets to a terminal; pass --show to print, --out FILE to save, or --no-secrets
```

The check runs **before** anything is fetched, so a refused invocation reads no
secret and leaves no audit rows behind. A pipe, `--show`, `--out FILE`, and
`--no-secrets` all satisfy it — the same rule `get-secret` applies to a single
value.

`--out FILE` writes through a private staging entry in the same directory and
publishes the result at mode `0600`; an existing path is refused with exit `6`
unless `--force` is given, and a failed write leaves nothing behind. The
destination must satisfy the [secure destination-path](#secure-destination-paths)
rules. `--quiet` suppresses the `Wrote N variables to ...` line and the base64
note, never an incomplete-secret warning. With `--output json` stdout still
carries one document, `{out_file, variables}`, and the values only ever reach
the file.

#### Under systemd

The simplest unit needs no file at all: `exec` resolves everything and then
becomes the server.

```ini
# /etc/systemd/system/gradethis.service
[Service]
User=gradethis
Environment=KMS_ENDPOINT=kms.internal:8443
Environment=KMS_TOKEN_FILE=/etc/gradethis/kms.token
ExecStart=/usr/local/bin/parameter-store exec prod/gradethis \
  --release runtime -- /usr/local/bin/gradethis-server
Restart=on-failure
```

On Unix the CLI replaces itself with the server once every value is resolved,
so systemd's `MainPID`, `Restart=`, signal delivery, and exit status all refer
to the server itself rather than to a surviving wrapper. Secret resolution is
fail-closed by default. (Windows has no
`exec(2)`; there the CLI stays as a thin parent that forwards the child's exit
status.)

Where the workload must have a real `EnvironmentFile=`, generate it from a
`oneshot` unit ordered before the service, rather than from an `ExecStartPre=`
in the same unit — the file must exist before systemd reads it:

```ini
# /etc/systemd/system/gradethis-env.service
[Service]
Type=oneshot
User=gradethis
RuntimeDirectory=gradethis
RuntimeDirectoryMode=0700
RuntimeDirectoryPreserve=yes
Environment=KMS_ENDPOINT=kms.internal:8443
Environment=KMS_TOKEN_FILE=/etc/gradethis/kms.token
ExecStart=/usr/local/bin/parameter-store env prod/gradethis \
  --release runtime --force --out /run/gradethis/env

# /etc/systemd/system/gradethis.service
[Unit]
Requires=gradethis-env.service
After=gradethis-env.service

[Service]
User=gradethis
EnvironmentFile=/run/gradethis/env
ExecStart=/usr/local/bin/gradethis-server
```

`RuntimeDirectory=` with `RuntimeDirectoryPreserve=yes` gives the pair an
owner-only `/run/gradethis` that outlives the oneshot, `--force` lets a restart
replace the previous file, and `dotenv` — the default format — is exactly what
`EnvironmentFile=` parses. The file is written `0600`, so it is readable only
by the service user.

#### Exit codes

`exec` returns the **command's** exit status unchanged. When the command could
not be started it uses the shell's own codes, so a supervisor reads them the
way it reads `sh`'s:

| Code | Meaning |
|---|---|
| `126` | The command was found but could not be executed: not executable, a directory, or a bad interpreter. |
| `127` | The command was not found — or a bare name resolved only through the current directory, which is refused; run it as `./name` or give its path. |

Everything before the launch uses the standard CLI codes: `2` for a usage
problem (a missing `--`, `--prefix` with `--release`, incomplete mode with a
release, one key in both token flags), `1` for a resolution failure (a digest
mismatch, a missing required token, a bound secret in a bulk selection, a
`--secret-token` that names nothing in the selection), and `3`–`9`
mirroring the server's status — `4` for a wrong per-secret token, `5` for a
release that is not there, `7` for a failed precondition. A resolution failure
never starts the command. Note the overlap: a command that itself exits `126`
or `127` is indistinguishable from a launch failure, exactly as under `sh -c`.

### Configuration release commands

The `release` command group uses the same gRPC connection flags as the other
convenience commands. A release definition is strict JSON or YAML:

```yaml
namespace: prod/gradethis
name: runtime
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

Every parameter and secret entry must refer to the release's own namespace.
Relative keys are recommended; an absolute key is accepted only when its
`env/app` exactly matches the release namespace. Specify `version` or `label`,
not both; omitting both resolves `current`. Creation persists the exact
immutable namespace-row ID for every resolved reference, so deleting and
recreating the same `env/app` cannot retarget an existing pin. The greenfield
`0.3.x` baseline has no migrated legacy release-entry form.

```bash
parameter-store release schema create gradethis runtime.schema.json \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure
parameter-store release schema show gradethis runtime 1 \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure
parameter-store release schema list gradethis runtime \
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

Both commands confirm first. `activate` prints the diff from the currently
active release to stderr — or `No active release in prod/gradethis; runtime v1
will become the first.` — and then asks `[y/N]`; `rollback` names the exact
transition (`roll back release runtime from v3 to v2 in prod/gradethis`) and
asks the operator to retype `prod/gradethis`. Neither preview is suppressed by
`--quiet`. A pipeline
must pass `--yes`, or the command refuses on its non-interactive stdin without
acting. An activation that release validation refuses exits `7` and prints the
individual validation errors.

`POST /api/v1/releases/rollback` is the HTTP form of the first recipe: it
targets the active release's `previous` version, carries the same
`expected_current_version` compare-and-swap guard, re-validates before moving
the labels, and returns `failed_precondition` when the release name has no
active version or the active version has no previous one. The console's
Roll back button (application page, Ship rollout
panel, and Releases page) calls it after validating the previous version so
violations are visible before the operator confirms; activating any other
retained version stays on the activate endpoint. See
[`http-api.md`](http-api.md#rollback).

`verify-defaults ENV/APP --artifact FILE|- [--release NAME]` checks a
generated defaults artifact (the file the generated exporter emits) against
the active release (`--release` defaults to the application's release name)
**by hash only**: the CLI
canonicalizes and hashes every parameter locally and sends aliases, content
types, and digests, so no parameter value leaves the machine and none comes
back. It prints one verdict per alias and a summary, exits `0` when every
alias matches and the artifact's schema digest matches the registered
schema, `1` on any `differs`/`missing_in_release`/`unknown_alias`/
`secret_alias`/`unsupported_content_type` verdict, schema mismatch, or RPC
failure, and `2` on usage errors. `unverified` (release aliases the artifact
does not mention) is reported but does not fail the check; an artifact
without `schema_sha256` prints `schema not checked` and the schema does not
participate in the exit code.

```bash
KMS_TOKEN="$VERIFY_TOKEN" parameter-store release verify-defaults prod/gradethis \
  --artifact ./gen/defaults.json \
  --endpoint kms.example.com:8443
# ALIAS        VERDICT
# database     match
# rate_limits  differs
# Release runtime version 7 (revision 91): 1 match, 1 differs, 0 missing_in_release,
#   0 unknown_alias, 0 secret_alias, 0 unsupported_content_type, 0 unverified; schema match
```

The RPC behind it is budgeted per identity (`server.verify_defaults.*`,
`RESOURCE_EXHAUSTED` / exit 1 when spent; a refusal drains the identity's
budgets, so do not retry in a loop) and requires the
`configuration-release:verify-defaults` operation, which is **never** part of
the implicit home-namespace grant. Mint a dedicated verify-only identity for
CI **without** `--namespace` (an unbound token identity has no implicit
access at all) and grant exactly that one operation on the target namespace.
The target namespace must allow `token` authentication for the identity to
reach it:

```bash
# once: the namespace must accept token auth (mtls,token keeps applications on mTLS)
parameter-store admin namespace update --env prod --app gradethis --auth-methods mtls,token \
  --endpoint kms.example.com:8443 --token "$ADMIN_TOKEN"

# a verify-only credential: unbound, token-only, one policy rule
parameter-store admin identity create gradethis-ci-verify --auth token \
  --endpoint kms.example.com:8443 --token "$ADMIN_TOKEN"
parameter-store admin policy create gradethis-ci-verify --subject gradethis-ci-verify \
  --allow configuration-release:verify-defaults@prod/gradethis \
  --endpoint kms.example.com:8443 --token "$ADMIN_TOKEN"
```

`show` and `diff` print aliases, references, exact versions, content types,
and parameter digests only. They never read or render secret plaintext or
tokens. `subscribers` is admin-only and pivots the per-state rows into one line
per process instance:

```bash
parameter-store release subscribers prod/gradethis runtime \
  --endpoint localhost:8443 --token "$ADMIN_TOKEN" --insecure
# IDENTITY  CLIENT  INSTANCE  RECEIVED  PREPARED  APPLIED  REJECTED  LAG  CONNECTED
```

A rejected cell is rendered as `vVERSION/rREVISION:category`. Categories are
bounded and contain no application diagnostic or secret material. Use the
[managed configuration rejection table](managed-go-configuration.md#diagnose-a-rejected-candidate)
for remediation.

The embedded **Releases** page offers equivalent create, schema, validate,
diff, activate, rollback, and subscriber views. Its secret entries are
metadata-only. The application page's **Ship** modal composes a parameter
change, a release, and its activation into one confirmed step
(`POST /api/v1/applications/ship`); an operator changing one production value
during an incident uses it instead of the four commands above. Full behavior
is in [`configuration-releases.md`](configuration-releases.md#management-surfaces).

Applications using generated Go configuration register the checked-in schema,
but do not register the generated machine contract. Use that contract to check
the release's exact aliases, kinds, `json` content types, policies, views, and
paired schema artifact; physical resource paths and versions remain
operator-owned. Publish complete `json` group documents and use KMS-only hot
changes as explicit emergency overrides. Source-owned defaults, restoration,
restart rejection, startup bypass, and subscriber diagnosis are covered end to
end in the [managed Go configuration operator workflow](managed-go-configuration.md#operator-workflow).

## Startup sequence and readiness

On `serve`, the process (`internal/cli/serve.go`):

1. Loads and validates config, logs the redacted summary.
2. Opens SQLite (`storage.Open`) — this materializes an empty database or
   verifies the exact current baseline before any other startup work.
3. Constructs the core service (not yet ready — no keyring attached).
4. **Acquires the master key** (below) and attaches it to the service,
   which is what flips readiness on.
5. **Bootstraps the built-in CA** (`Service.BootstrapCA`): normally it loads
   and decrypts the CA `init` created; on a database that has none it
   generates one and stores it KEK-wrapped (see
   [Built-in CA](#built-in-ca-and-client-certificates)).
6. Starts the watch hub (change-log tailer / subscriber registry).
7. Builds the TLS config and **settles the admin client-certificate
   posture**: the requirement is effective only when
   `security.admin_require_client_cert` is true *and* TLS is on. The log says
   which of the three states applies, and — when the requirement is
   effective — names every enabled admin identity that holds no valid
   certificate along with the command to issue one (see
   [Admin credentials and browser setup](#admin-credentials-and-browser-setup)).
8. Starts the gRPC listener (via `cli.GRPCFactory`, wired in
   `cmd/parameter-store/main.go` to `internal/server/grpcserver`), then the
   HTTP listener. Both accept — but never demand — a certificate from the
   built-in CA.
9. Blocks on signals. `SIGINT` and `SIGTERM` shut down: gRPC graceful stop is
   capped at 10 s before active streams are forced closed; HTTP shutdown uses
   a 20 s overall context; then the watch hub stops and the store closes.
   `SIGHUP` instead reloads the configuration in place and keeps serving — see
   [Hot reload (SIGHUP)](#hot-reload-sighup).

`SIGHUP` is **ignored** from flag parsing (step 1) until the listeners are up.
A hangup during the passphrase prompt, database baseline verification, or the CA bootstrap is
discarded rather than killing a process that is halfway through starting.
`SIGINT` is untouched throughout, so Ctrl-C at the passphrase prompt still
works.

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
ExecReload=/bin/kill -HUP $MAINPID
# systemctl reload parameter-store re-reads the config file and the TLS
# material — log level, server certificate, key and client CA — without
# dropping the listeners. See docs/operations.md "Hot reload (SIGHUP)".
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

The unit above is also shipped in the repository as
[`deploy/systemd/parameter-store.service`](../deploy/systemd/parameter-store.service);
a test keeps the two copies identical.

## Hot reload (SIGHUP)

`SIGHUP` makes a running `serve` re-read its configuration and its TLS material
without dropping a connection:

```bash
systemctl reload parameter-store            # ExecReload sends the signal
kill -HUP "$(pidof parameter-store)"        # or send it directly
```

### What a reload applies

| Setting | Effect |
|---|---|
| `log.level` | The running logger's level changes for every line from then on. |
| `security.server_cert_file`, `security.server_key_file` | The keypair is re-read from disk and served to the next handshake, on both listeners. |
| `security.client_ca_file` | The client-CA trust pool is rebuilt and governs the next handshake. |

The certificate, key and CA **contents** are re-read on every reload while TLS
is on, whether or not the paths changed — operators rotate the files far more
often than they rename them, so a `SIGHUP` with an untouched config file *is*
the certificate-rotation signal. The built-in CA is re-added to the pool every
time, so client certificates it issued keep working across a rotation.

A reload also re-scans the admin certificates and restates the posture (see
[Admin credentials and browser setup](#admin-credentials-and-browser-setup)):
a rotated client CA or server certificate changes which admin credentials still
complete a handshake, and that is worth saying again at the moment it changes.

### What a reload ignores

Everything else keeps its running value and is listed under `ignored` in the
log line:

- `server.grpc_addr`, `server.http_addr`, `storage.sqlite_path` — the
  listeners and the database are bound for the process lifetime.
- `encryption.kek_file` — the master key changes only through
  [`rotate-kek`](#kek-rotation).
- `security.tls_enabled`, `security.mtls_enabled` — these pick
  `ListenAndServe` versus `ListenAndServeTLS` at start. Turning TLS on or off
  is a restart. With TLS off the three `security.*_file` settings are ignored
  as well: there is no certificate on the listeners to replace.
- `security.trust_proxy_headers`, `frontend.enabled` — wired into the HTTP
  server when it is constructed.
- `security.admin_require_client_cert`, `audit.enabled` — privilege-boundary
  changes. They take a deliberate restart, which emits the startup posture log,
  so a reload can never quietly stop auditing or stop requiring an admin
  certificate.
- `watch.*`, `server.verify_defaults.*` — read once, into the watch hub and the
  service's per-identity budgets.

### All or nothing

The new file is parsed, validated, and the new TLS material fully loaded into a
local configuration **before** anything running is touched. If any step fails,
the server logs exactly one line at `error` level with the reason:

```text
configuration reload failed; running configuration unchanged
```

and nothing changes — not the log level, not the certificate the listeners
serve, not the configuration the next reload will diff against. A truncated
certificate, a mismatched key, an unknown key in the YAML or a value that fails
`Config.Validate()` is a failed reload, never a broken listener.

### Precedence on reload is the startup precedence

A reload re-resolves through the same layers as a start: **flag, then `KMS_*`
environment variable, then the config file, then the built-in default**. A
value pinned on the command line or in the unit's environment therefore cannot
be changed by editing the file — the resolved value does not differ, so the key
is not even reported. To make a setting reloadable, remove the flag (and the
environment variable) and let the config file own it.

### The log lines

Success is one `info` line naming what moved:

```json
{"level":"info","msg":"configuration reloaded","changed":["log.level"],
 "ignored":["server.http_addr"],"log_level":"debug","tls":true,
 "server_certificate_changed":true,"server_certificate_serial":"3fa1c0",
 "server_certificate_not_after":"2027-03-01T00:00:00.000Z",
 "client_ca_changed":false}
```

| Field | Meaning |
|---|---|
| `changed` | Reloadable keys whose resolved value differs from the running one. They have been applied. |
| `ignored` | Keys that differ but are not reloadable. The process keeps its running value; the file and the process now disagree. |
| `log_level` | The effective level after the reload. |
| `tls` | Whether the listeners serve TLS. When false, the certificate fields below are absent. |
| `server_certificate_changed` | The leaf now served differs from the one served before — the rotation really happened. |
| `server_certificate_serial` | Serial of the leaf now served, lowercase hex. |
| `server_certificate_not_after` | Its expiry. |
| `client_ca_changed` | The client-CA pool differs from the one previously in force. |

An empty `changed` together with `server_certificate_changed: true` is the
normal picture for a certificate rotation: the config file did not change, the
bytes behind it did.

**No audit event is written for a reload.** A signal carries no principal, so
there is no identity to attribute the change to; these two log lines are the
record, and host access — which is what it takes to send the signal or edit the
file — is the control.

### Rotating the server certificate

Write the new certificate and key **atomically** — a temporary file in the same
directory, then `mv` — so a reload can never read a half-written file, and
write both before signalling:

```bash
cd /etc/parameter-store/tls
install -m 0600 -o parameter-store -g parameter-store new.crt server.crt.new
install -m 0600 -o parameter-store -g parameter-store new.key server.key.new
mv server.crt.new server.crt
mv server.key.new server.key
systemctl reload parameter-store      # or: kill -HUP "$(pidof parameter-store)"
```

Verify from off-host that the new serial is on the wire:

```bash
openssl s_client -connect HOST:PORT </dev/null 2>/dev/null \
  | openssl x509 -noout -serial -enddate
```

and cross-check it against `server_certificate_serial` in the reload log line.
A reload that catches a new certificate beside the old key fails the keypair
check and leaves the old pair in service — safe, but it means the rotation has
not happened yet; finish writing and reload again.

### Established connections keep what they handshook with

A swap changes what the **next** handshake sees. Connections already open keep
the certificate and the verification state they negotiated:

- Rotating the server certificate does not disturb in-flight requests or open
  watch streams.
- Rotating the client CA does **not** evict established mTLS connections. A
  client whose authority you just removed keeps its current connection until it
  reconnects; restart the service to force that.
- Watch streams re-authorise the *identity* on every heartbeat — a disabled
  identity, a revoked certificate or a rotated token ends the stream — but they
  do not re-verify the chain against the new pool.

### Foreground runs survive a hangup

Because `serve` ignores `SIGHUP` until the listeners are up and treats it as a
reload thereafter, a foreground `serve` outlives its terminal the way `nginx`
and `sshd` do: closing an SSH session no longer takes the server with it. Stop
it with Ctrl-C (`SIGINT`) or `SIGTERM`.

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
  admin_require_client_cert: true
```

The **server certificate is operator-provided** (`server_cert_file` /
`server_key_file`); the built-in CA signs client certificates only, never the
server's serving certificate. `tls_enabled` alone terminates TLS on both
listeners with that certificate (`Config.BuildServerTLS`, minimum TLS 1.2).

**Client-certificate authentication uses the built-in CA and does not require
`mtls_enabled`.** Whenever TLS is on, **both** listeners add the built-in CA
to their client-CA pool and switch to `tls.VerifyClientCertIfGiven`
(`internal/server/listenertls`) — so a client presenting a certificate the
built-in CA issued authenticates by mTLS, while token-only clients, presenting
no certificate, still connect. The server reads that issuer directly from
SQLite; application onboarding does not involve `admin ca show`.
The TLS layer verifies any certificate offered; the per-namespace
`allowed_auth_methods` gate and the admin admission rule, not the handshake,
decide who is admitted.

`mtls_enabled` (which requires `tls_enabled` and a `client_ca_file`, both
checked to exist at config-validation time) adds an **operator-supplied** CA to
the listeners' TLS trust pool alongside the built-in one. Certificate
presentation
stays optional either way (`VerifyClientCertIfGiven`, not require-and-verify).
However, TLS trust alone does not create a KMS identity: certificate auth also
requires the `kms://identity/<name>` SAN and a matching `identity_certs` row.
The current public issuance API creates those rows only for built-in-CA
certificates, so an external CA is not independently usable for identity auth
without a separate provisioning mechanism. Do not enable `mtls_enabled` merely
to use the built-in CA.

**The embedded HTTP/frontend listener asks for a client certificate too.** It
derives its TLS config from the same `listenertls.Build` as gRPC, so a browser
is *offered* the built-in CA and may present an administrator's certificate,
but one is never demanded at the handshake — an unauthenticated visitor must
still be able to reach the login page and `GET /api/v1/health`. A browser with
no matching certificate connects normally and, if it presents an admin token,
is refused by the core admission rule. The handshake *does* reject a presented
certificate that the client-CA pool cannot verify — **expired**, not yet valid,
or issued by another CA: the browser or CLI then fails at the TLS layer with a
certificate error and never reaches the login page or the console's notice,
until the certificate is removed from the keystore or replaced. Revocation is
the other way round: a revoked certificate still passes the handshake
(revocation is a database check) and core refuses it. Admins need no reverse
proxy to authenticate with a certificate; see
[Admin credentials and browser setup](#admin-credentials-and-browser-setup).

**A TLS-terminating reverse proxy in front of the HTTP listener turns the
requirement off.** If the proxy terminates TLS, the KMS itself runs with
`tls_enabled: false` (or receives plain HTTP), no certificate reaches it, and
`serve` relaxes `admin_require_client_cert` with a warning: an admin bearer
token alone then grants full administrative access to anything that can reach
the KMS port. In that topology the proxy must enforce client certificates
itself, and the KMS listener must not be reachable except through it. KMS does
not currently trust a proxy-supplied client-certificate header (tracked as
issue #26). Note the opposite topology: a proxy that terminates the browser's
TLS and then **re-encrypts to a TLS-enabled KMS** leaves the requirement
enforced but unsatisfiable through the proxy — the admin's certificate ends at
the proxy, KMS sees only the proxy's connection, and every admin sign-in is
refused even if the proxy verified the certificate. Until #26 lands, either let
administrators reach the KMS listener directly for the console and CLI, or set
`admin_require_client_cert: false` for that deployment and enforce client
certificates at the proxy.

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
authority embedded in the KMS, and administrators present one in addition to
their bearer token. There is nothing to provision: the CA is **created by
`parameter-store init`** and lives inside the same database.

**Lifecycle.**

- `init` generates the CA — an Ed25519 key pair and a
  long-lived (10-year), self-signed CA certificate that signs client leaves
  only. Every `serve` afterwards loads and decrypts it. `BootstrapCA` is
  get-or-create, so re-running `init` keeps the existing CA, and a database
  created before this behavior still gets a CA on its next `serve`.
- **Leaf key algorithms differ by identity kind.** Client leaves are Ed25519;
  **admin leaves are ECDSA P-256**, because browser and OS keystores have
  poor Ed25519 support and an admin certificate has to be importable into
  one. Both are signed by the Ed25519 CA key and verify against the same
  single built-in CA.
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
  verification of KMS-issued client certificates. Both listeners load this
  client CA directly from the database. SDK clients instead need a CA bundle
  that trusts the separately operator-provided **server** certificate.
- The offline commands that mint certificates (`admin-cert issue`,
  `create-admin --cert-dir`) **require** an existing CA and never generate
  one: inserting a CA key retires every other row, so a second generator
  would silently invalidate every certificate already issued. If a database
  somehow has none, they fail and point at `init`.

**Certificate rollover runbook.** Issued client certificates default to a
**90-day** TTL (`--ttl` overrides). Because an identity may hold several
concurrently-valid certificates, rollover is zero-downtime — issue the
replacement *before* the current one expires. This runbook is for
**client-kind** identities: `issue-cert` refuses an admin-kind target for
every caller (`permission_denied`), and admin certificates are rolled over
with the offline `admin-cert issue`
([below](#admin-credentials-and-browser-setup)).

Certificate paths are reserved before the minting RPC. If the RPC or a local
write fails **before both credential files are fully written and closed**, the
CLI deliberately leaves the exclusive placeholders (and any partial private
key at `0600`) in place rather than unlinking a pathname that could have been
replaced concurrently. Inspect those incomplete files and remove them before
retrying. If the CLI instead reports that the one-time credentials were fully
written, preserve that pair and verify the identity/certificate state on the
server before retrying—the files may be the only copy of an enrolled private
key.

```bash
# 1. Reserve a fresh directory, then mint an additional cert for the existing
# identity (the old cert remains valid). Certificate output paths are created
# exclusively and are never overwritten.
mkdir -p ./certs
rollover_dir="$(mktemp -d ./certs/gradethis-be-rollover.XXXXXX)"
parameter-store admin identity issue-cert gradethis-be --ttl 90d --out "$rollover_dir" \
    --endpoint kms.internal:8443 --ca server-ca.crt --cert admin.crt --key admin.key
#    -> writes $rollover_dir/gradethis-be.crt and
#       $rollover_dir/gradethis-be.key (note the serial)

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
identities are the human management plane and administer every namespace, so
no per-namespace list applies to them; they are admitted by the stricter
[admin rule](#admin-credentials-and-browser-setup) (certificate *and* token)
instead, and remain fully audited. Changing a
namespace's auth methods is itself an audited admin action
(`admin:namespace:update`).

## Admin credentials and browser setup

An administrator has **two** credentials, and needs both on every request:
a bearer token and a client certificate issued by the built-in CA. This is
`security.admin_require_client_cert`, on by default. A stolen token alone is
useless, and the certificate's private key never travels in a request.

> **Upgrade note.** On an existing TLS deployment this takes effect the moment
> you restart into the new build: **every admin loses console and CLI access**
> until a certificate is issued for them. Either run `admin-cert issue` for
> each admin before or immediately after the restart, or set
> `security.admin_require_client_cert: false` (`KMS_ADMIN_REQUIRE_CLIENT_CERT=false`,
> `--admin-require-client-cert=false`) to keep the old token-only behavior
> with a startup warning. `serve` names the affected admins at startup, so an
> upgrade with no certificates issued logs one warning per stranded admin plus
> the command to fix it. Deployments running without TLS are unaffected — the
> requirement is relaxed there. One further behavior change on gRPC: a request
> presenting a valid certificate **and** a valid token naming a *different*
> identity is now refused (`unauthenticated`, audited
> `reason: credential_mismatch`); previously the certificate silently won.

**Issue a certificate.** Run on the server host, with the database and master
key reachable — there is no online path for this:

```bash
parameter-store admin-cert issue ops --out ./admin-creds
#   --ttl 90d          certificate lifetime (default 90 days; e.g. --ttl 365d)
#   --sqlite-path P    database (or KMS_SQLITE_PATH / storage.sqlite_path)
#   --kek-file F       master key (or KMS_KEK_FILE / encryption.kek_file)
```

`--out` is required: the private key is written to a `0600` file and never
printed. The command writes `./admin-creds/ops.crt` (`0644`) and
`./admin-creds/ops.key` (`0600`), both created exclusively, and refuses an
unknown, non-admin, or disabled target before anything is unsealed or
reserved. It fails if the database has no CA (run `init`). The issuance is
audited as `identity.cert.issue` with actor `cli` and `channel: local`.

`init --admin ops --cert-dir ./admin-creds` and
`create-admin --name ops --cert-dir ./admin-creds` do the same thing while
creating the identity, printing the one-time token alongside the certificate.

**Use it from the CLI.** Every `parameter-store admin …` command needs the
certificate pair *and* the token:

```bash
parameter-store admin identity list \
    --endpoint kms.internal:8443 --ca server-ca.crt \
    --cert ./admin-creds/ops.crt --key ./admin-creds/ops.key --token "$KMS_TOKEN"

# or once, in the environment:
export KMS_CLIENT_CERT_FILE=./admin-creds/ops.crt
export KMS_CLIENT_KEY_FILE=./admin-creds/ops.key
export KMS_TOKEN=<the admin token>
```

Without `--cert`/`--key` the call fails as `unauthenticated`, exactly as a
wrong token would — the error deliberately does not say which half was
missing.

**Use it from a browser.** Convert the pair to PKCS#12, then import it:

```bash
openssl pkcs12 -export -inkey ./admin-creds/ops.key -in ./admin-creds/ops.crt \
    -name "parameter-store ops" -out ops.p12
```

- **Chrome / Edge** read the operating system store: import `ops.p12` into
  macOS Keychain Access or Windows `certmgr`. On Linux they use NSS instead —
  import at `chrome://settings/certificates`, tab **Your certificates**,
  **Import**.
- **Firefox** keeps its own store: Settings › Privacy & Security › View
  Certificates › **Your Certificates** › **Import**.

Reload the console afterwards. Then sign in with the admin token as before —
the certificate alone never signs you in.

Caveats worth knowing before you hand this to an administrator:

- The browser picks a certificate **per TLS connection** and there is no
  "sign out" for it; closing the browser is what stops it being presented.
  (Server-side sessions with an explicit logout are a planned follow-up.)
- The console's login page detects the situation and says so: it reads the
  unauthenticated `GET /api/v1/health`, and when
  `admin_client_cert_required` is true while `client_cert_presented` is
  false it shows a notice explaining that a certificate is needed. Client
  identity tokens still sign in without one.
- Linux keystores (Chrome and Firefox via NSS) are verified to accept these
  certificates. macOS Keychain and the Windows store should be verified by
  the operator before rolling this out to a fleet.
- A `--p12` output flag is a possible follow-up; today the `openssl` step is
  manual.

**Inspect and revoke.** Both run offline against the database; revocation
needs no master key:

```bash
parameter-store admin-cert list ops
# SERIAL  FINGERPRINT  STATE  EXPIRES  ISSUED   (state: valid | revoked | expired)

parameter-store admin-cert revoke ops --serial <serial>
```

A revoked certificate stops authenticating on the next request, and a running
server sees the change immediately — no restart, no CRL. Revoking an admin
certificate **online** (`admin identity revoke-cert`, the frontend's
certificates dialog) also remains allowed: containment must not require host
access, even though issuance does. To cut an admin off entirely, `rotate-admin`
its token as well.

**Renewal needs host access, by design.** Certificates default to a 90-day
TTL; `--ttl 365d` is available if that cadence is impractical. There is no
automatic renewal and no online self-service path, so track `not_after` (the
`admin-cert list` `EXPIRES` column) and re-issue ahead of expiry. An admin
whose certificate has expired is locked out *harder* than one who never had
it: the expired certificate is rejected by the TLS handshake itself, so their
browser and CLI cannot even reach the login page until it is removed from the
keystore or replaced. `serve` warns at startup about every enabled admin whose
newest valid certificate expires within 14 days (naming the identity and
serial) as well as every admin who has none; re-issue and import the new
certificate before the old one lapses.

## Backups

**Backups must separate the encrypted database from the master key.** This
is the single most important operational fact about this system:

```text
SQLite backup WITHOUT the master key:  cannot decrypt any secret.
SQLite backup WITH the master key:     can decrypt every unbound secret;
                                        each bound version also requires its
                                        operator-held binding key.
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
parameter-store backup --sqlite-path /var/lib/parameter-store/kms.db \
    --out /var/backups/parameter-store/db/kms-$(date +%Y%m%dT%H%M%S).db
# Drop --sqlite-path if KMS_SQLITE_PATH or the config file already names it.
```

`backup` refuses to overwrite an existing output file, so each invocation
needs a fresh path (a timestamp, as above, is the simplest scheme). Its
destination parent must satisfy [secure destination-path](#secure-destination-paths)
checks. The backup is built in a private staging directory, restricted
owner-only, then published atomically without replacement; an entry created at
the destination during the backup wins. The command prints an explicit
reminder that the master key is not included. Purge can scrub only the active
database and WAL: an older backup still contains the retired encrypted
payload. Expire backups, filesystem/volume snapshots, copy-on-write copies,
and replicas containing a compromised cohort under your incident policy.

## Restore

Restoring requires **both** pieces: the database backup and the matching
master key (the same key file, or the same passphrase + salt from
`key_metadata`, whichever mode the backed-up database was initialized
with). A database restored without its key is exactly as unreadable as a
stolen one.

```bash
systemctl stop parameter-store
parameter-store restore --sqlite-path /var/lib/parameter-store/kms.db \
    --in /var/backups/parameter-store/db/kms-20260701T120000.db
# -> Target database: /var/lib/parameter-store/kms.db (source: flag --sqlite-path)
# -> Restore /var/lib/parameter-store/kms.db from .../kms-20260701T120000.db? [y/N]:
# --force is required if the destination file already exists
systemctl start parameter-store
```

`restore` asks for confirmation after naming both paths, so an operator who
pointed it at the wrong database through a stale `KMS_SQLITE_PATH` can still
stop it. **A non-interactive run needs `--yes`** — without it the command
refuses rather than hanging or proceeding — and restoring over an existing
database needs `--yes --force`, since `--force` retains its separate meaning.
An existing destination without `--force` is refused before the prompt, so the
answer is never wasted.

`restore` validates that `--in` is actually a SQLite file (checks the
16-byte file header) before copying it into place, removes any stale
`-wal`/`-shm` sidecar files left over from the previous database so the
restored copy is self-consistent, then opens the restored file to confirm its
baseline is accepted — all before you start the server against it.
The destination must satisfy [secure destination-path](#secure-destination-paths)
checks. Copying uses an owner-only staging file; without `--force`, atomic
no-replace publication preserves any existing or concurrently created entry.
`--force` is the only path that intentionally replaces the destination.
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
| **Master key lost, database intact** | **All secret versions are permanently unrecoverable, including bound versions** (which require the master key plus their binding key). There is no escrow, recovery mechanism, or support path. Parameters and metadata are unaffected. The KEK-wrapped CA private key is also unrecoverable, so the old instance cannot start; a replacement instance bootstraps a new CA and all client certificates must be re-issued. This is why the key backup procedure above must never be skipped. |
| **A binding key is lost** | The contiguous version cohorts wrapped by that key are permanently unreadable even with the master key and database intact. KMS stores no key, hash, fingerprint, or recovery copy. |
| **A binding key is compromised** | Rotate current to a new binding key, create and activate a release that pins the new version, retire old releases, then preview the old cohort around a known affected version and use the admin-only guarded `secret purge-binding-cohort`. Historical versions keep requiring the old key until purged. Restart or replace every affected workload to discard process-held plaintext. Separately expire backups, snapshots, copy-on-write copies, and replicas; active-database scrubbing cannot retract them. If the incident may also have exposed an application's KMS identity token or client certificate and private key, revoke or rotate those credentials too. |
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

For bound secrets, rotation **only touches the outer (KEK) layer**. The inner,
binding-key-derived layer is untouched — rotating the master key never requires
the binding key or invalidates it.

```bash
parameter-store rotate-kek --sqlite-path /var/lib/parameter-store/kms.db \
    --kek-file /etc/parameter-store/master.key \
    --new-key-file /etc/parameter-store/master-new.key
# -> Target database: /var/lib/parameter-store/kms.db (source: flag --sqlite-path)
# -> This will rotate the master key of /var/lib/parameter-store/kms.db. This
#    cannot be undone.
#    Type "/var/lib/parameter-store/kms.db" to confirm:
# -> KEK rotated: 128 secret versions and 1 CA keys rewrapped under kek-<id>
# The old key can no longer decrypt after this rotation; point any running
# server at the new master key and restart it.
```

The confirmation asks for the **absolute** database path, retyped exactly, and
runs before the database is opened or any passphrase is prompted for — a
refusal leaves the file untouched. Pass `--yes` to skip it; without `--yes` on
a non-interactive stdin the command refuses instead of rotating.

Stop `serve` before running this offline command; otherwise the live process
continues with its old in-memory keyring until restarted. The command prints
the count of rewrapped secret versions and CA keys. Omit
`--kek-file` to take the current key from `KMS_KEK_FILE` /
`encryption.kek_file`, or from a passphrase when no key file resolves at all;
omit `--new-key-file` to be prompted for a new passphrase rather than
generating a new key file. (`--new-key-file` names the *replacement* key and is
not a config setting, so it has no environment or config-file spelling.) Every
rotation is audited as `key.rotate`.

## Monitoring and readiness

- `GET /healthz` — liveness (plain text, no auth).
- `GET /readyz` — readiness: current store baseline accepted, master
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
  `{"healthy","ready","version","current_revision","grpc_addr","tls_enabled"}`
  — `current_revision` is useful as a coarse "is anything moving" signal;
  `tls_enabled: false` is what makes the console show its insecure-listener
  warning, so alert on it for any networked bind.
- The console's **Overview** and application pages are backed by
  `GET /api/v1/applications/overview`, which computes a per-environment
  status (`blocked`, `empty`, `incomplete`, `unreleased`, `degraded`,
  `rolling`, `drift`, `ready`) and a list of findings on the server. The
  per-application form is bounded at 64 environments (pass `env=`, repeated
  or comma-joined, beyond that); the fleet form carries no rows or subscriber
  detail, so it never reports `degraded` or `rolling` — open the application
  for those. See the [readiness model](http-api.md#readiness-model).
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
- The console's rollout views (application page columns, the Ship rollout
  panel, and the Releases workspace) follow the same rows live over
  `GET /api/v1/release-subscribers/stream`, a server-sent-events endpoint
  that sends a snapshot on connect, after every acknowledgement, connection
  change, or activation (coalesced over 250 ms), and on a 5 s safety
  re-query. Streams are capped at **4 per identity and 64 per server** (a
  refusal is HTTP 429 and audited as a `configuration_release.subscribers_stream`
  deny), live at most five minutes before the server sends `event: end` and
  the client reconnects, and write a keep-alive comment every 15 s. A reverse proxy in front of the HTTP
  listener must not buffer that path (`X-Accel-Buffering: no` is set) and
  must allow idle connections of at least that lifetime. When the stream is
  unavailable — a proxy that buffers, the cap, or two consecutive failures —
  the console falls back to polling `GET /api/v1/release-subscribers` every
  5 s while the tab is visible and shows a `polling` transport badge, so a
  misconfigured proxy degrades to a slower view rather than a blank one.
- The **Audit** page / `GET /api/v1/audit` is the record of every secret
  access, write, admin action, and authorization denial — see
  [`security.md`](security.md#audit-guarantees) for exactly what is and
  isn't recorded there.
- The console's **Security posture** page (`/posture`, admin-only, backed by
  `GET /api/v1/posture`) is the named form of the sampled expiry gauges: where
  `kms_identity_certs_expiring_soon`, `kms_secret_versions_expiring_soon`,
  `kms_admin_certs_lacking`, and `kms_admin_certs_expiring_soon` tell you *how
  many*, the page tells you *which* — the identities, serials, secret
  addresses, and expiry instants behind each number, soonest first, over a
  7/30/90-day window (the gauges sample the 30-day one, and the
  admin-certificate window is pinned to serve's 14-day startup warning on both
  sides). Beside them it shows the active key's age against
  `kms_kek_active_created_timestamp_seconds` and the
  `kms_kek_generations` count, plus the TLS/mTLS, admin-certificate, audit, and
  metrics posture the process is actually running with. Alert on the gauges;
  open the page to find out what to re-issue. It reports metadata only — no
  value, token, key material, or certificate PEM — and each list is capped at
  200 rows with the true total beside it.
- `GET /metrics` serves a Prometheus exposition of the whole picture above as
  numbers — see [Prometheus metrics](#prometheus-metrics) below.

### Prometheus metrics

`GET /metrics` on the **HTTP listener** serves a Prometheus exposition. It is
**unauthenticated**, exactly like `/healthz`, `/readyz`, and `/api/v1/health` —
a scrape carries no credential, so treat it as the same exposure class as those
and keep it off the public internet: give the reverse proxy an explicit path
rule (allow `/metrics` only from the monitoring network), or firewall the HTTP
listener and scrape it over the private interface. Setting `metrics.enabled:
false` (`KMS_METRICS_ENABLED=false`, `--metrics-enabled=false`) removes the
endpoint entirely — the path stops being special and answers whatever the
frontend catch-all does, a `404` with the frontend off.

Nothing in the exposition identifies **who touched what**. Every label is a
closed set fixed in code (`internal/metrics/labels.go`): a namespace, an
application, a key name, an identity, a client address, and a request ID can
never appear, and a value arriving from a call site that is not in its set
collapses to `other`/`unknown`/`unmatched` rather than minting a series. That
is what makes an unauthenticated endpoint acceptable: a scrape reveals *how
much* is happening, never *what*. The route label is the matched mux pattern
(`GET /api/v1/secrets`), never the request path; every embedded frontend asset
is a single `static` bucket, and anything unrouted is `unmatched`.

A scrape **never touches the database**. The gauges that need a query are
refreshed by a background sampler every 60 s, plus once synchronously at
startup so the first scrape after a restart already carries real numbers. A
failed sampler run leaves the previous values in place and increments
`kms_ops_sample_failures_total` — a transient database error must not read as
"the change log emptied" — so alert on the age of
`kms_ops_last_sample_timestamp_seconds` rather than trusting a flat gauge. The
watch gauges are the exception: they read the hub directly at scrape time.

The series, grouped by what they answer:

| Concern | Series |
|---|---|
| Build and posture | `kms_build_info{version,go_version}`, `kms_start_time_seconds`, `kms_ready`, `kms_tls_enabled`, `kms_admin_client_cert_required`, `kms_reloads_total{result}`, `kms_last_reload_timestamp_seconds` |
| Authentication and authorization | `kms_auth_failures_total{reason}`, `kms_authz_denials_total{operation}`, `kms_authz_method_denials_total{method}`, `kms_ratelimit_refusals_total{limiter}` |
| Audit and secrets | `kms_audit_events_total{event_type,decision}`, `kms_audit_write_failures_total`, `kms_audit_pruned_total`, `kms_secret_decrypt_failures_total`, `kms_release_outcomes_total{outcome}` |
| Transport | `kms_grpc_requests_total{service,method,code}`, `kms_grpc_request_duration_seconds{service,method}`, `kms_grpc_streams_active{service,method}`, `kms_http_requests_total{route,method,status}`, `kms_http_request_duration_seconds{route,method}`, `kms_http_sse_streams_active` |
| Watch fan-out (scrape-time) | `kms_watch_subscribers`, `kms_watch_release_subscribers`, `kms_watch_last_dispatched_revision`, `kms_watch_subscriber_lag_revisions_max`, `kms_watch_subscribers_dropped_total{reason}` |
| Keys and certificates (sampled) | `kms_kek_generations`, `kms_kek_active_created_timestamp_seconds`, `kms_admin_certs_lacking`, `kms_admin_certs_expiring_soon`, `kms_identity_certs_expiring_soon`, `kms_secret_versions_expiring_soon` |
| Storage (sampled) | `kms_changelog_rows`, `kms_changelog_last_revision`, `kms_changelog_oldest_revision`, `kms_db_file_bytes{file}` |
| The sampler itself | `kms_ops_last_sample_timestamp_seconds`, `kms_ops_sample_failures_total` |

The standard Go runtime and process collectors (`go_*`, `process_*`) share the
registry, so one scrape covers goroutines, heap, file descriptors, and CPU too.

Ready-to-load alerting rules are in
[`deploy/prometheus/alerts.yml`](../deploy/prometheus/alerts.yml). The
expressions, in short:

| Alert | Expression |
|---|---|
| Not being scraped | `up == 0` |
| Serving but not ready | `kms_ready == 0` for 2m |
| Credential rejections | `sum(rate(kms_auth_failures_total[5m])) > 1` |
| Admin refused for a missing certificate | `increase(kms_auth_failures_total{reason="admin_client_cert_required"}[10m]) > 0` |
| Admins with no usable certificate | `kms_admin_certs_lacking > 0` |
| Admin certificate expiring | `kms_admin_certs_expiring_soon > 0` |
| Master key older than a year | `time() - kms_kek_active_created_timestamp_seconds > 365 * 86400` |
| Audit rows not persisted | `increase(kms_audit_write_failures_total[5m]) > 0` |
| A watch subscriber is behind | `kms_watch_subscriber_lag_revisions_max > 0` for 10m |
| The hub dropped a subscriber | `increase(kms_watch_subscribers_dropped_total[15m]) > 0` |
| The sampler is stale | `time() - kms_ops_last_sample_timestamp_seconds > 300` |


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

Every new change row carries the immutable ID of the namespace incarnation
that produced it. A watch registration is bound to the exact current ID, and
both replay and live delivery require that match. Deleting/recreating an
`env/app` is therefore a new namespace: the stale stream closes on heartbeat
(including admin streams), and a reconnect authorizes the replacement afresh.
Legacy change rows without an ID force a current snapshot instead of ambiguous
replay; release watches use the same exact-incarnation rule.

Configuration release replay and history have additional guards. By default,
KMS retains at least the newest 100 inactive release versions and 90 days of
history (`watch.release_retain_versions`,
`watch.release_retain_duration`). It never prunes current or previous releases,
their schema dependencies, or versions required by retained activation replay.
Disconnected per-instance lifecycle state is pruned after 30 days by default
(`watch.release_subscriber_retain_duration`). These three settings must be
positive. Release activation rows are filtered out of the existing namespace
resource stream; release loaders use their dedicated stream.
