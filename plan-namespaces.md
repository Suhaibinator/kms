# Namespace-Native Rewrite — Implementation Plan

Status: **the (env, app) namespace model, built-in CA, and per-namespace
auth methods are implemented on branch `rewrite/namespaces`.** This revision
of the plan removes the per-*key* concepts that an earlier revision
introduced: there are no key patterns in policy, no watch selectors, and no
prefix matching anywhere. **The namespace `(env, app)` is the single unit of
authorization, subscription, and isolation; keys are opaque strings the
server never interprets.** The sections below describe that corrected target;
the remaining work brings the watch subsystem and the policy model in line
with it (see §15).

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

**The namespace is the only hierarchy the server understands.** Within a
namespace, keys are a flat set of opaque strings. A key may contain `/`
(`db/port`, `metrics/port`) — but that is purely a *client-side* naming
convention to avoid clashes; the server never parses, splits, or matches on
it. Authorization, subscription, and isolation are all at the granularity of
the namespace: if you need two things isolated from each other, they belong
in two namespaces, not two key prefixes.

## 2. Locked decisions

These were settled explicitly and are not up for re-litigation during
implementation:

1. **Namespace model:** fixed `(env, app)` pair, `UNIQUE(env, app)`. Keys
   within a namespace are a flat set of **opaque strings**. A key may
   contain `/` (e.g. `db/port`) but that is a client naming convention only —
   the server never parses, splits, prefix-matches, or otherwise interprets
   it. `db/port` and `metrics/port` are simply two distinct keys.
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
   escape hatch parsed **in the SDK** (for addressing a key in *another*
   namespace the client is authorized for), hot reload **on by default** for
   parameters (`Static: true` to opt out), secrets remain
   notify-and-refetch (plaintext is never pushed over a stream).
8. **Authorization, subscription, and isolation are per-namespace.** A
   client is authorized for a namespace or it isn't; if it is, it may read,
   list, and watch **every** key in that namespace — nothing finer. There is
   no per-key authorization and no key-level watch filtering. A subscriber to
   `(env, app)` receives every change in that namespace. Any narrower
   interest (e.g. "only wake me for `db/*`") is the client's own concern,
   applied in its callback; the server and wire know nothing about it.
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
  `parameter:read/list`, `secret:read/list`, and subscribe to its **own**
  namespace with **no policy required** — the token *is* the app. Writes,
  disables, destroys, promotes, and any access to *another* namespace still
  require an explicit policy grant (which is itself namespace-level — see
  §6). This is what makes "endpoint + credential and you're done" real.
- **Namespace deletion requires an empty namespace** (no parameters, no
  secrets, no bound identities). A secrets store should not support
  recursive delete of live secrets in one call. New `DeleteNamespace` RPC +
  endpoint (does not exist today).
