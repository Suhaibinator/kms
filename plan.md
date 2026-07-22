# Parameter Store and Secret Management Service Requirements

> **Historical requirements.** This document records the original design. The
> namespace-native rewrite in [`plan-namespaces.md`](plan-namespaces.md) was
> implemented and merged; several details here were subsequently refined. The
> current code, `README.md`, and `docs/` are authoritative. In particular, the
> flat path model, per-key policy/watch concepts, CA trust guidance, and exact
> operational behavior below must not be treated as current contracts.

## 1. Overview

This document defines the requirements for a self-contained parameter store and secret management service. The service provides a secure centralized system for storing, retrieving, versioning, rotating, and auditing application configuration values and secrets.

The service exposes a gRPC API for consuming services, publishes easy-to-use client SDKs, persists data in SQLite, and runs as a single compiled Go binary. The same Go binary also serves an HTTP-based frontend application for managing parameters, secrets, access policies, namespaces, and audit records.

The system is intended to support both non-sensitive configuration values and sensitive secrets such as API keys, database passwords, MinIO credentials, OAuth credentials, webhook signing secrets, and service tokens.

The concrete goal of this project is to replace SuhaibParameterStore, which gradethis currently uses. The project is done when gradethis runs entirely against this service with no dependency on `SuhaibParameterStoreClient` (see section 33).

## 2. Goals

The service must:

1. Provide a centralized parameter and secret store for internal services.
2. Store all secrets encrypted at rest.
3. Use envelope encryption for secrets.
4. Keep consuming services simple by exposing ergonomic client SDKs.
5. Compile and run as a single Go binary.
6. Use SQLite as the local storage engine.
7. Serve both the gRPC API and the management frontend from the Go application.
8. Embed the frontend static build into the Go binary.
9. Support versioned secrets.
10. Support role-based or policy-based access control.
11. Provide audit logging for secret reads, writes, updates, deletions, and administrative actions.
12. Support simple local deployment for development and production use.
13. Avoid requiring consuming services to understand encryption, KMS metadata, storage layout, or key-management internals.
14. Let consuming applications initialize secrets and parameters declaratively: config structs declare store-backed fields, and a single `Init`/`Resolve` call hydrates them (see 9.5).
15. Store non-secret dynamic parameters and push value changes to subscribed consuming applications while they run (hot reload, see 8.4 and 9.6).
16. Maintain a live registry of subscribed applications so operators can see which apps are connected, what they watch, and which config revision each has applied.
17. Fully replace SuhaibParameterStore: provide import tooling and a well-documented migration path for gradethis (see section 33). The SDK API is designed on its own merits and is not constrained by the old client's design.
18. Support opt-in client-bound secrets, where decryption requires both the master key and a client-supplied token, with explicitly accepted permanent-loss semantics (see 10.7).

## 3. Non-Goals

The service is not intended to:

1. Replace a cloud KMS, HSM, or enterprise key-management system in high-compliance environments.
2. Expose raw master key material to clients or operators.
3. Require every consuming service to directly call KMS.
4. Store plaintext secrets on disk.
5. Store plaintext secrets in logs, traces, metrics, audit records, or frontend responses unless explicitly requested by an authorized user.
6. Provide a general-purpose database.
7. Provide arbitrary file storage.
8. Act as a public internet-facing identity provider.
9. Automatically rotate third-party secrets unless integrations are explicitly implemented later.

## 4. Terminology

### Parameter

A non-sensitive configuration value.

Examples:

```text
/prod/payments/stripe/region
/prod/api/rate-limit
/staging/search/opensearch/endpoint
```

Parameters may be stored in plaintext in SQLite, although they should still be protected by access control.

### Secret

A sensitive value that must be encrypted at rest.

Examples:

```text
/prod/payments/stripe/api-key
/prod/orders/postgres/password
/prod/storage/minio/secret-key
```

Secrets must never be stored as plaintext in SQLite.

### DEK

Data Encryption Key. A random symmetric key used to encrypt one secret value or one secret version.

### KEK

Key Encryption Key. A higher-level key used to encrypt, or wrap, DEKs.

### Envelope Encryption

A cryptographic pattern where the secret value is encrypted with a DEK, and the DEK is encrypted with a KEK.

### Namespace

A logical grouping of parameters and secrets, typically aligned with environment, team, service, tenant, or domain.

Examples:

```text
/prod/payments
/staging/identity
/dev/platform
```

### Version

An immutable revision of a parameter or secret.

Example:

```text
/prod/payments/stripe/api-key:v1
/prod/payments/stripe/api-key:v2
```

### Label

A movable alias pointing to a version.

Examples:

```text
current -> v3
previous -> v2
deprecated -> v1
```

## 5. High-Level Architecture

The service consists of:

1. Go server application.
2. gRPC API for consuming services and SDKs.
3. HTTP API for frontend/admin operations where appropriate.
4. Embedded frontend static application.
5. SQLite database.
6. Encryption subsystem.
7. Authorization subsystem.
8. Audit logging subsystem.
9. Client SDKs.

Conceptual architecture:

```text
Consuming Service
   |
   | gRPC over TLS/mTLS
   v
Client SDK
   |
   v
Parameter Store Service - single Go binary
   |
   |-- gRPC API
   |-- HTTP frontend server
   |-- AuthN/AuthZ layer
   |-- Encryption layer
   |-- Audit logger
   |
   v
SQLite database
```

For secret storage:

```text
Plaintext secret
   |
   | encrypted with DEK
   v
Ciphertext secret

DEK
   |
   | encrypted with KEK
   v
Encrypted DEK

SQLite stores:
  - ciphertext secret
  - encrypted DEK
  - encryption metadata
  - version metadata
  - policy metadata
  - audit metadata
```

## 6. Runtime and Packaging Requirements

### 6.1 Language

The service must be implemented in Go.

### 6.2 Binary

The service must compile into a single executable binary.

The binary must include:

1. gRPC server.
2. HTTP server.
3. Embedded frontend static files.
4. Database migration logic.
5. CLI commands for initialization and administration.
6. Encryption subsystem.
7. Optional SDK code-generation assets if needed.

### 6.3 Frontend Embedding

The frontend must be a Next.js application built as a static export (`output: "export"`) and embedded into the Go binary using Go's `embed` package.

The Next.js source lives in its own directory in the repo (e.g. `frontend/`), separate from the Go code. The build pipeline runs the Next.js static export first, then compiles the Go binary with the export output embedded:

```text
frontend/            Next.js source (not embedded)
frontend/out/        next build static export output (embedded)
```

```go
//go:embed frontend/out
var frontendFS embed.FS
```

Build order:

```bash
cd frontend && npm ci && npm run build   # produces frontend/out
go build ./cmd/parameter-store           # embeds frontend/out
```

A `Makefile` (or equivalent) target must chain these so a single command produces the complete binary. CI must fail the Go build if the export output is missing or stale rather than silently shipping an empty frontend.

