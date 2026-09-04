# Binding keys and compromised-version purge

Binding keys are an optional, operator-owned credential for individual secret
versions. They solve a different problem from access tokens: an access token
authorizes a read, while a binding key participates in decrypting the version's
DEK. A version may require either credential, both, or neither. KMS is
deliberately agnostic about which aliases use the same string; it never stores,
hashes, fingerprints, or assigns an identity to a binding key. The only
equality check is between the current and replacement values supplied together
for one rotation request, which rejects an accidental no-op.

This is the `0.3.x` contract. It replaces the `0.2.x` client-bound token and
per-alias token-file design. `0.3.x` requires a freshly initialized database;
there is no in-place migration from a `0.2.x` database.

## Supplying a binding key

A non-empty binding key is opaque UTF-8 text containing at least 32 bytes. KMS
does not trim or normalize it and requires no prefix or encoding. The CLI can
generate a suitable 256-bit Base64URL value:

```bash
parameter-store binding-key generate
```

That command writes exactly the key and one newline to stdout. Store the value
in the deployment's secret manager. For a single-secret CLI operation, provide
the current key as `KMS_BINDING_KEY`; rotation takes the replacement from
`KMS_NEW_BINDING_KEY`. On an interactive terminal an absent key is requested
with a no-echo prompt, and a newly entered key is confirmed twice. There is no
binding-key file flag, file environment variable, directory convention, or
server-side recovery copy. `parameter-store exec` removes the exact
`KMS_BINDING_KEY` and `KMS_NEW_BINDING_KEY` variables, as well as the existing
per-secret token variables, before starting the child.

A binding key is not the mTLS client private key. `KMS_CLIENT_KEY_FILE` keeps
its existing meaning as the filesystem path to that authentication key.

Applications may obtain a declaration's key from any secret-injection
mechanism. The SDK convention is one optional key per secret declaration. In
the first example, the application package aliases `kmsclient.Secret` as
`config.Secret`:

```go
type Config struct {
    LinkedInOAuthClientSecret config.Secret
    OktaOAuthClientSecret     config.Secret
}

defaults := Config{
    LinkedInOAuthClientSecret: config.Secret{},
    OktaOAuthClientSecret: config.Secret{
        BindKey: resolveFromEnvVar("OKTA_OAUTH_KMS_BIND_KEY"),
    },
}
```

```python
defaults = Config(
    linkedin_oauth_client_secret=Secret(),
    okta_oauth_client_secret=Secret(
        bind_key=resolve_from_env("OKTA_OAUTH_KMS_BIND_KEY")
    ),
)
```

```ts
const defaults = {
  linkedInOAuthClientSecret: new Secret(),
  oktaOAuthClientSecret: new Secret("", {
    bindKey: resolveFromEnv("OKTA_OAUTH_KMS_BIND_KEY"),
  }),
};
```

Generated stores extract these declaration-only values into a private,
alias-keyed loader map. They clear the key from retained defaults and every
published snapshot. Directly fetched secrets never carry the supplied key,
and secret formatting, JSON, inspection, and enumeration remain redacted.

## Reads, writes, and metadata

`GetSecret` sends the access token and binding key independently. `PutSecret`
creates a bound version when `binding_key` is non-empty and an unbound version
when it is empty; protection is not inherited from the preceding version.
`generate_access_token` independently creates or rotates the secret-level
access token and returns it once. There is no write-side `secret_token`.

The compact `0.3.x` protobuf fields are:

```proto
message GetSecretRequest {
  ResourceRef ref = 1;
  uint64 version = 2;
  string label = 3;
  string secret_token = 4;
  string binding_key = 5;
}

message PutSecretRequest {
  ResourceRef ref = 1;
  bytes value = 2;
  string content_type = 3;
  string metadata_json = 4;
  string binding_key = 5;
  bool generate_access_token = 6;
  int64 expires_at_unix_ms = 7;
}
```

The HTTP equivalents are `POST /api/v1/secrets/reveal` with
`env`, `app`, `key`, `version` or `label`, and `binding_key`;
and `POST /api/v1/secrets` with `env`, `app`, `key`, `value_base64`,
`content_type`, `metadata_json`, `binding_key`, `generate_access_token`, and
`expires_at_unix_ms`. Credentials belong only in these request bodies, never
in a URL or custom header. The admin-only reveal path bypasses the access-token
gate and rejects `secret_token` as an unknown field; it still requires the
binding key for a bound version. Normal data-plane `GetSecret` enforces both
independent requirements.

`SecretMetadata.bound` describes the version selected by `current`. Every
`SecretVersionInfo` has its own live `bound` and `has_access_token` flags;
exact-version readers must use those fields rather than the current summary.
Destroyed purge tombstones report both flags as false.