- **`WhoAmI` RPC** on `AdminService`, callable by any authenticated
  identity (no policy check): returns identity name, kind, and bound
  namespace if any. This is the SDK's namespace-discovery mechanism and
  replaces nothing (it's new).
- **Naming rules:** `env` and `app` are 1–64 chars of `[a-z0-9-]`, not
  starting/ending with `-`. A key is 1–256 chars of `[a-z0-9-_./]` with no
  leading/trailing/double slash — but the slash is validated only as a legal
  character, **not** as structure; the server stores and compares the whole
  key verbatim. Enforced in one place (`internal/keyutil`, the successor of
  `internal/pathutil`).
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
  `AuditFilter.PathPrefix` becomes `Env/App/KeyPrefix` fields (the key
  prefix is a browsing filter only — an opaque `LIKE 'prefix%'`, see §6).
- `PolicyRule` becomes `{Operation, Env, App}` — **no key field**. A grant
  is an operation on a whole namespace (see §6).
- `Subscriber.NS []NamespaceRef` — the namespaces a stream is subscribed to
  (replaces the old `Paths`/selectors).
- New: `domain.OpAdminNamespaceDelete`.

`internal/pathutil` is replaced by `internal/keyutil`: `ValidateEnv`,
`ValidateApp`, `ValidateKey`, display formatting, and the SDK-facing
absolute-path split (`SplitDisplayPath("/env/app/key") (Ref, error)` — used
by SDKs and CLI only, never by the server request path). There is **no**
`MatchKey`/key-pattern matcher: the server never pattern-matches keys.

## 6. Authorization (`internal/policy`, `internal/core`)

**Authorization is per-namespace.** A grant is an operation on a whole
namespace; there is no key-level scoping. Rule shape (stored in
`policies.rules_json`, exposed via proto/HTTP):

```json
{ "operation": "secret:read", "env": "prod", "app": "gradethis" }
```

- `env`/`app`: exact or `"*"`. There is **no** `key` field.
- Evaluation order: deny → allow → default deny. `Evaluate(policies, op,
  ns)` takes a `NamespaceRef`, not a ref+key.
- **Implicit home-namespace grant (pre-step in `core`):** if the caller's
  identity has a bound namespace, that namespace equals the target
  namespace, and the operation is in the implicit set
  (`parameter:read|list`, `secret:read|list`, subscribe), allow without
  consulting policies. Deny rules still apply (deny > implicit grant), so an
  admin can carve out a whole namespace but not a single key.
- A read/list/subscribe on a namespace is a single yes/no decision. Because
  authorization is all-or-nothing per namespace, **once a subscriber is
  admitted to a namespace it receives every change in it** — there is no
  per-event authorization filtering (nothing to filter against).
- **List browsing filter:** `List*` accepts an optional `key_prefix`. It is
  a pure convenience filter (`name LIKE 'prefix%'` on the opaque key string,
  not segment-aware), never a security boundary — a caller authorized for
  the namespace may list any key in it; the prefix just narrows what the
  page returns.
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
see §3); as today, admin access is not policy-restricted but every action
is audited.

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
```

There is no `WatchSelector` and no `key_pattern` anywhere. Per-service
changes (mechanical unless noted):

- **ParameterService / SecretService:** every `string path` field becomes
  `ResourceRef ref`. `List*Request.path_prefix` becomes `NamespaceRef
  namespace` + optional `string key_prefix` (a browsing filter only — §6;
  cross-namespace overviews come from `ListNamespaces` + per-namespace
  counts, see §10).
- **WatchService:** collapses to one stream. `SubscribeRequest` carries
  `repeated NamespaceRef namespaces` (the namespaces to watch) +
  `last_seen_revision`; the server streams **every** change in those
  namespaces the caller is authorized for. `ParameterChange`/
  `SecretMetadataChange`/`Parameter` carry `ResourceRef` (namespace + exact
  key). The old `WatchParameter`/`WatchNamespace` convenience RPCs and all
  key-pattern selectors are **removed** — watching one key or one prefix is
  a namespace subscription plus a client-side filter in the callback.
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
  - `PolicyRule{operation, env, app}` (no key).
  - `ListAuditEventsRequest`: `path_prefix` → `env`/`app`/`key_prefix`.
  - `Subscriber.namespaces` (`repeated NamespaceRef`) replaces `paths`.

Regenerate `gen/kmsv1` (protoc per existing setup) and
`sdk/python/kms_paramstore/_gen` (`sdk/python/gen.sh`).

## 9. Watch hub (`internal/watch`) and change log

- The hub routes purely by namespace: a change in `(env, app)` is delivered
  to every subscriber of that namespace. No key matching, no selectors —
  `keyutil.MatchKey` does not exist.
- Authorization is checked **once, at subscribe time**, per requested
  namespace (a namespace-level yes/no, home grant or explicit policy). After
  admission the stream carries every change in the namespace; there is no
  per-event authorization predicate.
- `storage.SnapshotParameters(ctx, namespaces []NamespaceRef)` — snapshot
  query is `WHERE namespace_id = ?` per namespace, one consistent read
  transaction (the whole authorized namespace, as the declarative path
  already needs).
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
  env → app cascading picker (from `ListNamespaces`) + a key-prefix
  browsing box; rows show relative keys; create/edit forms take namespace +
  key.
- **Identities page:** optional namespace binding + auth methods on
  create; one-time display of token and/or PEM bundle (download button);
  per-identity cert list (fingerprint, expiry, revoke button, issue-new);
  bound namespace as a chip; explain the implicit grant in help text.
- **Policies page:** rule editor has operation + env/app fields (with `*`
  support). No key field — a grant is on a whole namespace.
- **Subscribers page:** render each subscriber's subscribed namespaces;
  group by namespace.
- **Audit page:** env/app dropdown filters + a key-prefix browsing box.

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
  (zero-value = hot reload on). Non-static `ParameterValue`s and every
  `Watch` share **one namespace subscription** on the Subscribe stream; the
  SDK routes each incoming change to the matching field by **exact key** and
  to any `Watch` callbacks.
- `Client.Watch(ctx, fn)` watches the client's whole namespace (fires for
  every change in it). It takes **no key pattern** — an app that only cares
  about a subset filters by its own convention inside `fn` (e.g.
  `strings.HasPrefix(ev.Key, "db/")`). An optional overload may watch a
  *different* namespace the client is authorized for.
- Namespace discovery: on first namespace-needing call, if
  `Config.Namespace` is empty, call `WhoAmI` once (cached for the client's
  lifetime); surface `ErrNoNamespace` if the identity is unbound.
- Reconnect/backoff/reconciliation/redaction semantics all carry over;
  reconciliation lists the whole subscribed namespace.
- `paramstoretest`: `SetParameter(ns, key, value)` etc.; keep a
  `SetParameterPath("/env/app/key", ...)` convenience that splits
  client-side, to keep test call-sites terse.

### Python (`sdk/python/kms_paramstore`)

Mirror of the Go changes: `Client(endpoint, token=..., namespace="prod/gradethis"
| None)`, relative keys in `SecretValue`/`ParameterValue`, `static=True`
replaces `dynamic=True` (default flips to hot-reload-on), `client.watch(cb)`
(whole-namespace, no pattern — the callback filters by its own convention),
WhoAmI discovery, `_gen` regenerated via `sdk/python/gen.sh`. The stricter
`fallback_to_defaults_on_error=False` default behavior is kept as-is.

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

> **Historical.** This section records the original build, which is done.
> Where it mentions watch key-patterns/selectors or per-key policy (Phase 2,
> Phase 3), it is superseded by the namespace-level model in §6/§8/§9 and the
> removal delta in §15.

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
- **Per-key authorization.** The namespace is the authorization unit; there
  is no way to grant read on one key but not another within a namespace. If
  two sets of keys need different access, they belong in different
  namespaces.
- **Key hierarchy / prefix matching / watch selectors on the server.** Keys
  are opaque; the server never interprets `/`. The only key-prefix operation
  is the list *browsing* filter, which is a plain `LIKE 'prefix%'` and not a
  security boundary.
- Batch-read RPC (SDK `Resolve` stays concurrent-per-field).
- Pushing secret plaintext over watch streams (unchanged invariant).
- Server-side acceptance of `/env/app/key` strings anywhere.
- CSR-based cert issuance (client-held keys) — server-side generation
  only for this pass.
- CRL/OCSP distribution — revocation is enforced in the KMS's own
  interceptor, which is the only relying party.

## 15. Remaining work — the simplification delta

The `(env, app)` model, schema, built-in CA, per-namespace auth methods, and
the SDK ergonomics are implemented. An earlier revision also built per-key
policy patterns and key-pattern watch selectors; this section is the delta
that removes them and lands the namespace-level model above. It is a
watch-and-authz refactor, not a fresh build.

**Server:**
- `internal/domain`: drop `KeyPattern` from `PolicyRule`; remove
  `WatchSelector`; `Subscriber` tracks `[]NamespaceRef`.
- `internal/keyutil`: delete `MatchKey` and `ValidateKeyPattern`. Keep
  env/app/key validation and `SplitDisplayPath`.
- `internal/policy`: `Evaluate`/`Authorize` take a `NamespaceRef` (not
  ref+key); delete `MayListUnder`'s key-prefix logic (list authz is a
  namespace yes/no). Implicit-home-grant helper stays, namespace-level.
- `internal/watch`: hub routes by namespace; delete selector matching and
  the per-event access predicate. `SnapshotParameters(namespaces)`.
- `internal/core`: `AuthorizeSubscribe` becomes a namespace-level check per
  requested namespace; drop the per-event `WatchAccessChecker`; policy
  storage/validation drops the key field.
- `internal/storage`: `SnapshotParameters(namespaces)`; list `key_prefix`
  becomes a plain `LIKE 'prefix%'` (documented as a non-authz browsing
  filter).
- `proto` + `gen`: remove `WatchSelector`/`key_pattern`; `SubscribeRequest`
  carries `repeated NamespaceRef namespaces`; drop `WatchParameter`/
  `WatchNamespace`; `PolicyRule` loses `key`; `Subscriber.namespaces`.
- `grpcserver`/`httpserver`: subscribe by namespace; policy DTOs drop `key`;
  subscriber views expose namespaces.

**SDKs (Go + Python):** one shared namespace subscription; route incoming
changes to fields by exact key; `Watch(fn)` is whole-namespace with no
pattern (callback filters by convention); reconciliation lists the whole
namespace. Delete `matchPattern`/`reconcilePrefix`/`normalizeSelectorKey`
and the base-key machinery.

**Frontend / docs:** policy editor loses the key field; subscribers page
shows namespaces; `docs/http-api.md`, `docs/sdk-*.md`, `docs/security.md`
updated to the namespace-level authz and pattern-free watch.

**Gate:** the same acceptance script in §13, minus any per-key step; plus a
test that a client authorized for a namespace receives a change to a key it
never "selected", and that a client *not* authorized for a namespace cannot
subscribe to it at all.