Because the export is static, the frontend must not use Next.js server-only features (server actions, API routes, ISR, image optimization). All dynamic data comes from the Go server's HTTP API.

The Go HTTP server must serve the embedded export and support browser refresh and deep-link routing by falling back to the exported HTML entry point for unknown frontend routes.

### 6.4 SQLite

SQLite must be the primary persistence layer.

The application must support configurable SQLite database location.

Example:

```text
./kms.db
/var/lib/parameter-store/kms.db
```

The service must enable appropriate SQLite settings for reliability and concurrent access.

Recommended baseline:

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;
```

For higher durability mode, the operator should be able to configure:

```sql
PRAGMA synchronous = FULL;
```

### 6.5 Deployment Modes

The service should support at least:

1. Local development mode.
2. Single-node production mode.
3. Read-only maintenance mode.
4. Backup/restore administrative mode.

Because SQLite is embedded storage, the v1 service is primarily single-writer/single-node. Multi-node active-active behavior is out of scope unless a future replication design is added.

## 7. Configuration Requirements

The service must be configurable through:

1. Environment variables.
2. YAML/TOML/JSON config file.
3. CLI flags where appropriate.

Configuration should include:

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

encryption:
  mode: "local"
  kek_provider: "local-file"
  kek_file: "/etc/parameter-store/master.key"

auth:
  mode: "token"        # v1 default; "mtls" available as hardening
  policy_mode: "rbac"

frontend:
  enabled: true

audit:
  enabled: true
```

Secrets used to configure the service itself must not be logged.

## 8. API Requirements

> **Superseded.** The API shapes below (path-string request fields) are
> replaced by the namespace-native wire protocol in
> [`plan-namespaces.md`](plan-namespaces.md) §8. See there for the current
> `NamespaceRef`/`ResourceRef` messages and per-service changes. The current
> protocol has no watch selector or key-pattern message.

The service must expose a gRPC API as the primary programmatic interface.

The service may expose HTTP APIs for frontend use, but consuming services should prefer gRPC through official SDKs.

### 8.1 gRPC Service: Parameters

Required operations:

```protobuf
service ParameterService {
  rpc GetParameter(GetParameterRequest) returns (GetParameterResponse);
  rpc PutParameter(PutParameterRequest) returns (PutParameterResponse);
  rpc ListParameters(ListParametersRequest) returns (ListParametersResponse);
  rpc DeleteParameter(DeleteParameterRequest) returns (DeleteParameterResponse);
  rpc GetParameterMetadata(GetParameterMetadataRequest) returns (GetParameterMetadataResponse);
}
```

### 8.2 gRPC Service: Secrets

Required operations:

```protobuf
service SecretService {
  rpc GetSecret(GetSecretRequest) returns (GetSecretResponse);
  rpc PutSecret(PutSecretRequest) returns (PutSecretResponse);
  rpc ListSecrets(ListSecretsRequest) returns (ListSecretsResponse);
  rpc DeleteSecret(DeleteSecretRequest) returns (DeleteSecretResponse);
  rpc DisableSecret(DisableSecretRequest) returns (DisableSecretResponse);
  rpc DestroySecretVersion(DestroySecretVersionRequest) returns (DestroySecretVersionResponse);
  rpc GetSecretMetadata(GetSecretMetadataRequest) returns (GetSecretMetadataResponse);
  rpc PromoteSecretVersion(PromoteSecretVersionRequest) returns (PromoteSecretVersionResponse);
}
```

### 8.3 gRPC Service: Watch

The service must support watching parameters and metadata updates so consuming applications can hot-reload configuration without restarting.

```protobuf
service WatchService {
  // Subscribe is the primary hot-reload mechanism. The client declares the
  // paths/prefixes it wants and its last-applied revision; the server streams
  // an initial snapshot (or delta), then pushes events as values change,
  // interleaved with heartbeats.
  rpc Subscribe(stream SubscribeRequest) returns (stream SubscribeEvent);

  // Single-resource conveniences built on the same event model.
  rpc WatchParameter(WatchParameterRequest) returns (stream ParameterEvent);
  rpc WatchNamespace(WatchNamespaceRequest) returns (stream NamespaceEvent);
}

message SubscribeRequest {
  // First message: subscription registration.
  string client_name = 1;          // e.g. "gradethis-be"; instance ID derived from identity + nonce
  repeated string paths = 2;       // exact paths and/or prefixes, e.g. "/prod/payments/*"
  uint64 last_seen_revision = 3;   // 0 = send full snapshot
  // Subsequent messages: acks / heartbeat responses.
  uint64 acked_revision = 4;
}

message SubscribeEvent {
  oneof event {
    Snapshot snapshot = 1;         // initial state for subscribed paths
    ParameterChange change = 2;    // put / delete / label move, with new value
    SecretMetadataChange secret_change = 3; // version promoted, disabled, etc. (no plaintext)
    Heartbeat heartbeat = 4;
  }
  uint64 revision = 5;             // global, monotonically increasing
}
```

Requirements:

1. Every write to a parameter (or label move) must increment a global revision counter persisted in SQLite; events carry the revision.
2. On subscribe, the client sends its `last_seen_revision`; the server replays events after that revision from a bounded change log, or sends a full snapshot if the revision is too old to replay.
3. Delivery is at-least-once; clients apply events idempotently keyed by revision.
4. Parameter change events carry the new value inline (parameters are non-sensitive), so subscribers do not need a follow-up fetch.
5. Secret value changes are delivered as metadata-only events (path, new version, label). Authorized clients re-fetch the secret through `GetSecret`; plaintext secrets are never pushed over watch streams in v1.
6. Watch authorization uses the same policy engine as reads: a client only receives events for paths it can read.

### 8.4 Subscription Registry and Liveness

The server must maintain an in-memory registry of live subscribers, backed by the open `Subscribe` streams themselves. The stream is the liveness mechanism — no separate registration or ping-pong protocol, and consuming apps never need to expose an inbound endpoint or webhook.

1. When a `Subscribe` stream opens, the server records: client identity, client name, instance ID, subscribed paths, connect time, and remote address.
2. The server sends a heartbeat event on an interval (default 30s); the client responds with its `acked_revision`. The registry tracks last-heartbeat time and last-acked revision per subscriber.
3. A subscriber that misses N heartbeats (default 3) is considered dead; the server closes the stream and drops it from the registry.
4. Standard gRPC keepalive settings should be configured in addition to application-level heartbeats.
5. Clients reconnect on stream loss with jittered exponential backoff, resuming from their last-seen revision.
6. The registry must be queryable via the Admin API and visible in the frontend: which apps are connected, what namespaces they watch, and which global revision each has applied. Because the revision is global, lag is a coarse signal; it does not prove that a particular namespace change propagated.
7. Subscriber counts and stale-subscriber detection should be exported as metrics.
8. As a safety net against missed events, SDKs should perform a periodic full-sync reconciliation poll (default every 5 minutes) comparing revisions.

