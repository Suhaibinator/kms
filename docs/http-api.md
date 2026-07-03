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

### Per-namespace allowed auth methods

Each namespace declares which authentication methods admit a caller into it,
stored as `allowed_auth_methods` (a JSON array of `"mtls"` and/or `"token"`).
The gate is enforced on **every** operation in the namespace, including reads
of unprotected parameters — a namespace can require mTLS-only, which makes
bearer-token theft useless for it. New namespaces default to `["mtls"]`
(strongest posture); adding `"token"` is an explicit, audited namespace-settings
change.

- **mTLS** clients authenticate with a client certificate minted by the
  built-in CA (see `GET /api/v1/ca` and the identities endpoints). Certificates
  prove possession — the private key never leaves the client after issuance.
- **token** clients authenticate with a bearer token, which is
  possession-free: whoever holds the string is treated as the app.

The auth-method gate applies to **client-kind** identities. **Admin-kind**
identities are the management plane: they administer any namespace from a
browser (which cannot practically present a client certificate), subject to the
same policy and audit controls, and therefore **bypass the method gate** — the
browser login above stays token-based regardless of a namespace's
`allowed_auth_methods`. Admin identities do not bypass policy or audit.

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
| failed_precondition  | 412  |
| internal             | 500  |
| unavailable (not ready) | 503 |

A `permission_denied` from the auth-method gate carries an explicit message
(e.g. "namespace requires mtls"). Error messages never contain secret values or
token material.

## Common types

Timestamps are Unix milliseconds (`*_unix_ms`, integer). Binary values are
base64 (`value_base64`). A resource reference is flattened at the top level of
each DTO as `env`, `app`, `key`; list queries use `env`, `app`, and
`key_prefix`. The `key_prefix` is a plain browsing filter (`LIKE 'prefix%'` on
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
    { "serial": "0a1b2c", "fingerprint": "sha256:…",
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
  The built-in CA's public certificate, for baking into app deploy images and
  configuring clients to trust the KMS-issued client certs.

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
  Only succeeds when the namespace is empty (no parameters, no secrets).
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
  once, never again.)
  Client-bound updates additionally require header `X-KMS-Secret-Token: <token>`.
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
  effect on the next RPC.
- `POST /api/v1/identities/revoke` — `{"name"}` → `{}`
  Disables the identity: its token and **all** of its certificates stop working
  immediately.

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
  UI compares `last_acked_revision` against `current_revision` to show which apps
  have applied the latest configuration.

### Key metadata

- `GET /api/v1/keys` → `{"keys": [{"id","source","state","created_at_unix_ms"}]}`
  (never any key material)

## Static frontend serving

- `/` and all non-`/api` routes serve the embedded Next.js static export.
- Unknown frontend routes fall back to the exported entry HTML so client-side
  routing works on refresh/deep links.
- `/healthz` (liveness) and `/readyz` (readiness) are plain-text endpoints
  outside `/api`.
