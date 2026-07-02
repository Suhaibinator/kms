# HTTP API (frontend/admin)

The Go server exposes a JSON HTTP API under `/api/v1/` for the embedded
frontend. It is backed by the same service layer, auth, and audit pipeline as
the gRPC API. All responses are JSON. This document is the contract between
the Go server and the Next.js frontend.

## Authentication

Every request (except `/api/v1/auth/login` and `/api/v1/health`) requires:

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

## Errors

Non-2xx responses carry:

```json
{ "error": { "code": "not_found", "message": "parameter /a/b: not found" } }
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

Error messages never contain secret values or token material.

## Common types

Timestamps are Unix milliseconds (`*_unix_ms`, integer). Binary values are
base64 (`value_base64`). Resource paths look like `/prod/payments/stripe/api-key`
and are passed via the `path` query parameter (URL-encoded) or JSON body field.

`Parameter`:
```json
{
  "path": "/prod/api/rate-limit",
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
  "path": "/prod/payments/stripe/api-key",
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

## Endpoints

### Auth & health

- `POST /api/v1/auth/login` — body `{"token": "..."}` →
  `{"identity": {"name": "...", "kind": "admin|client"}}`
- `GET /api/v1/health` — no auth →
  `{"healthy": true, "ready": true, "version": "...", "current_revision": 42}`

### Namespaces

- `GET /api/v1/namespaces?page_size=&page_token=` →
  `{"namespaces": [{"path","description","created_by","created_at_unix_ms"}], "next_page_token": ""}`
- `POST /api/v1/namespaces` — `{"path": "/prod/payments", "description": ""}` → `{"namespace": {...}}`

### Parameters

- `GET /api/v1/parameters?prefix=/prod&page_size=&page_token=` →
  `{"parameters": [Parameter], "next_page_token": ""}`
- `GET /api/v1/parameters/get?path=&version=&label=` → `{"parameter": Parameter}`
- `GET /api/v1/parameters/metadata?path=` →
  `{"path","content_type","metadata_json","created_at_unix_ms","updated_at_unix_ms","labels",
    "versions":[{"version","content_type","state","created_by","created_at_unix_ms","metadata_json"}]}`
- `PUT /api/v1/parameters` — `{"path","value","content_type","metadata_json"}` →
  `{"version": 4, "revision": 99}`
- `DELETE /api/v1/parameters?path=` → `{"revision": 100}`

### Secrets

- `GET /api/v1/secrets?prefix=&page_size=&page_token=` →
  `{"secrets": [SecretMetadata], "next_page_token": ""}`
- `GET /api/v1/secrets/metadata?path=` → `{"secret": SecretMetadata}`
- `POST /api/v1/secrets` — create/update (new version):
  ```json
  { "path": "/prod/x/api-key", "value_base64": "...", "content_type": "text/plain",
    "metadata_json": "{}", "client_bound": false, "generate_access_token": false,
    "expires_at_unix_ms": 0 }
  ```
  → `{"version": 1, "revision": 7, "access_token": "..."}`
  (`access_token` present only when `generate_access_token` was true — shown once, never again.)
  Client-bound updates additionally require header `X-KMS-Secret-Token: <token>`.
- `POST /api/v1/secrets/reveal` — `{"path", "version": 0, "label": ""}` →
  `{"path", "version", "value_base64", "content_type"}`.
  Admin only. Every call is audited as a reveal event. Returns
  `failed_precondition` (412) for client-bound secrets — they have no reveal flow;
  the UI must show metadata only and explain why.
- `POST /api/v1/secrets/disable` — `{"path", "version": 0, "enable": false}` → `{"revision"}`
  (`version: 0` = all versions; `enable: true` re-enables.)
- `POST /api/v1/secrets/destroy` — `{"path", "version"}` → `{"revision"}` (irreversible)
- `POST /api/v1/secrets/promote` — `{"path", "version"}` →
  `{"current_version", "previous_version", "revision"}`
- `DELETE /api/v1/secrets?path=` → `{"revision"}`

### Policies

Policy shape:
```json
{ "name": "gradethis-read", "subject": "gradethis-be",
  "allow": [ {"operation": "secret:read", "path": "/prod/gradethis/*"} ],
  "deny":  [],
  "created_at_unix_ms": 0, "updated_at_unix_ms": 0 }
```

- `GET /api/v1/policies?page_size=&page_token=` → `{"policies": [...], "next_page_token": ""}`
- `POST /api/v1/policies` — `{"policy": {...}}` → `{"policy": {...}}`
- `PUT /api/v1/policies` — `{"policy": {...}}` (matched by name) → `{"policy": {...}}`
- `DELETE /api/v1/policies?name=` → `{}`

### Identities

- `GET /api/v1/identities?page_size=&page_token=` →
  `{"identities": [{"name","kind","disabled","created_at_unix_ms"}], "next_page_token": ""}`
- `POST /api/v1/identities` — `{"name": "gradethis-be", "kind": "client"}` →
  `{"identity": {...}, "token": "..."}` (token shown once)
- `POST /api/v1/identities/rotate` — `{"name"}` → `{"token": "..."}` (shown once)
- `POST /api/v1/identities/revoke` — `{"name"}` → `{}`

### Audit

- `GET /api/v1/audit?path_prefix=&actor=&event_type=&from_unix_ms=&to_unix_ms=&page_size=&page_token=` →
  ```json
  { "events": [ { "id": 1, "event_type": "secret.read", "actor_identity": "gradethis-be",
      "actor_type": "client", "resource_type": "secret", "resource_path": "/prod/x",
      "resource_version": 2, "decision": "allow", "source_ip": "10.0.0.5",
      "user_agent": "", "request_id": "r-123", "created_at_unix_ms": 0,
      "metadata_json": "{}" } ],
    "next_page_token": "" }
  ```

### Subscribers

- `GET /api/v1/subscribers` →
  ```json
  { "subscribers": [ { "client_name": "gradethis-be", "instance_id": "gradethis-be-8f3a",
      "identity": "gradethis-be", "paths": ["/prod/gradethis/*"],
      "remote_addr": "10.0.0.5:53411", "connected_at_unix_ms": 0,
      "last_heartbeat_unix_ms": 0, "last_acked_revision": 41 } ],
    "current_revision": 42 }
  ```
  The UI compares `last_acked_revision` against `current_revision` to show
  which apps have applied the latest configuration.

### Key metadata

- `GET /api/v1/keys` → `{"keys": [{"id","source","state","created_at_unix_ms"}]}`
  (never any key material)

## Static frontend serving

- `/` and all non-`/api` routes serve the embedded Next.js static export.
- Unknown frontend routes fall back to the exported entry HTML so client-side
  routing works on refresh/deep links.
- `/healthz` (liveness) and `/readyz` (readiness) are plain-text endpoints
  outside `/api`.
