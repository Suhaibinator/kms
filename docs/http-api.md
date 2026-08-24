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

### Console aggregates

<!-- TODO reconcile with fixtures: this section was written from plan §3
     (contracts) while Lane B was implementing. Diff every JSON example against
     frontend/tests/fixtures/backend/{overview-ready,overview-incident,
     overview-setup,ship-preview,ship-conflict,readiness-cases}.json once
     `go test ./internal/server/httpserver -run TestConsoleFixtures -update`
     has produced them. Shapes were cross-checked against the frozen
     frontend/lib/types.ts. Known open behaviours: whether the fleet form
     consults subscriber state for env status, the exact `error.code` values
     on ShipResult, and the 10-minute `request_id` window. -->

The console's application page, fleet overview, Ship modal, and rollout view
each need one round-trip. These admin-only endpoints compute the aggregate on
the server so the frontend renders state rather than deriving it. Their
response shapes are pinned by Go-generated fixtures under
`frontend/tests/fixtures/backend/` (see
[`testing.md`](testing.md#console-fixtures-and-journeys)). Nothing in this
section ever returns a parameter value the caller did not just send, secret
plaintext, or a token.

- `GET /api/v1/applications/get?name=` → `{"application": Application}`
  (404 when the application does not exist). `Application` is the object
  returned by the list and create endpoints above (`name`, `description`,
  `release_name`, `schema_id`, `schema_version`, `contract`, `created_by`,
  `created_at_unix_ms`, `updated_at_unix_ms`, `environment_count`).
- `GET /api/v1/applications/overview` — **fleet form**, no query →
  ```json
  { "applications": [
      { "application": Application, "status": "attention",
        "environments": [
          { "env": "dev",  "status": "ready",    "production": false },
          { "env": "prod", "status": "degraded", "production": true } ] } ] }
  ```
  This is the cheap listing: it carries no configuration rows, findings, or
  subscriber detail. The console uses it for the fleet grid and fetches the
  per-application form for at most 25 applications to fill in cards.
- `GET /api/v1/applications/overview?name=[&env=]` — **per-application
  form** → `ApplicationOverview` (below). Every environment is included unless
  `env=` selects one. An application with more than **64 environments**
  returns `failed_precondition` (412); pass `env=` to read one environment at
  a time.
- `POST /api/v1/applications/ship` → `ShipResult` — write values, create a
  release, and activate it in one call ([Ship](#ship)).
- `POST /api/v1/applications/environments/clone` → `CloneEnvironmentResponse`
  ([Clone an environment](#clone-an-environment)).
- `POST /api/v1/releases/rollback` → `RollbackResponse`
  ([Rollback](#rollback)).
- `GET /api/v1/release-subscribers/stream?env=&app=&name=` →
  `text/event-stream` ([Subscriber stream](#subscriber-stream)).

#### ApplicationOverview

```json
{
  "application": Application,
  "status": "attention",
  "findings": [
    { "code": "schema_unpinned", "severity": "info", "scope": {}, "params": {} }
  ],
  "schema_json": "{\"type\":\"object\",…}",
  "rows": [ … ],
  "environments": [
    {
      "namespace": Namespace,
      "production": true,
      "status": "drift",
      "values_state": "complete",
      "release_state": "drift",
      "rollout_state": "applied",
      "values": [
        { "alias": "rate_limits", "kind": "parameter", "key": "config/rate-limits",
          "present": true, "content_type": "json",
          "current_version": 10, "pinned_version": 9 },
        { "alias": "db_password", "kind": "secret", "key": "db-password",
          "present": true, "content_type": "text/plain",
          "current_version": 3, "pinned_version": 3, "client_bound": false }
      ],
      "release": {
        "active": {
          "name": "runtime", "version": 12, "activation_revision": 41,
          "previous_version": 11, "created_by": "admin",
          "created_at_unix_ms": 1710000000000, "is_rolled_back": false,
          "schema_id": "gradethis/runtime", "schema_version": 1,
          "digest": "<sha256 hex>", "entries": [ ConfigurationReleaseEntry ]
        },
        "latest_version": 12,
        "release_count": 12
      },
      "rollout": {
        "total": 5, "connected": 5, "applied_current": 5, "rejected": 0,
        "pending": 0, "stale": 0, "other_release_names": [],
        "rejected_instances": [], "truncated": false
      },
      "findings": [
        { "code": "unreleased_changes", "severity": "warning",
          "scope": { "env": "prod", "alias": "rate_limits" },
          "params": { "current": 10, "pinned": 9 } },
        { "code": "production", "severity": "info",
          "scope": { "env": "prod" }, "params": {} }
      ]
    }
  ]
}
```

`rows` is the same cross-environment matrix returned by
`GET /api/v1/applications/dashboard` (the Matrix tab); `schema_json` is the
pinned schema document when the application pins one. `release.active` is
absent when nothing is active. `rollout.rejected_instances` holds at most 50
instances and `truncated` says whether more were dropped; the full list comes
from `GET /api/v1/release-subscribers`. Each instance is the effective
lifecycle row for one `(identity, client_name, instance_id)` — the per-state
subscriber rows collapsed to the latest one, as the Subscribers view groups
them:

```json
{ "identity": "gradethis-be", "client_name": "gradethis-be",
  "instance_id": "gradethis-be-8f3a", "state": "rejected",
  "release_version": 13, "activation_revision": 119,
  "rejection_category": "config_validation_failed", "diagnostic": "",
  "connected": true, "server_timestamp_unix_ms": 1710000000000 }
```

`diagnostic` is the persisted redaction marker, never the client's text (see
[`configuration-releases.md`](configuration-releases.md#release-contents-and-digest)). `other_release_names` lists release names
that connected instances of this namespace subscribe to other than the
application's `release_name` (an SDK configured with the wrong name).

#### Readiness model

Readiness is computed on the server per environment from: the application
contract and schema pin, the namespace's parameter and secret rows, the active
release and its entries, the latest release version, and the grouped
subscriber instances. The frontend renders these states and never re-derives
them.

Contract aliases are resolved to keys with one rule shared by readiness, Ship,
and clone (**alias → key resolution**): the active release's entry for the
alias → the latest release's entry → a resource whose key equals the alias in
this namespace → the key another environment's active release uses for that
alias → unresolved. `values[].key` carries the resolved key so the UI can show
both; an unresolved alias has no `key` and `present: false`.

Three column states per environment:

| field | values | meaning |
|---|---|---|
| `values_state` | `empty` | the namespace has no parameter or secret rows at all |
| | `incomplete` | any contract alias is unresolved, absent, of the wrong kind, or a parameter whose `content_type` differs from the contract |
| | `complete` | every contract alias resolves to a matching resource |
| `release_state` | `none` | no active release |
| | `active` | active release pins exactly the current resource versions |
| | `drift` | any active entry pins a version other than the resource's `current`, or a contract alias is absent from the active release |
| | `blocked` | `contract_release_mismatch`, `release_pin_stale`, or `schema_missing` (activation cannot succeed until fixed) |
| `rollout_state` | `no_subscribers` | no instance has registered for this release name |
| | `applied` | every connected instance applied the current activation revision |
| | `rolling` | any connected instance is below `applied` at the current revision |
| | `degraded` | any connected instance rejected the current activation revision |
| | `stale` | any instance has been disconnected for more than 90 s and last applied a revision below the current one |

`rollout_state` is only reported when `release_state` is `active` or `drift`.

Environment `status` is the first match in this order:
`blocked` → `empty` → `incomplete` → `unreleased` → `degraded` → `rolling` →
`drift` → `ready` (`unreleased` = values complete but no active release).

Application `status` is: `blocked` when any environment is blocked or the
schema pin cannot be loaded (`schema_missing`); otherwise `setup` when there
are no environments or every environment is `empty`, `incomplete`, or
`unreleased`; otherwise `attention` when any environment is `incomplete`,
`unreleased`, `degraded`, `rolling`, or `drift`, or any warning finding
exists; otherwise `ready`.

`release.active.is_rolled_back` is `previous_version > 0 &&
active.version < previous_version` — the active release is older than the one
it replaced. The console demotes the Roll back button to "Re-activate vN" in
that state.

**Findings.** A finding is `{code, severity, scope, params}`. `severity` is
`blocking`, `warning`, or `info`; `scope` names the `env`, `alias`, and/or
`instance` the finding applies to (empty for application-wide findings);
`params` carries only the numbers and identifiers the frontend needs to
render copy (versions, categories, counts) — display text lives in
`frontend/lib/readiness.ts`, and no finding ever carries a value, secret
material, or a client diagnostic string.

| code | severity | scope | params | console fix action |
|---|---|---|---|---|
| `no_environments` | warning | app | — | add environment |
| `contract_empty` | warning | app | — | edit contract (offers derive-from-active-release) |
| `schema_unpinned` | info | app | — | pin schema |
| `schema_missing` | blocking | app | — | pin schema (pinned schema cannot be loaded) |
| `schema_property_missing_alias` | warning | app + alias | — | edit contract (derive) |
| `schema_required_missing_alias` | blocking | app + alias | — | edit contract (derive) |
| `alias_not_in_schema` | warning | app + alias | — | edit contract (derive) |
| `contract_type_mismatch` | warning | app + alias | — | edit contract (derive) |
| `contract_release_mismatch` | blocking | env | — | edit contract / open release |
| `release_pin_stale` | blocking | env, alias | — | ship |
| `resource_missing` | blocking | alias | — | create parameter / create secret |
| `kind_mismatch` | blocking | alias | — | open resource |
| `content_type_mismatch` | blocking | alias | — | open resource |
| `secret_unreadable` | blocking | alias | — | open secret (disabled, expired, or destroyed) |
| `secret_token_required` | info | alias | — | open secret |
| `no_active_release` | warning | env | — | ship |
| `unreleased_changes` | warning | alias | `current`, `pinned` | ship |
| `alias_not_in_release` | warning | alias | — | ship |
| `no_subscribers` | info | env | — | connect SDK |
| `subscriber_other_release` | warning | env | — | connect SDK |
| `instance_rejected` | warning | instance | `category` | open subscribers |
| `instance_pending` | info | instance | — | open subscribers |
| `instance_stale` | info | instance | — | open subscribers |
| `rolled_back` | info | env | — | open release |
| `previous_unavailable` | info | env | — | — |
| `production` | info | env | — | — |
| `insecure_listener` | warning | app | — | open health (`tls_enabled` is false) |

**JSON schema type ↔ parameter content type.** The console derives a contract
from a schema (and a schema from a contract) with one mapping, implemented as
a Go table and mirrored in TypeScript, pinned by the shared
`readiness-cases.json` fixture:

| schema `type` | content type | notes |
|---|---|---|
| `object`, `array` | `json` | |
| `string` | `string` | `"format": "kms-base64"` → `binary` |
| `integer` | `integer` | |
| `number` | `float` | |
| `boolean` | `boolean` | |
| union, absent | `json` | |

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
    { "alias": "rate_limits", "value": "{\"request_limit\":2000}", "content_type": "json" },
    { "alias": "database", "version": 12 },
    { "alias": "db_password", "label": "current" }
  ],
  "metadata_json": "{}",
  "dry_run": false,
  "expected_active_version": 12,
  "request_id": "8f3a2c1e-…"
}
```

Each `changes[]` item names a contract alias and exactly one of:

- `value` (+ optional `content_type`, defaulting to the contract's) — write a
  **new parameter version** and pin it. Only contract **parameter** aliases
  may carry a value; sending a value for a secret alias is
  `invalid_argument` (400) — secret plaintext is never shipped.
- `version` or `label` — **pin without writing**. This is how the console
  opts an existing unreleased change (`database v12`) into the release, pins
  a secret (`db_password` at `current`), or retries with a parameter version
  written by an earlier attempt.

Aliases not listed keep their **base** pin: the active release's entry, or
the resource's `current` label when no release is active (a first release
therefore needs every parameter alias either listed or already present).
`metadata_json` is stored on the created release, merged with
`{"source":"console.ship"}`. `expected_active_version` freezes the release
version the caller previewed (`0` = expect no active release; omit for no
guard). `request_id` is an optional idempotency key: a repeat with the same
id within 10 minutes returns the original result instead of shipping twice.

**Preflight** runs before anything is written and is the **only** source of
4xx responses: admin required (403); application and target namespace must
exist (404); the contract must be non-empty (412); every alias must be in the
contract, each change must be well-formed, and content types must match
(400); and, when `expected_active_version` is given, the currently active
version must still match — otherwise `aborted` (409) with no write. After
preflight the server builds the candidate release and **validates it in
memory** (resource identity, content types, readable secrets, and the pinned
schema against the edited values) before touching storage.

Every evaluated outcome is **HTTP 200** with `ShipResult.status`:

| `status` | parameters written | release created | activated | notes |
|---|---|---|---|---|
| `preview` | no | no | no | `dry_run: true`; not audited |
| `rejected` | no | no | no | in-memory validation failed; `error.validation_errors` |
| `activated` | yes | yes | yes | `activation.changed` is true and a revision was allocated |
| `release_created_not_activated` | yes | yes | no | activation's own re-validation failed (e.g. a secret was disabled or the contract changed between preview and ship); `error.validation_errors` |
| `conflict` | yes | yes | no | another activation moved the active version between preflight and activation; `error.current_version` is the version now active |

Execution is sequential — re-validate, `PutParameter` per value change,
`CreateConfigurationRelease`, then `ActivateConfigurationRelease` with the
CAS guard — with **no compensation**: each step is already atomic, and a
parameter version or release left behind by a later failure is inspectable,
harmless, and reusable. The console's "Fix and retry" ships again with
`changes[].version` set to the versions reported in `parameters[]`, so the
value is not written twice.

```json
{
  "status": "activated",
  "preview": {
    "base_version": 12, "release_name": "runtime",
    "schema_id": "gradethis/runtime", "schema_version": 1,
    "entries": [
      { "alias": "rate_limits", "kind": "parameter", "key": "config/rate-limits",
        "from_version": 9, "to_version": 10, "change": "edited" },
      { "alias": "database", "kind": "parameter", "key": "config/database",
        "from_version": 11, "to_version": 12, "change": "included" },
      { "alias": "db_password", "kind": "secret", "key": "db-password",
        "from_version": 3, "to_version": 3, "change": "pinned" }
    ],
    "validation": { "valid": true, "errors": [] },
    "warnings": [ Finding ]
  },
  "parameters": [
    { "alias": "rate_limits", "key": "config/rate-limits", "version": 10, "revision": 118 }
  ],
  "release": { "name": "runtime", "version": 13, "digest": "<sha256 hex>" },
  "activation": { "activation_revision": 119, "previous_version": 12, "changed": true }
}
```

`preview` is always present (a `dry_run` response contains only `preview`).
`preview.entries[].change` is `edited` (a value was written), `pinned`
(explicit version/label), `included` (an unreleased current version the
caller opted in), or `missing` (a contract alias with no resolvable resource —
the preview is then invalid). `base_version` is the active release the
candidate was built from (`0` for a first release). `validation` has the
shape of `POST /api/v1/releases/validate`; when it is invalid the console
disables Ship. `parameters` lists the versions written, `release` the created
release, and `activation` the activation result; each is present only when
that step happened. `error` is present for `rejected`,
`release_created_not_activated`, and `conflict`, with `code`, `message`, and
either `validation_errors` or `current_version`.

Every non-`dry_run` call is audited as one `application.ship` event
(`decision` allow/deny/error; metadata `environment`, `aliases`,
`activated`, `previous_version`, and `reason` on failure) in addition to the
ordinary per-step events for the parameter writes,
`configuration_release.create`, and `configuration_release.activate`.

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
    { "alias": "rate_limits", "key": "config/rate-limits", "kind": "parameter",
      "action": "copied", "source_version": 9, "target_version": 1 },
    { "alias": "database", "key": "config/database", "kind": "parameter",
      "action": "exists", "target_version": 4 },
    { "alias": "db_password", "key": "db-password", "kind": "secret",
      "action": "needs_value" }
  ],
  "needs_value": ["db_password"]
}
```

The target namespace is created when missing, with the source's
`allowed_auth_methods` unless `auth_methods` is given; an existing target
namespace is attached, not an error. The alias set is the application
contract (or every source parameter when the contract is empty), resolved to
keys with the alias → key rule above. Per item, `action` is:

- `copied` — a new parameter version was written in the target with the
  source's current value and content type;
- `exists` — the key already exists in the target and was **left untouched**
  (clone never overwrites);
- `needs_value` — a secret; secret values are never copied, so the operator
  must add one in the target (the console offers an Add secret button per
  entry). Also used for parameters when `copy_values` is false;
- `missing_in_source` — the alias does not resolve in the source;
- `error` — the write failed; `error` carries a bounded message.

`needs_value` repeats the aliases that still need a value. Partial failure is
reported per item, not as a 4xx. Audited as `application.environment_clone`
plus the ordinary namespace-create and parameter-write events.

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
successful rollback is that same version. Semantics match
`POST /api/v1/releases/activate`: `expected_current_version` is a CAS guard
(omit for none; a mismatch is `aborted`, 409); the target is re-validated
immediately before the labels move, and a failure is `failed_precondition`
(412) with the same `validation_errors` envelope documented under activate
(the previous release can be un-activatable when a pinned secret was disabled
or expired, or the contract was edited); `changed: false` means the target was
already active and no revision was allocated. When there is no `previous`
label the response is `failed_precondition` (412, "no previous release")
without `validation_errors`. The event is audited as
`configuration_release.rollback`. The console's Rollback dialog calls
`POST /api/v1/releases/validate` on the previous version first so the
operator sees violations before confirming; to activate any other retained
version use `POST /api/v1/releases/activate`.

#### Subscriber stream

`GET /api/v1/release-subscribers/stream?env=&app=&name=` pushes the
per-instance lifecycle state of one release name as **server-sent events**
(`Content-Type: text/event-stream`, `Cache-Control: no-store`,
`X-Accel-Buffering: no`). It is the live transport behind the console's
rollout view; `GET /api/v1/release-subscribers` remains the paged,
poll-friendly form.

- Authentication is the same bearer header as every other endpoint. The
  browser `EventSource` API cannot set request headers, so consume the stream
  with `fetch` and read the body as a stream (`frontend/lib/sse.ts` does
  this); there is no cookie or query-string token form.
- The first event is an immediate `snapshot`; later `snapshot` events follow
  lifecycle acknowledgements, connect/disconnect transitions, and activations
  for that release name, coalesced so bursts produce one event (about 250 ms
  apart at most), with a periodic safety re-query about every 5 s.
- A comment line `: keep-alive` is written every 15 s so idle proxies keep
  the connection open.
- A stream lives at most **5 minutes**; the server then sends `event: end`
  and closes. Clients reconnect (the console uses a 1 → 30 s jittered
  backoff) — every reconnect starts with a fresh snapshot, so nothing is lost.
- Limits: **4** concurrent streams per identity and **64** per server. Beyond
  either, the response is `rate_limited` (429) and the refusal is audited.
- After two consecutive failures the console falls back to polling
  `GET /api/v1/release-subscribers` every 5 s while the tab is visible and
  shows the transport badge as `polling`; a stream that stops delivering is
  shown as `stale`.

```text
event: snapshot
data: {"summary":{"total":5,"connected":5,"applied_current":4,"rejected":1,"pending":0,"stale":0,"other_release_names":[],"rejected_instances":[…],"truncated":false},"subscribers":[ReleaseSubscriberState…],"current_revision":119,"server_time_unix_ms":1710000000000}

: keep-alive

event: end
data: {}
```

`summary` has the shape of `EnvironmentOverview.rollout`; `subscribers` are
the rows of `GET /api/v1/release-subscribers`, ungrouped. Reverse proxies in
front of the HTTP listener must not buffer this path (`X-Accel-Buffering: no`
covers nginx; disable `proxy_buffering`/equivalent elsewhere) and must allow
idle connections of at least the 5-minute lifetime. The handler clears the
server's write deadline for its lifetime; every other route keeps the normal
60 s timeout.

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
| rate_limited         | 429  |
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
  `{"healthy": true, "ready": true, "version": "...", "current_revision": 42,
    "grpc_addr": "0.0.0.0:8443", "tls_enabled": true}`
  `grpc_addr` is the configured gRPC listen address (empty when the gRPC
  server is not wired); `tls_enabled` is true when the server was started with
  TLS or the request itself arrived over TLS.
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
