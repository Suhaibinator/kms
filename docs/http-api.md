# HTTP API (frontend/admin)

The Go server exposes a JSON HTTP API under `/api/v1/` for the embedded
frontend. It is backed by the same service layer, auth, and audit pipeline as
the gRPC API. All responses are JSON. This document is the contract between
the Go server and the Next.js frontend.

Resources are addressed by a **namespace** — a fixed `(env, app)` pair — plus a
relative **key**. There is no flat `path` string on the wire: requests carry
explicit `env`, `app`, and `key` fields (query params or JSON body). The
`/env/app/key` form survives only as a display convention in logs, audit
rendering, and the frontend; the server never parses it.

## Authentication

Every request (except `/api/v1/auth/login`, `/api/v1/health`, and
`/api/v1/ca`) requires:

```
Authorization: Bearer <token>
```

Tokens are identity tokens (admin or client) minted by `parameter-store
create-admin` or the identities API. The frontend flow:

1. User pastes a token on the login page.
2. `POST /api/v1/auth/login` with `{"token": "..."}`.
3. On 200, response is `{"identity": {"name": "...", "kind": "admin"}}`.
   The frontend stores the token (memory + sessionStorage) and sends it as
   the `Authorization` header on every subsequent call.
4. On 401 the token is invalid.

Most management endpoints require `kind == "admin"`.

### Applications

Applications are the environment-independent configuration owners. These
endpoints are admin-only:

- `GET /api/v1/applications?page_size=&page_token=` lists applications and
  their environment counts.
- `POST /api/v1/applications` creates an application; `PATCH` replaces its
  mutable definition:

  ```json
  {
    "name": "payments-api",
    "description": "Payments service",
    "release_name": "runtime",
    "schema_id": "payments-api/runtime",
    "schema_version": 3,
    "contract": [
      {"alias":"runtime","kind":"parameter","content_type":"json"},
      {"alias":"stripe_key","kind":"secret"}
    ]
  }
  ```

- `DELETE /api/v1/applications?name=` succeeds only after every environment
  namespace has been deleted.
- `GET /api/v1/applications/dashboard?name=` returns the application,
  environment namespaces, and the union of current parameter values and
  secret metadata as a cross-environment matrix. Secret plaintext is absent.
- `PUT /api/v1/applications/parameters` writes the same parameter value to
  selected environments:

  ```json
  {
    "application":"payments-api", "key":"rate-limit", "value":"100",
    "content_type":"integer", "metadata_json":"{}",
    "environments":["dev","prod-gcp"]
  }
  ```

  Each target receives an independent immutable version and result. The
  response may contain per-environment errors when only some targets succeed;
  the operation intentionally does not create shared mutable state.

### Per-namespace allowed auth methods

Each namespace declares which authentication methods admit a caller into it,
stored as `allowed_auth_methods` (a JSON array of `"mtls"` and/or `"token"`).
The gate is enforced on **every** operation in the namespace, including reads
of unprotected parameters — a namespace can require mTLS-only, which makes
bearer-token theft useless for it. New namespaces default to `["mtls"]`
(strongest posture); adding `"token"` is an explicit, audited namespace-settings
change.

The HTTP listener itself is bearer-token only; machine clients present mTLS
certificates on the gRPC listener. The HTTP API exposes the same namespace
configuration, so the two methods are summarized here for completeness:

- **mTLS** clients authenticate with a client certificate minted by the
  built-in CA (see `GET /api/v1/ca` and the identities endpoints). Certificates
  prove possession. The private key is generated server-side, returned once,
  never persisted by the server, and must then be retained by the client.
- **token** clients authenticate with a bearer token, which is
  possession-free: whoever holds the string is treated as the app.

