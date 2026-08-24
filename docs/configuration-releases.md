# Configuration releases

Configuration releases are the atomic hot-reload contract for a set of
related values. A release is immutable, belongs to one namespace and name,
and stores stable aliases pointing to exact parameter or secret versions. An
activation moves the release name's `current` and `previous` labels in one
SQLite transaction and appends exactly one authoritative global revision.

This is separate from the namespace-wide `WatchService`. Its acknowledgement
means only that an SDK received a transport revision. A release subscriber
reports application lifecycle states (`received`, `prepared`, `applied`, or
`rejected`) through `ConfigurationReleaseService.WatchRelease`.

## Applications and environments

An application is the environment-independent owner of a configuration shape.
It records a canonical release name, an optional immutable schema pin, and the
required release aliases, resource kinds, and parameter content types. Each
`(env, app)` namespace is one isolated deployment environment of that
application. Environments never inherit values or share mutable versions.
Environment names are scoped to that application: there is no KMS-wide
environment record, and two applications may define completely different sets
of environment names.

Release creation and activation compare the complete manifest to the owning
application record. A release with a different name, schema pin, missing or
extra alias, wrong resource kind, or wrong parameter content type fails with
`failed_precondition`. Thus `dev/payments`, `prod/payments`, and
`prod-gcp/payments` can carry different values while retaining one application
contract. If an application was created without an explicit contract, its
first release atomically establishes the canonical schema and alias/type shape;
concurrent first releases compare against whichever contract wins that write.

The Applications page in the embedded console compares current parameter
values and secret metadata across every environment. A reviewed multi-target
parameter write creates a separate immutable version and change-log revision
in each selected namespace; it never links the resulting values. Partial
failures are reported per environment. Secret plaintext is never included in
the matrix and client-bound secrets are not copied by this workflow.

## Release contents and digest

Creation accepts each entry with either an exact `version` or a movable
`label`; if both are absent, `current` is used. KMS resolves labels before
persistence. The immutable stored entry contains:

- alias, `parameter`/`secret` kind, structured `ResourceRef`, and exact version;
- the pinned version's content type and immutable non-sensitive metadata;
- SHA-256 of the exact parameter bytes, or no value digest for a secret; and
- secret protection flags (`client_bound`, `has_access_token`), never a token
  or plaintext.

The release digest is SHA-256 over a deterministic, alias-sorted protobuf
projection containing the release schema reference, resource pins, captured
metadata, and parameter digests. It excludes plaintext, credentials,
timestamps, creator identity, activation revision, and movable labels. SDK
snapshots use the same projection to verify the release before preparation.

Limits are 256 entries, 64 bytes per alias, 1 MiB per stored parameter
document, 256 KiB per schema, and 64 KiB for release or captured entry
metadata. Aliases start with an ASCII letter and then contain only ASCII
letters, digits, `_`, or `-`. Release client names and instance IDs are each
limited to 128 bytes; acknowledgement diagnostics are accepted up to 1,024
bytes but persisted only as a redaction marker.

## Optional schema registry

`ConfigurationSchemaService` provides immutable `CreateSchema`, `GetSchema`,
and `ListSchemas` operations. Schema IDs are global and each successful create
allocates the next version. Registration is admin-managed and accepts at most
256 KiB of JSON. KMS compiles schemas with `jsonschema/v6` as Draft 2020-12;
an explicit `$schema` must name that dialect.

A release either pins both `schema_id` and `schema_version`, or neither. During
validation, KMS parses each parameter according to its declared content type
and builds one object keyed by release alias. JSON parameters become JSON
values; `integer`, `float`, and `boolean` become JSON scalars, while `string`
and validated base64 `binary` parameters remain strings. Secrets are excluded
from this object and checked separately as readable
references. Schema errors return a bounded code, alias when it can be derived,
optional schema pointer, and sanitized message. Application-specific semantic
validation still belongs in the loader's prepare callback.

Parameter content types are KMS tokens, not MIME types. The JSON token is the
literal, case-sensitive `json`; `application/json` is not accepted as a
parameter content type. Generated managed Go contracts require `json` for every
group document.