Secret plaintext is never cached by the Go, Python, or TypeScript SDK. Watches
carry metadata-only notifications, and a consumer explicitly refetches when it
needs plaintext.

## Bind, unbind, rotate, and purge

`bound` and `has_access_token` are immutable properties of every live secret
version. Binding, unbinding, and binding-key rotation therefore clone only the
version labeled `current` into one new high-water version. KMS fully decrypts
and re-encrypts the value with fresh ciphertext, DEK, nonce, version-bound AAD,
active KEK, and (for a bound result) binding salt. The new version becomes
`current`; the unchanged source becomes `previous`.

Pass a positive `--expected-current-version` to submit that version as the
required compare-and-swap guard without reading secret metadata. This lets an
identity authorized only for `secret:binding-manage` perform the transition.
When the flag is omitted, the CLI reads current metadata immediately before the
transition and uses the observed version:

```bash
KMS_BINDING_KEY="$new_key" parameter-store secret bind /prod/app/api-key \
  --expected-current-version 7
KMS_BINDING_KEY="$current_key" parameter-store secret unbind /prod/app/api-key \
  --expected-current-version 8

KMS_BINDING_KEY="$old_key" KMS_NEW_BINDING_KEY="$new_key" \
  parameter-store binding-key rotate /prod/app/api-key --expected-current-version 8
```

Bind requires an unbound current version. Unbind and rotation require a bound
current version and the key that opens it. Rotation rejects a byte-for-byte
identical replacement. A stale current guard aborts without creating a version,
moving a label, advancing the revision, or writing an allow audit.

Each transition preserves plaintext, content type, metadata, enabled/disabled
state, expiry, and the source version's access-token requirement. It records a
fresh creation time and actor. Rotation creates exactly one new current version;
it neither modifies nor clones the historical cohort. Those versions continue
to require the old binding key.

Compromised historical bound versions are removed separately. Cohort purge
previews the contiguous cryptographic cohort around an explicit anchor, prints
the exact affected versions, and replays the revision and version set as CAS
guards:

```bash
KMS_BINDING_KEY="$compromised_key" \
  parameter-store secret purge-binding-cohort /prod/app/api-key --version 7
```

An administrator can also purge every non-destroyed unbound version of one
secret, including disabled, expired, or cryptographically corrupt rows. This is
a distinct preview-and-confirm operation and does not accept a binding key:

```bash
parameter-store secret purge-unbound-versions /prod/app/api-key
```

Both purge forms require authenticated administrator status and
`secret:destroy` authorization, are irreversible, and bypass release-reference
protection. Preview is mandatory for unbound purge; purge aborts atomically if
either the global revision or the sorted, unique version set differs from the
preview. An empty preview fails with `failed_precondition`.

The console displays bind, unbind, and rotation only on the current row and
reports the newly created version. Historical bound rows retain their
cohort-purge action. Administrators also receive a secret-level “Purge unbound
versions” action that previews the exact set before enabling confirmation.

A cohort is discovered cryptographically. KMS opens the bound anchor with the
supplied key, then scans adjacent version numbers in both directions. It stops
at the first unbound, missing, destroyed, corrupt, or differently bound
version. It never jumps over that boundary, even if a later version reuses the
same key. For `v1=A`, `v2-v3=B`, and `v4-v5=A`, anchoring at v5 with A affects
only v4 and v5.

The RPCs are `BindSecret`, `UnbindSecret`, `RotateSecretBindingKey`,
`PreviewSecretBindingCohort`, `PurgeSecretBindingCohort`,
`PreviewSecretUnboundVersions`, and `PurgeSecretUnboundVersions`. Transitions
require `expected_current_version` and return
`{current_version, previous_version, revision}`. Version-set previews and
purges return `{affected_versions, revision}`. Binding-cohort preview/purge
also return their anchor.

The matching HTTP endpoints are:

| Operation | Endpoint | Additional request fields |
|---|---|---|
| Bind | `POST /api/v1/secrets/bind` | `expected_current_version`, `binding_key` |
| Unbind | `POST /api/v1/secrets/unbind` | `expected_current_version`, `binding_key` |
| Rotate | `POST /api/v1/secrets/binding-key/rotate` | `expected_current_version`, `binding_key`, `new_binding_key` |
| Preview bound cohort | `POST /api/v1/secrets/binding-cohort/preview` | `anchor_version`, `binding_key` |
| Purge bound cohort | `POST /api/v1/secrets/binding-cohort/purge` | `anchor_version`, `binding_key`, and required `expected_revision`, `expected_affected_versions`; they must match the exact prior preview |
| Preview unbound versions | `POST /api/v1/secrets/unbound-versions/preview` | none |
| Purge unbound versions | `POST /api/v1/secrets/unbound-versions/purge` | required `expected_revision`, `expected_affected_versions` |

