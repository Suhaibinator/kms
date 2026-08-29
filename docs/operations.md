# Operations

This is the production runbook: starting the service unattended, TLS/mTLS,
backup and restore, disaster recovery, key rotation, and what to monitor.
For the encryption and authorization design behind these procedures, see
[`security.md`](security.md). For the HTTP API the frontend uses, see
[`http-api.md`](http-api.md).

> **Status note.** Every command below matches the CLI implemented in
> `internal/cli`. The offline commands (`init`, `migrate`, `check`, `backup`,
> `restore`, `create-admin`, `rotate-admin`, `rotate-kek`, `import`) operate directly on the
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
| `KMS_VERIFY_DEFAULTS_REQUESTS_PER_HOUR` | `server.verify_defaults.requests_per_hour` (integer) |
| `KMS_VERIFY_DEFAULTS_BURST` | `server.verify_defaults.burst` (integer) |
| `KMS_VERIFY_DEFAULTS_MISMATCH_BUDGET_PER_HOUR` | `server.verify_defaults.mismatch_budget_per_hour` (integer) |
| `KMS_LOG_LEVEL` | `log.level` |
| `KMS_MASTER_PASSPHRASE` | Supplies the master passphrase without a TTY prompt (see below) |

`Config.Validate()` enforces: both listen addresses set; `sqlite_path` set;
`mtls_enabled` requires `tls_enabled`; `tls_enabled` requires
`server_cert_file`/`server_key_file` to exist; `mtls_enabled` requires
`client_ca_file` to exist; every `server.verify_defaults.*` budget is
positive and `mismatch_budget_per_hour` is at least 300. `Config.Redacted()` is what the server logs at
startup — addresses, paths, and feature flags, deliberately never a
wholesale dump of the file (so nothing sensitive that might end up in the
YAML by mistake gets logged).