JSON parsing rejects duplicate properties recursively and retains exact JSON
numbers, so large integer bounds are not rounded through `float64`. Schemas
emitted by `kms-config-gen` use the asserted KMS formats `go-duration` and
`kms-base64`; unrelated Draft 2020-12 `format` keywords retain their normal
annotation behavior.

Validation codes are `not_found`, `permission_denied`, `unreadable`,
`content_type`, `malformed_json`, `schema_violation`, and `digest_mismatch`.

## gRPC contract

The authoritative wire definitions are in
[`proto/kms/v1/kms.proto`](../proto/kms/v1/kms.proto). The release service is:

```protobuf
service ConfigurationReleaseService {
  rpc CreateRelease(CreateReleaseRequest) returns (CreateReleaseResponse);
  rpc ValidateRelease(ValidateReleaseRequest) returns (ValidateReleaseResponse);
  rpc ActivateRelease(ActivateReleaseRequest) returns (ActivateReleaseResponse);
  rpc GetRelease(GetReleaseRequest) returns (GetReleaseResponse);
  rpc GetActiveRelease(GetActiveReleaseRequest) returns (GetActiveReleaseResponse);
  rpc ListReleases(ListReleasesRequest) returns (ListReleasesResponse);
  rpc WatchRelease(stream WatchReleaseRequest) returns (stream WatchReleaseEvent);
}
```

`CreateRelease` resolves selectors but does not activate. `ValidateRelease`
fresh-reads every pin, independently authorizes each resource, verifies
content type and parameter digest, rejects malformed JSON, and applies the
pinned schema when present. It returns structured validation errors rather
than resource values.

`ActivateRelease` reruns the same schema and resource validation used by
`ValidateRelease`. It then transactionally rechecks exact resource identity,
content type and digest, secret enabled/expiry state, and secret protection
metadata before moving `current`/`previous`; publication occurs only after
commit. Validation failure is gRPC `FAILED_PRECONDITION` (HTTP 412) with a
sanitized structured `ValidateReleaseResponse` detail, and does not allocate an
activation revision or move either label. The optional-presence
`expected_current_version` is a compare-and-swap guard: omit it for an
unguarded activation, set it to `0` to require no current release, or set it
to the exact current version. A conflict is gRPC `ABORTED` (HTTP 409). An
already-active target is an idempotent no-op (`changed=false`) and creates no
revision. Any earlier immutable version can be activated directly as a
rollback.

The first `WatchReleaseRequest` is a registration with namespace, release
name, client name, stable process instance ID, and last-seen revision. Later
messages are lifecycle acknowledgements. The server sends the current release
immediately, replays retained activations monotonically after a resume point,
or sends a complete current snapshot if replay was pruned. Heartbeats
reauthorize the stream. A slow consumer's pending activation is replaced with
the latest current activation rather than being permanently dropped. Delivery
is at least once in monotonically increasing activation-revision order, so a
client must accept an idempotent duplicate after reconnect.

Lifecycle acknowledgements are idempotent by namespace, release name,
authenticated identity, client, instance, state, and activation identity. The
client timestamp is diagnostic; server receipt time orders retries for the same
activation. The admin subscriber API stores the
latest `received`, `prepared`, `applied`, and `rejected` rows separately, plus
transport connection state. A newly registered instance is therefore visible
as connected before it has acknowledged any lifecycle state. UIs group
instances by `(identity, client_name, instance_id)`; different authenticated
identities and replicas do not overwrite one another.

