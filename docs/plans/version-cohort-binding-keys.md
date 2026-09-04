# Version-cohort binding keys, SDK contract, and compromise purge

## Context and motivation

The `0.2.x` client-bound design couples a server-minted token to each secret version. Applications must manage alias-specific token files, older release pins require old unrecoverable tokens, and changing protection can force a new secret version and a manually rebuilt release. This operational cost provides little additional isolation under KMS’s threat model, where possession of a deployment already implies possession of that deployment’s KMS credentials.

The `0.3.x` design instead treats a binding key as operator-owned encryption material supplied directly with a specific secret operation or SDK declaration. It preserves independently keyed historical version cohorts, permits in-place DEK rewrapping, and adds a focused break-glass purge: if a binding key is compromised, an administrator can irreversibly destroy precisely the contiguous versions encrypted with that key without deleting unrelated history or rewriting immutable releases.

## Summary

Replace client-bound tokens with operator-supplied binding keys attached to individual secret versions.

Adjacent versions encrypted with the same binding key form an implicit cohort. KMS never stores a key, key hash, fingerprint, or cohort identifier; membership is discovered only by cryptographically opening adjacent DEKs with a supplied key.

A compromised binding key can be purged by anchoring at one version. KMS destroys that version and adjacent versions of the same secret that open with the same key, stopping at the first cohort boundary.

## Wire and domain contracts

### Secret metadata

- Replace `SecretMetadata.client_bound` with `bound` in the clean `0.3.x` message layout; no old field name or number is reserved.
- `SecretMetadata.bound` describes the version selected by the `current` label.
- Extend `SecretVersionInfo` with:
  - `bool bound`
  - `bool has_access_token`
- Exact-version operations and release loading must use the version fields, never infer protection from the current-version summary.
- Rename internal domain/model fields from `ClientBound` and `ClientKeySalt` to `Bound` and `BindingKeySalt`.
- Internal wrap modes become `standard` and `binding_key`.

### Secret reads and writes

- Define the compact `0.3.x` read request as `ref = 1`, `version = 2`, `label = 3`, `secret_token = 4`, and `binding_key = 5`.
- Define the compact `0.3.x` write request as `ref = 1`, `value = 2`, `content_type = 3`, `metadata_json = 4`, `binding_key = 5`, `generate_access_token = 6`, and `expires_at_unix_ms = 7`.
- Remove `client_bound` and the write-side `secret_token` outright. Do not reserve their old names or numbers; the latter’s only existing purpose was proving the conflated client-bound token.
- A non-empty `binding_key` creates a bound version. An empty value creates an unbound version, regardless of the preceding version’s protection.
- A non-empty binding key must contain at least 32 UTF-8 bytes. No normalization, trimming, prefix requirement, or equality comparison is performed server-side.
- Access-token behavior remains separate:
  - `generate_access_token` creates or rotates the secret-level access token.
  - New versions inherit token gating while `access_token_hash` exists.
  - Reads of token-gated versions require `secret_token`.
  - Reads of versions that are both bound and token-gated require both fields.

### Binding mutation RPCs

Add these SecretService operations:

```proto
rpc BindSecret(BindSecretRequest) returns (SecretVersionMutationResponse);
rpc UnbindSecret(UnbindSecretRequest) returns (SecretVersionMutationResponse);
rpc PreviewSecretBindingCohort(PreviewSecretBindingCohortRequest)
    returns (SecretBindingCohortResponse);
rpc RotateSecretBindingKey(RotateSecretBindingKeyRequest)
    returns (SecretBindingCohortResponse);
rpc PurgeSecretBindingCohort(PurgeSecretBindingCohortRequest)
    returns (SecretBindingCohortResponse);
```

Request semantics:

- `BindSecret(ref, version, binding_key)`
  - `version = 0` means current.
  - Requires an existing, non-destroyed, unbound version.
  - Binds only that exact version in place.