### 8.5 gRPC Service: Admin

Required administrative operations:

```protobuf
service AdminService {
  rpc CreateNamespace(CreateNamespaceRequest) returns (CreateNamespaceResponse);
  rpc ListNamespaces(ListNamespacesRequest) returns (ListNamespacesResponse);
  rpc CreatePolicy(CreatePolicyRequest) returns (CreatePolicyResponse);
  rpc UpdatePolicy(UpdatePolicyRequest) returns (UpdatePolicyResponse);
  rpc DeletePolicy(DeletePolicyRequest) returns (DeletePolicyResponse);
  rpc ListAuditEvents(ListAuditEventsRequest) returns (ListAuditEventsResponse);
  rpc ListSubscribers(ListSubscribersRequest) returns (ListSubscribersResponse);
  rpc Health(HealthRequest) returns (HealthResponse);
}
```

Administrative APIs must require elevated privileges.

## 9. Client SDK Requirements

The project must publish client SDKs that are easy for consuming services to use.

### 9.1 Required SDKs

At minimum:

1. Go SDK.
2. Python SDK.

Additional SDKs may include:

1. TypeScript/Node.js.
2. Java/Kotlin.
3. C#/.NET.
4. Rust.

### 9.2 SDK Responsibilities

SDKs must:

1. Hide gRPC boilerplate.
2. Support TLS/mTLS configuration.
3. Provide simple get APIs.
4. Provide typed configuration hydration.
5. Support local in-memory caching.
6. Avoid logging secret values.
7. Redact secret values in errors and debug output.
8. Support request timeouts and retries.
9. Support fallback behavior where explicitly configured.
10. Support version labels such as `current` and `previous`.
11. Provide declarative store-backed value types that resolve with a single `Init` call (see 9.5).
12. Support subscribing to parameter changes and hot-reloading values without restart (see 9.6).

### 9.3 SDK Example: Go

```go
client, err := kmsclient.NewClient(kmsclient.Config{
    Endpoint: "parameter-store.prod.internal:8443",
    TLS:      kmsclient.MTLSFromFiles("client.crt", "client.key", "server-ca.crt"),
    CacheTTL: time.Minute,
})
if err != nil {
    return err
}

dbPassword, err := client.GetSecret(ctx, "/prod/payments/postgres/password")
if err != nil {
    return err
}

stripeKey, err := client.GetSecret(ctx, "/prod/payments/stripe/api-key")
if err != nil {
    return err
}
```

### 9.4 SDK Example: Typed Hydration

```go
type AppConfig struct {
    DatabasePassword string `secret:"/prod/payments/postgres/password"`
    StripeAPIKey     string `secret:"/prod/payments/stripe/api-key"`
    RateLimit        int    `parameter:"/prod/payments/rate-limit"`
}

var cfg AppConfig
err := client.Hydrate(ctx, &cfg)
```

### 9.5 Declarative Config Initialization (Drop-In Pattern)

Consuming applications should be able to declare store-backed values as plain config struct fields and resolve them with minimal ceremony. The Go SDK must provide value types for this:

```go
type SecretValue struct {
    Key     string // parameter store path, e.g. "/prod/payments/stripe/api-key"
    Token   string // per-secret access token; for client-bound secrets (10.7) it is also the client key share
    EnvVar  string // optional environment variable override
    Default string // optional default, intended for dev only
}

type ParameterValue struct {
    Key     string
    EnvVar  string
    Default string
    Dynamic bool   // when true, the value hot-reloads on change (see 9.7)
}
```

Each value type must expose `Init(client)`, so apps that prefer explicit per-field initialization can do this:

```go
type Config struct {
    StripeAPIKey    kmsclient.SecretValue
    OpenAIAPIKey    kmsclient.SecretValue
    RateLimit       kmsclient.ParameterValue
}

func (c *Config) Init(client *kmsclient.Client) error {
    if err := c.StripeAPIKey.Init(client); err != nil { return err }
    if err := c.OpenAIAPIKey.Init(client); err != nil { return err }
    return c.RateLimit.Init(client)
}
```

The SDK must also provide a one-call resolver that walks a struct (including nested structs) and initializes every store-backed field, so apps do not need to enumerate fields by hand:

```go
if err := client.Resolve(ctx, &cfg); err != nil {
    return err
}
```

Resolution order for each field:

1. Environment variable override, if `EnvVar` is set and non-empty in the environment.
2. Parameter store fetch over gRPC.
3. `Default`, if configured.
4. Otherwise an error naming the missing path (fail fast at startup).

Additional requirements:

1. `Resolve` must batch fetches into as few RPCs as possible.
2. Resolved secret values must redact themselves in `String()`/`Format()`/JSON marshaling; access to plaintext requires an explicit `Value()` call.
3. `Init` on an already-initialized value must be idempotent.
4. Non-Go SDKs must provide the equivalent idiom (e.g. decorators/descriptors in Python).

### 9.6 Hot Reload

SDKs must support hot reloading of non-secret parameters using the `Subscribe` stream (8.3/8.4). The SDK owns the connection lifecycle: subscribe on startup, heartbeat, reconnect with backoff, resume by revision, and periodic full-sync reconciliation. Applications only see values and callbacks.

Access patterns, in order of preference:

```go
// 1. Live handle: always returns the latest value. Safe for concurrent use.
rateLimit := cfg.RateLimit.Get()

// 2. Change callback: for values that need explicit reaction
//    (resize pools, rebuild clients, etc.).
cfg.RateLimit.OnChange(func(old, new string) {
    pool.Resize(mustAtoi(new))
})

// 3. Namespace-level watch for advanced use.
client.Watch(ctx, "/prod/payments/*", func(ev kmsclient.Event) { ... })
```

Requirements:

1. `ParameterValue` fields with `Dynamic: true` (and values hydrated via `Resolve` with a `dynamic` tag) are automatically registered on the app's subscription; `Get()` returns the latest value without an RPC.
2. Updates must be atomic per value: readers see either the old or the new value, never a partial state.
3. Callbacks run serially on a dedicated goroutine; a panic is recovered, and a slow callback must not stall the stream but does delay later callbacks.
4. Env-var-overridden values do not hot-reload (the override pins them); the SDK should log this at startup.
5. Secrets hot-reload indirectly: on a secret metadata change event the SDK may re-fetch the secret and update the handle, but only when the app opts in (e.g. `Reloadable: true`), since consuming code must be written to tolerate rotation.
6. If the store is unreachable, apps keep serving the last-known values; the SDK reconnects in the background and reconciles on resume.

### 9.7 SDK Local Config

Consuming services should store only references to secrets, not ciphertext or KMS metadata.

Example:

```yaml
parameter_store:
  endpoint: "parameter-store.prod.internal:8443"

secrets:
  database_password: "/prod/payments/postgres/password"
  stripe_api_key: "/prod/payments/stripe/api-key"
  minio_access_key: "/prod/payments/minio/access-key"
  minio_secret_key: "/prod/payments/minio/secret-key"
```

## 10. Secret Storage Requirements

### 10.1 Encryption at Rest

All secret values must be encrypted before being persisted to SQLite.

SQLite must never store plaintext secret values.

### 10.2 Envelope Encryption

Every secret version must be encrypted with a DEK.

The DEK must be encrypted with a KEK.

SQLite must store:

1. Ciphertext secret.
2. Encrypted DEK.
3. KEK identifier.
4. Encryption algorithm.
5. Nonce or IV.
6. Authentication tag if not embedded in ciphertext.
7. Encryption context.
8. Secret version metadata.

### 10.3 Encryption Algorithm

The service must use authenticated encryption.

Approved algorithms:

```text
AES-256-GCM
XChaCha20-Poly1305
ChaCha20-Poly1305
```

AES-256-GCM is the default recommended algorithm.

The service must not use unauthenticated encryption modes for secret payloads.

Disallowed for secret payload encryption:

```text
AES-CBC without MAC
AES-ECB
custom cryptography
homegrown stream ciphers
```

### 10.4 Nonce Requirements

The encryption subsystem must generate a unique nonce per encryption operation.

For AES-GCM, nonce reuse with the same DEK must be prevented.

Because each secret version should use a unique DEK, nonce collision risk is reduced, but the system must still generate nonces using a cryptographically secure random source.

### 10.5 Associated Data

Secret encryption must bind metadata as associated data.

Required associated data:

```text
namespace
secret name
version
environment if available
tenant if available
record type
```

This prevents ciphertext from being copied between records and decrypted under the wrong context.

### 10.6 Plaintext Handling

Plaintext secrets and plaintext DEKs must:

1. Exist only in process memory.
2. Never be written to SQLite.
3. Never be written to logs.
4. Never be written to traces.
5. Never be written to metrics.
6. Never be included in audit events.
7. Be discarded as soon as possible.

Where practical, the Go implementation should avoid unnecessary copies of secret values.

### 10.7 Client-Bound Secrets (Opt-In)

A secret may opt in to client-bound encryption at creation time (`client_bound: true`). The default remains standard master-key-only wrapping.

For client-bound secrets, the DEK is wrapped in two layers:

```text
DEK
   |
   | encrypted with client key
   | (derived via HKDF from the per-secret client access token)
   v
inner-wrapped DEK
   |
   | wrapped with master key (KEK)
   v
stored encrypted DEK
```

On read and write, the client supplies its access token with the request. The server unwraps the outer layer with the master key, derives the client key from the supplied token, unwraps the inner layer, decrypts the value, and discards the token and derived key from memory. The client token is never persisted server-side in plaintext.

Layering (rather than deriving one key from master + token) is deliberate: master key rotation rewraps only the outer layer and remains a server-side-only operation. Client token rotation requires the client to supply the old token and write a new version.

Properties and accepted trade-offs:

1. The service cannot decrypt a client-bound secret on its own. Theft of the SQLite database plus the master key file is insufficient; the missing key material lives in the consuming applications' configuration.
2. Frontend reveal, CLI plaintext output, and admin export are impossible for client-bound secrets. The UI and CLI show metadata only.
3. There is deliberately no recovery escrow. Loss of either the master key or the per-secret client access token makes the secret permanently unrecoverable. Opting a secret into client-bound mode is an explicit acceptance of this risk, and the frontend/CLI must state it at creation time.
4. This defends against offline theft, not live host compromise: a fully compromised running KMS host can still capture client tokens from request memory.
5. A leaked client token alone is also insufficient (the attacker still needs the ciphertext and master key), and exposes only the secrets bound to that token.

## 11. KEK and Root Key Management Requirements

The service needs a KEK provider for encrypting DEKs.

### 11.1 Provider Interface

The implementation must define a KEK provider interface.

Example:

```go
type KEKProvider interface {
    GenerateDataKey(ctx context.Context, keyID string, context map[string]string) (plaintextDEK []byte, encryptedDEK []byte, err error)
    DecryptDataKey(ctx context.Context, encryptedDEK []byte, keyID string, context map[string]string) ([]byte, error)
}
```

### 11.2 Required Provider: Local KEK

Because the service must run as a single binary with SQLite, v1 must include a local KEK provider.

The local KEK provider may use a master key stored outside SQLite, for example:

1. File-based master key.
2. Environment-provided master key.
3. OS keychain where available.
4. Hardware-backed key source where available.

The local KEK must never be stored inside the SQLite database.

### 11.2.1 Master Key Acquisition at Startup

On startup the service acquires the master key in this order:

1. **Key file** (`encryption.kek_file`): read raw key material from the configured path. This is the unattended mode — restarts need no human.
2. **Stdin prompt**: if the file is absent or unreadable, prompt for a master passphrase on the terminal using a no-echo read (as the existing parameter_store does with `terminal.ReadPassword`). On first initialization the prompt requires confirmation (enter twice); on subsequent unlocks a single entry suffices.

Requirements:

1. A stdin-entered value is a human passphrase, not raw key material. The actual KEK must be derived from it with a memory-hard KDF (argon2id) using a salt stored in the key-metadata table. File-based keys are used as raw key material directly.
2. The service must store a key-check value (e.g. the encryption of a fixed canary under the KEK) so a wrong passphrase or wrong key file fails immediately at startup with a clear error, rather than surfacing later as decryption failures.
3. The two modes must produce interchangeable KEKs from the provider interface's perspective; which mode supplied the key is recorded in key metadata.
4. In stdin mode the service must not report ready (20) until the key has been entered and verified; gRPC/HTTP listeners may start but must answer not-ready.
5. If stdin is not a TTY and no key file exists, the service must fail fast with an actionable error (this prevents a systemd-managed instance from hanging silently on a prompt).
6. The passphrase must never be logged, and must be zeroed from memory after derivation.

### 11.3 Optional Provider: Cloud KMS

The architecture should allow future or optional support for:

1. AWS KMS.
2. Google Cloud KMS.
3. Azure Key Vault.
4. HashiCorp Vault Transit.
5. External HSM/KMS integrations.

### 11.4 KEK Rotation

The service must support KEK rotation.

At minimum:

1. New secrets can be encrypted under the new KEK.
2. Existing encrypted DEKs can be rewrapped under the new KEK without decrypting and re-encrypting every secret value.
3. Historical records retain the KEK identifier required for decryption.
4. For client-bound secrets (10.7), rewrapping applies to the outer layer only and must not require client participation or client tokens.

### 11.5 Master Key Bootstrap

The service must provide an initialization command.

Example:

```bash
parameter-store init --db ./kms.db --master-key-file ./master.key
```

The init command must:

1. Create the SQLite database if absent.
2. Apply migrations.
3. Initialize encryption metadata.
4. Create or validate the local master key.
5. Create an initial admin identity or bootstrap token if configured.

## 12. Storage Requirements

> **Superseded.** The schema below (flat `path TEXT UNIQUE` rows) is replaced
> by the namespace-native schema in
> [`plan-namespaces.md`](plan-namespaces.md) §4 (first-class `namespaces`
> table with foreign keys, `ca_keys`, `identity_certs`, denormalized
> `env`/`app`/`key` in history tables).

### 12.1 SQLite Tables

The service should define tables for:

1. Namespaces.
2. Parameters.
3. Parameter versions.
4. Secrets.
5. Secret versions.
6. Secret labels.
7. Policies.
8. Identities.
9. Access grants.
10. Audit events.
11. Key metadata.
12. Schema migrations.
13. Change log (revisioned parameter/secret-metadata change events for watch replay).

The change log must record the global revision, resource path, change type, and (for parameters) the new value, and must be pruned to a bounded retention window (by age or row count). Subscribers whose `last_seen_revision` predates the retained window receive a full snapshot instead of a replay.

### 12.2 Secret Versions Table

A secret version record should include:

```text
id
secret_id
version_number
ciphertext
encrypted_dek
kek_id
wrap_mode        (standard | client_bound)
client_token_hash (verifier only; never the token itself)
algorithm
nonce
auth_tag
aad
state
created_by
created_at
destroyed_at
expires_at
metadata_json
```

### 12.3 Parameter Versions Table

A parameter version record should include:

```text
id
parameter_id
version_number
value
content_type
state
created_by
created_at
metadata_json
```

### 12.4 Audit Events Table

An audit event record should include:

```text
id
event_type
actor_identity
actor_type
resource_type
resource_path
resource_version
decision
source_ip
user_agent
request_id
created_at
metadata_json
```

Audit records must not contain plaintext secret values.

### 12.5 Migrations

The service must include automatic database migrations.

On startup, the service should:

1. Open SQLite.
2. Acquire a migration lock.
3. Apply pending migrations.
4. Refuse to start if the database schema is newer than the binary supports.

There should also be a manual migration command.

```bash
parameter-store migrate --db ./kms.db
```

## 13. Data Model Requirements

### 13.1 Path Format

> **Superseded.** The flat path-string addressing below is replaced by the
> namespace-native model in [`plan-namespaces.md`](plan-namespaces.md) §2/§5:
> a resource is a `(namespace, key)` pair where the namespace is a fixed
> `(env, app)` entity. The `/env/app/key` form survives only as a *display*
> format and as client-side SDK sugar; the server never parses a path string.

Resources should be addressed by path.

Examples:

```text
/prod/payments/stripe/api-key
/prod/payments/postgres/password
/staging/orders/minio/access-key
```

Paths must:

1. Start with `/`.
2. Not contain empty path segments.
3. Not contain `..`.
4. Have a maximum length.
5. Use a normalized canonical representation.

### 13.2 Secret Metadata

Secrets must support metadata:

```json
{
  "owner": "payments",
  "description": "Stripe API key for production payments service",
  "rotation_period_days": 90,
  "sensitivity": "high"
}
```

### 13.3 Parameter Metadata

Parameters must support metadata:

```json
{
  "owner": "payments",
  "description": "Payments API rate limit",
  "content_type": "integer"
}
```

### 13.4 Value Types

Parameters should support typed values:

1. String.
2. Integer.
3. Float.
4. Boolean.
5. JSON.
6. Binary/base64 if needed.

Secrets should be treated as opaque byte strings with optional content type metadata.

## 14. Versioning Requirements

### 14.1 Immutable Versions

Parameter and secret versions must be immutable.

Updating a value creates a new version.

### 14.2 Labels

The system must support labels:

```text
current
previous
candidate
deprecated
```

`current` must point to the default version returned by clients.

### 14.3 Promotion

The service must support promoting a version to `current`.

Promotion must:

1. Update labels atomically.
2. Move the old `current` to `previous`.
3. Emit an audit event.
4. Notify watchers if configured.

### 14.4 Rollback

The service must support rollback by promoting a previous version.

## 15. Authentication Requirements

The service must authenticate both machine clients and human/admin users.

### 15.1 Machine Authentication

For consuming services, v1 machine authentication uses bearer access tokens, matching the model consuming apps already use with SuhaibParameterStore (`ParameterStoreSecret` per key):

1. A per-client token identifies the application. It establishes the identity used for authorization and the subscriber registry.
2. Per-secret access tokens may additionally be required by policy. For client-bound secrets (10.7), the per-secret token also serves as key material.

Token requirements:

1. Tokens must be high-entropy random values generated by the service.
2. The server stores only token hashes, never plaintext tokens.
3. Tokens must be revocable and rotatable per client and per secret.
4. Tokens travel only over TLS.

mTLS remains a supported hardening option for production (Phase 4). When enabled, the authenticated identity may come from:

1. Client certificate subject.
2. SPIFFE ID.
3. Service mesh identity.
4. Configured identity mapping.

Example identity:

```text
spiffe://internal/prod/payments-api
```

### 15.2 Human Authentication

For frontend users, the service should support at least one administrative authentication mode.

Potential v1 options:

1. Static bootstrap admin token.
2. Local username/password with secure password hashing.
3. Reverse-proxy authentication headers.
4. OIDC integration as a later enhancement.

For production, OIDC or reverse-proxy authentication is preferred over local users.

### 15.3 Authentication Failure

Failed authentication must:

1. Return a generic error.
2. Avoid leaking whether a specific resource exists.
3. Emit an audit event where possible.
4. Be rate-limited for human login surfaces.

## 16. Authorization Requirements

The service must enforce authorization on every operation.

Authorization decisions must consider:

```text
actor identity
resource path
resource type
operation
environment
namespace
version
request context
```

### 16.1 Operations

At minimum, the policy system must distinguish:

```text
parameter:read
parameter:write
parameter:list
parameter:delete

secret:read
secret:write
secret:list
secret:disable
secret:destroy
secret:promote

admin:namespace:create
admin:policy:write
admin:audit:read
admin:key:rotate
```

### 16.2 Path-Based Access

> **Superseded.** Path-prefix policy matching below is replaced by the
> namespace-native rule shape in [`plan-namespaces.md`](plan-namespaces.md) §6:
> rules are `{operation, env, app}` (env/app exact or `*`) over a whole
> namespace, plus an implicit home-namespace read/list grant. Deny
> precedence and least-privilege intent are unchanged.

Policies should support path prefixes.

Example:

```yaml
subject: spiffe://internal/prod/payments-api
allow:
  - operation: secret:read
    path: /prod/payments/*
  - operation: parameter:read
    path: /prod/payments/*
deny:
  - operation: secret:read
    path: /prod/payments/admin/*
```

### 16.3 Deny Precedence

Explicit deny rules must override allow rules.