The auth-method gate applies to **client-kind** identities. **Admin-kind**
identities are the management plane: they administer any namespace from a
browser (which cannot practically present a client certificate), and therefore
**bypass both the method gate and data-plane policy checks** — the browser login
above stays token-based regardless of a namespace's `allowed_auth_methods`.
Admin identities never bypass auditing or the cryptographic impossibility of
revealing a client-bound secret without its token.

A namespace-bound client identity also carries an **implicit home-namespace
grant**: it may read, list, and subscribe within its own namespace with no
policy required, but only when it presents a method the namespace allows. Writes
and any cross-namespace access still require an explicit policy. See
[`plan-namespaces.md`](../plan-namespaces.md) §3 and §7 for the full model.

## Errors

Non-2xx responses carry:

```json
{ "error": { "code": "not_found", "message": "parameter /prod/gradethis/rate-limit: not found" } }
```

| code                 | HTTP |
|----------------------|------|
| invalid_argument     | 400  |
| unauthenticated      | 401  |
| permission_denied    | 403  |
| not_found            | 404  |
| already_exists       | 409  |
| aborted (CAS conflict) | 409 |
| failed_precondition  | 412  |
| internal             | 500  |
| unavailable (not ready) | 503 |

A `permission_denied` from the auth-method gate carries an explicit message
(e.g. "namespace requires mtls"). Error messages never contain secret values or
token material.

## Common types

Timestamps are Unix milliseconds (`*_unix_ms`, integer). Binary values are
base64 (`value_base64`). Parameter and secret resource references are flattened
at the top level as `env`, `app`, `key`; configuration release entries retain a
structured `ref` because a single release may point across namespaces. List
queries use `env`, `app`, and `key_prefix`. The `key_prefix` is a plain browsing filter (`LIKE 'prefix%'` on
the opaque key string), **not** an authorization boundary — a caller authorized
for a namespace may list any key in it; the prefix only narrows what a page
returns. Display paths shown in the UI look like `/prod/gradethis/rate-limit`.

`Namespace`:
```json
{
  "env": "prod",
  "app": "gradethis",
  "description": "GradeThis backend",
  "allowed_auth_methods": ["mtls"],
  "created_by": "admin",
  "created_at_unix_ms": 1710000000000,
  "parameter_count": 12,
  "secret_count": 4
}
```

`Parameter`:
```json
{
  "env": "prod",
  "app": "gradethis",
  "key": "rate-limit",
  "value": "100",
  "content_type": "integer",
  "version": 3,
  "metadata_json": "{}",
  "created_by": "admin",
  "created_at_unix_ms": 1710000000000,
  "labels": { "current": 3, "previous": 2 }
}
```

`SecretMetadata` (never contains values):
```json
{
  "env": "prod",
  "app": "gradethis",
  "key": "stripe-api-key",
  "content_type": "text/plain",
  "client_bound": false,
  "has_access_token": true,
  "metadata_json": "{}",
  "created_at_unix_ms": 0,
  "updated_at_unix_ms": 0,
  "labels": { "current": 2, "previous": 1 },
  "versions": [
    { "version": 2, "state": "enabled", "created_by": "admin",
      "created_at_unix_ms": 0, "destroyed_at_unix_ms": 0,
      "expires_at_unix_ms": 0, "metadata_json": "{}" }
  ]
}
```

`Identity`:
```json
{
  "name": "gradethis-be",
  "kind": "client",
  "disabled": false,
  "created_at_unix_ms": 0,
  "namespace": { "env": "prod", "app": "gradethis" },
  "has_token": true,
  "certs": [
    { "serial": "0a1b2c", "fingerprint": "<64 lowercase hex characters>",
      "not_after_unix_ms": 0, "revoked_at_unix_ms": 0,
      "created_at_unix_ms": 0 }
  ]
}
```

`namespace` is `null` for unbound (admin/tooling) identities. `has_token` is
true when the identity has a bearer token. `certs` lists issued client
certificates; `revoked_at_unix_ms` of `0` means valid.