- `UnbindSecret(ref, version, binding_key)`
  - Requires an existing, non-destroyed, bound version.
  - The supplied key must open that version.
  - Unbinds only that exact version.
- `RotateSecretBindingKey(ref, anchor_version, binding_key, new_binding_key)`
  - `anchor_version = 0` means current.
  - Rotates the contiguous cohort around the anchor.
- `PreviewSecretBindingCohort(ref, anchor_version, binding_key)`
  - `anchor_version = 0` means current.
  - Discovers the cohort without mutating it and returns the current storage revision.
- `PurgeSecretBindingCohort(ref, anchor_version, binding_key)`
  - `anchor_version = 0` means current.
  - Admin-only and purges the contiguous matching cohort.
- Mutation responses return:
  - Anchor version.
  - Sorted affected version numbers.
  - Resulting storage revision.
  - No derived key or cohort identifier.
- Interactive rotate and purge callers pass the preview revision and affected
  versions back as compare-and-swap guards. The server rediscovers and compares
  the cohort inside the mutation transaction; any intervening change aborts
  before rewrap or destruction, so the executed set is exactly the set the
  administrator confirmed.

Missing and incorrect binding keys return the same sanitized permission/decryption errors used for other secret credentials. Errors and audit records must never echo request fields.

## Cryptographic and cohort behavior

### Version encryption

For a bound version:

1. Generate a random DEK and encrypt the plaintext as today.
2. Derive a 256-bit wrapping key using HKDF-SHA256 over the opaque binding-key bytes and a fresh random 32-byte salt.
3. Encrypt the DEK under the derived binding key.
4. Encrypt that inner result under the active server KEK.
5. Persist ciphertext, outer encrypted DEK, KEK ID, binding salt, algorithm, nonce, AAD, and `bound=true`.

For an unbound version, wrap the DEK directly under the server KEK and persist no binding salt.

Zero derived keys, unwrapped DEKs, and temporary inner plaintext buffers as soon as each operation completes. Go strings containing caller-supplied keys cannot be reliably zeroed, so they must never be copied unnecessarily or retained beyond request/configuration lifetime.

### In-place bind, unbind, and rotate

- Binding mutations never decrypt or rewrite the stored secret-value ciphertext.
- Bind:
  - Open the standard DEK using the version’s recorded KEK.
  - Add a binding-key layer with a new salt.
  - Reapply the KEK layer.
- Unbind:
  - Open the KEK layer and then the supplied binding-key layer.
  - Rewrap the raw DEK directly under the same KEK.
- Rotate:
  - Discover the cohort using the old key.
  - Rewrap each selected DEK with the new key and a fresh independent salt.
  - Preserve each version’s original KEK ID unless a separate KEK rotation occurs.
- Bind/unbind affect one version. Rotate affects a discovered cohort.
- Serialize these operations with secret puts, purge, and server-KEK rotation.
- Commit all cohort changes in one transaction. Any stale row, wrong anchor key, corrupt selected version, or concurrent mutation aborts the operation.

### Cohort discovery

Given an anchor version and binding key:

1. The anchor must exist, be non-destroyed, and be bound.
2. Cryptographically open the anchor’s binding layer. Failure rejects the operation without scanning further.
3. Scan version numbers downward from `anchor-1` and upward from `anchor+1`.
4. For each adjacent version:
   - Bound and successfully opened with the supplied key: include it.
   - Unbound: stop in that direction.
   - Bound but not opened by the key: stop in that direction.
   - Missing, destroyed, or cryptographically corrupt: stop in that direction.
5. Never jump across a boundary, even if a later version happens to reuse the same key.
6. Sort and deduplicate the resulting version list before mutation.

This produces the intended grouping without storing key identity. For key epochs `v1=A`, `v2–v3=B`, and `v4–v5=C`, anchoring at `v5` with key C affects only versions 4 and 5.

## Purge semantics

