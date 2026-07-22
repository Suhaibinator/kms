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