`CertBundle` (returned exactly once, never stored server-side):
```json
{ "cert_pem": "-----BEGIN CERTIFICATE-----\n…",
  "key_pem": "-----BEGIN PRIVATE KEY-----\n…",
  "serial": "0a1b2c", "not_after_unix_ms": 0 }
```

`ConfigurationRelease` (secret entries contain metadata only):
```json
{
  "namespace": { "env": "prod", "app": "gradethis" },
  "name": "runtime",
  "version": 14,
  "schema_id": "gradethis/runtime",
  "schema_version": 1,
  "entries": [
    { "alias": "rate_limits", "kind": "parameter",
      "ref": { "namespace": { "env": "prod", "app": "gradethis" },
               "key": "config/rate-limits" },
      "version": 8, "content_type": "json", "metadata_json": "{}",
      "parameter_digest": "<sha256 hex>",
      "client_bound": false, "has_access_token": false },
    { "alias": "db_password", "kind": "secret",
      "ref": { "namespace": { "env": "prod", "app": "gradethis" },
               "key": "db-password" },
      "version": 3, "content_type": "text/plain", "metadata_json": "{}",
      "parameter_digest": "", "client_bound": false,
      "has_access_token": true }
  ],
  "digest": "<deterministic sha256 hex>",
  "metadata_json": "{}", "created_by": "admin",
  "created_at_unix_ms": 1710000000000
}
```

Release values, secret plaintext, and per-secret access tokens never appear in
this DTO. Creation selectors use the same `ref` shape, plus either `version` or
`label`; an empty reference namespace means the release namespace.

## Endpoints

### Auth & health

- `POST /api/v1/auth/login` — no auth — body `{"token": "..."}` →
  `{"identity": {"name": "...", "kind": "admin|client"}}`
- `GET /api/v1/health` — no auth →
  `{"healthy": true, "ready": true, "version": "...", "current_revision": 42}`
- `GET /api/v1/whoami` →
  `{"name": "...", "kind": "admin|client",
    "namespace": {"env": "...", "app": "..."} | null, "auth_method": "mtls|token"}`
  Callable by any authenticated identity (no policy check). This is the SDK's
  namespace-discovery mechanism.
- `GET /api/v1/ca` — **no auth** → `{"cert_pem": "-----BEGIN CERTIFICATE-----\n…"}`
  The built-in **client-issuing** CA's public certificate, useful for
  out-of-band validation of KMS-issued client certificates. It is not the
  server-trust CA used by SDKs; the server certificate is operator-provided.

### Namespaces

- `GET /api/v1/namespaces?page_size=&page_token=` →
  `{"namespaces": [Namespace], "next_page_token": ""}`
  (each item includes `parameter_count` and `secret_count`, powering the
  dashboard and per-namespace counts)
- `POST /api/v1/namespaces` — create:
  ```json
  { "env": "prod", "app": "gradethis", "description": "",
    "allowed_auth_methods": ["mtls"] }
  ```
  → `{"namespace": Namespace}`
  (if `allowed_auth_methods` is omitted or empty, defaults to `["mtls"]`)
- `PATCH /api/v1/namespaces` — update description and/or auth methods (full
  replacement of both):
  ```json
  { "env": "prod", "app": "gradethis", "description": "…",
    "allowed_auth_methods": ["mtls", "token"] }
  ```
  → `{"namespace": Namespace}`
- `DELETE /api/v1/namespaces?env=&app=` → `{}`
  Only succeeds when the namespace is empty (no parameters, no secrets, and no
  bound identities).
  Otherwise returns `failed_precondition` (412).

### Parameters

Listing is always namespace-scoped: `env` and `app` are required.

- `GET /api/v1/parameters?env=&app=&key_prefix=&page_size=&page_token=` →
  `{"parameters": [Parameter], "next_page_token": ""}`
- `GET /api/v1/parameters/get?env=&app=&key=&version=&label=` →
  `{"parameter": Parameter}`
