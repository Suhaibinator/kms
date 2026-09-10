# Configuration releases

Configuration releases are the atomic hot-reload contract for a set of
related values. A release is immutable and belongs to one track identified by
`(environment, application, release name, schema version)`. Each track numbers
its releases independently from 1 and stores stable aliases pointing to exact
parameter or secret versions. An activation moves that track's `current` and
`previous` labels in one SQLite transaction and appends exactly one authoritative
global revision. Release numbers must always be interpreted together with their
schema version.

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

Release creation and activation compare the complete manifest to the contract
established for the pinned schema version. A mismatched alias, resource kind,
or parameter content type fails with `failed_precondition`. The same schema
contract applies across environments; each environment has independent values
and releases. A schema's contract is established once from its definition or
first release. Concurrent attempts must agree with the winning contract.

Publishing a new schema does not retire older tracks. For example, schema 1's
release 3 and schema 2's release 1 can both be active in `prod/payments`.
Activating, editing, or rolling back either track leaves the other track's
active and previous releases unchanged. Resources remain shared within their
namespace, but immutable release pins prevent a new resource version from
changing another track's active configuration.

The Applications page in the embedded console compares current parameter
values and secret metadata across every environment. A reviewed multi-target
parameter write creates a separate immutable version and change-log revision
in each selected namespace; it never links the resulting values. Partial
failures are reported per environment. Secret plaintext is never included in
the matrix. A secret's live binding/token metadata is displayed, but the
cross-environment parameter write never copies secret material.

## Release contents and digest

Creation accepts each entry with either an exact `version` or a movable
`label`; if both are absent, `current` is used. KMS resolves labels before
persistence. The immutable stored entry contains:

- alias, `parameter`/`secret` kind, structured `ResourceRef`, and exact version;
- the pinned version's content type and immutable non-sensitive metadata;
- SHA-256 of the exact parameter bytes, or no value digest for a secret.

Both parameter and secret references must be in the release's own namespace.
Entries carry no protection flags: `bound` is
deliberately absent from both the entry and release digest. That property
is immutable for the lifetime of a non-destroyed secret version, so an exact
version pin implicitly pins its protection mode without changing the release
entry schema or deterministic digest format.

The release digest is SHA-256 over a deterministic, alias-sorted protobuf
projection containing the release schema reference, resource pins, captured
metadata, and parameter digests. It excludes plaintext, credentials,
timestamps, creator identity, activation revision, and movable labels. SDK
snapshots use the same projection to verify the release before preparation.

Limits are 256 entries, 64 bytes per alias, 1 MiB per stored parameter
document, 1 MiB per schema, and 64 KiB for release or captured entry
metadata. Aliases start with an ASCII letter and then contain only ASCII
letters, digits, `_`, or `-`. Release client names and instance IDs are each
limited to 128 bytes; acknowledgement diagnostics are accepted up to 1,024
bytes but persisted only as a redaction marker.

## Migrating an active release to a registered schema

In the console, choose **Migrate to schema** from an application's environment
actions or a registered schema's viewer. Select an environment with an active
release, review the proposed contract, map renamed aliases, and edit any
parameter values needed by the target schema. Schema properties suggest
parameter fields; existing secret bindings remain explicit because JSON Schema
does not describe secrets.

Migration starts from the active release's exact resource versions, even when
newer unreleased values exist. Unchanged parameters and secrets retain those
pins. Only edited or new parameters receive new versions. Removed aliases do
not delete their resources. New secret bindings select existing versions in
the same environment; create missing secrets through normal secret management.

The final preview identifies the source and destination schema tracks and
validates the candidate. **Review and activate** writes edited parameters,
creates a release in the destination track, and activates it transactionally.
The source track keeps its active release and remains available for new
releases and rollback. Production environments require typing the environment
name. A stale preview must be refreshed; failed validation or a transaction
conflict leaves configuration unchanged.

Only the selected environment and destination schema track receive the new
activation. Other tracks and environments keep their active releases.
Registration itself does not activate anything. Environments without an active
release use the existing setup/import workflow.

## Optional schema registry

### Array editing and upgrade preparation

