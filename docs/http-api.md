# HTTP API (frontend/admin)

The Go server exposes a JSON HTTP API under `/api/v1/` for the embedded
frontend. It is backed by the same service layer, auth, and audit pipeline as
the gRPC API. Non-streaming success and application-error responses are JSON;
the release-subscriber stream is server-sent events. This document is the
contract between the Go server and the Next.js frontend.

Resources are addressed by a **namespace** — a fixed `(env, app)` pair — plus a
relative **key**. There is no flat `path` string on the wire: requests carry
explicit `env`, `app`, and `key` fields (query params or JSON body). The
`/env/app/key` form survives only as a display convention in logs, audit
rendering, and the frontend; the server never parses it.

## Wire constraints

- JSON request bodies are limited to **4 MiB** and must contain exactly one
  valid UTF-8 JSON document. Duplicate object names, trailing JSON values, and
  invalid UTF-8 are rejected. For ordinary endpoint DTOs, JSON field names are
  case-sensitive and unknown fields are ignored; the raw defaults-artifact
  endpoint additionally validates its artifact schema.
- Parameter and secret values are limited to **1 MiB after decoding**. A
  base64-encoded secret therefore also has to fit inside the 4 MiB request
  body limit.
- Paged endpoints default `page_size` to **100** and cap it at **1,000**.
  Omitted, malformed, or non-positive sizes use the default. Treat
  `page_token` as opaque and return the supplied filters unchanged on the next
  request.
- Normal HTTP routes use 10 s read-header, 30 s read, 60 s write, and 120 s
  idle timeouts. The subscriber SSE handler clears the write deadline for its
  response and applies its own five-minute lifetime.

## Authentication

Every request (except `/api/v1/auth/login`, `/api/v1/health`, and
`/api/v1/ca`) requires:

```
Authorization: Bearer <token>
```

Tokens are identity tokens (admin or client) minted by `parameter-store
create-admin` or the identities API.