- `PurgeSecretBindingCohort` requires an authenticated administrator; `secret:destroy` policy delegation is insufficient.
- The operation deliberately bypasses current/previous release-reference protection.
- For each selected version, irreversibly erase:
  - Secret ciphertext.
  - Encrypted DEK.
  - Nonce.
  - Binding-key salt.
  - Any other recoverable cryptographic payload.
- Retain a minimal tombstone containing version number, destroyed state, creation/destruction timestamps, and non-sensitive audit identity.
- Clear version metadata if it may contain operator-supplied sensitive information.
- Preserve labels exactly. If `current` points into the purged cohort, current becomes unreadable; KMS never auto-promotes another version.
- Append one transactional change-log entry describing the affected versions and one sanitized admin audit event. Neither contains the binding key.
- There is no purge-all flag and no arbitrary version list.
- Ordinary `DestroySecretVersion` and `DeleteSecret` retain their release-reference safeguards.
- Maintain a non-secret per-path version high-water mark even after deletion, so recreating `/env/app/key` cannot reuse version numbers and accidentally satisfy an old release pin.
- Releases remain immutable:
  - Reading the manifest still works.
  - Validation and activation fail if they reference a purged version.
  - A currently active invalidated release remains labeled active, but new application startups cannot resolve it.
  - Already-running applications retain their previously published snapshot until restarted or replaced.

## Go SDK

### Direct client

Add:

```go
func WithBindingKey(key string) GetOption
func WithPutBindingKey(key string) PutSecretOption
```

Remove `WithClientBound` and `WithPutSecretToken`.

`GetSecret` sends `SecretToken` and `BindingKey` independently. `PutSecret` determines bound state from `WithPutBindingKey`.

Add management methods with `version == 0` meaning current:

```go
func (c *Client) BindSecret(
    ctx context.Context, key string, version uint64, bindingKey string,
) (SecretVersionMutationResult, error)

func (c *Client) UnbindSecret(
    ctx context.Context, key string, version uint64, bindingKey string,
) (SecretVersionMutationResult, error)

func (c *Client) RotateSecretBindingKey(
    ctx context.Context, key string, anchorVersion uint64,
    bindingKey, newBindingKey string,
) (SecretBindingCohortResult, error)

func (c *Client) PurgeSecretBindingCohort(
    ctx context.Context, key string, anchorVersion uint64, bindingKey string,
) (SecretBindingCohortResult, error)
```

All successful mutations invalidate the complete secret cache entry.

### Secret types

Extend `kmsclient.Secret`:

```go
type Secret struct {
    BindKey string // declaration-only credential
    // existing private plaintext and metadata fields
}
```

Conventions:

- `BindKey` allows generated configuration defaults such as:
  ```go
  OpenAIAPIKey: config.Secret{
      BindKey: resolveFromEnvVar("OPENAI_API_KMS_BIND_KEY"),
  }
  ```
- `IsZero` continues to mean “contains no plaintext”; a declaration containing only `BindKey` is zero for secret-value purposes.
- `Clone` copies `BindKey` while the value is still a declaration.
- Generated startup extracts `BindKey` into private loader credentials and clears it from cloned defaults and published resolved secrets.
- Secrets returned directly by `GetSecret` do not contain the supplied binding key.
- `String`, `GoString`, `Format`, and JSON serialization continue to emit only `[REDACTED]`.

Extend declarative `SecretValue`:

```go
type SecretValue struct {
    Key     string
    Token   string
    BindKey string
    EnvVar  string
    Default string
}
```

- Environment overrides skip KMS and do not consume either credential.
- Store resolution calls `GetSecret` with both configured credentials.
- Default fallback behavior stays unchanged and must not mask credential errors unless the caller explicitly enabled fallback-on-any-error.
- All `SecretValue` formatting remains redacted.

### Managed release loading

Add:

```go
type ReleaseLoaderConfig struct {
    // existing fields
    SecretTokenProvider SecretTokenProvider
    BindingKeys         map[string]string // keyed by release alias
}
```