- `GET /api/v1/parameters/metadata?env=&app=&key=` →
  `{"env","app","key","content_type","metadata_json","created_at_unix_ms",
    "updated_at_unix_ms","labels",
    "versions":[{"version","content_type","state","created_by",
      "created_at_unix_ms","metadata_json"}]}`
- `PUT /api/v1/parameters` — `{"env","app","key","value","content_type","metadata_json"}` →
  `{"version": 4, "revision": 99}`
- `DELETE /api/v1/parameters?env=&app=&key=` → `{"revision": 100}`

### Secrets

Listing is always namespace-scoped: `env` and `app` are required.

- `GET /api/v1/secrets?env=&app=&key_prefix=&page_size=&page_token=` →
  `{"secrets": [SecretMetadata], "next_page_token": ""}`
- `GET /api/v1/secrets/metadata?env=&app=&key=` → `{"secret": SecretMetadata}`
- `POST /api/v1/secrets` — create/update (new version):
  ```json
  { "env": "prod", "app": "gradethis", "key": "stripe-api-key",
    "value_base64": "...", "content_type": "text/plain",
    "metadata_json": "{}", "client_bound": false,
    "generate_access_token": false, "expires_at_unix_ms": 0 }
  ```
  → `{"version": 1, "revision": 7, "access_token": "..."}`
  (`access_token` present only when `generate_access_token` was true — shown
  once, never again.) Creating a client-bound secret requires both
  `client_bound: true` and `generate_access_token: true`; the server-minted
  token is its only client key share. Client-bound updates additionally require
  header `X-KMS-Secret-Token: <current-token>`; setting
  `generate_access_token: true` on an update rotates the token for the new
  version and returns it once.
- `POST /api/v1/secrets/reveal` — `{"env","app","key","version": 0,"label": ""}` →
  `{"env","app","key","version","value_base64","content_type"}`.
  Admin only. Every call is audited as a reveal event. Returns
  `failed_precondition` (412) for client-bound secrets — they have no reveal
  flow; the UI shows metadata only and explains why.
- `POST /api/v1/secrets/disable` — `{"env","app","key","version": 0,"enable": false}` →
  `{"revision"}` (`version: 0` = all versions; `enable: true` re-enables.)
- `POST /api/v1/secrets/destroy` — `{"env","app","key","version"}` →
  `{"revision"}` (irreversible)
- `POST /api/v1/secrets/promote` — `{"env","app","key","version"}` →
  `{"current_version", "previous_version", "revision"}`
- `DELETE /api/v1/secrets?env=&app=&key=` → `{"revision"}`

### Policies

Policy shape (a rule grants an operation over a whole namespace):
```json
{ "name": "gradethis-read", "subject": "gradethis-be",
  "allow": [ {"operation": "secret:read", "env": "prod", "app": "gradethis"} ],
  "deny":  [],
  "created_at_unix_ms": 0, "updated_at_unix_ms": 0 }
```

A rule's `env` and `app` are exact or `"*"`. There is **no** `key` field: the
namespace `(env, app)` is the unit of authorization, so a grant applies to every
key in the matched namespace. Deny rules always override allow; evaluation is
deny → allow → default deny. The implicit home-namespace grant sits behind deny
rules (a deny still wins).

- `GET /api/v1/policies?page_size=&page_token=` →
  `{"policies": [...], "next_page_token": ""}`
- `POST /api/v1/policies` — `{"policy": {...}}` → `{"policy": {...}}`
- `PUT /api/v1/policies` — `{"policy": {...}}` (matched by name) → `{"policy": {...}}`
- `DELETE /api/v1/policies?name=` → `{}`

### Identities

- `GET /api/v1/identities?page_size=&page_token=` →
  `{"identities": [Identity], "next_page_token": ""}`
