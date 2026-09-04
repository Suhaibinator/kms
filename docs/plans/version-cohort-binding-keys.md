# Versioned protection transitions and unbound-version purge

## Contract

This is the breaking, greenfield `0.3.x` contract. It does not add a SQLite
migration and does not change the database table layout, application
configuration schema, `ConfigurationReleaseEntry`, or deterministic release
digest format. It changes the SecretService API and all public clients.

For every non-destroyed secret version, `bound` and `has_access_token` are
immutable. An exact release pin therefore implicitly pins the protection mode,
even though protection flags remain absent from release entries and digests.
Destroyed rows are minimal tombstones and clear those flags.

## Current-version transitions

`BindSecret`, `UnbindSecret`, and `RotateSecretBindingKey` operate only on the
version carrying the `current` label. Each request requires a positive
`expected_current_version`; a mismatch returns `Aborted` without side effects.
Each successful request:

1. allocates the next high-water version;
2. decrypts current and re-encrypts it with fresh ciphertext, DEK, nonce,
   version-bound AAD, active KEK, and binding salt where applicable;
3. preserves plaintext, content type, metadata, state, expiry, and the source
   version's access-token requirement while recording fresh creation identity
   and time;
4. makes the clone `current` and the byte-for-byte unchanged source `previous`;
5. commits the change event, sanitized audit row, and revision atomically; and
6. invalidates affected caches.

Bind requires current to be unbound. Unbind and rotation require current to be
bound and the supplied old key to open it. Rotation also rejects identical old
and new keys. Rotation creates only one new current version: it never modifies
or clones the historical cohort, whose versions continue requiring the old key.
Transitions fail closed if the stored `bound` flag contradicts wrapping
metadata: bound sources must carry structurally valid binding-key wrapping
before any decrypt callback runs, while bind accepts only structurally valid
standard unbound wrapping.

The three RPCs return
`{current_version, previous_version, revision}`. Go, Python, and TypeScript call
this `SecretVersionTransitionResult`. CLI bind, unbind, and rotation do not
accept `--version`; they read current metadata and submit that version as the
guard. The console offers these actions only on current.

## Bound-cohort purge

Cryptographic cohort discovery is retained for compromised historical binding
keys. Preview opens the bound anchor and adjacent version numbers with the
supplied key, stopping at an unbound, missing, destroyed, corrupt, or
differently keyed boundary. Every purge API requires the administrator to
submit exactly the previewed cohort with a positive revision and positive,
sorted, unique affected-version CAS guard. There is no unguarded public SDK
path. A revision or version-set mismatch aborts atomically. The CLI and console
also require confirmation. Historical bound rows retain this console action.

## Unbound-version purge

`PreviewSecretUnboundVersions(ref)` returns every non-destroyed `bound=false`
version of one secret, sorted and unique. Selection is structural, so disabled,
expired, and cryptographically corrupt rows are included. No match is a failed
precondition.

`PurgeSecretUnboundVersions(ref, expected_revision,
expected_affected_versions)` requires an exact prior preview. A revision or set
mismatch aborts the transaction. Preview and purge require authenticated
administrator status plus `secret:destroy`, checked before resource lookup.

Purge clears ciphertext, encrypted DEK, nonce, AAD, KEK/wrapping data, expiry,
content type, metadata, and protection flags, retaining only minimal
tombstones. It bypasses release-reference safeguards, preserves labels without
auto-promotion, clears the current projection if current is purged, and relies
on destroyed-version validation to invalidate affected releases. Audit,
change-log, cache invalidation, secure deletion, WAL truncation, and the
post-commit `purge_cleanup_pending` outcome match bound-cohort purge.

The HTTP endpoints are:

- `POST /api/v1/secrets/unbound-versions/preview`
- `POST /api/v1/secrets/unbound-versions/purge`

The CLI command is `secret purge-unbound-versions PATH`. CLI and console first
display the exact set, warn that the operation is irreversible, require
confirmation, and submit the preview guards.

Credential and cryptographic failures remain deliberately sanitized at every
public boundary. In particular, the console displays the same generic secret
operation failure for wrong-key and identical-key rotation failures; it never
surfaces server diagnostic details or adds a credential-specific exception.

## Releases and operations

Existing release manifests are never rewritten automatically. After a
transition, an existing release still pins the unchanged source and requires
its original credentials. A new release must explicitly pin the new version;
its digest differs because the exact version differs, not because the digest
format changed.

The operational sequence is:

1. transition current;
2. create and activate a release that pins the new version;
3. retire releases that pin the old version; and
4. purge the old bound cohort or unbound versions when required.

Any future protection-mode toggle must create a new version. Rotating the
credential accepted by token-gated versions is allowed, but it may never remove
a live version's `has_access_token` requirement.

## Verification

Tests cover one-version allocation, current/previous movement, preservation of
non-protection fields, fresh cryptographic material and AAD, unchanged source
rows, stale guards, invalid modes/keys, disabled and expired current versions,
audit rollback, concurrency, KEK rotation, cache invalidation, and the release
regression where an old release still opens the bound source only with its old
key while a new release pins the new unbound version with a different digest.

Purge tests cover alternating protection modes, disabled, expired, destroyed,
missing and corrupt versions; authorization-before-lookup; optional paired
bound-purge guards and mandatory unbound-purge guards; release-reference bypass;
label preservation; tombstone contents;
rollback, redaction, audit/change events; cleanup pending; CLI/console/HTTP;
SDK parity; generated contracts; and full language/race suites.