Logs are structured JSON emitted by [Uber zap](https://github.com/uber-go/zap):
each line carries `ts` (ISO8601, millisecond precision), a lowercase `level`
(`debug`/`info`/`warn`/`error`), `msg`, and typed fields. `log.level` /
`KMS_LOG_LEVEL` sets the minimum level (default `info`). Secret plaintext,
tokens, and key material never appear in a log line at any level.

## Connect a production application with mTLS

Use one KMS identity and one client certificate/key pair per consuming
application. Before issuing anything, distinguish the three certificate roles:

| Certificate role | Created and stored by | Used for |
|---|---|---|
| **KMS server certificate/key + server trust CA** | The operator obtains the serving certificate from the organization's PKI or another trusted CA, configures `server_cert_file`/`server_key_file`, and distributes a `server-ca.crt` trust bundle to applications. | KMS presents the serving certificate; applications use `server-ca.crt` to verify the KMS server. The server private key stays on the KMS host. |
| **KMS built-in client-issuing CA** | KMS creates this self-signed CA on first startup and stores it in SQLite's `ca_keys` table; the private key is KEK-wrapped. | KMS issues and verifies application client certificates. Applications do **not** use this CA for server trust. `admin ca show` exports its public certificate for diagnostics or out-of-band verification only. |
| **Per-application client certificate/key** | KMS creates it when an mTLS identity is enrolled. Its serial/fingerprint enrollment remains in `identity_certs`; the one-time PEM files go to the operator. | The application presents the certificate and proves possession of its private key. KMS maps its `kms://identity/<name>` URI SAN to the enrolled identity and checks its serial, fingerprint, expiry, and revocation state. |

The following secure path assumes the server is already running with
[`security.tls_enabled: true`](#tls-and-mtls), the operator has the CA bundle
that trusts its serving certificate, and an admin credential is available.
The serving certificate's DNS or IP SAN must match the host applications use
in `KMS_ENDPOINT` (`kms.internal` below), because the SDKs perform normal
server-name verification. The built-in client CA works with
`mtls_enabled: false`; that flag is only for adding a separate
operator-supplied client CA.

### 1. Create the application's namespace

New namespaces default to mTLS-only, but specifying the method makes the
intended posture visible in scripts and reviews:

```bash
ADMIN_TOKEN=...                 # bootstrap or another admin credential
KMS_ENDPOINT=kms.internal:8443
SERVER_CA=/etc/parameter-store/trust/server-ca.crt

parameter-store admin namespace create --env prod --app gradethis \
    --auth-methods mtls --endpoint "$KMS_ENDPOINT" --ca "$SERVER_CA" \
    --token "$ADMIN_TOKEN"
```

For an existing namespace, inspect and update it with the same secure
connection flags:

```bash
parameter-store admin namespace list \
    --endpoint "$KMS_ENDPOINT" --ca "$SERVER_CA" --token "$ADMIN_TOKEN"

parameter-store admin namespace update --env prod --app gradethis \
    --auth-methods mtls --endpoint "$KMS_ENDPOINT" --ca "$SERVER_CA" \
    --token "$ADMIN_TOKEN"
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
    --endpoint "$KMS_ENDPOINT" --ca "$SERVER_CA" --token "$ADMIN_TOKEN"
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

### 3. Deploy the application credentials and server trust

Deliver these paths to the consuming application through its normal secret
and configuration mechanism:

```text
KMS_ENDPOINT     = kms.internal:8443
KMS_CLIENT_CERT  = /run/credentials/gradethis-be.crt
KMS_CLIENT_KEY   = /run/credentials/gradethis-be.key
KMS_SERVER_CA    = /etc/ssl/kms/server-ca.crt
```

The client certificate is public identity material, but its private key is a
secret and should be readable only by the application account. The server CA
bundle is public trust configuration and must come from the operator or the
organization's PKI. Do **not** deploy the KMS server private key, the built-in
client-issuing CA, or an admin token to the application. In particular, do not
substitute output from `admin ca show` for `KMS_SERVER_CA`.

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
        os.Getenv("KMS_CLIENT_CERT"),
        os.Getenv("KMS_CLIENT_KEY"),
        os.Getenv("KMS_SERVER_CA"),
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
        os.environ["KMS_CLIENT_CERT"],
        os.environ["KMS_CLIENT_KEY"],
        os.environ["KMS_SERVER_CA"],
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
SQLite file passed via `--db` — they do not require a running server (except
`create-admin` and `rotate-admin` identity changes, which a running `serve`
process reads immediately via its shared database). All flags are the standard library
`flag` package's double-dash-or-single-dash form (e.g. `--db` or `-db`).

| Command | Flags | Purpose |
|---|---|---|
| `init` | `--db` (default `./kms.db`), `--master-key-file` (omit for a passphrase prompt), `--admin NAME` (optional) | Creates/migrates the database and the master key (generating a key file, or prompting for a new passphrase with confirmation). With `--admin`, also creates a bootstrap admin identity and prints its token once. |
| `migrate` | `--db` | Opens the database, applying any pending migrations, and exits. |
| `check` | `--db`, `--key-file` (optional) | Verifies the database is reachable and, if a key source is available (file, `KMS_MASTER_PASSPHRASE`, or TTY), verifies the master key against the stored key-check value. Never prints key material. |
| `backup` | `--db`, `--out` (required, must not already exist) | Writes a consistent online backup through owner-only staging and atomic no-replace publication. Prints a reminder that the master key is not included. |
| `restore` | `--db` (destination), `--in` (required, source backup), `--force` (overwrite an existing destination) | Validates the input is a KMS SQLite backup, stages an owner-only copy, publishes it atomically, removes stale `-wal`/`-shm` sidecars, then opens (and migrates) it. Without `--force`, publication never replaces an existing or concurrently created entry. |
| `create-admin` | `--db`, `--name` (required) | Creates an admin identity directly against the database file and prints its token once. Uses WAL mode's concurrent-reader support, but coordinating this against a live `serve` process is the operator's responsibility. |
| `rotate-admin` | `--db`, `--name` (required) | Recovery command that directly replaces an existing enabled admin identity's token hash and prints the new token once. The old token becomes invalid immediately; a disabled admin, client identity, missing identity, or identity without a token is rejected without mutation. It does not require the old token, master key, or a running server. If output is lost, rerun the command to mint another replacement. A running server observes the shared-database update immediately, but operators must coordinate concurrent identity administration. |
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

### Management commands (`admin` group, over gRPC)

The `admin` command group manages namespaces, identities, and the built-in
CA on a **running** server over gRPC (unlike the offline `--db` commands
above). Every admin command shares the connection flags `--endpoint`
(default `localhost:8443`), `--token` (admin bearer token; env `KMS_TOKEN`),
`--insecure` (skip TLS, development only), `--ca`, and `--cert`/`--key`
(mTLS). The diagnostic `admin ca show` command needs no credential because the
built-in client issuer's certificate is public.

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
| `admin policy create NAME` | `--subject IDENTITY` (or `*`), `--allow OP@ENV/APP` (repeatable), `--deny OP@ENV/APP` (repeatable) | Create a namespace-level policy. Either label may be `*`; a bare `OP` means every namespace. Operations and labels are validated by the server (`policy.ValidateRules`). |
| `admin policy list` | `--page-size` | Table: name, subject, allow rules, deny rules (`op@env/app`). |
| `admin policy delete NAME` | — | Delete a policy. |
| `admin ca show` | `--out FILE` | **Diagnostic/out-of-band only:** print (or write) the public built-in **client-issuing** CA certificate to inspect or independently verify KMS-issued client certificates. This is not the SDK's server-trust CA and is not part of application onboarding. |

`--ttl` accepts a Go duration (`720h`) or a bare day count (`90d`); omitting
it uses the server's 90-day default. `--auth-methods` and `--auth` values are
`mtls` and/or `token`. An admin-kind identity always receives a bearer token;
requesting `mtls` for an admin adds a certificate rather than replacing that
token. Tokens and certificate private keys are shown exactly once and are
never retrievable again. `admin namespace`/`identity` map onto
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

Parameter content types are literal KMS tokens: `string`, `integer`, `float`,
`boolean`, `json`, or `binary`. They are not MIME types. In particular, publish
a managed JSON group with `--content-type json`; `application/json` is rejected.

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
Creation persists the exact immutable namespace-row ID for every resolved
reference. Deleting and recreating the same `env/app` therefore cannot retarget
an existing pin. Releases migrated without those IDs remain readable and keep
conservative name-based deletion guards, but activation fails closed; recreate
such a release before activating it so every source obtains an exact pin.

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
access at all) and grant exactly that one operation on the target namespace
(and on any namespace whose parameters the release pins cross-namespace).
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
built-in CA issued authenticates by mTLS, while token-only clients, presenting
no certificate, still connect. The server reads that issuer directly from
SQLite; application onboarding does not involve `admin ca show`.
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
needs a fresh path (a timestamp, as above, is the simplest scheme). Its
destination parent must satisfy [secure destination-path](#secure-destination-paths)
checks. The backup is built in a private staging directory, restricted
owner-only, then published atomically without replacement; an entry created at
the destination during the backup wins. The command prints an explicit
reminder that the master key is not included.

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