### 16.4 Least Privilege

Consuming services should generally have read-only access to a narrowly scoped namespace.

Only the Parameter Store service itself should access SQLite and the KEK provider.

Consuming services should not receive direct access to encrypted DEKs, KEK material, or raw encryption metadata unless explicitly exposed for administrative diagnostics.

## 17. Frontend Requirements

The service must serve a full frontend application over HTTP from the Go binary. The frontend is a Next.js static export embedded at build time (see 6.3).

### 17.1 Frontend Capabilities

The frontend must support:

1. Login or authenticated session handling.
2. Dashboard.
3. Namespace browsing.
4. Parameter creation, editing, versioning, and deletion.
5. Secret creation, update, disable, destroy, and version promotion.
6. Secret metadata viewing.
7. Secret value reveal flow for authorized users.
8. Policy management.
9. Identity management where applicable.
10. Audit log browsing and filtering.
11. Key metadata viewing.
12. Health/status page.
13. Backup/export administrative guidance or controls.
14. Connected subscribers view: which applications are live-subscribed, the paths they watch, last heartbeat, and last-applied revision — so an operator can confirm a parameter change has propagated everywhere.

### 17.2 Secret Reveal UX

Secret values must be hidden by default.

Revealing a secret must require an explicit user action.

The UI should:

1. Show metadata by default.
2. Require clicking “Reveal secret” or equivalent.
3. Display a warning.
4. Optionally require re-authentication for highly sensitive values.
5. Emit a specific audit event for every reveal.
6. Auto-hide revealed values after a short period.
7. Prevent accidental copy where practical, while still allowing authorized copy operations.

Client-bound secrets (10.7) have no reveal flow at all: the server cannot decrypt them without the client token, so the UI shows metadata only and explains why. When creating a client-bound secret, the UI must display the permanent-loss warning (loss of master key or client token destroys the secret; no escrow exists) and require explicit confirmation.

### 17.3 Redaction

The frontend must never accidentally render secret values in:

1. Tables.
2. Search results.
3. Error messages.
4. Browser logs.
5. Debug panels.
6. Audit event views.

### 17.4 HTTP Routing

The Go server must serve:

```text
/
  frontend app

/assets/*
  static frontend assets

/api/*
  HTTP API used by frontend if needed

/healthz
  health endpoint

/readyz
  readiness endpoint
```

Frontend routes should support direct navigation.

Example:

```text
/secrets/prod/payments/stripe/api-key
/policies
/audit
```

Because the embedded build is a static Next.js export, resource-addressed routes like these cannot be pre-rendered per path. Unknown routes must fall back to the exported entry HTML and let the Next.js client-side router resolve the page from the URL.

## 18. Security Requirements

### 18.1 Transport Security

The gRPC API must support TLS.

Production deployments should require mTLS for machine clients.

The HTTP frontend must support TLS directly or run behind a TLS-terminating reverse proxy.

### 18.2 Secret Redaction

The service must implement centralized redaction helpers.

All logs, errors, traces, and panic handlers must avoid printing secret values.

### 18.3 Audit Logging

The service must audit:

1. Secret read.
2. Secret reveal in frontend.
3. Secret write.
4. Secret version promotion.
5. Secret disable.
6. Secret destruction.
7. Parameter write.
8. Parameter delete.
9. Policy changes.
10. Namespace changes.
11. Authentication failures.
12. Authorization denials.
13. KEK rotation.
14. Backup and restore operations if performed through the app.

### 18.4 Rate Limiting

The service should rate-limit:

1. Login attempts.
2. Failed authentication attempts.
3. Secret reveal attempts.
4. High-volume secret reads where suspicious.

### 18.5 Backups

Backups must include SQLite data but must not include local master key material unless explicitly requested.

Operators must understand:

```text
SQLite backup without master key:
  cannot decrypt secrets

SQLite backup with master key:
  can decrypt secrets
```

Backup tooling must clearly separate encrypted database backup from key backup.

### 18.6 Disaster Recovery

The service must provide documented procedures for:

1. Database backup.
2. Database restore.
3. Master key backup.
4. Master key restore.
5. Lost master key handling.
6. Database corruption handling.

If the master key is lost, encrypted secrets may be unrecoverable.

For client-bound secrets there is deliberately no recovery escrow: loss of either the master key or the per-secret client token permanently destroys the secret. This is by design and must be documented, not mitigated.

## 19. Observability Requirements

### 19.1 Logs

The service must emit structured logs.

Logs should include:

```text
timestamp
level
request_id
actor identity when available
operation
resource path when safe
status
duration
```

Logs must not include plaintext secrets.

### 19.2 Metrics

The service should expose metrics for:

```text
request count
request latency
error count
authz denials
secret reads
secret writes
parameter reads
parameter writes
database errors
encryption errors
cache hit/miss where applicable
watch connections
```

Metrics must not include secret values.

### 19.3 Tracing

Tracing should be optional.

Traces must not include plaintext secret values.

## 20. Health and Readiness Requirements

The service must expose:

```text
/healthz
/readyz
```

Health should verify the process is alive.

Readiness should verify:

1. SQLite is reachable.
2. Migrations are complete.
3. Encryption provider is available (master key acquired and verified; in stdin mode the service is not ready until the passphrase has been entered).
4. Required server listeners are active.

The gRPC API should also expose a health service.

## 21. CLI Requirements

The binary should provide administrative CLI commands.

Required commands:

```bash
parameter-store serve
parameter-store init
parameter-store migrate
parameter-store backup
parameter-store restore
parameter-store create-admin
parameter-store rotate-kek
parameter-store import
parameter-store check
```

Optional convenience commands:

```bash
parameter-store put-secret /prod/payments/stripe/api-key
parameter-store get-secret /prod/payments/stripe/api-key
parameter-store put-parameter /prod/payments/rate-limit 100
parameter-store list prod/payments
```

CLI secret output should be carefully controlled. By default, secret retrieval should avoid printing to terminal unless explicitly requested.

## 22. Performance Requirements

The service should be optimized for low-latency reads.

### 22.1 Expected Workloads

The service should support:

1. Many read requests.
2. Fewer write/update requests.
3. Occasional admin and audit queries.
4. Client-side caching for high-frequency consumers.

### 22.2 Target Baseline

Initial targets:

```text
p50 GetParameter: < 10 ms locally
p50 GetSecret: < 25 ms locally with local KEK
p95 GetSecret: < 100 ms locally with local KEK
```

Targets may vary depending on TLS, storage medium, and hardware.

### 22.3 SQLite Concurrency

Because SQLite supports many readers but constrained writes, the service should:

1. Use WAL mode.
2. Keep transactions short.
3. Avoid long-running write transactions.
4. Use pagination for list operations.
5. Avoid loading huge audit datasets into memory.

## 23. Availability and Failure Behavior

### 23.1 Startup

On startup, the service must:

1. Load configuration.
2. Initialize logging.
3. Open SQLite.
4. Apply migrations.
5. Acquire the master key: key file first, stdin passphrase prompt as fallback (11.2.1), and verify it against the stored key-check value.
6. Initialize encryption provider.
7. Start gRPC server.
8. Start HTTP server.
9. Report readiness.

### 23.2 Failure to Decrypt

If a secret cannot be decrypted, the service must:

1. Return a generic internal error to unauthorized or normal clients.
2. Emit a secure audit/log event.
3. Avoid revealing whether key metadata, ciphertext, or authorization caused the issue unless the caller is an admin.

### 23.3 Missing Secret

Missing secrets should return a not-found response only when the caller is authorized to know the resource path exists.

Otherwise, the system may return a generic not-found or permission-denied response based on policy.

### 23.4 Database Locked

The service must handle SQLite busy/locked errors gracefully using timeouts and retries where appropriate.

## 24. Backup and Restore Requirements

### 24.1 Database Backup

The service must support safe online SQLite backups.

Backup command:

```bash
parameter-store backup --db /var/lib/parameter-store/kms.db --out backup.db
```

### 24.2 Key Backup

For local KEK mode, the service must document and optionally provide commands for backing up the master key.

The key backup must be handled separately from the SQLite backup.

### 24.3 Restore

Restore must require:

1. SQLite database backup.
2. Correct master key or KEK provider access.
3. Compatible binary/schema version.

Restore command:

```bash
parameter-store restore --db /var/lib/parameter-store/kms.db --in backup.db
```

## 25. Testing Requirements

### 25.1 Unit Tests

Unit tests must cover:

1. Path validation.
2. Access policy evaluation.
3. Encryption and decryption.
4. Version promotion.
5. SQLite repositories.
6. Redaction helpers.
7. SDK behavior.
8. Frontend API handlers.

### 25.2 Integration Tests

Integration tests must cover:

1. Full secret put/get flow.
2. Full parameter put/get flow.
3. Token authentication (and mTLS once implemented).
4. Client-bound secret put/get flow, including wrong-token failure.
5. Authorization denials.
6. Audit event generation.
7. SQLite migrations.
8. Backup and restore.
9. Embedded frontend serving.
10. SDK hydration.
11. Watch subscribe/reconnect/resume: a parameter change reaches a subscribed client; a client that reconnects with an old revision receives the missed changes or a snapshot.
12. Subscriber liveness: dead clients are dropped from the registry after missed heartbeats.
13. Declarative `Init`/`Resolve` resolution order (env override, store, default, error).

### 25.3 Security Tests

Security tests must verify:

1. Secrets are not stored plaintext in SQLite.
2. Secrets are not printed in logs.
3. Secret values are not included in audit records.
4. Unauthorized callers cannot retrieve secrets.
5. Disabled secrets cannot be read.
6. Destroyed secret versions cannot be decrypted.
7. Ciphertext tampering causes decryption failure.
8. Associated data mismatch causes decryption failure.
9. Client-bound secrets cannot be decrypted from the database plus master key alone.
10. A wrong client token on a client-bound secret fails cleanly without leaking whether the token or the ciphertext was at fault.
11. Client tokens are never persisted in plaintext (only hashes) and never logged.

### 25.4 Fuzz Tests

Fuzz tests should cover:

1. Path parser.
2. Policy parser.
3. Metadata parser.
4. Encryption metadata parser.
5. API request validation.

## 26. SDK Acceptance Criteria

Each SDK must provide:

1. Simple initialization.
2. Get parameter.
3. Get secret.
4. Get typed config.
5. Cache configuration.
6. Timeout configuration.
7. TLS/mTLS configuration.
8. Clear error types.
9. Secret redaction in string formatting.
10. Examples and README.
11. Declarative store-backed value types with `Init(client)` and struct-level `Resolve` (9.5).
12. Hot reload for dynamic parameters via `Subscribe`, with `Get()` handles and `OnChange` callbacks (9.6).

Example SDK ergonomics target:

```go
secret, err := client.Secret(ctx, "/prod/payments/stripe/api-key")
```

The user should not need to know about:

```text
encrypted DEKs
nonces
auth tags
KMS context
SQLite storage format
```

## 27. Frontend Acceptance Criteria

The frontend must allow an authorized admin to:

1. Log in.
2. View namespaces.
3. Create a parameter.
4. Update a parameter and see a new version.
5. Create a secret.
6. Update a secret and see a new version.
7. Promote a previous secret version.
8. Reveal a secret value only after explicit action.
9. View audit events.
10. Create or edit access policies.
11. Verify service health.
12. Navigate directly to a frontend route after browser refresh.

The frontend must be a Next.js static export embedded into the Go binary, with no Next.js server runtime required at deploy time.

## 28. Security Acceptance Criteria

A production-ready build must satisfy:

1. Secret plaintext is never stored in SQLite.
2. Secret plaintext is never emitted in logs.
3. Secret plaintext is never emitted in metrics.
4. Secret plaintext is never emitted in audit events.
5. Secret values are encrypted using authenticated encryption.
6. DEKs are encrypted using a KEK.
7. The local KEK is not stored in SQLite.
8. All secret reads are authorized.
9. All secret reads are audited.
10. mTLS is supported for service-to-service gRPC access.
11. Frontend secret reveal is audited.
12. Disabled secret versions cannot be retrieved.
13. Destroyed secret versions cannot be retrieved.
14. Backup and restore procedures are documented.
15. Client-bound secrets are undecryptable by the service alone; database plus master key is insufficient.
16. Client-bound secrets are excluded from frontend reveal, CLI plaintext output, and admin export.
17. Client-bound secret creation requires explicit acknowledgment of permanent-loss semantics.

## 29. Suggested Implementation Phases

### Phase 1: Core Service

Deliver:

1. Go server skeleton.
2. SQLite schema and migrations.
3. Parameter CRUD.
4. Secret CRUD.
5. Local KEK provider.
6. AES-256-GCM envelope encryption.
7. gRPC API.
8. Basic CLI.
9. Basic audit logging.

### Phase 2: Access Control and SDKs

Deliver:

1. Token-based machine authentication (per-client and per-secret tokens).
2. Client-bound secret wrapping (10.7).
3. Path-based authorization.
4. Go SDK.
5. Python SDK.
6. SDK caching.
7. SDK typed hydration.
8. Declarative `SecretValue`/`ParameterValue` types with `Init`/`Resolve`.
9. Policy management APIs.
10. Import tooling and gradethis migration (section 33).

### Phase 2.5: Watch and Hot Reload

Deliver:

1. Global revision counter and change log table.
2. `WatchService.Subscribe` bidirectional stream with snapshot, replay, and heartbeats.
3. Subscription registry with liveness tracking and `ListSubscribers` admin API.
4. Go SDK hot reload: dynamic value handles, `OnChange` callbacks, reconnect/resume, reconciliation poll.
5. Python SDK watch support.