- Defensive-copy `BindingKeys` during loader construction.
- Never expose it through status, stats, errors, acknowledgements, or formatting.
- The generated `Options` type does not expose `BindingKeys`; the generated binding builds the map from credential-only secret defaults.
- Empty `BindKey` fields are omitted.
- Do not compare binding keys belonging to different aliases.
- The existing `SecretTokenProvider(alias, path)` remains because access tokens are independently per secret.

## Python SDK

### Direct sync/async clients

Use snake_case consistently:

```python
client.get_secret(
    key,
    version=0,
    label="",
    secret_token="",
    binding_key="",
    timeout=None,
)

client.put_secret(
    key,
    value,
    content_type="",
    metadata_json="",
    binding_key="",
    generate_access_token=False,
    expires_at_unix_ms=0,
    timeout=None,
)
```

Apply identical signatures to `AsyncClient`.

Add sync and async methods:

```python
bind_secret(key, *, version=0, binding_key, timeout=None)
unbind_secret(key, *, version=0, binding_key, timeout=None)
rotate_secret_binding_key(
    key, *, anchor_version=0, binding_key, new_binding_key, timeout=None
)
purge_secret_binding_cohort(
    key, *, anchor_version=0, binding_key, timeout=None
)
```

Remove `client_bound` and write-side `secret_token` arguments from `put_secret`.

### Secret declarations

Extend `Secret` with a keyword-only `bind_key` constructor argument and read-only `bind_key` property.

Generated Pydantic configuration fields may declare:

```python
openai_api_key: Annotated[Secret, SecretField("openai-api-key")] = Secret(
    bind_key=resolve_from_env("OPENAI_API_KMS_BIND_KEY")
)
```

- `Secret()` remains the unbound declaration.
- Config generation accepts defaults only when the Secret has no plaintext, path, version, or content type; `bind_key` may be populated.
- Implement `clone`, `__copy__`, and `__deepcopy__` so declaration copying preserves the key without exposing it.
- Generated binding construction extracts alias keys and removes them from source-default payloads and resolved snapshots.
- `repr`, `str`, `format`, Pydantic serialization, and validation errors remain redacted.

Extend `SecretValue` with `bind_key=""`; sync and async resolution pass both `secret_token` and `binding_key`.

Add `binding_keys: Mapping[str, str]` to both `ReleaseLoaderConfig` and `AsyncReleaseLoaderConfig`. Normalize it to a private immutable copy. Generated managed stores populate it automatically while explicit low-level loader users may pass it directly.

## TypeScript SDK

### Direct client

Update options:

```ts
interface GetOptions {
  version?: bigint;
  label?: string;
  secretToken?: string;
  bindingKey?: string;
  signal?: AbortSignal;
  deadline?: Date;
}

interface PutSecretOptions {
  contentType?: string;
  metadataJson?: string;
  bindingKey?: string;
  generateAccessToken?: boolean;
  expiresAtUnixMs?: bigint;
  signal?: AbortSignal;
  deadline?: Date;
}
```

Remove `clientBound` and write-side `secretToken`.

Add:

```ts
bindSecret(key, { version?, bindingKey, signal?, deadline? })
unbindSecret(key, { version?, bindingKey, signal?, deadline? })
rotateSecretBindingKey(
  key,
  { anchorVersion?, bindingKey, newBindingKey, signal?, deadline? },
)
purgeSecretBindingCohort(
  key,
  { anchorVersion?, bindingKey, signal?, deadline? },
)
```

Return frozen mutation/cohort result objects.

### Secret declarations

Replace the current non-sensitive-only constructor metadata type with:

```ts
interface SecretOptions {
  path?: string;
  version?: bigint;
  contentType?: string;
  bindKey?: string;
}
```

Support:

```ts
openAIAPIKey: new Secret("", {
  bindKey: resolveFromEnv("OPENAI_API_KMS_BIND_KEY"),
})
```