Bounded rejection categories are `resolution_failed`, `token_unavailable`,
`version_mismatch`, `digest_mismatch`, `prepare_failed`,
`config_contract_mismatch`, `config_decode_failed`,
`config_validation_failed`, `default_mismatch`, `restart_required`,
`superseded`, `active_check_failed`, and `internal`. The managed Go layer uses
the configuration-specific categories while keeping detailed diagnostics
local. A default-mismatch callback may intentionally expose expected and actual
non-secret values, but acknowledgements contain only the bounded category;
lower-level application preparation failures continue to use `prepare_failed`
unless explicitly classified. Operator remediation is in the
[managed configuration workflow](managed-go-configuration.md#diagnose-a-rejected-candidate).

## Loader lifecycle

Equivalent Go and Python loaders perform these steps:

1. fresh-read or receive the active release;
2. resolve all exact pins concurrently (default limit 16);
3. ask the local token provider only for a protected secret;
4. verify returned resource versions, parameter digests, and release digest;
5. construct an immutable snapshot whose normal formatting omits resolved
   values and whose `Secret` values always redact;
6. acknowledge `received`, call application preparation, then acknowledge
   `prepared`;
7. cancel and abort a candidate superseded by a newer activation;
8. immediately before commit, fresh-read and compare release name, version,
   activation revision, and digest;
9. commit and acknowledge `applied`, or acknowledge `rejected` and retain the
   last-known-good release.

Startup fails until an initial release is successfully applied. After that,
transport outages and rejected candidates do not displace the last-known-good
state. Every successfully prepared candidate that does not commit is aborted
exactly once. `Commit`/`commit` must be infallible and normally be an atomic
pointer/reference swap; a panic or exception is fatal and is not acknowledged
as applied.

The final active read is a staleness fence, not a distributed commit lock. An
activation racing immediately after that read can briefly leave a replica on
the older release; the stream presents the newer release as the next candidate.
Version 1 has no fleet-wide activation barrier, so replicas apply independently.

See [`sdk-go.md`](sdk-go.md#atomic-release-loading) and
[`sdk-python.md`](sdk-python.md#atomic-release-loading) for application code.
Generated Go bindings, source-owned defaults, consumer views, and emergency
override operations are documented in
[`managed-go-configuration.md`](managed-go-configuration.md).

## Authorization, retention, and destructive operations

Namespaced policy operations are `configuration-release:create`, `read`,
`validate`, `activate`, `list`, and `watch`; `configuration-release:*` is the
category wildcard. The implicit home-namespace grant includes only release
`read` and `watch`. Release access never grants access to a referenced
parameter or secret: create, validate, and loaders all perform independent
resource authorization, including cross-namespace references.

Current and previous releases protect their referenced parameter versions and
secret versions. Parameter deletion, secret deletion, and secret-version
destruction fail with `FAILED_PRECONDITION` and identify the release/version/
alias when they would break a protected release. These attempts are audited.
Promoting a parameter or secret's ordinary `current` label never changes an
active release pin. Ordinary secret value rotation preserves the existing
per-secret access token unless token generation/rotation is explicitly
requested; the token still remains outside the release. Each secret version
also retains its own content type, client-bound flag, and token-required flag.
Enabling a token on a later standard-secret version therefore does not
retroactively protect an older version. Explicit token rotation replaces the
shared credential used by every standard-secret version already marked as
token-protected; client-bound versions remain cryptographically bound to the
token used when each version was written.

Release history defaults to at least the newest 100 inactive versions and 90
days. Current, previous, schema dependencies, and versions needed by retained
activation replay are not pruned. Disconnected subscriber lifecycle state is
retained for 30 days by default. Configure these with
`watch.release_retain_versions`, `watch.release_retain_duration`, and
`watch.release_subscriber_retain_duration`.

## Management surfaces

The gRPC CLI provides `parameter-store release` commands for `create`,
`validate`, `show`, `list`, `diff`, `activate`, `rollback`, and `subscribers`,
plus `release schema create|show|list`. The embedded Releases page exposes
creation, validation, diff, activation, rollback, schema registration/listing,
and per-instance subscriber status. Secret rows show metadata only. See
[`operations.md`](operations.md#configuration-release-commands) and
[`http-api.md`](http-api.md#configuration-releases-and-schemas).

### Application-centred console

The console is organised around the application rather than around the five
underlying resources. Its surfaces are thin views over the server-side
aggregates in [`http-api.md`](http-api.md#console-aggregates); the browser
renders readiness state and never recomputes it.

- **Overview** (`/`). An admin with no applications and no namespaces sees a
  first-run checklist (keep the one-time admin token safe, create an
  application, add an environment, set values, activate a first release,
  connect an SDK, see it applied). Otherwise a fleet grid shows one card per
  application with an application status, one status dot per environment
  (production marked), the active release per environment, the rejected
  instance count, and the last activation. Existing namespaces with no owning
  application are offered for adoption: creating application `X` attaches
  every `*/X` namespace.
- **Application page** (`/applications?app=`). A definition card shows the
  release name, schema pin, and contract aliases, and an alignment row that
  compares the contract with the pinned schema and offers one-click fixes
  (derive the contract from the schema, derive a schema from the contract, or
  edit the contract). A setup panel lists the remaining steps while the
  application is in `setup`. The default **pipeline** tab shows one column
  per environment, non-production first, production outlined: a *Values*
  section with one row per contract alias (present version, drift badge when
  the current version is newer than the active pin, Edit & ship), a *Release*
  section (active release and revision, previous version, Roll back, and a
  call to action naming the number of unreleased changes), and a
  *Subscribers* section (connected/applied/prepared/received/rejected counts
  with rejected instances expandable to their bounded category and the
  remediation from the
  [managed configuration table](managed-go-configuration.md#diagnose-a-rejected-candidate)).
  The *Matrix* tab keeps the cross-environment value table and the reviewed
  multi-target parameter write.
- **Ship** (Quick change). One modal composes parameter changes for one
  environment, previews them with a server dry run (writes, release entries
  with changed rows highlighted, unreleased changes offered for opt-in, the
  schema pin, the activation it will perform, and the validation result), and
  ships with one confirmation. Environments whose name matches
  `^prod(-|$)` or `^production$` require typing the environment name. The
  server writes the parameter versions, creates the release, and activates it
  under a compare-and-swap guard frozen from the preview; the four possible
  outcomes (`activated`, `rejected` before any write, release created but not
  activated, and a lost race) are shown with the exact next action, and the
  modal never offers "activate anyway". A rollout panel then follows
  per-instance acknowledgements live. The modal has a guided mode with a
  four-step header for applications that have never had an active release and
  an express mode afterwards.
- **Roll back**. The rollback dialog validates the previous version first, so
  an un-activatable previous release (a disabled or expired secret, an edited
  contract) is shown as violations rather than discovered on confirm. It uses
  `POST /api/v1/releases/rollback` with the CAS guard, reports a concurrent
  change as "changed meanwhile", and applies production type-to-confirm.
- **Add environment / clone**. A new environment can start empty or copy
  parameter values from an existing environment. Clone never overwrites a key
  that already exists in the target and never copies a secret value; each
  secret is listed as needing a value with an Add secret button.
- **Connect SDK** shows Go and TypeScript snippets templated from the server's
  gRPC address, the namespace, the release name, and the first alias, links to
  identity creation and the mTLS runbook, and warns when the server reports
  `tls_enabled: false`.
- **Command palette** (`⌘K` / `Ctrl+K`) indexes applications, environments,
  aliases (as "Ship a change"), pages, and actions such as "Roll back".

### Alias → key resolution

A contract alias is a release-level name; the physical parameter or secret
key is chosen per environment. Readiness, Ship, and clone resolve an alias to
a key with one shared rule, in order: the active release's entry for that
alias → the latest release's entry → a resource in the namespace whose key
equals the alias → the key another environment's active release uses for
that alias → unresolved. The console shows both identifiers, and an
unresolved alias becomes a "Create parameter" action that can also pick an
existing key.

### Schema type ↔ content type

When the console derives a contract from a pinned schema (or a schema from a
contract) it uses one mapping, implemented once in Go and mirrored in the
frontend, and pinned by a shared fixture:

| schema `type` | parameter content type |
|---|---|
| `object`, `array` | `json` |
| `string` | `string` (`format: kms-base64` → `binary`) |
| `integer` | `integer` |
| `number` | `float` |
| `boolean` | `boolean` |
| union or absent | `json` |

The reverse direction emits `{}` for `json`, `{"type": …}` otherwise, lists
every parameter alias in `required`, and sets `additionalProperties: false`.
Secrets are never part of the schema. The full table, with the readiness
states and finding codes the console renders, is in
[`http-api.md`](http-api.md#readiness-model).