- `POST /api/v1/identities` — create:
  ```json
  { "name": "gradethis-be", "kind": "client",
    "namespace": {"env": "prod", "app": "gradethis"},
    "auth_methods": ["mtls"], "cert_ttl_seconds": 7776000 }
  ```
  → `{"identity": Identity, "token": "...", "cert": CertBundle}`
  `namespace` may be `null` (unbound). `auth_methods` selects which credentials
  are minted: `token` present in the response only when `"token"` was
  requested; `cert` (the one-time PEM bundle) present only when `"mtls"` was
  requested. `cert_ttl_seconds` applies to the initial certificate (server
  default 90 days when omitted). Both `token` and `cert` are shown once.
- `POST /api/v1/identities/rotate` — `{"name"}` → `{"token": "..."}`
  Mints a new bearer token and invalidates the current one (shown once). For
  token identities; mTLS identities rotate via `issue-cert` instead.
- `POST /api/v1/identities/issue-cert` — `{"name", "ttl_seconds": 7776000}` →
  `{"cert": CertBundle}`
  Issues an additional client certificate (shown once). Multiple concurrent
  valid certificates per identity allow zero-downtime rollover.
- `POST /api/v1/identities/revoke-cert` — `{"name", "serial"}` → `{}`
  Revokes a single certificate by serial; other certs keep working. Takes
  effect on the next RPC; an existing watch stream is closed on its next
  heartbeat reauthorization.
- `POST /api/v1/identities/revoke` — `{"name"}` → `{}`
  Disables the identity: its token and **all** of its certificates stop working
  for new RPCs immediately; an existing watch stream is closed on its next
  heartbeat reauthorization.

### Audit

- `GET /api/v1/audit?env=&app=&key_prefix=&actor=&event_type=&from_unix_ms=&to_unix_ms=&page_size=&page_token=` →
  ```json
  { "events": [ { "id": 1, "event_type": "secret.read", "actor_identity": "gradethis-be",
      "actor_type": "client", "resource_type": "secret",
      "resource_env": "prod", "resource_app": "gradethis", "resource_key": "stripe-api-key",
      "resource_version": 2, "decision": "allow", "source_ip": "10.0.0.5",
      "user_agent": "", "request_id": "r-123", "created_at_unix_ms": 0,
      "metadata_json": "{}" } ],
    "next_page_token": "" }
  ```
  `env`, `app`, and `key_prefix` scope events to a namespace and key range; all
  filters are optional and combine.

### Subscribers

- `GET /api/v1/subscribers` →
  ```json
  { "subscribers": [ { "client_name": "gradethis-be", "instance_id": "gradethis-be-8f3a",
      "identity": "gradethis-be",
      "namespaces": [ { "env": "prod", "app": "gradethis" } ],
      "remote_addr": "10.0.0.5:53411", "connected_at_unix_ms": 0,
      "last_heartbeat_unix_ms": 0, "last_acked_revision": 41 } ],
    "current_revision": 42 }
  ```
  `namespaces` are the namespaces the stream is subscribed to. A subscriber
  receives **every** change in each namespace it watches; there is no key-level
  filtering on the wire (a client narrows its interest in its own callback). The
  UI compares `last_acked_revision` against the server's **global**
  `current_revision`. This is a coarse lag signal: revisions from namespaces a
  subscriber does not watch can make it appear behind even when it has applied
  every relevant event.

### Configuration releases and schemas

- `POST /api/v1/releases` creates, but does not activate, an immutable release:
  ```json
  { "namespace": { "env": "prod", "app": "gradethis" },
    "name": "runtime", "schema_id": "gradethis/runtime", "schema_version": 1,
    "entries": [
      { "alias": "rate_limits", "kind": "parameter",
        "ref": { "namespace": {}, "key": "config/rate-limits" }, "label": "current" },
      { "alias": "db_password", "kind": "secret",
        "ref": { "namespace": {}, "key": "db-password" }, "version": 3 }
    ], "metadata_json": "{}" }
  ```
  → `201 {"release": ConfigurationRelease}`. Labels are resolved to exact
  versions before the returned release is persisted.