- Store `bindKey` privately and expose a read-only getter for generated binding extraction.
- `clone()` preserves it while cloning a declaration.
- Generated startup extracts and strips it before publishing resolved snapshots.
- Directly fetched Secrets never retain request credentials.
- `toString`, `toJSON`, coercion, and Node inspection remain `[REDACTED]`.

Add `bindKey?: string` to `SecretValueOptions`. Pass it as `bindingKey` during store resolution.

Add `bindingKeys?: Readonly<Record<string, string>>` to `ClientReleaseLoaderOptions`, internal `ReleaseLoaderOptions`, and `ManagedConfigOptions`. Normalize into a null-prototype frozen object and never include it in object spreads used for diagnostics or status.

Generated TypeScript stores derive the alias map from credential-only default Secrets; callers of generated stores do not supply a separate map.

## Cross-SDK release-loader algorithm

For every pinned secret entry:

1. Verify that its namespace equals the release namespace.
2. Fetch live metadata and locate the exact pinned version.
3. Reject missing/destroyed/disabled/expired versions as resolution failures.
4. If `has_access_token`:
   - Invoke the existing token provider for that alias/path.
   - Missing provider, provider error, or empty result → `ReleaseRejectTokenUnavailable`.
5. If `bound`:
   - Look up the release alias in the private binding-key map.
   - Missing or empty value → `ReleaseRejectTokenUnavailable`.
6. Call `GetSecret` with only the credentials required by that exact version.
7. Any supplied-but-wrong credential, permission failure, or decryption error → `ReleaseRejectResolutionFailed`.
8. Verify returned ref, version, and content type before admitting the value.
9. Do not publish any candidate until every parameter and secret resolves and generated validation succeeds.

The metadata lookup and secret fetch count as one unit under the existing concurrency limit and cancellation signal. A superseded candidate cancels both.

## Credential and cache conventions

- Binding keys are always application/operator-owned strings.
- SDKs never read binding-key files or implement a binding-key directory.
- Generated application code may source keys from any mechanism; environment-variable helpers are application code, not SDK policy.
- Low-level alias maps use release aliases, not resource paths.
- Extra map entries are retained privately but never transmitted. Only the exact alias being resolved is looked up.
- Secret-value caching is disabled. Binding and access-token requirements are
  mutable live metadata, so even a formerly uncredentialed cache entry could
  otherwise bypass protection added by another client after the cache fill.
- Binding keys are never included in cache keys, exception text, callbacks, tracing attributes, metrics labels, acknowledgements, or test snapshots.
- Secret comparison and change reporting use only resolved path/version identity; changing a local binding-key declaration alone is not reported as a configuration-value change.

## CLI and offline conventions

- `kms binding-key generate` writes one 256-bit Base64URL key to stdout and nothing else, allowing redirection into the operator’s chosen secret manager.
- No binding-key file flags or environment-file variables exist.
- Single-secret commands accept:
  - `KMS_BINDING_KEY` for non-interactive invocation.
  - A non-echoing prompt when the variable is absent and stdin is a terminal.
  - `KMS_NEW_BINDING_KEY` or a second prompt for rotation.
- Online operations use existing bearer-token/mTLS administrator authentication; the running server uses its configured KEK.
- There is no offline secret-read or secret-export command in the current CLI.
  If a single-secret offline read is introduced later, it must use the existing
  KEK-loading mechanism plus the supplied binding key; this design does not add
  that interface.
- Bulk `env` and `exec` never request binding keys:
  - Emit parameters normally.
  - Resolve unbound secrets normally, including access-token handling.
  - Emit an empty string for every bound-secret output.
  - Never silently substitute defaults for those bound values.
  - Scrub `KMS_BINDING_KEY` and `KMS_NEW_BINDING_KEY` before launching a child process.

## Releases and console