The console distinguishes omitted properties, empty lists (`[]`), populated
lists, and explicit `null`. Opening an existing value preserves those states.
Adding a list item opens a draft; **Add item** commits it and **Cancel** leaves
the value unchanged. Pending items block saving and switching the parent editor
to JSON until added or cancelled. Removing the last stored item leaves `[]`.
Use **Omit field** to remove an optional property. Required fields do not offer
omission, and **Use empty list** is offered only when local schema checks allow
it. Nullable lists expose **Set to null** under **More options**. Existing empty
strings remain stored items; creating one requires **Add empty string**.

During an upgrade, **Prepare draft** previews list initializations, schema
defaults, conversions, and forbidden-field removals. A missing required list
is initialized only when an empty list is allowed. Existing empty, null, and
omitted optional fields without defaults are preserved. The backend migration
preview remains authoritative for full schema validation. **Restore
pre-preparation value** restores the original JSON, including edits made after
preparation and any subsequent preparations.

A new string-list property whose name is the old sibling name plus `s` (for
example, `redirect_url` → `redirect_urls`) can suggest wrapping the old string
as a single item. Name-based suggestions must be selected before preparation;
unaccepted or incompatible sources are retained for manual review. An empty
old string requires a separate, explicit choice to discard it and use `[]`.
An existing target property is never overwritten by a conversion.

Schema authors may declare a sibling mapping and application-specific state
labels on an array property:

```json
{
  "type": "array",
  "items": { "type": "string" },
  "x-kms-migrate-from": "redirect_url",
  "x-kms-array": {
    "omittedLabel": "Use application defaults",
    "emptyLabel": "No redirect URLs",
    "nullLabel": "Provider disabled"
  }
}
```

These optional console annotations do not change validation or application
runtime behavior. Only use labels that describe the consuming application's
actual semantics. Labels may also appear on the supported nullable wrapper.
`x-kms-migrate-from` names a sibling key, not a dotted path; a compatible nonempty
string is included in the preparation plan without a name-based confirmation.
The source is removed only if the target schema forbids it. Complex or
incompatible conversions remain manual. Source string tokens and unrelated
numeric tokens are preserved exactly.

### Registry semantics

`ConfigurationSchemaService` provides immutable `CreateSchema`, `GetSchema`,
and `ListSchemas` operations. A schema belongs to exactly one application and
that application's immutable release name. Each successful create allocates
the next version in the `(application, release_name)` lineage. Create accepts
the application name and KMS derives its release name; there is no free-form
schema ID. Registration is admin-managed and accepts at most 1 MiB of JSON.
KMS compiles schemas with `jsonschema/v6` as Draft 2020-12; an explicit
`$schema` must name that dialect.

Schema registration compacts JSON before storing it and computing its digest.
The generator uses the same compact representation, so whitespace and file
formatting cannot make a generated defaults artifact disagree with the
registered schema.

A release pins only `schema_version`; its application and release name already
identify the owning schema lineage. During
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

