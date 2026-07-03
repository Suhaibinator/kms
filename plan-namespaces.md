# Namespace-Native Rewrite — Implementation Plan

Status: **approved design, not yet implemented.**
Supersedes the path-string data model in [`plan.md`](plan.md) (§13.1 path
format, §12 storage, §8 API shapes). Everything else in `plan.md` (crypto,
versioning, labels, redaction, audit guarantees, deployment) carries over
unchanged.

## 1. Motivation

Today a namespace is a naming convention inside a flat path string
(`/prod/gradethis/rate-limit`). The `namespaces` table exists but nothing
references it; parameters and secrets are `path TEXT UNIQUE` rows, policies
match path globs, the SDK repeats the full path in every config field, and
namespace membership everywhere is `strings.HasPrefix`. Environment and
application are not queryable concepts anywhere in the system.

This rewrite makes the namespace — a fixed **(env, app)** pair — a
first-class entity end to end: in the schema (foreign keys, not prefixes),
on the wire (explicit fields, no server-side path parsing), in authorization
(identities belong to a namespace), in the frontend (real env/app
management), and in the SDKs (a namespaced client resolving relative keys,
with hot reload on by default).

## 2. Locked decisions

These were settled explicitly and are not up for re-litigation during
implementation:

1. **Namespace model:** fixed `(env, app)` pair, `UNIQUE(env, app)`. Keys
   within a namespace are relative names; a key may contain `/` (e.g.
   `billing/stripe-key`) but interior slashes are just part of the name,
   never namespace structure.
2. **Wire protocol:** requests carry explicit namespace + key fields. The
   `/env/app/key` form survives only as a *display* format (logs, audit
   rendering, frontend breadcrumbs) and as *client-side* SDK sugar. The
   server never parses a path string.
3. **Greenfield:** rewrite `0001_initial.sql` in place. No data migration,
   no legacy read path, no shims. (No deployed instance exists; gradethis
   has not migrated yet.)
4. **Identity ⇄ namespace binding:** `identities.namespace_id` (nullable).
   A client token can belong to an app; the SDK discovers its namespace
   from the token at connect time, so application config needs only
   endpoint + token.
5. **SDK ergonomics:** namespaced client (`Config.Namespace` /
   `Client(..., namespace=...)`), relative keys, leading-`/` absolute-path
   escape hatch parsed **in the SDK**, hot reload **on by default** for
   parameters (`Static: true` to opt out), secrets remain
   notify-and-refetch (plaintext is never pushed over a stream).
