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
Destroyed cohort tombstones report both flags as false.

Secret plaintext is never cached by the Go, Python, or TypeScript SDK. Watches
carry metadata-only notifications, and a consumer explicitly refetches when it
needs plaintext.

## Bind, unbind, rotate, and purge

Binding and unbinding rewrap one exact version in place, without creating a
version or changing the secret-value ciphertext:

```bash
KMS_BINDING_KEY="$new_key" parameter-store secret bind /prod/app/api-key --version 7
KMS_BINDING_KEY="$current_key" parameter-store secret unbind /prod/app/api-key --version 7
```

Bind refuses a proposed key that already opens either immediate live bound
neighbor of the selected version. This prevents an exact-version operation
from silently joining cohorts outside its singleton affected-version result.
Unbind can only split a cohort and needs no corresponding check.

`--version 0` (the default) selects `current`. Rotation and purge first preview
the contiguous cohort around the anchor, print the exact affected versions,
and request confirmation. They replay the preview's revision and version list
as compare-and-swap guards, so a concurrent change aborts before mutation:

```bash
KMS_BINDING_KEY="$old_key" KMS_NEW_BINDING_KEY="$new_key" \
  parameter-store binding-key rotate /prod/app/api-key --version 7

KMS_BINDING_KEY="$compromised_key" \
  parameter-store secret purge-binding-cohort /prod/app/api-key --version 7
```

A rotation rejects `binding_key == new_binding_key` byte for byte. SDKs, the
CLI, and the console reject this locally before sending the mutation. The
server remains authoritative: it validates and authorizes the request, proves
that the current key opens the anchor, and checks any CAS guard before reporting
the fixed `invalid_argument` error. Consequently, a missing or wrong current
key still follows the same sanitized credential/decryption path, and a stale
preview still reports a conflict rather than revealing the equality decision.
No key material is included in the error.

Rotation also refuses a replacement key that opens an immediate live bound
neighbor just outside the rediscovered source cohort. The server performs this
check inside the mutation transaction, after validating the optional preview
guard and before preparing or writing any rewrapped DEK. A rejected implicit
merge returns a fixed, sanitized `failed_precondition` and changes no version,
revision, change log, or allow audit. Missing, unbound, destroyed, corrupt, and
differently keyed versions remain hard boundaries, so the same key may still
be reused beyond one of them. Intentional cohort merging is not supported in
`0.3.x`; a future merge operation would need to preview and guard the complete
resulting cohort.

Purge requires an authenticated administrator and is irreversible. It bypasses
the normal current/previous release-reference safeguard because its purpose is
to remove compromised material. It does not delete every historical version.

A cohort is discovered cryptographically. KMS opens the bound anchor with the
supplied key, then scans adjacent version numbers in both directions. It stops
at the first unbound, missing, destroyed, corrupt, or differently bound
version. It never jumps over that boundary, even if a later version reuses the
same key. For `v1=A`, `v2-v3=B`, and `v4-v5=A`, anchoring at v5 with A affects
only v4 and v5.

The RPCs are `BindSecret`, `UnbindSecret`,
`PreviewSecretBindingCohort`, `RotateSecretBindingKey`, and
`PurgeSecretBindingCohort`. Bind/unbind return the anchor, a singleton sorted
affected-version list, and the resulting revision. Preview, rotate, and purge
return the anchor, sorted affected versions, and current/resulting revision.
Rotate and purge accept optional `expected_revision` and
`expected_affected_versions`; they must be supplied together for a guarded
operation.

The matching HTTP endpoints are:

| Operation | Endpoint | Additional request fields |
|---|---|---|
| Bind | `POST /api/v1/secrets/bind` | `version`, `binding_key` |
| Unbind | `POST /api/v1/secrets/unbind` | `version`, `binding_key` |
| Preview | `POST /api/v1/secrets/binding-cohort/preview` | `anchor_version`, `binding_key` |
| Rotate | `POST /api/v1/secrets/binding-key/rotate` | `anchor_version`, `binding_key`, `new_binding_key`, optional paired CAS fields |
| Purge | `POST /api/v1/secrets/binding-cohort/purge` | `anchor_version`, `binding_key`, optional paired CAS fields |

Every body also contains `env`, `app`, and `key`. Every successful response is
`{"anchor_version":N,"affected_versions":[...],"revision":N}`. Missing and
incorrect keys collapse to the same sanitized credential/decryption boundary;
responses, logs, metrics, and audit events never echo a key.

Bind, unbind, preview, and rotate require the dedicated
`secret:binding-manage` policy operation. It is not granted implicitly to a
namespace's own identity: delegate it with an exact namespace rule when
needed, or with `secret:*` when the identity intentionally manages the entire
secret lifecycle. `secret:write` alone does not grant binding management.
Purge remains administrator-only regardless of delegated policy.

Successful bind, unbind, and rotate operations commit their sanitized allow
audit in the same transaction as the DEK rewrap, revision, and change-log row;
an audit insertion failure rolls the mutation back and watchers are not
woken. Preview returns cohort information only after its sanitized allow audit
is durable. Authorization denials and authorized failures are audited through
the normal sanitized paths as far as the audit sink is available. Binding keys
are never included in those rows.

## Purge erasure boundary

Purge replaces each selected version with a minimal tombstone and erases its
ciphertext, encrypted DEK, nonce, binding salt, and operator metadata. Labels
are preserved exactly. If `current` points at a purged version, it becomes
unreadable; KMS does not silently promote another version. A per-path
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
`PurgeCleanupPendingError`. The purge is logically committed: do not retry it
with the retired key. Because gRPC cannot return a response with an error, no
cohort result accompanies this outcome.

This guarantee covers only the active SQLite database and its WAL. It cannot
retract copies in backups, filesystem or volume snapshots, copy-on-write
layers, replicas, crash dumps, or raw media. Operators must expire those copies
under their retention policy, use storage encryption and media destruction as
appropriate, and rotate or revoke the compromised upstream application secret
itself.

## Releases

Release entries contain only alias, kind, home-namespace resource reference,
exact version, content type, metadata, and a parameter digest. They never carry
`bound` or `has_access_token`; protection is live state, not part of an
immutable pin or digest. Both parameter and secret pins must belong to the
release's own `(env, app)` namespace.

Before fetching an exact secret pin, release loaders fetch live metadata and
verify the response identity, version, enabled/destroyed state, expiry, and the
exact version's two protection flags. They resolve access tokens and binding
keys independently, per alias. A missing required credential rejects the whole
candidate as `token_unavailable`; a wrong credential or failed resolution is
`resolution_failed`. Startup fails until one complete snapshot applies. During
hot reload, a rejected candidate never partially replaces the last-known-good
snapshot.

Bulk `env` and `exec` deliberately do not consume binding keys or call
`GetSecret` for bound versions. Secret-inclusive bulk resolution fails closed
when it selects a bound version; it never synthesizes an empty credential.
`--no-secrets` is the intentional parameter-only path. Namespace mode may
explicitly use `--allow-incomplete-secrets` to omit unavailable secrets with a
warning; `exec` also removes their plain and possible `_B64` names from the
inherited environment. Release mode remains atomic and rejects incomplete
resolution.