**An admin identity also needs a client certificate.** While
`security.admin_require_client_cert` is enforced — the default whenever the
server has TLS on — an admin-kind identity is admitted only when the request
carries **both** a bearer token and a client certificate issued by the
built-in CA and chain-verified during the TLS handshake. The listener asks for
a certificate but never demands one, so unauthenticated callers and token-only
client identities connect as before. Either credential alone, or a certificate
and token naming different identities, yields the same generic
`401 unauthenticated` — the API is not an oracle for which half was wrong.
Admin certificates are issued only by `parameter-store admin-cert issue NAME
--out DIR` on the server host; see
[`operations.md`](operations.md#admin-credentials-and-browser-setup). Without
TLS the requirement is unenforceable and the server relaxes it, so a
development server accepts an admin token alone.

The frontend flow:

1. User pastes a token on the login page.
2. `POST /api/v1/auth/login` with `{"token": "..."}`. The browser supplies any
   imported client certificate itself, as part of the TLS connection.
3. On 200, response is
   `{"identity": {"name": "...", "kind": "admin"}, "auth_method": "mtls"}`.
   The frontend stores the token (memory + sessionStorage) and sends it as
   the `Authorization` header on every subsequent call; TLS keeps supplying
   the certificate per connection.
4. On 401 the credentials are invalid — a bad token, or, for an admin, a
   missing or invalid certificate. The login page reads
   `GET /api/v1/health` to tell the second case apart and explain it.

Most management endpoints require `kind == "admin"`.

### Applications

Applications are the environment-independent configuration owners. These
endpoints are admin-only:

- `GET /api/v1/applications?page_size=&page_token=&archived=` lists
  applications and their environment counts. `archived` is `exclude`
  (default), `include`, or `only`.
- `POST /api/v1/applications` creates an application; `PATCH` replaces its
  mutable definition:

  ```json
  {
    "name": "payments-api",
    "description": "Payments service",
    "release_name": "runtime",
    "contract": [
      {"alias":"runtime","kind":"parameter","content_type":"json"},
      {"alias":"stripe_key","kind":"secret"}
    ]
  }
  ```

- Creation may include `"schema":{"schema_json":"{...}","metadata_json":"{}"}`;
  the application, schema version 1, contract, and pin are committed atomically.
- `DELETE /api/v1/applications?name=` succeeds only when there are no
  environments and no schema history.
- `POST /api/v1/applications/archive` and `/unarchive` accept `{"name":"..."}`.
  Archiving requires zero environments. Archived applications remain readable,
  reject definition/schema/release mutations, and are excluded from default lists.
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
- `POST /api/v1/applications/defaults?env=ENV&app=APP&overwrite=false&update_definition=false&execute=false&plan_digest=`
  accepts a raw `kms-config-defaults/v1` JSON artifact. Preview is the default.
  The response contains the artifact profile and schema digest, an opaque plan
  digest, value-free alias rows (`create`, `unchanged`, `update`, or `blocked`),
  missing secret names, and whether the application definition differs. To
  execute, submit the same artifact with
  `execute=true` and the fresh preview's `plan_digest`; differing current
  values additionally require `overwrite=true`, while definition drift
  requires `update_definition=true`. The latter replaces the existing
  contract and repins an already registered schema selected by canonical
  digest in the same transaction as the parameter writes. This operation
  never creates applications, namespaces, schemas, secrets, or releases. See
  [`managed-defaults.md`](managed-defaults.md).

### Console aggregates

The console's application page, fleet overview, Ship modal, and rollout view
each need one round-trip. These admin-only endpoints compute the aggregate on
the server so the frontend renders state rather than deriving it. Their
response shapes are pinned by Go-generated fixtures under
`frontend/tests/fixtures/backend/` (see
[`testing.md`](testing.md#serverfrontend-contract-fixtures)); the DTOs in
`internal/server/httpserver/dto_console.go` mirror the "Console aggregates"
block of `frontend/lib/types.ts` field for field. Nothing in this section
returns a parameter value the caller did not just send, secret plaintext, or
a token.

- `GET /api/v1/applications/get?name=` → `{"application": Application}`
  (404 when the application does not exist). `Application` is the object
  returned by the list and create endpoints above (`name`, `description`,
  `release_name`, `schema_version`, `contract`, `created_by`,
  `created_at_unix_ms`, `updated_at_unix_ms`, `archived_at_unix_ms`,
  `archived_by`, `environment_count`).
- `GET /api/v1/applications/overview` — **fleet form**, no query →
  ```json
  { "applications": [
      { "application": Application, "status": "attention",
        "environments": [
          { "env": "dev",  "status": "ready", "production": false },
          { "env": "prod", "status": "drift", "production": true } ] } ] }
  ```
  This is the cheap listing: per-environment status is computed from the
  configuration rows and the active/latest release only, without subscriber
  acknowledgements and without re-validating the active release's pins. It
  therefore never reports `degraded`, `rolling`, or a `release_pin_stale`
  block; those appear only in the per-application form, which the console
  fetches for at most 25 applications to fill in the fleet cards. Every
  application is listed (no paging).
- `GET /api/v1/applications/overview?name=[&env=]` — **per-application
  form** → `ApplicationOverview` (below). All environments are included
  unless `env=` selects some: it may be repeated (`env=dev&env=prod`) or
  comma-joined (`env=dev,prod`); an environment the application does not
  have is 404. More than **64 environments** in one response is
  `failed_precondition` (412) — narrow with `env=`. Subscriber state is
  folded from at most 1,000 acknowledgement rows per environment.
- `POST /api/v1/applications/ship` → `ShipResult` — write values, create a
  release, and activate it in one call ([Ship](#ship)).
- `POST /api/v1/applications/environments/clone` → `CloneEnvironmentResponse`
  ([Clone an environment](#clone-an-environment)).
- `POST /api/v1/releases/rollback` → `RollbackResponse`
  ([Rollback](#rollback)).
- `GET /api/v1/release-subscribers/stream?env=&app=&name=` →
  `text/event-stream` ([Subscriber stream](#subscriber-stream)).

#### ApplicationOverview

Taken from the `overview-incident.json` fixture (abridged):

```json
{
  "application": Application,
  "status": "attention",
  "findings": [],
  "schema_json": "{\"type\":\"object\",\"properties\":{…},\"required\":[…],\"additionalProperties\":false}",
  "rows": [ ApplicationConfigurationRow ],
  "environments": [
    {
      "namespace": Namespace,
      "production": true,
      "status": "degraded",
      "values_state": "complete",
      "release_state": "drift",
      "rollout_state": "degraded",
      "values": [
        { "alias": "database", "kind": "parameter", "key": "database",
          "present": true, "content_type": "json",
          "current_version": 1, "pinned_version": 1 },
        { "alias": "db_password", "kind": "secret", "key": "db_password",
          "present": true, "content_type": "text/plain",
          "current_version": 1, "pinned_version": 1 },
        { "alias": "rate_limits", "kind": "parameter", "key": "rate_limits",
          "present": true, "content_type": "integer",
          "current_version": 4, "pinned_version": 3 }
      ],
      "release": {
        "active": {
          "name": "runtime", "version": 2, "activation_revision": 12,
          "previous_version": 1, "created_by": "admin",
          "created_at_unix_ms": 1755000000000, "is_rolled_back": false,
          "schema_version": 1,
          "digest": "<sha256 hex>", "entries": [ ConfigurationReleaseEntry ]
        },
        "latest_version": 2,
        "release_count": 2
      },
      "rollout": {
        "total": 3, "connected": 3, "applied_current": 2, "applied_divergent": 0,
        "rejected": 1, "pending": 0, "stale": 0, "other_release_names": [],
        "rejected_instances": [
          { "identity": "admin", "client_name": "api", "instance_id": "prod-3",
            "state": "rejected", "release_version": 2, "activation_revision": 12,
            "rejection_category": "config_validation_failed", "diagnostic": "",
            "connected": true, "server_timestamp_unix_ms": 1755000000000,
            "applied_divergent": false, "divergent_field_count": 0 }
        ],
        "truncated": false
      },
      "findings": [
        { "code": "unreleased_changes", "severity": "warning",
          "scope": { "env": "prod", "alias": "rate_limits" },
          "params": { "alias": "rate_limits", "current": 4, "pinned": 3 } },
        { "code": "instance_rejected", "severity": "warning",
          "scope": { "env": "prod", "instance": "prod-3" },
          "params": { "category": "config_validation_failed", "client_name": "api",
                      "identity": "admin", "instance_id": "prod-3" } },
        { "code": "production", "severity": "info",
          "scope": { "env": "prod" }, "params": {} }
      ]
    }
  ]
}
```

`rows` is the same cross-environment matrix returned by
`GET /api/v1/applications/dashboard` (the Matrix tab), restricted to the
selected environments. `schema_json` is present only when the application
pins a schema the registry still has. `release.active` is absent when nothing
is active; `values[].key`, `content_type`, `current_version`,
`pinned_version`, and `bound` are omitted when zero or empty.
`rollout.applied_divergent` counts instances that applied the current
revision while reporting `applied_divergent` (their generation differs from
the application's source-owned defaults); it is a warning that never degrades
the rollout state. Each instance carries `applied_divergent` and
`divergent_field_count` (field names and values are never sent).
`rollout.rejected_instances` holds at most 50 instances and `truncated` says
whether more were dropped; the full list comes from
`GET /api/v1/release-subscribers`. Each instance is the effective lifecycle
row for one `(identity, client_name, instance_id)`: the per-state subscriber
rows collapsed to the highest lifecycle state at the instance's newest
activation revision, `connected` if any row is, sorted by identity, client,
instance (the same grouping as `frontend/lib/subscribers.ts`). `diagnostic`
is the persisted redaction marker, never the client's text (see
[`configuration-releases.md`](configuration-releases.md#release-contents-and-digest)).
`other_release_names` lists release names, sorted, that instances of this
namespace subscribe to other than the application's `release_name` (an SDK
configured with the wrong name).

#### Readiness model

Readiness is computed on the server per environment
(`internal/core/application_readiness.go`, pure functions over data the
overview already fetched) from: the application contract and schema pin, the
namespace's parameter and secret rows, the `current` state of each contract
secret, the active release and its entries (re-validated in memory), the
latest release version and count, and the namespace's grouped subscriber
instances. The frontend renders these states and never re-derives them.

Contract aliases are resolved to keys with one rule shared by readiness,
Ship, and clone (**alias → key resolution**): the active release's entry for
the alias → the latest release's entry → a resource whose key equals the
alias in this namespace → the key name another environment's active release
uses for that alias → unresolved. The last fallback borrows only the key name:
the resolved resource must still exist in the target environment's own
namespace, because 0.3 release pins are always home-namespace. `values[].key`
carries the resolved key so the UI can show both; an unresolved alias has no
`key` and `present: false`.

Three column states per environment:

| field | values | meaning |
|---|---|---|
| `values_state` | `empty` | the namespace has no parameter or secret rows at all |
| | `incomplete` | any contract alias is unresolved or absent (`resource_missing`), exists under the other kind (`kind_mismatch`), is a parameter whose content type differs from the contract (`content_type_mismatch`), or is a secret whose `current` version is disabled, destroyed, or expired (`secret_unreadable`) |
| | `complete` | every contract alias resolves to a matching, readable resource |
| `release_state` | `none` | no active release |
| | `active` | the active release matches the contract and pins the current version of every alias |
| | `drift` | a contract alias is missing from the active release (`alias_not_in_release`), or an active entry pins a version other than the resource's current one (`unreleased_changes`) |
| | `blocked` | the pinned schema is missing from the registry (`schema_missing`), the active release's aliases/kinds/content types no longer match the contract (`contract_release_mismatch`), or an active pin fails re-validation (`release_pin_stale`); takes precedence over `drift` |
| `rollout_state` | `no_subscribers` | no instance has registered for this release name |
| | `applied` | every instance applied the current activation revision |
| | `degraded` | any instance rejected the current activation revision |
| | `rolling` | otherwise, any instance is below `applied` at the current revision and is connected or was seen within the last 90 s (`rollout.pending`) |
| | `stale` | otherwise, any instance has been disconnected for more than 90 s without applying the current revision (`rollout.stale`) |

`rollout` counts and `rollout_state` are always populated, but rollout
**findings** (`no_subscribers`, `subscriber_other_release`, `instance_*`) are
emitted only while `release_state` is `active` or `drift`, and at most 50
instance findings per environment.

Environment `status` is the first match in this order:
`blocked` (release state blocked) → `empty` → `incomplete` → `unreleased`
(values complete, no active release) → `degraded` → `rolling` → `drift` →
`ready`.

Application `status` is: `blocked` when any environment is blocked or any
application-level finding is blocking (`schema_missing`,
`schema_required_missing_alias`); otherwise `setup` when there are no
environments or every environment is `empty`, `incomplete`, or `unreleased`;
otherwise `attention` when any environment is `incomplete`, `unreleased`,
`degraded`, `rolling`, or `drift`, or any warning finding exists at either
level — except `insecure_listener`, which is reported but deliberately kept
out of the status so a loopback development install is not permanently
"attention"; otherwise `ready`.

`release.active.is_rolled_back` is `previous_version > 0 &&
active.version < previous_version` — the active release is older than the one
it replaced. The console demotes the Roll back button to "Re-activate vN" in
that state.

**Findings.** A finding is `{code, severity, scope, params}`. `severity` is
`blocking`, `warning`, or `info`; `scope` names the `env`, `alias`, and/or
`instance` the finding applies to (empty keys are omitted; application-level
findings have `{}` or `{alias}`); `params` carries only names and numbers the
frontend needs to render copy — display text lives in
`frontend/lib/readiness.ts`, and no finding ever carries a value, secret
material, or a client diagnostic string. Each `findings` list is sorted
blocking → warning → info and, within a severity, in emission order
(values → release → rollout).

| code | severity | scope | params | console fix action |
|---|---|---|---|---|
| `no_environments` | warning | app | — | add environment |
| `contract_empty` | warning | app | — | edit contract (offers derive-from-active-release) |
| `schema_unpinned` | info | app | — | pin schema |
| `schema_missing` | blocking | app | `application`, `release_name`, `schema_version` | pin schema (the pinned version is no longer in the registry) |
| `schema_property_missing_alias` | warning | alias | `alias` | edit contract (derive) — schema has no property for a parameter alias and is open (`additionalProperties` not `false`) |
| `alias_not_in_schema` | warning | alias | `alias` | edit contract (derive) — same, but the schema is closed |
| `schema_required_missing_alias` | blocking | alias | `alias` | edit contract (derive) — schema requires a name that is not a parameter alias |
| `contract_type_mismatch` | warning | alias | `alias`, `content_type`, `schema_type` | edit contract (derive) |
| `contract_release_mismatch` | blocking | env | — | edit contract / open release |
| `release_pin_stale` | blocking | env + alias | `alias`, `reason` (a validation code) | ship |
| `resource_missing` | blocking | env + alias | `alias`, `kind` | create parameter / create secret |
| `kind_mismatch` | blocking | env + alias | `alias`, `kind`, `found` | open resource |
| `content_type_mismatch` | blocking | env + alias | `alias`, `content_type`, `found` | open resource |
| `secret_unreadable` | blocking | env + alias | `alias`, `state` (`disabled`, `destroyed`, `expired`) | open secret |
| `secret_token_required` | info | env + alias | `alias` | open secret |
| `no_active_release` | warning | env | — | ship |
| `unreleased_changes` | warning | env + alias | `alias`, `current`, `pinned` | ship |
| `alias_not_in_release` | warning | env + alias | `alias` | ship |
| `no_subscribers` | info | env | — | connect SDK |
| `subscriber_other_release` | warning | env | `count`, `names` | connect SDK |
| `instance_rejected` | warning | env + instance | `client_name`, `instance_id`, `identity`, `category` | open subscribers |
| `instance_divergent` | warning | env + instance | `client_name`, `instance_id`, `identity`, `divergent_fields` | open subscribers |
| `instance_pending` | info | env + instance | `client_name`, `instance_id`, `identity` | open subscribers |
| `instance_stale` | info | env + instance | `client_name`, `instance_id`, `identity` | open subscribers |
| `rolled_back` | info | env | `from` | open release |
| `previous_unavailable` | info | env | — | — (no `previous` label yet) |
| `production` | info | env | — | — |
| `insecure_listener` | warning | app | — | open health (the request reached a cleartext listener) |

**JSON schema type ↔ parameter content type.** The console derives a contract
from a schema (and a schema from a contract) with one mapping,
`JSONTypeToContentType` in Go, mirrored in `frontend/lib/contract-derive.ts`
and pinned by the `type_mapping` block of the `readiness-cases.json` fixture:

| schema `type` | content type | notes |
|---|---|---|
| `object`, `array` | `json` | |
| `string` | `string` | `"format": "kms-base64"` → `binary` |
| `integer` | `integer` | |
| `number` | `float` | |
| `boolean` | `boolean` | |
| anything else, or no `type` | `json` | |

The reverse mapping emits `{}` for `json` and `{"type": …}` for the others;
a derived schema lists every parameter alias in `required` and sets
`additionalProperties: false`. Only parameter aliases enter the validated
object — secrets never appear in a schema (see
[`configuration-releases.md`](configuration-releases.md#optional-schema-registry)).

#### Ship

`POST /api/v1/applications/ship` performs the console's "Quick change" as one
request: write new parameter versions, create a release whose entries pin
them, and activate it with a compare-and-swap guard.

```json
{
  "application": "gradethis",
  "environment": "prod",
  "changes": [
    { "alias": "rate_limits", "value": "20", "content_type": "integer" },
    { "alias": "database", "version": 3 },
    { "alias": "db_password", "label": "current" }
  ],
  "metadata_json": "{}",
  "dry_run": false,
  "expected_active_version": 7,
  "request_id": "8f3a2c1e-…"
}
```

Each `changes[]` item names a contract alias (once) and exactly one of:

- `value` (+ optional `content_type`, which defaults to the contract's and
  must equal it) — write a **new parameter version** and pin it. Only
  contract **parameter** aliases may carry a value; a value for a secret
  alias is `invalid_argument` (400) — secret plaintext is never shipped. The
  value is checked against its content type (size, JSON, numeric, boolean,
  base64) before anything else happens.
- `version` or `label` (not both) — **pin without writing**. This is how the
  console opts an unreleased version (`database v3`) into the release, pins a
  secret (`db_password` at `current`), or retries with a parameter version
  written by an earlier attempt.

Aliases not listed keep their **base** pin — the active release's entry, or
the resource's `current` label when nothing is active — and are reported as
`included` in the preview. A newer unreleased version of an untouched alias
is **not** silently picked up: it is reported as an `unreleased_changes`
warning in `preview.warnings`, and the console offers a per-alias opt-in
that resends it as `{alias, version}`. A first release therefore needs every
parameter alias either listed with a value or already present.
`metadata_json` must be a JSON object; it is stored on the created release
with `"source": "console.ship"` added. `expected_active_version` is a
pre-write guard: omit it for none, `0` to require that nothing is active, or
the version the caller previewed. `request_id` is accepted for forward
compatibility but **not used**: there is no idempotency window, and a repeat
ships again.

**Preflight** runs before anything is written and is the **only** source of
non-200 responses on a well-formed call: admin required (403; audited as
`application.ship` deny); the application and the target namespace must
exist (404); the contract must be non-empty (412); every alias must be in
the contract, listed once, with a well-formed change and matching content
type, and `metadata_json` must be an object (400); and, when
`expected_active_version` is given, the currently active version must still
match — otherwise `aborted` (409) with nothing written. After preflight the
server builds the candidate from the base pins plus the changes, resolves
the explicit pins, checks the candidate against the contract, and
**validates it in memory** (resource identity, content types, readable
secrets, digests, and the pinned schema against the unsaved values) before
touching storage. Those checks fill `preview.validation`; a contract
mismatch surfaces there as code `contract_mismatch`, and an alias that
resolves to no resource as `not_found` with `preview.entries[].change:
"missing"`.

Every evaluated outcome is **HTTP 200** with `ShipResult.status`:

| `status` | parameters written | release created | activated | `error.code` |
|---|---|---|---|---|
| `preview` | no | no | no | — (`dry_run: true`; not audited) |
| `rejected` | no | no | no | `failed_precondition` + `validation_errors` — in-memory validation failed |
| `activated` | yes | yes | yes | — |
| `release_created_not_activated` | yes | yes | no | `failed_precondition` + `validation_errors` — activation's own re-validation failed (a secret disabled, the contract edited, … between preview and ship) |
| `conflict` | yes | yes | no | `aborted` + `current_version` (the version now active) — another activation moved the release between the preflight read and activation |

Execution is sequential — `PutParameter` per value change,
`CreateConfigurationRelease`, then `ActivateConfigurationRelease` with a CAS
guard on the version observed at preflight (so `conflict` can occur even when
the caller sent no `expected_active_version`) — with **no compensation**:
each step is already atomic, and a parameter version or release left behind
by a later failure is inspectable, harmless, and reusable. A storage or
authorization failure in the middle of execution is returned as an ordinary
error response after the audit event; parameter versions already written
persist. The console's "Fix and retry" ships again with `changes[].version`
set to the versions reported in `parameters[]`, so a value is never written
twice.

Response for an `activated` ship (the `ship-conflict.json` fixture shows the
`conflict` shape):

```json
{
  "status": "activated",
  "preview": {
    "base_version": 7, "release_name": "runtime",
    "schema_version": 1,
    "entries": [
      { "alias": "database",    "kind": "parameter", "key": "database",
        "from_version": 3, "to_version": 3,  "change": "pinned" },
      { "alias": "db_password", "kind": "secret",    "key": "db_password",
        "from_version": 2, "to_version": 2,  "change": "pinned" },
      { "alias": "rate_limits", "kind": "parameter", "key": "rate_limits",
        "from_version": 10, "to_version": 11, "change": "edited" }
    ],
    "validation": { "valid": true, "errors": [] },
    "warnings": []
  },
  "parameters": [
    { "alias": "rate_limits", "key": "rate_limits", "version": 11, "revision": 52 }
  ],
  "release": { "name": "runtime", "version": 9, "digest": "<sha256 hex>" },
  "activation": { "activation_revision": 53, "previous_version": 7, "changed": true }
}
```

`preview` is always present (a `dry_run` response is `preview` plus an empty
`parameters` list). `preview.entries` has one row per contract alias, sorted
by alias, with `change` = `edited` (a value is written; `to_version` is the
predicted next version until the write happens, then the real one), `pinned`
(explicit version/label), `included` (untouched alias at its base pin), or
`missing` (nothing resolves; the preview is invalid). `from_version` is the
active pin (omitted for a first release); `base_version` is the active
release the candidate was built on (`0` for a first release). `validation`
has the shape of `POST /api/v1/releases/validate`; when invalid, the console
disables Ship. `parameters` lists the versions written; `release` and
`activation` are present only when that step happened. `error` is present
for `rejected`, `release_created_not_activated`, and `conflict`.

Every non-`dry_run` call is audited as one `application.ship` event on the
application in the target namespace: `allow` when activated, `deny` for
`rejected` (`reason: validation_failed`, nothing written),
`release_created_not_activated` (`activation_validation_failed`) and
`conflict` (`cas_conflict`), or `error` when a step failed; metadata carries
`environment`, `aliases` (comma-joined, sorted), `activated`,
`previous_version`, `release_version` once one exists, and `reason`. The
ordinary per-step events for the parameter writes,
`configuration_release.create`, and `configuration_release.activate` are
recorded as well.

#### Clone an environment

`POST /api/v1/applications/environments/clone` creates an additional
environment for an application, optionally seeded from an existing one:

```json
{ "application": "gradethis", "source_env": "dev", "target_env": "prod",
  "copy_values": true, "auth_methods": ["mtls"], "description": "Production" }
```

→

```json
{
  "namespace": Namespace,
  "namespace_created": true,
  "items": [
    { "alias": "rate_limits", "key": "rate_limits", "kind": "parameter",
      "action": "copied", "source_version": 2, "target_version": 1 },
    { "alias": "database", "key": "database", "kind": "parameter",
      "action": "exists", "source_version": 1, "target_version": 4 },
    { "alias": "db_password", "key": "db_password", "kind": "secret",
      "action": "needs_value", "source_version": 1 }
  ],
  "needs_value": ["db_password"]
}
```

`source_env` and `target_env` must differ (400) and the source namespace
must exist (404). The target namespace is created when missing, with the
source's `allowed_auth_methods` unless `auth_methods` is given; an existing
target namespace is attached (`namespace_created: false`), not an error. The
item list is the application contract resolved to keys with the alias → key
rule in the source environment, or — when the contract is empty — every
parameter and secret present in the source, keyed by its own name. Per item,
`action` is decided in this order:

- `exists` — the key already exists in the target (whatever its kind) and is
  **left untouched**; clone never overwrites;
- `needs_value` — a secret: secret values are never copied, so the operator
  must add one in the target (the console offers an Add secret button per
  entry);
- `missing_in_source` — the alias does not resolve to a present resource in
  the source;
- `needs_value` — a parameter when `copy_values` is false;
- `copied` — a new parameter version was written in the target with the
  source's current value and content type (`target_version` is the new
  version);
- `error` — the write failed; `error` carries a bounded message and the
  remaining items are still processed.

`source_version` is set whenever the source has the key. `needs_value`
repeats the aliases that still need a value. Partial failure is reported per
item, not as a 4xx. Audited as `application.environment_clone` on the target
namespace (metadata `source_env`, `target_env`, `namespace_created`,
`copied`, `needs_value` counts) plus the ordinary namespace-create and
parameter-write events.

#### Rollback

`POST /api/v1/releases/rollback` re-activates the release name's `previous`
version. It requires `configuration-release:activate`, like activate.

```json
{ "env": "prod", "app": "gradethis", "name": "runtime", "expected_current_version": 13 }
```

→

```json
{ "release": ConfigurationRelease, "activation_revision": 120,
  "previous_version": 13, "rolled_back_from": 13, "changed": true }
```

`rolled_back_from` is the version that was active when the request was
evaluated; `previous_version` is the new `previous` label, which after a
successful rollback is that same version. The response is
`failed_precondition` (412) when the release name has **no active version**
or the active version has **no previous** (`previous_version` is 0); neither
carries `validation_errors`. `expected_current_version` is a CAS guard (omit
for none): a mismatch is `aborted` (409) and audited as
`configuration_release.cas_conflict`. The activation itself then runs with
a CAS on the observed current version and the same semantics as
`POST /api/v1/releases/activate`: the target is re-validated immediately
before the labels move, and a failure is `failed_precondition` (412) with
the `validation_errors` envelope documented under activate (the previous
release can be un-activatable when a pinned secret was disabled or expired,
or the contract was edited); `changed: false` means the target was already
active and no revision was allocated. The activation is audited as
`configuration_release.rollback`. The console's Rollback dialog calls
`POST /api/v1/releases/validate` on the previous version first so the
operator sees violations before confirming; to activate any other retained
version use `POST /api/v1/releases/activate`.

#### Subscriber stream

`GET /api/v1/release-subscribers/stream?env=&app=&name=` pushes the
per-instance lifecycle state of one release name as **server-sent events**
(`Content-Type: text/event-stream; charset=utf-8`, `Cache-Control:
no-store`, `X-Accel-Buffering: no`). It is the live transport behind the
console's rollout view; `GET /api/v1/release-subscribers` remains the paged,
poll-friendly form. Admin-only, like the list.

- Authentication is the same bearer header as every other endpoint. The
  browser `EventSource` API cannot set request headers, so consume the stream
  with `fetch` and read the body as a stream (`frontend/lib/sse.ts` does
  this); there is no cookie or query-string token form.
- The first event is an immediate `snapshot`. A new `snapshot` follows
  whenever an acknowledgement, a connect/disconnect transition, or an
  activation touches that release name — the server-side notifier wakes the
  stream and a 250 ms debounce coalesces bursts into one event — and on a
  5 s safety re-query in case a change was not signalled.
- A comment line `: keep-alive` is written every 15 s so idle proxies keep
  the connection open.
- A stream lives at most **5 minutes**; the server then sends
  `event: end` with `data: {"reason":"lifetime"}` and closes, so clients
  periodically re-authenticate by reconnecting (the console uses a 1 → 30 s
  jittered backoff). Every reconnect starts with a fresh snapshot, so
  nothing is lost.
- Limits: **4** concurrent streams per identity and **64** per server. Beyond
  either, the response is `rate_limited` (429) and the refusal is audited as
  `configuration_release.subscribers_stream` deny with reason
  `identity_stream_limit` or `global_stream_limit`. The first snapshot is
  authorized and read before the slot is taken, so an unauthorized or
  malformed request gets its ordinary 4xx rather than a 429.
- After two consecutive failures the console falls back to polling
  `GET /api/v1/release-subscribers` every 5 s while the tab is visible and
  shows the transport badge as `polling`; a stream that stops delivering is
  shown as `stale`.

```text
event: snapshot
data: {"summary":{"total":3,"connected":3,"applied_current":2,"rejected":1,"pending":0,"stale":0,"other_release_names":[],"rejected_instances":[…],"truncated":false},"subscribers":[ReleaseSubscriberState…],"current_revision":12,"server_time_unix_ms":1755000000000}

: keep-alive

event: end
data: {"reason":"lifetime"}
```

`summary` has the shape of `EnvironmentOverview.rollout`, computed over at
most 1,000 acknowledgement rows for that release name; `subscribers` are
those raw rows, as returned by `GET /api/v1/release-subscribers`, ungrouped;
`current_revision` is the release name's active activation revision (`0`
when nothing is active). Reverse proxies in front of the HTTP listener must
not buffer this path (`X-Accel-Buffering: no` covers nginx; disable
`proxy_buffering`/equivalent elsewhere) and must allow idle connections of
at least the 5-minute lifetime. The handler clears the server's write
deadline for its own response; every other route keeps the normal 60 s
timeout.

### Per-namespace allowed auth methods

Each namespace declares which authentication methods admit a caller into it,
stored as `allowed_auth_methods` (a JSON array of `"mtls"` and/or `"token"`).
The gate is enforced on **every** operation in the namespace, including reads
of unprotected parameters — a namespace can require mTLS-only, which makes
bearer-token theft useless for it. New namespaces default to `["mtls"]`
(strongest posture); adding `"token"` is an explicit, audited namespace-settings
change.

Client identities normally present mTLS certificates on the gRPC listener; the
HTTP listener also accepts a client certificate, which is how an admin
satisfies the certificate half of its credentials. The HTTP API exposes the
same namespace configuration, so the two methods are summarized here for
completeness:

- **mTLS** clients authenticate with a client certificate minted by the
  built-in CA (see `GET /api/v1/ca` and the identities endpoints). Certificates
  prove possession. The private key is generated server-side, returned once,
  never persisted by the server, and must then be retained by the client.
- **token** clients authenticate with a bearer token, which is
  possession-free: whoever holds the string is treated as the app.

The auth-method gate applies to **client-kind** identities. **Admin-kind**
identities are the management plane: they administer any namespace, so no
per-namespace `allowed_auth_methods` list applies to them and they are not
subject to data-plane policy checks. They are admitted by the stricter rule
described under [Authentication](#authentication) instead — certificate *and*
token — so an admin reports `auth_method: "mtls"` from `/whoami` while the
requirement is enforced.
Admin identities never bypass auditing or the cryptographic impossibility of
revealing a bound secret without its binding key.

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
| rate_limited         | 429  |
| internal             | 500  |
| unavailable (not ready) | 503 |
| purge_cleanup_pending | 503 |

A `permission_denied` from the auth-method gate carries an explicit message
(e.g. "namespace requires mtls"). Error messages never contain secret values or
token or binding-key material. A purge whose transaction committed but whose
active SQLite/WAL scrub is still pending returns HTTP 503 with code
`purge_cleanup_pending` and the fixed message
`secret purge committed; database artifact cleanup is pending`;
the service remains fail-closed until cleanup succeeds.

## Common types

Timestamps are Unix milliseconds (`*_unix_ms`, integer). Binary values are
base64 (`value_base64`). Parameter and secret resource references are flattened
at the top level as `env`, `app`, `key`; configuration release entries retain a
structured `ref`; every release entry must nevertheless point into the
release's own namespace. List
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
  "bound": false,
  "has_access_token": true,
  "metadata_json": "{}",
  "created_at_unix_ms": 0,
  "updated_at_unix_ms": 0,
  "labels": { "current": 2, "previous": 1 },
  "versions": [
    { "version": 2, "state": "enabled", "created_by": "admin",
      "created_at_unix_ms": 0, "destroyed_at_unix_ms": 0,
      "expires_at_unix_ms": 0, "metadata_json": "{}",
      "bound": false, "has_access_token": true }
  ]
}
```

Top-level `bound` summarizes the version selected by `current`; top-level
`has_access_token` reports whether the secret currently has an access-token
hash. Exact-version decisions use both fields on that item in `versions`;
binding is mutable live metadata, not an attribute of a release pin. Purged
tombstones have `state: "destroyed"` with both version flags false.

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
  "schema_version": 1,
  "entries": [
    { "alias": "rate_limits", "kind": "parameter",
      "ref": { "namespace": { "env": "prod", "app": "gradethis" },
               "key": "config/rate-limits" },
      "version": 8, "content_type": "json", "metadata_json": "{}",
      "parameter_digest": "<sha256 hex>" },
    { "alias": "db_password", "kind": "secret",
      "ref": { "namespace": { "env": "prod", "app": "gradethis" },
               "key": "db-password" },
      "version": 3, "content_type": "text/plain", "metadata_json": "{}",
      "parameter_digest": "" }
  ],
  "digest": "<deterministic sha256 hex>",
  "metadata_json": "{}", "created_by": "admin",
  "created_at_unix_ms": 1710000000000
}
```

Release values, secret plaintext, credentials, and live protection flags never
appear in this DTO or its digest. Creation selectors use the same `ref` shape,
plus either `version` or `label`. Both parameter and secret references must
exactly equal the release namespace; an empty selector namespace means that
namespace.

## Endpoints

### Auth & health

- `POST /api/v1/auth/login` — no auth — body `{"token": "..."}` →
  `{"identity": {"name": "...", "kind": "admin|client"},
    "auth_method": "mtls|token"}`
  A token is always required in the body; a certificate alone never signs
  anyone in. `auth_method` reports how the caller was resolved — `mtls` when a
  chain-verified client certificate on this connection named the same identity
  as the token, `token` otherwise — and is `mtls` for every admin while the
  client-certificate requirement is enforced. Every failure is the same
  `401 unauthenticated`.
- `GET /api/v1/health` — no auth →
  `{"healthy": true, "ready": true, "version": "...", "current_revision": 42,
    "grpc_addr": "0.0.0.0:8443", "tls_enabled": true,
    "admin_client_cert_required": true, "client_cert_presented": false}`
  `grpc_addr` is the configured gRPC listen address (empty when the gRPC
  server is not wired); `tls_enabled` is true when the server was started with
  TLS or the request itself arrived over TLS.
  `admin_client_cert_required` is the server's **effective** setting — false
  when `security.admin_require_client_cert` is off *or* TLS is off, since the
  requirement cannot be enforced without a handshake.
  `client_cert_presented` is true when *this* connection carried a client
  certificate the TLS layer chain-verified against the built-in CA; it says
  nothing about whether that certificate is enrolled, unrevoked, or names an
  admin. Together they let the login page explain a refusal before it
  happens (`required && !presented` → the browser has no certificate loaded)
  without revealing anything about any token's validity.
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
- `DELETE /api/v1/namespaces?env=&app=` → `{}`. Live parameters, secrets, or
  bound identities block deletion; environment-scoped release/subscriber rows
  are retired atomically so the owning application can subsequently be archived.
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
    "metadata_json": "{}", "binding_key": "",
    "generate_access_token": false, "expires_at_unix_ms": 0 }
  ```
  → `{"version": 1, "revision": 7, "access_token": "..."}`
  (`access_token` present only when `generate_access_token` was true — shown
  once, never again.) A non-empty `binding_key` creates a bound version; empty
  creates an unbound version, independent of the preceding version. A non-empty
  key must be opaque valid UTF-8 of at least 32 bytes. Access-token generation
  is independent and there is no write-side `secret_token`.
- `POST /api/v1/secrets/reveal` — `{"env","app","key","version": 0,"label": "","secret_token": "...","binding_key":"..."}` →
  `{"env","app","key","version","value_base64","content_type"}`.
  Admin only. Every successful reveal and decryption failure is audited as a
  reveal event. This administrator break-glass path deliberately bypasses the
  access-token gate, so `secret_token` is accepted for transport symmetry but
  ignored. A bound version still requires `binding_key`, because KMS cannot
  decrypt it without that material. Per-secret credentials are not accepted in
  custom headers, avoiding exposure through proxy configurations that log
  them. Request bodies are not logged by the server, and credentials are never
  included in the response or audit event. Missing, wrong, and unusable
  binding-key material collapse to the same sanitized credential/decryption
  response. By contrast, data-plane gRPC `GetSecret` enforces the exact
  version's access-token and binding-key requirements independently and needs
  both when both flags are set.
- `POST /api/v1/secrets/disable` — `{"env","app","key","version": 0,"enable": false}` →
  `{"revision"}` (`version: 0` = all versions; `enable: true` re-enables.)
- `POST /api/v1/secrets/destroy` — `{"env","app","key","version"}` →
  `{"revision"}` (irreversible)
- `POST /api/v1/secrets/promote` — `{"env","app","key","version"}` →
  `{"current_version", "previous_version", "revision"}`
- `POST /api/v1/secrets/bind` — `{"env","app","key","version":0,"binding_key":"..."}` →
  `{"anchor_version","affected_versions":[N],"revision"}`. Rewraps one exact
  version in place; `0` selects `current`.
- `POST /api/v1/secrets/unbind` — the same shape and response; the supplied key
  must open the selected bound version.
- `POST /api/v1/secrets/binding-cohort/preview` —
  `{"env","app","key","anchor_version":0,"binding_key":"..."}` →
  `{"anchor_version","affected_versions":[...],"revision"}` without mutation.
- `POST /api/v1/secrets/binding-key/rotate` — preview-shaped body plus
  `new_binding_key` and optional paired `expected_revision` /
  `expected_affected_versions` compare-and-swap guards → the cohort result.
- `POST /api/v1/secrets/binding-cohort/purge` — admin only; preview-shaped body
  plus the optional paired guards → the cohort result. This irreversibly
  destroys the contiguous matching cohort even when immutable releases pin it.
- `DELETE /api/v1/secrets?env=&app=&key=` → `{"revision"}`

Binding keys are never stored, hashed, fingerprinted, or echoed. Cohorts are
found by cryptographically opening adjacent bound versions and stopping at the
first boundary; see [`binding-keys.md`](binding-keys.md).

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
  For `"kind": "admin"` the only valid credential is a token: an omitted or
  empty `auth_methods` means `["token"]`, and including `"mtls"` is rejected
  with `400 invalid_argument`, because admin certificates are minted only by
  `parameter-store admin-cert issue` on the server host. The response for an
  admin therefore always has `cert: null`, and the new admin cannot sign in
  until a certificate is issued offline.
- `POST /api/v1/identities/rotate` — `{"name"}` → `{"token": "..."}`
  Mints a new bearer token and invalidates the current one (shown once). For
  token identities; mTLS identities rotate via `issue-cert` instead.
- `POST /api/v1/identities/issue-cert` — `{"name", "ttl_seconds": 7776000}` →
  `{"cert": CertBundle}`
  Issues an additional client certificate (shown once). Multiple concurrent
  valid certificates per identity allow zero-downtime rollover.
  An **admin-kind target is refused for every caller**, admins included, with
  `403 permission_denied` (audited `identity.cert.issue` deny,
  `reason: admin_target`, `channel: online`): an admin certificate is minted
  only offline with `parameter-store admin-cert issue NAME --out DIR`, so a
  stolen online admin credential cannot mint durable new admin credentials.
- `POST /api/v1/identities/revoke-cert` — `{"name", "serial"}` → `{}`
  Revokes a single certificate by serial; other certs keep working. Takes
  effect on the next RPC; an existing watch stream is closed on its next
  heartbeat reauthorization. Unlike issuance, this **is** allowed for an
  admin-kind target: containment must not require host access.
- `POST /api/v1/identities/revoke` — `{"name"}` → `{}`
  Disables the identity: its token and **all** of its certificates stop working
  for new RPCs immediately; an existing watch stream is closed on its next
  heartbeat reauthorization.

### Audit

- `GET /api/v1/audit?env=&app=&key_prefix=&actor=&event_type=&decision=&from_unix_ms=&to_unix_ms=&page_size=&page_token=` →
  ```json
  { "events": [ { "id": 1, "event_type": "secret.read", "actor_identity": "gradethis-be",
      "actor_type": "client", "resource_type": "secret",
      "resource_env": "prod", "resource_app": "gradethis", "resource_key": "stripe-api-key",
      "resource_version": 2, "resource_namespace_id": 3, "decision": "allow", "source_ip": "10.0.0.5",
      "user_agent": "", "request_id": "r-123", "created_at_unix_ms": 0,
      "metadata_json": "{}" } ],
    "next_page_token": "" }
  ```
  `env`, `app`, and `key_prefix` scope events to a namespace and key range;
  `decision` selects one recorded outcome and must be `allow`, `deny`, or
  `error` (any other value is `invalid_argument`, 400). All filters are optional
  and combine.
  `resource_namespace_id` is the immutable incarnation of the namespace the
  event was authorized against (`0` for global events and rows that predate
  it), so a deleted-and-recreated `env/app` can be told apart in an export.

### Security posture

- `GET /api/v1/posture?cert_window=&secret_window=` →
  ```json
  { "generated_at": "2026-09-01T12:00:00Z",
    "windows": { "cert": "720h0m0s", "secret": "720h0m0s", "admin_cert": "336h0m0s" },
    "kek": { "active_id": "kek-2026-01", "created_at": "2026-01-04T09:12:00Z",
      "age_seconds": 20779200, "generations": 2 },
    "auth": { "tls_enabled": true, "mtls_enabled": true, "admin_client_cert_required": true },
    "audit": { "enabled": true, "retain_duration": "forever", "archive_enabled": false },
    "metrics_enabled": true,
    "admin_certs": { "lacking": ["ops-oncall"],
      "expiring": [ { "identity": "root", "serial": "3f…", "not_after": "2026-09-10T00:00:00Z" } ] },
    "identity_certs_expiring": { "items": [ { "identity": "gradethis-be", "env": "prod",
        "app": "gradethis", "serial": "a1…", "not_after": "2026-09-14T00:00:00Z" } ],
      "total": 1, "truncated": false },
    "secret_versions_expiring": { "items": [ { "env": "prod", "app": "gradethis",
        "key": "stripe-api-key", "version": 2, "expires_at": "2026-09-20T00:00:00Z" } ],
      "total": 1, "truncated": false },
    "changelog": { "rows": 412, "last_revision": 900, "oldest_revision": 488 } }
  ```
  **Admin only** (403 `permission_denied` for any other identity, 401 before
  that for an unauthenticated one): the snapshot spans every namespace at once,
  which no delegated policy grant scopes. It backs the console's **Security
  posture** page.
  `cert_window` and `secret_window` are the look-ahead for identity
  certificates and secret versions. Each takes a Go duration (`720h`) or a bare
  day count (`30d`); both default to **30d**, the window the
  `kms_*_expiring_soon` gauges sample. Zero, negative, unparseable, and
  anything over `365d` are `invalid_argument` (400) rather than clamped. The
  admin-certificate window is **not** a parameter: it is fixed at 14 days so the
  page, serve's startup warning, and `kms_admin_certs_expiring_soon` agree on
  what "expiring" means.
  Both lists are sorted by expiry ascending and capped at **200** items;
  `total` is the full count behind the list and `truncated` says the two
  disagree. Already-expired certificates and versions are excluded (the
  handshake already enforces those), as are revoked certificates and versions
  that are not `enabled`. `retain_duration` is `"forever"` when audit rows are
  never retired.
  Unlike the rest of this API, its timestamps are RFC 3339 UTC strings rather
  than `*_unix_ms`, so they sit in the same vocabulary as the duration windows
  beside them.

  **Metadata only.** No field carries a secret value, a bearer token or its
  hash, key material, a private key, or a certificate PEM. Identities appear by
  name and certificates by serial and expiry; the KEK appears as an id, a
  creation time, and a generation count. Building the response reads metadata
  rows only — it never unwraps a DEK and no decrypt path is reachable from it.

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
    "name": "runtime", "schema_version": 1,
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
  `{"application":"gradethis","schema_json":"{...}","metadata_json":"{}"}`
  → `201 {"schema":{"application","release_name","version","schema_json","digest",
  "metadata_json","created_by","created_at_unix_ms"}}`.
- `GET /api/v1/configuration-schemas?application=&release_name=&page_size=&page_token=` →
  `{"schemas":[...],"next_page_token":""}`. Coordinate filters are optional.
- `GET /api/v1/release-subscribers?env=&app=&name=&page_size=&page_token=` →
  `{"subscribers":[{"namespace","release_name","client_name","instance_id",
  "identity","state","release_version","activation_revision",
  "rejection_category","diagnostic","client_timestamp_unix_ms",
  "server_timestamp_unix_ms","connected","applied_divergent",
  "divergent_field_count"}],"next_page_token":"",
  "current_revision":42}`. `applied_divergent` is true only on an `applied`
  row whose running generation differs from the application's source-owned
  defaults, and `divergent_field_count` is how many fields differ; both are
  `false`/`0` on every other state and on connection-only rows.
- `POST /api/v1/releases/rollback` re-activates the `previous` version with
  the same CAS and validation-failure semantics as activate; see
  [Rollback](#rollback) under Console aggregates.
- `GET /api/v1/release-subscribers/stream?env=&app=&name=` is the
  server-sent-events form of the subscriber list; see
  [Subscriber stream](#subscriber-stream).

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
- `/metrics` is the Prometheus exposition, also outside `/api` and also
  unauthenticated. It is absent (and the path falls through to the frontend
  catch-all) when `metrics.enabled` is false. See
  [`operations.md`](operations.md#prometheus-metrics).