- Remove protection flags from configuration-release protobufs, domain structs, database models, digest projections, all SDK release models, and console views.
- `ConfigurationReleaseEntry` ends at `parameter_digest = 7`; remove the former protection fields without reserving their names or numbers.
- Server creation and storage validation reject every parameter or secret ref outside the release’s namespace, without an admin exception.
- Store version high-water marks independently from deletable secret material.
- Console secret history displays `bound` and `has_access_token` per version.
- Bind/unbind actions target one version; rotate and purge preview and confirm the discovered affected version range.
- Binding-key form fields are password inputs held only in request-local browser state and cleared after submission.
- Purge confirmation names the secret, anchor, and affected versions; no key-derived identifier is displayed.

## 0.3.x greenfield storage baseline

- Version `0.3.0` establishes a new storage baseline for the entire `0.3.x` line; it is not an incremental migration from `0.2.x`.
- Treat the protobuf contract the same way: `0.3.0` makes no binary or JSON wire-compatibility promise to `0.2.x` clients.
- Remove `reserved` declarations that exist only to preserve removed `0.2.x` fields, including the former release-protection and schema-ID gaps, and renumber affected messages densely from field 1.
- Delete the complete existing migration history, including SQL migration files, bespoke Go migration helpers, legacy repair/upgrade branches, migration fixtures, and tests whose purpose is upgrading an older schema.
- Set the new baseline schema version to 1 and materialize only the final `0.3.x` table, index, constraint, and trigger layout from current storage models.
- Keep the schema-version table as the starting point for future migrations within or after the `0.3.x` line, but do not retain executable migrations predating this baseline.
- Database initialization succeeds only for an empty/new database or a database already stamped with the exact `0.3.x` baseline schema.
- Opening a `0.2.x`, unstamped non-empty, partially migrated, or otherwise legacy database fails before any schema or data mutation with a clear incompatible-baseline error.
- Do not implement data copy, backfill, compatibility columns, dual reads/writes, legacy protobuf translation, or an in-place upgrade command.
- Deployment documentation must require operators upgrading to `0.3.0` to create a fresh database and repopulate KMS through normal administrative/bootstrap workflows.

## Verification

- Contract tests verify the new compact protobuf field layouts and generated Go/Python/TypeScript output; they do not preserve or test legacy field reservations.
- Crypto tests cover bound/unbound encryption, wrong keys, fresh salts, in-place rewrap, zeroization paths, and unchanged value ciphertext.
- Cohort tests cover first/middle/last anchors, bidirectional scans, key reuse after a boundary, destroyed/missing/corrupt boundaries, disabled/expired versions, and transaction rollback.
- Purge tests verify exact affected versions, current-label failure, immutable invalid releases, admin-only authorization, audit redaction, and version-number non-reuse.
- SDK tests in all three languages cover:
  - Direct get/put option mapping.
  - Both credentials together.
  - Credential-aware cache bypass.
  - Declarative `Secret` and `SecretValue` behavior.
  - Generated alias-map extraction and stripping.
  - Sync/async parity.
  - Redaction under every supported formatter/serializer.
  - Missing versus wrong release credential categories.
  - Startup failure and last-known-good hot reload.
- CLI tests cover prompts, environment-string inputs, stdout-only generation,
  bulk empty bound values, and child-environment scrubbing.
- Run race-enabled Go tests, Python sync/async tests, TypeScript tests and type checks, frontend tests/build, protobuf generation checks, and configuration-generator golden tests.

## Explicit consequences

- A release pinned to an older binding-key cohort requires that cohort’s key. KMS cannot recover historical keys because it stores no identifier or verifier.
- Changing a secret value while supplying a different binding key begins a new cohort; it does not rewrap older versions.
- Rotation and compromise purge are deliberately cohort-scoped, while bind and unbind are exact-version operations.
- This is the breaking greenfield baseline for the `0.3.x` line, with no migration path for `0.2.x` databases, client-bound tokens, generated bindings, or old release digests.