6. **Proof of app identity: mTLS client certificates from a built-in CA.**
   Bearer tokens are possession-free ("whoever holds the string is the
   app"); machine clients instead prove identity with a client certificate
   minted by a CA embedded in the KMS itself. See §7.
7. **Allowed auth methods are registered per namespace.** Each namespace
   declares which authentication methods (`mtls`, `token`) admit a client
   into it — enforced on every operation in the namespace, including reads
   of unprotected parameters. A namespace can require mTLS-only, making
   token theft useless for that namespace.

## 3. Decisions made by this plan

Defaults chosen to complete the design; flag during review if any is wrong:

- **Implicit home-namespace grant.** An identity bound to a namespace may
  `parameter:read/list`, `secret:read/list`, and subscribe within that
  namespace with **no policy required** — the token *is* the app. Writes,
  disables, destroys, promotes, and any cross-namespace access still
  require explicit policy. This is what makes "endpoint + credential and
  you're done" real.
- **Namespace deletion requires an empty namespace** (no parameters, no
  secrets, no bound identities). A secrets store should not support
  recursive delete of live secrets in one call. New `DeleteNamespace` RPC +
  endpoint (does not exist today).
- **`WhoAmI` RPC** on `AdminService`, callable by any authenticated
  identity (no policy check): returns identity name, kind, and bound
  namespace if any. This is the SDK's namespace-discovery mechanism and
  replaces nothing (it's new).
- **Naming rules:** `env` and `app` are 1–64 chars of `[a-z0-9-]`, not
  starting/ending with `-`. Keys are 1–256 chars of `[a-z0-9-_.]` segments
  separated by single `/`, no leading/trailing/double slashes. Enforced in
  one place (`internal/keyutil`, the successor of `internal/pathutil`).
- **Audit and change-log rows denormalize `env`/`app`/`key` as text** (no
  FK). Both tables are append-only history and must stay readable after a
  namespace is deleted; joining through a FK would break that.
- **Proto stays `kms.v1`.** Greenfield means no deployed consumer to
  version against; bumping to v2 would imply a compatibility story we are
  deliberately not building.
- **SDK namespace config is a single string** `"prod/gradethis"` (parsed
  client-side into env/app), not two fields — it matches the "base
  resolver names the base application" mental model and keeps config to
  one value. Go: `Config.Namespace`; Python: `Client(..., namespace=)`.
- **New namespaces default to `allowed_auth_methods = ["mtls"]`** —
  strongest posture by default; adding `"token"` is an explicit, audited
  namespace-settings change.
- **The namespace auth-method gate applies to client-kind identities.**
  Admin-kind identities (human, token-based frontend login) are the
  management plane: they can administer any namespace from a browser
  (which cannot practically do client-cert auth), with the same policy and
  audit controls as today, including the audited secret-reveal flow.
- **Client cert keys are generated server-side and returned exactly once**
  (PEM bundle), like tokens today. A CSR flow (client keeps its own key)
  is out of scope for this pass.
- **Certificate lifetime defaults to 90 days**, settable per issuance.
  Renewal is reissuance (`IssueIdentityCertificate`); an identity may have
  several concurrent valid certs to allow overlap-rollover. Revocation is
  per-serial (plus identity disable, which kills all its certs).

## 4. Target schema (`internal/storage/migrations/0001_initial.sql`)

Unchanged tables: `key_metadata`. Everything else changes as follows
(abbreviated — timestamps/metadata columns as today):

```sql
CREATE TABLE namespaces (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    env                  TEXT NOT NULL,
    app                  TEXT NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    allowed_auth_methods TEXT NOT NULL DEFAULT '["mtls"]',  -- JSON array: "mtls" | "token"
    created_by           TEXT NOT NULL DEFAULT '',
    created_at           TEXT NOT NULL,
    UNIQUE (env, app)
);

CREATE TABLE parameters (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace_id INTEGER NOT NULL REFERENCES namespaces(id),
    name         TEXT NOT NULL,        -- relative key, e.g. 'rate-limit'
    ...                                 -- content_type, metadata, timestamps as today
    UNIQUE (namespace_id, name)
);
-- parameter_versions / parameter_labels unchanged (FK to parameters.id).

CREATE TABLE secrets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace_id INTEGER NOT NULL REFERENCES namespaces(id),
    name         TEXT NOT NULL,
    ...                                 -- client_bound, access_token_hash, etc. as today
    UNIQUE (namespace_id, name)
);
-- secret_versions / secret_labels unchanged (FK to secrets.id).

CREATE TABLE identities (
    ...,                                -- as today, except:
    token_hash   BLOB,                  -- now NULLable: cert-only identities have none
    namespace_id INTEGER REFERENCES namespaces(id)  -- NULL = unbound (admin/tooling)
);

-- Built-in CA (one active row; key material encrypted under the KEK, same
-- envelope discipline as secret versions — never plaintext at rest):
CREATE TABLE ca_keys (
    id            TEXT PRIMARY KEY,     -- e.g. "ca-7f3a"
    cert_pem      TEXT NOT NULL,        -- public; served at GET /api/v1/ca
    encrypted_key BLOB NOT NULL,
    encrypted_dek BLOB NOT NULL,
    kek_id        TEXT NOT NULL REFERENCES key_metadata(id),
    state         TEXT NOT NULL DEFAULT 'active',  -- active | retired
    created_at    TEXT NOT NULL
);

-- Issued client certificates (never private keys — those are returned once):
CREATE TABLE identity_certs (
    serial      TEXT PRIMARY KEY,
    identity_id INTEGER NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,          -- sha256 of DER, for display/pinning
    not_after   TEXT NOT NULL,
    revoked_at  TEXT,                   -- NULL = valid
    created_at  TEXT NOT NULL
);
CREATE INDEX idx_identity_certs_identity ON identity_certs(identity_id);

-- policies.rules_json rule shape changes (see §6); table shape unchanged.

CREATE TABLE audit_events (
    ...,                                -- resource_path replaced by:
    resource_env TEXT NOT NULL DEFAULT '',
    resource_app TEXT NOT NULL DEFAULT '',
    resource_key TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_audit_ns ON audit_events(resource_env, resource_app);

CREATE TABLE change_log (
    revision INTEGER PRIMARY KEY AUTOINCREMENT,
    ...,                                -- path replaced by:
    env  TEXT NOT NULL,
    app  TEXT NOT NULL,
    key  TEXT NOT NULL
);
CREATE INDEX idx_change_log_ns ON change_log(env, app);
```

**Referential rules:** `parameters`/`secrets` FK is plain `REFERENCES` (no
cascade) — `DeleteNamespace` verifies emptiness in the same transaction.
Deleting a *parameter/secret* cascades to versions/labels as today. AAD for
secret envelope encryption (plan.md §10.5) binds `env`, `app`, `key`, and
version — the canonical AAD string changes from path-based to
`env=<env>;app=<app>;key=<key>;version=<n>` (crypto layer touch, one
function).

## 5. Domain model (`internal/domain`)

New value types used everywhere a `Path string` appears today:

```go
type NamespaceRef struct{ Env, App string }        // "" fields invalid
type Ref struct { NS NamespaceRef; Key string }     // one parameter/secret

func (n NamespaceRef) String() string  // "prod/gradethis" (display)
func (r Ref) String() string           // "/prod/gradethis/rate-limit" (display)
```

- `Parameter`, `ParameterInfo`, `Secret`, `SecretValue`, `ChangeLogEntry`
  replace `Path string` with `Ref`.
- `Namespace` becomes `{ID int64; NamespaceRef; Description;
  AllowedAuthMethods []AuthMethod; CreatedBy; CreatedAt}`.
- New: `AuthMethod` (`AuthMethodMTLS = "mtls"`, `AuthMethodToken =
  "token"`), `IdentityCert{Serial, Fingerprint, NotAfter, RevokedAt}`.
- `Identity` gains `Namespace *NamespaceRef` and `HasToken bool`;
  `Certs []IdentityCert` on the admin-facing view.
- `AuditEvent` replaces `ResourcePath` with `ResourceEnv/App/Key`;
  `AuditFilter.PathPrefix` becomes `Env/App/KeyPrefix` fields.
- `PolicyRule` becomes `{Operation, Env, App, KeyPattern}` (see §6).
- `Subscriber.Paths` becomes `Selectors []WatchSelector` where
  `WatchSelector{NS NamespaceRef; KeyPattern string}`.
- New: `domain.OpAdminNamespaceDelete`.

`internal/pathutil` is replaced by `internal/keyutil`: `ValidateEnv`,
`ValidateApp`, `ValidateKey`, `MatchKey(pattern, key)` (exact or trailing
`*` prefix), display formatting, and the SDK-facing absolute-path split
(`SplitDisplayPath("/env/app/key") (Ref, error)` — used by SDKs and CLI
only, never by the server request path).

## 6. Authorization (`internal/policy`, `internal/core`)

Rule shape (stored in `policies.rules_json`, exposed via proto/HTTP):

```json
{ "operation": "secret:read", "env": "prod", "app": "gradethis", "key": "billing/*" }
```

- `env`/`app`: exact or `"*"`. `key`: exact, `"*"`, or `"prefix/*"`.
- Evaluation order unchanged: deny → allow → default deny.
- `Evaluate(policies, op, ref)` and `MayListUnder(policies, op, ns,
  keyPrefix)` take refs instead of strings.
- **New pre-step in `core`:** if the caller's identity has a bound
  namespace and `ref.NS` equals it and the operation is in the implicit
  set (`parameter:read|list`, `secret:read|list`, subscribe), allow
  without consulting policies. Deny rules still apply (deny > implicit
  grant), so an admin can carve exceptions.
- Admin operations gain `admin:namespace:delete`, `admin:namespace:update`
  (auth-method changes), and `admin:identity:cert` (issue/revoke).

## 7. Authentication — proof of app identity (built-in CA + per-namespace methods)

Bearer tokens scope access but don't prove possession: anyone holding the
string is the app. Machine clients therefore authenticate with **mTLS
client certificates minted by a CA embedded in the KMS**, and each
namespace registers which auth methods admit a caller at all.

**Built-in CA (`internal/ca`, new package):**

- Generated at first unseal (Ed25519 or ECDSA P-256), private key
  encrypted under the active KEK exactly like a secret version; plaintext
  key exists only in memory while issuing. KEK rotation rewraps it along
  with secret DEKs (`RotateKEK` gains the `ca_keys` row).
- Signs **client certificates only**. The server's own TLS certificate
  remains operator-provided, as today.
- The gRPC/HTTP listeners add the built-in CA to their client-CA pool
  (alongside any operator-supplied client CA) with
  `tls.VerifyClientCertIfGiven` — token-only clients still connect.
- CA certificate is public: `GET /api/v1/ca` (no auth) and
  `parameter-store admin ca show`, for baking into app deploy images.

**Certificate contents & mapping:** CN and URI SAN `kms://identity/<name>`
name the identity; the namespace binding stays in the database (so
re-binding an identity doesn't require reissuing certs). The auth
interceptor maps a verified peer cert → SAN → identity, then checks the
serial against `identity_certs` (not revoked, not expired, identity not
disabled). Result: an authenticated context carrying `{identity, method:
mtls|token}`.

**Issuance / rotation / revocation (AdminService + CLI + frontend):**

- `CreateIdentity` gains `auth_methods` — a client identity may be minted
  with a cert bundle (key returned once, never stored), a token, or both.
- New `IssueIdentityCertificate(name, ttl)` → PEM bundle, once. Multiple
  concurrent valid certs per identity allow zero-downtime rollover.
- New `RevokeIdentityCertificate(name, serial)`; `RevokeIdentity` (and
  `disabled`) implicitly invalidates all of an identity's certs.
  Revocation is a DB check in the interceptor (serials cached in memory,
  invalidated on change) — no CRL/OCSP machinery.

**Per-namespace gate (`internal/core`, before any authz):** every
namespaced operation — parameter reads included — first checks
`caller.method ∈ namespace.AllowedAuthMethods`. Failure is
`PermissionDenied` with an explicit "namespace requires mtls" message and
an audit event. The implicit home-namespace grant (§6) sits *behind* this
gate: a namespace-bound identity using a disallowed method still gets
nothing. Admin-kind identities bypass the method gate (management plane;
see §3) but not policy or audit.

**SDK surface:** already 90% present — `MTLSFromFiles`/`TLSConfig` exist
in both SDKs. Changes: `Token` becomes optional when a client cert is
supplied (identity derives from the cert); `WhoAmI` discovery works
identically under either method. Docs steer apps to cert-only as the
default posture.

## 8. Wire protocol (`proto/kms/v1/kms.proto`)

Breaking rewrite, same package. Shared messages:

```proto
message NamespaceRef { string env = 1; string app = 2; }
message ResourceRef  { NamespaceRef namespace = 1; string key = 2; }
message WatchSelector { NamespaceRef namespace = 1; string key_pattern = 2; } // "" = "*"
```

Per-service changes (mechanical unless noted):

- **ParameterService / SecretService:** every `string path` field becomes
  `ResourceRef ref`. `List*Request.path_prefix` becomes `NamespaceRef
  namespace` + `string key_prefix` (listing is always namespace-scoped;
  cross-namespace overviews come from `ListNamespaces` + per-namespace
  counts, see §10).
- **WatchService:** `SubscribeRequest.paths` becomes `repeated
  WatchSelector selectors`. `ParameterChange`/`SecretMetadataChange`/
  `Parameter` carry `ResourceRef`. `WatchNamespaceRequest` takes
  `NamespaceRef` + optional `key_pattern`; `WatchParameterRequest` takes
  `ResourceRef`.
- **AdminService:**
  - `Namespace{env, app, description, allowed_auth_methods, ...}`;
    `CreateNamespace` takes env/app (+ optional auth methods); new
    `UpdateNamespace` (description, auth methods) and
    `DeleteNamespace(NamespaceRef)`; `ListNamespaces` response items gain
    `parameter_count` and `secret_count` (cheap COUNT joins, powers the
    dashboard).
  - `CreateIdentityRequest` gains `NamespaceRef namespace` (optional) and
    `repeated string auth_methods`; response returns token and/or a
    one-time PEM cert bundle. `Identity` message gains namespace,
    `has_token`, and issued-cert summaries.
  - New `IssueIdentityCertificate(name, ttl)` / 
    `RevokeIdentityCertificate(name, serial)` (§7).
  - New `WhoAmI() returns (identity name, kind, NamespaceRef namespace,
    string auth_method)`.
  - `PolicyRule{operation, env, app, key}`.
  - `ListAuditEventsRequest`: `path_prefix` → `env`/`app`/`key_prefix`.
  - `Subscriber.paths` → `repeated WatchSelector selectors`.

Regenerate `gen/kmsv1` (protoc per existing setup) and
`sdk/python/kms_paramstore/_gen` (`sdk/python/gen.sh`).

## 9. Watch hub (`internal/watch`) and change log

- Hub interest matching keys on `(NamespaceRef, key pattern)` via
  `keyutil.MatchKey` — replaces path-prefix matching.
- `storage.SnapshotParameters(ctx, selectors []WatchSelector)` — snapshot
  query becomes `WHERE namespace_id = ? AND (name = ? | name LIKE ?)` per
  selector, one consistent read transaction, as today.
- `ListChangesSince` unchanged in shape; entries carry env/app/key.
- Replay/revision semantics (monotonic `change_log.revision`, prune rules,
  at-least-once delivery) are untouched.

## 10. HTTP API + frontend

`docs/http-api.md` endpoints change mechanically with the proto: every
`path=` query param becomes `env=&app=&key=`; `prefix=` becomes
`env=&app=&key_prefix=`; namespace bodies become `{"env","app",
"description","allowed_auth_methods"}`; policy rule JSON as §6. New:
`PATCH /api/v1/namespaces` (description/auth methods), `DELETE
/api/v1/namespaces?env=&app=`, `GET /api/v1/whoami`, `GET /api/v1/ca`
(public CA cert), `POST /api/v1/identities/issue-cert`, `POST
/api/v1/identities/revoke-cert`. Namespace list items include
`parameter_count`/`secret_count`.

Frontend (`frontend/`):

- `lib/types.ts` / `lib/api.ts`: regenerate shapes; add
  `NamespaceRef`-aware helpers; display-path formatting in one util.
- **Namespaces page** becomes the management hub: table grouped by env,
  create (env, app, description, allowed auth methods — default mTLS-only),
  edit auth methods, delete (disabled unless empty, with explanation),
  per-row parameter/secret counts linking into the pages below.
- **Parameters / Secrets pages:** replace free-text prefix filter with an
  env → app cascading selector (from `ListNamespaces`) + key-prefix box;
  rows show relative keys; create/edit forms take namespace + key.
- **Identities page:** optional namespace binding + auth methods on
  create; one-time display of token and/or PEM bundle (download button);
  per-identity cert list (fingerprint, expiry, revoke button, issue-new);
  bound namespace as a chip; explain the implicit grant in help text.
- **Policies page:** rule editor gets env/app/key fields with `*` support.
- **Subscribers page:** render selectors as `env/app · pattern`; group by
  namespace.
- **Audit page:** env/app dropdown filters + key prefix.

## 11. SDKs

### Go (`sdk/go/paramstore`)

```go
client, err := paramstore.NewClient(paramstore.Config{
    Endpoint: os.Getenv("PARAM_STORE_ENDPOINT"),
    // Preferred: cert-only identity (proof of possession, §7).
    TLS: paramstore.MTLSFromFiles("app.crt", "app.key", "ca.crt"),
    // Token is optional when a client cert is supplied; required only for
    // token-method identities (and only admitted where the namespace's
    // allowed_auth_methods includes "token").
    // Namespace optional: "prod/gradethis". Empty => discovered via WhoAmI
    // when the identity is namespace-bound; error at first use otherwise.
})

cfg := Config{
    StripeAPIKey: paramstore.SecretValue{Key: "stripe-api-key"},
    RateLimit:    paramstore.ParameterValue{Key: "rate-limit"},          // hot-reloads
    LogFormat:    paramstore.ParameterValue{Key: "log-format", Static: true},
}
err = client.Resolve(ctx, &cfg)
```

- Key resolution (SDK-side only): relative key → client namespace;
  leading `/` → `keyutil.SplitDisplayPath` → explicit `ResourceRef` for
  cross-namespace reads. A relative key on a client with no namespace
  (unbound token, no `Config.Namespace`) is a config error naming the key.
- `Dynamic bool` field is **removed**, replaced by `Static bool`
  (zero-value = hot reload on). All non-static `ParameterValue`s resolve
  through one namespace-wide selector (`{ns, "*"}`) on the shared
  Subscribe stream instead of per-path registrations; `Client.Watch(ctx,
  "billing/*", fn)` takes a relative pattern (or absolute with `/`).
- Namespace discovery: on first namespace-needing call, if
  `Config.Namespace` is empty, call `WhoAmI` once (cached for the client's
  lifetime); surface `ErrNoNamespace` if the identity is unbound.
- Reconnect/backoff/reconciliation/redaction semantics all carry over;
  reconciliation lists by `(ns, key_prefix)` instead of path prefix.
- `paramstoretest`: `SetParameter(ns, key, value)` etc.; keep a
  `SetParameterPath("/env/app/key", ...)` convenience that splits
  client-side, to keep test call-sites terse.

### Python (`sdk/python/kms_paramstore`)

Mirror of the Go changes: `Client(endpoint, token=..., namespace="prod/gradethis"
| None)`, relative keys in `SecretValue`/`ParameterValue`, `static=True`
replaces `dynamic=True` (default flips to hot-reload-on),
`client.watch("billing/*", cb)`, WhoAmI discovery, `_gen` regenerated via
`sdk/python/gen.sh`. The stricter `fallback_to_defaults_on_error=False`
default behavior is kept as-is.

**Hot-reload default flip risk (both SDKs):** values now change at runtime
by default. This is the intended semantic ("updates propagate to all
subscribed applications"), and `ParameterValue.Get()` was already the
documented read pattern; `Static: true` is the pin for boot-time-only
reads. Docs must state the flip prominently.

## 12. CLI (`internal/cli`)

- `import`: gains `--env`/`--app` (or reads them from the mapping file);
  writes through the new storage API. The import mapping report renders
  display paths.
- `admin` subcommands: namespace create/update/delete/list with env/app
  args (`--auth-methods mtls,token`); identity create gains `--namespace
  env/app`, `--auth mtls|token|both`, `--ttl`, `--out <dir>` (writes the
  one-time PEM bundle); new `identity issue-cert` / `identity revoke-cert`
  and `ca show`.
- `serve`, `unseal`, `backup`, `restore`: untouched except compile-level
  ripples.

## 13. Phases

Work happens on one feature branch (`rewrite/namespaces`), one commit per
phase; the branch merges only when everything is green. `go test ./...`
cannot be green between phases 1–2 (the module includes the SDK, which
compiles against the old proto until phase 3) — per-phase gates below are
the honest checkpoints. No backward-compatibility shims at any point.

**Phase 1 — data model.** `internal/domain` (Ref types, AuthMethod, rule
shape, op constants), `internal/keyutil` (new; delete `internal/pathutil`),
`0001_initial.sql` rewrite (incl. `ca_keys`, `identity_certs`,
`allowed_auth_methods`), `internal/storage` (models, store, parameters,
secrets, namespaces incl. Update/Delete + counts, identities + certs,
policies, audit, changelog, api.go contract).
*Gate:* `go test ./internal/domain/... ./internal/keyutil/... ./internal/storage/... -race` green.

**Phase 2 — service + wire.** `proto/kms/v1/kms.proto` rewrite + regen
`gen/kmsv1`, `internal/ca` (new: CA bootstrap at unseal, issuance,
KEK-rewrap hook), `internal/policy` (rule evaluation on refs +
implicit-grant helper), `internal/core` (all services, WhoAmI,
Update/DeleteNamespace, namespace auth-method gate, implicit grant,
cert issue/revoke, AAD change in `internal/crypto` call-site),
`internal/watch`, `internal/server/grpcserver` (client-cert auth in the
interceptor + `VerifyClientCertIfGiven`), `internal/server/httpserver`,
`internal/cli`.
*Gate:* `go build ./cmd/... && go test ./internal/... -race` green; manual
smoke: serve + create namespace + put/get parameter via HTTP, and a
cert-authenticated gRPC read against an mTLS-only namespace.

**Phase 3 — Go SDK.** `sdk/go/paramstore` (+ `paramstoretest`): Config
(cert-only identity, optional Token), key resolution, Static flip,
namespace-wide subscribe selector, WhoAmI discovery, watch patterns,
reconciliation, README.
*Gate:* `go test ./... -race` green (entire module), `go vet ./...`.

**Phase 4 — Python SDK.** `sdk/python/gen.sh` regen, `kms_paramstore/*`
mirror changes, tests.
*Gate:* `pytest sdk/python` green (currently 40 tests; will grow).

**Phase 5 — frontend.** `lib/`, all pages per §10.
*Gate:* `make frontend && make backend && make check-frontend`; manual
pass over every page against a live server.

**Phase 6 — docs + acceptance.** Update `docs/http-api.md`,
`docs/sdk-go.md`, `docs/sdk-python.md`, `docs/migration.md` (concept
mapping now targets namespace+key), `docs/security.md` (AAD string,
implicit grant, built-in CA, per-namespace auth methods),
`docs/operations.md` (CA lifecycle, cert rollover runbook), both SDK
READMEs, top-level
`README.md`; annotate superseded `plan.md` sections (§8, §12, §13.1, §16.2)
with pointers here.
*Gate — end-to-end acceptance:* against a live `parameter-store`:
1. create namespace `prod/gradethis` (default mTLS-only), identity bound
   to it with a cert bundle;
2. app boots with endpoint + cert only (no token, no namespace config),
   resolves a secret and a parameter by relative key via WhoAmI discovery
   and the implicit grant;
3. the same identity presenting only a bearer token is **denied** — even
   for a plain parameter read; after adding `"token"` to the namespace's
   allowed methods via the frontend, it succeeds;
4. cert revocation takes effect on the next RPC; reissue restores access;
5. `put-parameter` from the CLI propagates to a running subscribed app
   without restart (default hot reload);
6. cross-namespace read denied without policy, allowed with one;
7. namespace delete blocked while non-empty, succeeds when emptied;
8. frontend: namespaces/parameters/secrets/identities/policies/
   subscribers/audit pages all reflect the above.

## 14. Explicitly out of scope

- Data migration from any existing instance (greenfield by decision).
- Hierarchical namespaces deeper than (env, app).
- Batch-read RPC (SDK `Resolve` stays concurrent-per-field).
- Pushing secret plaintext over watch streams (unchanged invariant).
- Server-side acceptance of `/env/app/key` strings anywhere.
- CSR-based cert issuance (client-held keys) — server-side generation
  only for this pass.
- CRL/OCSP distribution — revocation is enforced in the KMS's own
  interceptor, which is the only relying party.