- `GET /api/v1/releases?env=&app=&name=&page_size=&page_token=` →
  `{"releases":[{"release":ConfigurationRelease,"current":true,
  "previous":false,"activation_revision":42}],"next_page_token":""}`.
  `name` is optional; the namespace is required.
- `GET /api/v1/releases/get?env=&app=&name=&version=` →
  `{"release": ConfigurationRelease}`.
- `GET /api/v1/releases/active?env=&app=&name=` →
  `{"release":ConfigurationRelease,"activation_revision":42,
  "previous_version":13}`.
- `POST /api/v1/releases/validate` with
  `{"namespace":{"env":"prod","app":"gradethis"},"name":"runtime","version":14}`
  → `{"valid":false,"errors":[{"alias":"rate_limits",
  "code":"schema_violation","schema_pointer":"/properties/rate_limits/type",
  "message":"configuration value does not satisfy schema"}]}`. Error messages
  are sanitized and never include values.
- `POST /api/v1/releases/activate` with
  `{"namespace":{"env":"prod","app":"gradethis"},"name":"runtime",
  "version":14,"expected_current_version":13}` →
  `{"release":ConfigurationRelease,"activation_revision":42,
  "previous_version":13,"changed":true}`. Omit `expected_current_version` for
  no CAS guard; an explicit `0` requires that no release is active. A conflict
  is HTTP 409. Activating the current version is an idempotent no-op. Activation
  revalidates every pinned entry and the release schema immediately before the
  active pointer changes. If that validation fails, the active release and
  revision remain unchanged and the response is HTTP 412 with the same
  sanitized diagnostics exposed by the validation endpoint:
  ```json
  { "error": { "code": "failed_precondition",
      "message": "release validation failed",
      "validation_errors": [
        { "alias": "rate_limits", "code": "schema_violation",
          "schema_pointer": "/properties/rate_limits/type",
          "message": "configuration value does not satisfy schema" }
      ] } }
  ```
- `POST /api/v1/configuration-schemas` with
  `{"id":"gradethis/runtime","schema_json":"{...}","metadata_json":"{}"}`
  → `201 {"schema":{"id","version","schema_json","digest",
  "metadata_json","created_by","created_at_unix_ms"}}`.
- `GET /api/v1/configuration-schemas?id=&page_size=&page_token=` →
  `{"schemas":[...],"next_page_token":""}`. `id` is optional.
- `GET /api/v1/release-subscribers?env=&app=&name=&page_size=&page_token=` →
  `{"subscribers":[{"namespace","release_name","client_name","instance_id",
  "identity","state","release_version","activation_revision",
  "rejection_category","diagnostic","client_timestamp_unix_ms",
  "server_timestamp_unix_ms","connected"}],"next_page_token":"",
  "current_revision":42}`.

Release subscriber rows are per process instance and lifecycle state. Group
rows by `(identity, client_name, instance_id)` to show
received/prepared/applied/rejected together without combining different
authenticated identities that reuse the same client and instance names. A
connected instance with no lifecycle acknowledgement yet is
represented by a row whose `state` is empty. `current_revision` is the active
revision for the requested release name, so lag is release-specific rather
than namespace-global.

Release watch and acknowledgement traffic is gRPC-only. See
[`configuration-releases.md`](configuration-releases.md#grpc-contract) for the
bidirectional `WatchRelease` contract.

### Key metadata

- `GET /api/v1/keys` → `{"keys": [{"id","source","state","created_at_unix_ms"}]}`
  (never any key material)

## Static frontend serving

- `/` and all non-`/api` routes serve the embedded Next.js static export.
- Unknown frontend routes fall back to the exported entry HTML so client-side
  routing works on refresh/deep links.
- `/healthz` (liveness) and `/readyz` (readiness) are plain-text endpoints
  outside `/api`.