### Phase 3: Frontend

Deliver:

1. Next.js frontend app with static export build.
2. Build pipeline: Next.js export then Go build with embedded assets.
3. Embedded static serving with client-side routing fallback.
4. Namespace browser.
5. Parameter management.
6. Secret management.
7. Secret reveal flow.
8. Audit log viewer.
9. Policy management UI.
10. Connected subscribers view.

### Phase 4: Production Hardening

Deliver:

1. Backup and restore.
2. KEK rotation.
3. mTLS for service-to-service gRPC.
4. Metrics.
5. Tracing.
6. Advanced audit filtering.
7. Rate limiting.
8. Fuzz tests.
9. Security test suite.
10. Documentation.
11. Example deployments.

### Phase 5: Optional Integrations

Potential future work:

1. AWS KMS provider.
2. Google Cloud KMS provider.
3. Azure Key Vault provider.
4. HashiCorp Vault Transit provider.
5. OIDC frontend authentication.
6. SPIFFE/SPIRE identity integration.
7. Kubernetes operator.
8. Automatic third-party secret rotation.
9. Read replicas or replication strategy.
10. High-availability deployment mode.

## 30. Open Design Questions

The following decisions should be made before implementation:

1. Is the service intended for single-tenant internal use or multi-tenant use?
2. Should the local KEK be file-based, environment-based, OS-keychain-based, or pluggable from day one?
3. Should human users authenticate through local accounts, OIDC, or reverse-proxy headers?
4. Should authorization be simple RBAC, path-based ACLs, or a policy language?
5. Should the frontend use the same gRPC API through gRPC-web, or a dedicated HTTP/JSON API?
6. Should secret values be returned directly to authorized frontend users, or should production reveal require break-glass approval?
7. Should SQLite backups be managed by the service or left to operators?
8. What is the target maximum number of secrets, parameters, audit events, and clients?
9. Should the service support binary secrets?
10. Should clients fail closed if Parameter Store is unavailable, or support configured fallback caches?
11. How long should the change log retain events for watch replay (age vs. row count), and what snapshot size makes replay preferable?
12. Should secret rotation ever push new plaintext over an established authorized stream, or always remain notify-then-refetch?
13. Should the subscriber registry persist across server restarts (informational history) or be purely in-memory state rebuilt from live streams?

## 31. Recommended v1 Defaults

Recommended defaults for v1:

```text
Language:
  Go

Storage:
  SQLite with WAL mode

Packaging:
  single binary

Frontend:
  Next.js static export (output: "export") embedded with Go embed
  Next.js source kept separate in frontend/; export runs before go build

Programmatic API:
  gRPC

Frontend API:
  HTTP/JSON backed by the same service layer

Encryption:
  AES-256-GCM

Secret encryption:
  one DEK per secret version

DEK wrapping:
  local KEK provider

Master key:
  stored outside SQLite as a file with strict filesystem permissions
  acquisition: key file first, no-echo stdin passphrase prompt as fallback
  stdin passphrases run through argon2id; wrong key fails fast via key-check value

Authentication:
  bearer access tokens for service clients (per-client, plus per-secret where required)
  mTLS as later production hardening
  bootstrap admin token or reverse-proxy auth for frontend initially

Client-bound secrets:
  opt-in per secret; DEK double-wrapped (client-token-derived key, then KEK)
  no recovery escrow; losing master key or client token loses the secret permanently
  no frontend reveal or CLI plaintext output

Authorization:
  path-based RBAC with deny precedence

Versioning:
  immutable versions with current/previous labels

Audit:
  required for all secret and admin operations

Client SDKs:
  Go first, then Python

Config initialization:
  declarative SecretValue/ParameterValue fields resolved by Init/Resolve
  resolution order: env override, then store, then default, else fail fast

Hot reload:
  client-initiated gRPC Subscribe stream (stream = liveness, no webhooks)
  global revision counter with bounded change log for resume/replay
  parameters push values on change; secrets push metadata only (notify-then-refetch)
  heartbeat 30s, 3 missed = dead, jittered reconnect, 5m reconciliation poll
```

## 32. Core Principle

Consuming services should store only references to secrets.

Example consuming service config:

```yaml
parameter_store:
  endpoint: "parameter-store.prod.internal:8443"

secrets:
  database_password: "/prod/payments/postgres/password"
  stripe_api_key: "/prod/payments/stripe/api-key"
  minio_access_key: "/prod/payments/minio/access-key"
  minio_secret_key: "/prod/payments/minio/secret-key"
```

The consuming service must not store:

```text
ciphertext secrets
encrypted DEKs
KMS metadata
nonces
auth tags
KEK identifiers
raw encryption context
```

Those details belong inside the Parameter Store service.

The service boundary should be:

```text
Client SDK:
  resolves references and talks to Parameter Store

Parameter Store:
  authenticates, authorizes, decrypts, audits

SQLite:
  stores ciphertext and metadata

KEK provider:
  protects DEKs

Consuming service:
  receives only the plaintext values it is authorized to use
```

## 33. Migration from SuhaibParameterStore

This service exists to replace SuhaibParameterStore. The migration is the primary acceptance test: the project is complete when gradethis runs against this service with no dependency on `SuhaibParameterStoreClient`.

### 33.1 SDK Migration Posture

The Go SDK is not required to be API-compatible with `SuhaibParameterStoreClient` or its `ParameterStoreConfig` type. Where the old client's design is lacking, the new SDK must not inherit it. The concepts do map (store key to path, per-key access secret to token, env fallback, dev default), so migrating a consuming app is a mechanical rewrite of its config fields, but the new API's shape — declarative fields with `Init`/`Resolve` (9.5), redaction by default, concurrent per-field resolution, hot reload — is designed on its own merits.

The migration guide must include a before/after example for a typical gradethis config field so the rewrite is unambiguous.

### 33.2 Import Tooling

The binary must provide an import command that migrates existing SuhaibParameterStore data:

```bash
parameter-store import --from <export-file-or-sqlite-db> --namespace prod/gradethis
```

The importer must:

1. Map flat keys (e.g. `gradethis_TWILIO_ACCOUNT_SID`) to slugged namespaced paths without stripping the source prefix.
2. Re-issue per-secret access tokens, emitting a mapping report so app configs can be updated.
3. Encrypt all imported secrets under the new envelope scheme.
4. Support a dry-run mode that reports the resulting paths without writing or minting tokens.

### 33.3 Cutover

1. Stand up the new service and import data.
2. Dual-run window: gradethis dev/staging move to the new SDK first; the old store stays up as fallback.
3. Migrate gradethis production config to the new SDK.
4. Decommission SuhaibParameterStore.

### 33.4 Acceptance

1. gradethis builds and boots with only the new SDK.
2. All existing secrets resolve to identical values post-import (verified by a comparison tool during dual-run).
3. Env-var overrides and dev defaults behave exactly as before.