Every body also contains `env`, `app`, and `key`. Missing and incorrect keys
collapse to the same sanitized credential/decryption boundary; responses,
logs, metrics, and audit events never echo a key.

Bind, unbind, bound-cohort preview, and rotate require the dedicated
`secret:binding-manage` policy operation. It is not granted implicitly to a
namespace's own identity: delegate it with an exact namespace rule when
needed, or with `secret:*` when the identity intentionally manages the entire
secret lifecycle. `secret:write` alone does not grant binding management.
Bound-cohort purge and both unbound-version operations require administrator
status plus `secret:destroy`; these checks happen before secret lookup. Bound-
cohort preview remains a non-destructive `secret:binding-manage` operation.

Successful bind, unbind, and rotate operations commit their sanitized allow
audit in the same transaction as the new version, revision, and change-log row;
an audit insertion failure rolls the mutation back and watchers are not
woken. Preview returns cohort information only after its sanitized allow audit
is durable. Authorization denials and authorized failures are audited through
the normal sanitized paths as far as the audit sink is available. Binding keys
are never included in those rows.

## Purge erasure boundary

Purge replaces each selected version with a minimal tombstone and erases its
ciphertext, encrypted DEK, nonce, AAD, KEK/wrapping data, expiry, content type,
metadata, and protection flags. Labels are preserved exactly and no historical
version is promoted. If `current` points at a purged version, the current
projection is cleared. A per-path
high-water mark prevents a delete/recreate cycle from reusing an old version
number and accidentally satisfying an immutable release pin.

KMS enables SQLite secure deletion on every connection. It reports ordinary
purge success only after the committed tombstones have passed a successful
truncating WAL checkpoint, removing the retired payload from the active
database and WAL. If the transaction committed but physical cleanup remains
pending, the service fails closed and reports HTTP 503 with code
`purge_cleanup_pending` or gRPC `Unavailable`, with fixed text
`secret purge committed; database artifact cleanup is pending`. SDKs expose
Go `ErrPurgeCleanupPending`, Python `PurgeCleanupPendingError`, and TypeScript
`PurgeCleanupPendingError`. The purge is logically committed. Because gRPC
cannot return a response with an error, no purge result accompanies this
outcome. Do not retry a bound-cohort purge with the retired key, and do not
retry an unbound-version purge as though its preview were still live.

This guarantee covers only the active SQLite database and its WAL. It cannot
retract copies in backups, filesystem or volume snapshots, copy-on-write
layers, replicas, crash dumps, or raw media. Operators must expire those copies
under their retention policy, use storage encryption and media destruction as
appropriate, and rotate or revoke the compromised upstream application secret
itself.

## Releases

Release entries contain only alias, kind, home-namespace resource reference,
exact version, content type, metadata, and a parameter digest. They never carry
`bound` or `has_access_token`, and those fields do not enter the release digest.
Because a non-destroyed version's protection flags cannot change, an exact
version pin nevertheless pins its protection mode implicitly. Both parameter
and secret pins must belong to the release's own `(env, app)` namespace.

Before fetching an exact secret pin, release loaders fetch live metadata and
verify the response identity, version, enabled/destroyed state, expiry, and the
exact version's two protection flags. They resolve access tokens and binding
keys independently, per alias. A missing required credential rejects the whole
candidate as `token_unavailable`; a wrong credential or failed resolution is
`resolution_failed`. Startup fails until one complete snapshot applies. During
hot reload, a rejected candidate never partially replaces the last-known-good
snapshot.

A protection transition does not rewrite an existing release. The operational
sequence is: transition current, create and activate a release that explicitly
pins the new version, retire releases that pin the source, then purge the old
bound cohort or unbound versions if policy requires erasure. The new release
has a different digest because its exact version pin differs; the digest format
itself is unchanged.

Any future protection-mode toggle must likewise create a new version. Rotating
the per-secret access-token credential may replace the credential accepted by
an already gated version, but it may never clear that version's
`has_access_token` requirement.

Bulk `env` and `exec` deliberately do not consume binding keys or call
`GetSecret` for bound versions. Secret-inclusive bulk resolution fails closed
when it selects a bound version; it never synthesizes an empty credential.
`--no-secrets` is the intentional parameter-only path. Namespace mode may
explicitly use `--allow-incomplete-secrets` to omit unavailable secrets with a
warning; `exec` also removes their plain and possible `_B64` names from the
inherited environment. Release mode remains atomic and rejects incomplete
resolution.