Profiles do not select schemas. A profile chooses source-owned default values
and normally maps to an environment; every profile for an application shares
the same application/release schema lineage.

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
  rpc ResolveReleaseSchema(ResolveReleaseSchemaRequest) returns (ResolveReleaseSchemaResponse);
  rpc ListReleases(ListReleasesRequest) returns (ListReleasesResponse);
  rpc WatchRelease(stream WatchReleaseRequest) returns (stream WatchReleaseEvent);
  rpc VerifyReleaseDefaults(VerifyReleaseDefaultsRequest) returns (VerifyReleaseDefaultsResponse);
}
```

`CreateRelease` resolves selectors but does not activate. `ValidateRelease`
fresh-reads every pin, independently authorizes each resource, verifies
content type and parameter digest, rejects malformed JSON, and applies the
pinned schema when present. It returns structured validation errors rather
than resource values.

`ActivateRelease` reruns the same schema and resource validation used by
`ValidateRelease`. It then transactionally rechecks exact resource identity,
home namespace, content type and digest, and secret enabled/expiry state before
moving `current`/`previous`; publication occurs only after
commit. Validation failure is gRPC `FAILED_PRECONDITION` (HTTP 412) with a
sanitized structured `ValidateReleaseResponse` detail, and does not allocate an
activation revision or move either label. The optional-presence
`expected_current_version` is a compare-and-swap guard: omit it for an
unguarded activation, set it to `0` to require no current release, or set it
to the exact current version. A conflict is gRPC `ABORTED` (HTTP 409). An
already-active target is an idempotent no-op (`changed=false`) and creates no
revision. Any earlier immutable version can be activated directly as a
rollback.

The first `WatchReleaseRequest` registers the namespace, release name, exact
`schema_version`, client name, stable process instance ID, and last-seen revision.
Schema selection is required, including explicit `0` for a schema-free track.
An omitted version never selects the newest schema. Later messages are lifecycle
acknowledgements carrying that same schema version. The server sends the track's
current release immediately, replays only that track's retained activations
after a resume point, or sends its current snapshot if replay was pruned. A
known schema without an active release stays subscribed and receives heartbeats
until its first activation; an unknown schema fails registration. Heartbeats
reauthorize the stream. A slow consumer's pending activation is replaced with
the latest current activation rather than being permanently dropped. Delivery
is at least once in monotonically increasing activation-revision order, so a
client must accept an idempotent duplicate after reconnect.

Lifecycle acknowledgements are idempotent by namespace, release name, schema
version, authenticated identity, client, instance, state, and activation identity. The
client timestamp is diagnostic; server receipt time orders retries for the same
activation. The admin subscriber API stores the
latest `received`, `prepared`, `applied`, and `rejected` rows separately, plus
transport connection state. A newly registered instance is therefore visible
as connected before it has acknowledged any lifecycle state. Within each schema track, UIs group
instances by `(identity, client_name, instance_id)`; different authenticated
identities and replicas do not overwrite one another.

An `applied` acknowledgement may additionally carry `applied_divergent` and
`divergent_field_count`: the managed Go layer sets them when the generation it
applied differs from the application's source-owned defaults. Divergence is a
warning, not a rollout failure — the release was applied — and it is only
accepted on the `applied` state (`divergent_field_count` requires the flag and
is capped at 65535; any other combination is `INVALID_ARGUMENT`). The
subscriber listing and rollout summaries surface both fields so operators can
see which instances run a configuration that no longer matches what the
source tree declares, and the acknowledgement audit event records
`divergent=true|false`.

### Verifying source defaults against the active release

`VerifyReleaseDefaults` is a value-free oracle for CI and the managed Go
binding: the caller hashes each parameter of its generated defaults artifact
locally (`configstore.ParameterHash`, sorted-key compact JSON for the `json`
content type, exact bytes otherwise) and sends only aliases, content types,
and hashes, optionally with the artifact's `schema_sha256`. The server
resolves the application's active release (the request's `name` defaults to
the application's release name), recomputes the same canonical hash for every
pinned parameter, and answers with one bounded verdict per alias — `match`,
`differs`, `missing_in_release` (in the application contract but not pinned),
`unknown_alias`, `secret_alias` (secret aliases are answered structurally and
never read), or `unsupported_content_type` — plus per-verdict counts,
`schema_matches` (constant-time comparison against the registered schema
digest), and `unverified_count`, the number of release parameter aliases the
request did not mention. No stored value, digest, or hash is ever returned.
The operation is `configuration-release:verify-defaults` (never implicit) and
is budgeted per identity; see the
[security note](security.md#defaults-verification-oracle) and the
[`release verify-defaults`](operations.md#configuration-release-commands)
command.

Bounded rejection categories are `resolution_failed`, `binding_key_unavailable`,
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

Equivalent Go, Python, and TypeScript loaders perform these steps:

1. fresh-read or receive the active release, then verify its identity and
   complete deterministic manifest digest;
2. resolve all exact pins concurrently (default limit 16); before fetching a
   secret value, fresh-read its metadata and verify response identity, exact
   version, enabled/destroyed state, expiry, and `bound`;
3. resolve the binding key by alias only when
   that exact live version requires each credential;
4. verify returned resource identity/version and parameter digests;
5. construct an immutable snapshot whose normal formatting omits resolved
   values and whose `Secret` values always redact;
6. acknowledge `received`, call application preparation, then acknowledge
   `prepared`;
7. cancel and abort a candidate superseded by a newer activation;
8. immediately before commit, fresh-read and compare release name, version,
   activation revision, and digest;
9. commit and acknowledge `applied`, or acknowledge `rejected` and retain the
   last-known-good release.

Missing either required local credential rejects the whole candidate as
`binding_key_unavailable`; wrong credentials or failed resolution reject it as
`resolution_failed`. There is no partial snapshot. Startup fails until an
initial release is successfully applied. After that,
transport outages and rejected candidates do not displace the last-known-good
state. Every successfully prepared candidate that does not commit is aborted
exactly once. `Commit`/`commit` must be infallible and normally be an atomic
pointer/reference swap; a panic or exception is fatal and is not acknowledged
as applied.

The final active read is a staleness fence, not a distributed commit lock. An
activation racing immediately after that read can briefly leave a replica on
the older release; the stream presents the newer release as the next candidate.
Version 1 has no fleet-wide activation barrier, so replicas apply independently.

See [`sdk-go.md`](sdk-go.md#atomic-release-loading),
[`sdk-python.md`](sdk-python.md#atomic-release-loading), and the
[`TypeScript release API`](sdk-typescript-api.md#lifecycle-and-concurrency) for
application code.
Generated Go bindings, source-owned defaults, consumer views, and emergency
override operations are documented in
[`managed-go-configuration.md`](managed-go-configuration.md).

## Authorization, retention, and destructive operations

Namespaced policy operations are `configuration-release:create`, `read`,
`validate`, `activate`, `list`, `watch`, and `verify-defaults`;
`configuration-release:*` is the category wildcard. The implicit
home-namespace grant includes only release `read` and `watch`;
`verify-defaults` always needs an explicit allow rule, even for the caller's
own namespace (existing `configuration-release:*` and `*` rules cover it —
review them when upgrading). Release access never grants access to a
referenced parameter or secret: create, validate, and loaders all perform
independent resource authorization. Cross-namespace references are invalid for
both parameters and secrets, so `verify-defaults` operates only on the
release's home namespace.

Current and previous releases protect their referenced parameter versions and
secret versions. Parameter deletion, secret deletion, and secret-version
destruction fail with `FAILED_PRECONDITION` and identify the release/version/
alias when they would break a protected release. These attempts are audited.
Promoting a parameter or secret's ordinary `current` label never changes an
active release pin. Every exact secret version has an immutable `bound` flag. Bind, unbind, and
binding-key rotation clone the current secret into exactly one new current
version and leave the source unchanged as `previous`. Existing releases
therefore continue resolving the source with its original credentials; a newly
created release must explicitly pin the new version and has a different digest
because the version changed. The digest algorithm and release schema do not
change.

The safe operational sequence is: transition current, create and activate a
new release, retire old releases, then purge the old bound cohort or all
unbound versions when required. Both administrator purge operations bypass
release-reference protection and leave referencing releases immutable but
unresolvable. Future protection-mode toggles must create versions as well. See
[`binding-keys.md`](binding-keys.md).

Release history defaults to at least the newest 100 inactive versions and 90
days. Current, previous, schema dependencies, and versions needed by retained
activation replay are not pruned. Disconnected subscriber lifecycle state is
retained for 30 days by default. Configure these with
`watch.release_retain_versions`, `watch.release_retain_duration`, and
`watch.release_subscriber_retain_duration`.

## Management surfaces

The gRPC CLI provides `parameter-store release` commands for `create`,
`validate`, `show`, `list`, `diff`, `activate`, `rollback`, `subscribers`, and
`verify-defaults`, plus `release schema create|show|list`. The embedded Releases page exposes
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
  ships with one confirmation. Aliases the operator did not touch keep
  their active pin; a newer unreleased version is offered as a per-alias
  opt-in rather than picked up silently. Environments whose name matches
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
equals the alias → the key name another environment's active release uses
for that alias → unresolved. That final fallback borrows only a key name;
the matching resource and resulting pin must still belong to the target
environment's home namespace. The console shows both identifiers, and an
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
Secret values are excluded from the validated payload; generated schemas include
secret aliases and kinds in the contract annotation. The full table, with the readiness
states and finding codes the console renders, is in
[`http-api.md`](http-api.md#readiness-model).

## Schema selection and deployment cutover

The application console's schema selector is part of its URL. It selects the
schema forms, release history, publishing and rollback actions, and subscriber
progress. Release links and identities include the schema version because
several tracks can each have a release 1. Switching the selector invalidates
previews and ignores stale responses from the previous selection.

Exact release reads and activation requests require `schema_version`. List
requests can omit the filter to inspect all tracks, and every result identifies
its schema. CLI release operations and `defaults apply` expose `--schema-version`.

Schema-free defaults artifacts retain the `schema_sha256` field with an empty
string and require an explicit schema-0 selection for import. Generated artifacts
keep their nonempty digest; they cannot be imported as schema-free defaults.
Defaults imports and generated release commands resolve that embedded digest when
no numeric selection is supplied, so an older generated client continues managing
its own track after a newer schema registers. An explicit numeric selection must
match the artifact digest.

Low-level Go, TypeScript, and Python loaders accept either an exact schema
version or a schema SHA-256 digest, never both. `ResolveReleaseSchema` resolves
the digest under release-read authorization; clients do not need schema-admin
access. The loader pins the resolved version for startup, subscriptions,
reconnects, reconciliation, and the final active check before commit. A known
track without an activation can remain subscribed until its first release becomes
active. Unknown-track and permission errors terminate the loader; temporary
connection failures still retry. If a retained acknowledgement references an
activation that is no longer available, the server rejects that acknowledgement
without closing the watch or recording lifecycle state. SDKs discard only its
matching sequence and continue watching and reconciling; rejection responses do
not advance the release cursor.

Generated managed clients supply their embedded digest automatically. Regenerate
bindings with the updated generator. Generated schemas include a sorted
`x-kms-contract` annotation with each parameter and secret alias, kind, and
parameter content type. This metadata contains no secret values or binding keys
and makes secret-contract changes part of schema identity.

This change requires a **fresh database** and updated server, SDKs, and generated
clients. Previous database baselines are rejected without conversion or deletion.
Create and provision a new database explicitly; retain any existing database
separately. Unscoped old clients cannot subscribe to schema-backed releases.

Existing database inspection uses a private temporary copy of the database and
WAL so a rejected database and its sidecars remain untouched, including
uncheckpointed WAL contents. Inspection needs temporary space for that copy and
roughly two sequential reads of the source files. Concurrent changes cause
bounded retries and then a retry error. A nonempty rollback journal requires
operator recovery before inspection; KMS does not perform that recovery on the
original files.

## Pin one client process to a release

**Pin to release** assigns one connected release subscriber an exact version
within its existing `(environment, application, release name, schema version)`
track. The version can be older than current or never activated fleet-wide.
Other processes continue following the track's active release. Find the actions
in the release workspace's rollout table or an application's rollout panel.
Validate the selected release, review its schema/version, then assign it.
Production environments require typing the environment name.

A pin belongs to a random SDK loader session, independent of a configured
instance name. Network reconnects and **KMS server restarts preserve the pin**.
Restarting the **client application** creates a new session and follows the
active track, even when it reuses the same instance name. Do not persist or
copy session IDs in deployment configuration. Disconnected session history,
including its pin, is retained using `watch.release_subscriber_retain_duration`
(default 30 days). A client trying to resume an expired session receives an
explicit failure and must restart; it never silently loses its pin.

Assignment is separate from application. The console shows the desired target,
last applied release, pin actor/time, and rejection state. A client that cannot
prepare a pinned release retains its last working configuration and reports the
failure; the pin remains until changed or removed. **Unpin** follows the track's
current release immediately, or waits for its first activation when none exists.
Disconnected sessions can be unpinned; creating or changing a pin requires a
connected session. Successfully pinned processes are reported separately from
processes updated to the fleet activation.

Operators need `configuration-release:instance-manage` on the namespace, plus
existing release list/read/validate and underlying resource permissions needed
to select and validate releases. There is no implicit home-namespace grant for
instance management. Namespace-scoped subscriber listing and its live stream
accept this permission; the global subscriber inventory remains admin-only.
`configuration-release:*` and `*` include the new operation, subject to denies.
Pin and unpin writes are audited atomically with the assignment.

Pinned manifests and their resource versions are protected from ordinary
retention/deletion while the session is retained. Normal authorization,
application preparation, secret expiry/disable rules, and emergency purge
bypasses still apply. This does not revoke already-loaded values from process
memory or force an application to accept configuration.

All release loaders (Go, generated Go, Python sync/async, and TypeScript) support
sessions automatically. Deploy the server first. Older SDKs remain functional
but cannot be pinned; updated SDKs fall back to following active releases when
an older server reports the session RPC as unimplemented. Direct parameter
watchers and `exec` workloads are not release-loader sessions.

`GetActiveRelease` continues reporting the fleet activation. `GetInstanceRelease`
reports an effective target with a separate monotonic `target_revision`.
Pinning does not create an activation: a pinned target's `activation_revision`
is zero, and its manifest/digest is unchanged. Watch target events and lifecycle
acknowledgements use the target revision for ordering, replay, and supersession.
