# Security Model

This document describes the encryption, authentication, authorization, and
audit design implemented in `internal/crypto`, `internal/ca`, `internal/core`,
`internal/policy`, and `internal/keyutil`, and states plainly what the system
does and does not protect against. It is the security reference; see
[`operations.md`](operations.md) for the operational procedures (backup,
restore, key rotation) that follow from it.

## Threat model, in one paragraph

The design goal is: an attacker who obtains the SQLite database file (backup
theft, disk snapshot, stolen laptop with a dev copy) cannot recover **secret
plaintext** without also obtaining the master key. The database still exposes
parameters, configuration release/schema metadata, resource metadata,
identities, policies, and audit records, so it
must remain access-controlled. An attacker who *also* has the master key
still cannot decrypt secrets that were opted into client-bound mode — that
requires the plaintext client token as well, which never touches the
database. This defends against **offline theft of secret data at rest**. It does
**not** defend against a live compromise of the running server process: an
attacker with code execution on the KMS host can read master key material
from process memory, intercept client tokens as they arrive on requests, and
decrypt anything the server itself can decrypt. Section
["What this does not protect against"](#what-this-does-not-protect-against)
spells this out further.

## Envelope encryption

Every secret **version** is encrypted independently under its own
Data Encryption Key (DEK):

```
plaintext secret
   | AES-256-GCM, fresh random 12-byte nonce, fresh random 32-byte DEK
   v
ciphertext + nonce           (stored in secret_versions.ciphertext / .nonce)

DEK
   | AES-256-GCM, wrapped under the active KEK
   v
encrypted_dek                (stored in secret_versions.encrypted_dek)
```

Implementation: `internal/crypto/aead.go` (`seal`/`open`, `sealPacked`/
`openPacked` — nonce and ciphertext packed as `nonce || ciphertext`, GCM tag
appended by the cipher) and `internal/crypto/envelope.go` (`Encrypt`,
`EncryptClientBound`, `Decrypt`, `RewrapDEK`). The only algorithm implemented
is AES-256-GCM (`crypto.AlgorithmAES256GCM`); there is no unauthenticated
mode and no custom cipher.

A fresh DEK per version means nonce reuse under the same key is structurally
avoided — the nonce only ever needs to be unique per DEK, and each DEK is
used to seal exactly one value plus (for client-bound secrets) wrap one
inner layer.

### Associated data (AAD) binding

Every ciphertext is bound to an associated-data string built by
`crypto.BuildAAD` from the secret's namespace, key, and version:

```
env=<env>;app=<app>;key=<key>;version=<version>
```

plus an internal domain-separation suffix appended per layer
(`|layer:value`, `|layer:dek`, `|layer:dek-inner`) so the value ciphertext,
the DEK wrap layer, and the client-bound inner layer can never be confused
for one another even though they may use overlapping key material. Because
AES-GCM authenticates the AAD, a ciphertext blob copied into a different
record, a different version, or a different layer fails authentication
immediately rather than decrypting to garbage or — worse — to a
plausible-looking wrong value. This is what plan §10.5 calls binding
"namespace, secret name, version" as associated data — now expressed as the
explicit `(env, app, key)` triple rather than a packed path.

The old `type:<resource_type>` component is gone: only secrets are
envelope-encrypted (parameters are stored in plaintext, see below), so there
is no second resource type for a secret's ciphertext to be confused with, and
`(env, app, key, version)` already identifies a secret version uniquely. The
per-layer suffixes are unchanged.

### Parameters vs. secrets

Parameters are non-secret and are stored as plaintext in `parameters` /
`parameter_versions` (still gated by the same namespace-level
authorization). Secrets are always encrypted; there is no code path that
writes secret plaintext to SQLite.

## KEK management and master key acquisition

The Key Encryption Key (KEK) wraps DEKs. It is never stored in SQLite. On
startup (`internal/crypto/unseal.go`, `Unseal`), the service acquires it in
this order:

1. **Key file** (`encryption.kek_file`): raw key material read from disk —
   32 raw bytes, 64 hex characters, or base64 of 32 bytes
   (`LoadKEKMaterialFromFile`). This is the unattended path: a restart needs
   no human.
2. **Passphrase**: if no key file is configured/present, a human passphrase
   is required — either pre-supplied (e.g. via `KMS_MASTER_PASSPHRASE`) or
   read interactively with a no-echo TTY prompt (confirmed twice on first
   initialization). The passphrase is stretched into 32 bytes of KEK
   material with **argon2id** (`DeriveKEKMaterialFromPassphrase`), using a
   random salt persisted in `key_metadata.kdf_salt` and cost parameters
   persisted as JSON in `key_metadata.kdf`. Default parameters
   (`DefaultArgon2Params`) follow RFC 9106's second recommended profile: 64
   MiB memory, 3 iterations, up to 4 threads.

If neither is available — no key file and stdin is not a TTY (and no
pre-supplied passphrase) — the service fails fast with `ErrNoKeySource`
rather than hanging a systemd unit on an invisible prompt.

**Key-check canary.** At initialization the service encrypts a fixed
plaintext (`"kms/v1 key-check canary"`) under the new KEK and stores the
result in `key_metadata.key_check` (`NewKeyCheck`). On every subsequent
unseal, `VerifyKeyCheck` decrypts this canary with the presented key; a wrong
passphrase or the wrong key file fails immediately at startup with an
actionable error, rather than surfacing later as scattered decryption
failures on live traffic.

**Which mode was used is recorded**, not guessable: `key_metadata.source` is
`"file"` or `"passphrase"`. A database initialized with a file-based key
cannot later be unlocked with a passphrase, and vice versa — `unlock()`
returns a specific error naming the mismatch.

Key material is held only as an unexported `[]byte` inside `*crypto.KEK` and
is explicitly zeroed (`crypto.Zero`) as soon as it is no longer needed —
after deriving/loading it into a `KEK`, after using a passphrase, after a
rewrap during rotation. Go cannot guarantee no copy of a byte slice ever
existed in memory (GC, escape analysis), but this removes the primary
buffer immediately rather than leaving it for GC.

### Keyring and rotation

`crypto.Keyring` can hold active and retired KEKs, which permits safe concurrent
reads during a rotation. `Service.RotateKEK` (`internal/core/admin.go`) rewraps
every **non-destroyed** secret version's `encrypted_dek` from the old KEK to the new one via
`crypto.RewrapDEK` — **without ever decrypting the value ciphertext itself**
— and updates each row's `kek_id`; destroyed versions already have their
ciphertext and DEK nulled. The CA keys are rewrapped in the same transaction.
No readable secret or CA row depends on the retired KEK after commit. This runs
inside one storage transaction (metadata swap + rewrap), so rotation is
crash-safe. For client-bound
secrets, rotation only touches the outer (KEK) layer; the inner
client-token-derived layer is untouched and requires no client
participation (plan §11.4.4).

## Client-bound secrets (opt-in double wrapping)

A secret opts into client-bound mode at creation (`client_bound: true`,
`WithClientBound()` in the Go SDK). Creation must also request server-side
token generation (`generate_access_token: true` / `WithGenerateAccessToken()`):
the returned token is the only client key share, is shown once, and cannot be
recovered. Its DEK is wrapped in two layers instead of one:

```
DEK
   | HKDF-SHA256(client token, random 32-byte salt) -> client key
   | AES-256-GCM seal under client key
   v
inner-wrapped DEK
   | AES-256-GCM seal under the KEK
   v
stored encrypted_dek
```

(`crypto.EncryptClientBound`, `deriveClientKey` — HKDF info string `"kms/v1
client-bound key"`.) The random salt is stored per-version in
`secret_versions.client_key_salt`; the client token itself is **never**
stored — only `sha256(token)` in `secrets.access_token_hash` as a lookup/
verification hash (`crypto.TokenHash`).

To decrypt, the server needs both the master key (to unwrap the outer layer)
**and** the client-supplied token on the request (to derive the inner-layer
key). It discards the request token and derived key after use. Because token
rotation is per-version, `secrets.access_token_hash` represents only the latest
token and is not used to validate historical client-bound reads. A missing
token is rejected as `PermissionDenied`; a supplied wrong token fails the
version-specific authenticated unwrap as a generic `ErrDecryptFailed`, the same
failure class as corrupted ciphertext or a wrong KEK.

Layering rather than deriving one key from `master ⊕ token` is deliberate
(plan §10.7): KEK rotation rewraps only the outer layer as a pure
server-side operation; client token rotation is independent and only
requires the client to supply the old token when writing a new version.

**Token rotation is per-version, not global.** The per-version HKDF key
share is the token itself, bound to that version's own
`client_key_salt` — not the secret-level `access_token_hash`, which only
tracks the most recently minted token. Rotating (`PutSecret` with
`generate_access_token: true` on an existing client-bound secret —
requires the *current* token to authorize the write) encrypts only the
**new** version under the new token; every prior version stays encrypted
under whichever token was active when it was written
(`internal/core/secrets.go`, `PutSecret`/`GetSecret`). Keep old tokens if
you need to read historical versions after a rotation, and note that
promoting an older version back to "current" (`PromoteSecretVersion`,
which only moves a pointer — it never re-encrypts) means callers must
present *that version's* token, not the latest one, to read it.

**Threat model for client-bound secrets, specifically:**

| Attacker has | Can decrypt? |
|---|---|
| SQLite DB only | No |
| SQLite DB + master key | **No** — this is the whole point. The inner layer requires the client token, which lives only in the consuming application's configuration, never in the KMS database. |
| A leaked client token alone | No — still needs the ciphertext (from the DB) and the master key (to unwrap the outer layer) to get anywhere, and even then only decrypts secrets bound to that specific token. |
| Full live compromise of the running KMS host | Yes, for any request whose token arrives while the attacker has code execution — this defends against **offline** database+key theft, not a live host compromise (see below). |

**No recovery escrow, by design.** Losing the master key makes every secret
version irrecoverable. Losing a client token makes the versions encrypted under
that token irrecoverable; versions written under other retained tokens remain
readable. There is no backdoor, admin override, or support path around this.
`Service.RevealSecret` (the admin/frontend/CLI reveal path) explicitly
refuses to even attempt decryption of a client-bound secret
(`domain.ErrFailedPrecondition`, "client-bound secrets cannot be revealed:
the server cannot decrypt them without the client token") — the frontend and
CLI show metadata only, and the "New secret" UI requires an explicit
checkbox acknowledgment of this before it will submit the form
(`frontend/pages/secrets/new.tsx`).

## Token model

Two kinds of bearer tokens, both high-entropy random values minted by the
server and stored **only as a SHA-256 hash** (`crypto.GenerateToken`,
`crypto.TokenHash`) — the plaintext is shown to the caller exactly once at
creation/rotation time and is not retrievable again:

- **Identity tokens** (`identities.token_hash`, prefix `kms_`): the token
  method of authenticating a client or admin identity. Sent as
  `authorization: Bearer <token>` (gRPC metadata key `authorization`; HTTP
  `Authorization` header). This is what `Service.Authenticate` looks up to
  resolve a `domain.Identity`, and what establishes the caller's identity for
  authorization and for the watch subscription registry. `token_hash` is now
  **nullable**: a **cert-only** identity (one that authenticates by mTLS,
  §[Proof of identity](#proof-of-identity-the-built-in-ca-and-mtls)) has no
  token at all and stores `NULL`. As before, the stored material is a hash
  only — the store never holds a usable credential, whether a token
  (hash-only) or a certificate (public key only; the leaf private key is
  returned once at issuance and never persisted).
- **Per-secret access tokens** (`secrets.access_token_hash`, prefix
  `kmss_`): optional, attached to an individual secret. Each immutable
  version records whether a token was required when it was written, so first
  enabling a token on a later standard-secret version does not retroactively
  protect older versions. `GetSecret` for a standard-secret version marked
  token-protected — **including when called by admins** — requires the matching
  token via the `x-kms-secret-token` gRPC metadata key /
  `X-KMS-Secret-Token` HTTP header (`tokenHashMatches`, constant-time
  comparison via `hmac.Equal`). Explicit rotation replaces the shared
  credential for every standard-secret version already marked protected. For
  client-bound secrets, writing another version requires the current token,
  and reads use the token as the per-version key-derivation material described
  above. The admin `RevealSecret` path
  bypasses the standard per-secret token gate (a break-glass capability,
  fully audited) but — as noted above — still cannot decrypt a client-bound
  secret without that version's token.

Authentication failures are generic (`domain.ErrUnauthenticated`,
"invalid credentials") regardless of whether the token was malformed,
unknown, or belonged to a disabled identity, and every failure is audited
(`auth.failure`) with the source IP and user agent but never the attempted
token. Failed authentications are also rate-limited per source IP — see
[below](#login-and-failed-authentication-rate-limiting).

## Proof of identity: the built-in CA and mTLS

A bearer token scopes access but does not prove possession — anyone holding
the string is the identity. Machine clients therefore authenticate with
**mTLS client certificates minted by a certificate authority embedded in the
KMS** (`internal/ca`, plan-namespaces.md §7). The certificate is proof of
possession of a private key the KMS issued. In an mTLS-only namespace, a stolen
database or leaked bearer token does not let an attacker impersonate the
identity on the wire without its client private key.

**The CA.** On the first `serve` startup after unseal, the service generates a self-signed CA
(`ca.Generate`): an **Ed25519** key pair and a long-lived (**10-year**),
self-signed CA certificate marked `IsCA` with **`MaxPathLenZero`**, so it can
sign leaf client certificates but no intermediates. It is not automatically
renewed, and the CLI currently has no dedicated CA-rotation command. It signs
**client certificates only** — the
server's own TLS serving certificate remains operator-provided.

**The CA private key is never at rest in plaintext.** It is stored in the
`ca_keys` table enveloped exactly like a secret version: its own DEK wraps the
PKCS#8 key material (`encrypted_key`), and that DEK is wrapped under the active
KEK (`encrypted_dek`, `kek_id`). KEK rotation rewraps the CA key's DEK
alongside every secret DEK (plan-namespaces.md §7), so rotation needs no
certificate reissue — the identity↔namespace binding lives in the database,
not in the cert. The plaintext signing key remains in server memory for the
process lifetime so it can issue certificates.

**Issued client certificates** (`ca.IssueClientCert`) are short-lived
Ed25519 leaves carrying the identity name in the CommonName **and** in a URI
SAN of the form `kms://identity/<name>`, marked `ExtKeyUsageClientAuth`. The
default TTL is **90 days** (`ca.DefaultCertTTL`), settable per issuance, and
`NotBefore` is backdated **5 minutes** (`clockSkew`) to tolerate small clock
differences. A **fresh key pair is generated per issuance and returned exactly
once** (PEM bundle); the CA never retains or stores a leaf private key, exactly
like the token model.

**Mapping a peer certificate to an identity is URI-SAN-only.**
`ca.IdentityFromCert` requires exactly one `kms://identity/<name>` URI SAN and
returns its `<name>`; the CommonName is cosmetic and is **never** used as a
fallback, so a certificate lacking the SAN (or carrying more than one) is
rejected. The authentication interceptor maps a TLS-verified peer certificate
through this SAN to a stored identity, then checks the certificate serial
against `identity_certs`: the serial must be present, not revoked
(`revoked_at IS NULL`), not past `not_after`, and belong to an identity that
is not disabled. The result is an authenticated context carrying
`{identity, method: mtls}`.

**Revocation is a database check, not CRL/OCSP.** Because the KMS is the only
relying party for these certificates, revocation is per-serial:
`RevokeIdentityCertificate(name, serial)` stamps `identity_certs.revoked_at`,
and disabling or revoking an identity invalidates **all** of its certificates
at once. The interceptor consults `identity_certs` directly in the database on
each new RPC; there is no CRL or OCSP machinery to distribute
or poll. An identity may hold several concurrently-valid certificates, which is
what makes zero-downtime certificate rollover (issue new, deploy, revoke old)
possible.

The built-in CA certificate is public: it is served unauthenticated (`GET
/api/v1/ca`) and shown by the CLI for inspection or out-of-band verification of
KMS-issued **client** certificates. It is not the SDK's server-trust root: the
server certificate is operator-provided and clients must trust its issuer
separately.

## Per-namespace authentication methods

Each namespace records which authentication methods admit a caller at all, in
`namespaces.allowed_auth_methods` (a JSON array of `"mtls"` and/or `"token"`).
**New namespaces default to `["mtls"]`** — the strongest posture — and adding
`"token"` is an explicit, audited namespace-settings change
(`admin:namespace:update`).

The method gate runs in `internal/core` **before authorization**, on every
namespaced operation — parameter reads included: if the caller's
authentication method is not in the target namespace's
`allowed_auth_methods`, the operation is refused with `PermissionDenied`
("namespace requires mtls") and audited, before any policy or implicit-grant
evaluation. The implicit home-namespace grant (below) sits *behind* this gate,
so a namespace-bound identity presenting a disallowed method still gets
nothing.

The gate applies to **client-kind** identities. **Admin-kind** identities are
the management plane: a human administering namespaces from a browser cannot
practically present a client certificate, so admins bypass the method gate
(browser login stays token-based). Bypassing the *method gate* does not waive
auditing — every admin action is still audited exactly as before (see
[Audit guarantees](#audit-guarantees)).

## Login and failed-authentication rate limiting

The HTTP server throttles credential guessing with a per-IP token-bucket
limiter (`internal/server/httpserver/ratelimit.go`), shared by two call
sites: every request to `POST /api/v1/auth/login`, and every failed
authentication on any other endpoint (`serveAPI`,
`internal/server/httpserver/server.go`) — so an attacker can't dodge the
throttle by hitting arbitrary API paths with bad credentials instead of
the login endpoint. Each bucket allows a burst of 10 immediate attempts
and refills at 5 per minute; once exhausted, further attempts from that
key get `429 rate_limited` instead of being evaluated.

The bucket key is the caller's IP as resolved by `clientIP` — the real TCP
peer address by default, or the first address in `X-Forwarded-For` if
`security.trust_proxy_headers` is enabled (see
[`operations.md`](operations.md#tls-and-mtls) for when that's safe to
turn on). It is resolved once per request and reused as the source IP on
every audit event the request produces (`auth.failure`, `authz.denial`,
`secret.read`, and so on — see [Audit guarantees](#audit-guarantees)
below), so the rate-limit key and the audited source IP are always
consistent with each other.

**The bucket map itself is bounded**, so a caller presenting an unbounded
set of distinct keys (e.g. a spoofed or rotating source IP once
`trust_proxy_headers` is on) cannot exhaust server memory: the map caps
at 65,536 tracked keys, and when full, idle buckets that have refilled
back to burst capacity are swept before a new key is admitted. If the map
is still full after sweeping — every tracked key is actively
throttled — the new event is refused outright rather than growing the map
without limit.

## Authorization: namespace-native RBAC with deny precedence

`internal/policy` implements policy evaluation; `internal/core` is the only
caller. A `Policy` binds a `subject` (an identity name, or `"*"` for every
non-admin client) to `allow` and `deny` rule lists. **Authorization is
namespace-level:** a grant is an operation on a whole namespace, and there is
no per-key scoping. Each rule is an `(operation, env, app)` tuple:

```json
{ "operation": "secret:read", "env": "prod", "app": "gradethis" }
```

- **Operation** patterns: an exact operation (`secret:read`), a category
  wildcard (`secret:*`, `parameter:*`, `configuration-release:*`, `admin:*`,
  matching even multi-segment admin ops), or the global wildcard `*`. The
  known operations are
  `parameter:{read,write,list,delete}`,
  `secret:{read,write,list,disable,destroy,promote}`,
  `configuration-release:{create,read,validate,activate,list,watch}`, and
  `admin:{namespace:create,namespace:update,namespace:delete,identity:cert,policy:write,audit:read,key:rotate}`
  (`domain.Op*` constants).
- **`env` / `app`**: an exact label or `"*"` (`policy.labelMatches`). There is
  no `key` field — a grant that matches an operation on `(env, app)` covers
  **every** key in that namespace.

**Evaluation precedence** for a namespaced operation is
**deny > implicit home-namespace grant > explicit allow > default deny**
(`policy.Authorize`), evaluated against the target `NamespaceRef`:

1. If any `deny` rule across every policy bound to the subject matches
   `(operation, ns)` → **deny**, full stop.
2. Else if the **implicit home-namespace grant** applies → **allow**.
3. Else if any `allow` rule matches → **allow**.
4. Else → **deny**.

**The implicit home-namespace grant** (`policy.HasImplicitHomeGrant`, plan
§3/§6) is what makes "endpoint + credential and you're done" real: an identity
bound to a namespace may perform read/list operations **in its own namespace
with no policy rule at all**. The implicit set is exactly
`parameter:read`, `parameter:list`, `secret:read`, `secret:list`,
`configuration-release:read`, and `configuration-release:watch`; an ordinary
subscribe rides on the same grant (it is authorized once, as a namespace-level
`parameter:read`, when the stream registers). It applies **only** to the
caller's own namespace — access to any *other* namespace, and every write,
delete, disable, destroy, and promote, always requires an explicit `allow`
rule (itself namespace-level). Deny rules still override the implicit grant
(step 1 precedes step 2), so a policy author can carve out a whole namespace —
but not a single key. `policy.Authorize` with a nil home namespace is the
rule-only form used where the implicit grant must not apply.

Admin identities (`Identity.Kind == "admin"`) bypass namespace method gates and
data-plane policy — they are the management plane — except the one place that
is cryptographically impossible regardless of privilege (revealing a
client-bound secret without its token). Their secret and administrative actions
still follow the normal audit guarantees.

**Delegating `admin:identity:cert` is delegating impersonation.** Whoever can
issue a client certificate for identity *B* can authenticate *as* *B*. The
server enforces the sharp boundary — `guardCertTarget` refuses any non-admin
caller a certificate for an **admin-kind** target or for a target **outside the
caller's own namespace** (`IssueIdentityCertificate` and
`RevokeIdentityCertificate` both apply it; admins are unrestricted). What it
cannot prevent is the inherent meaning of the grant *within* a namespace: a
non-admin holding `admin:identity:cert` can mint a certificate for any
**non-admin identity in its own namespace**, and thereby assume that identity's
policy grants. Read the operation as "may assume any non-admin identity in the
caller's home namespace," and do **not** delegate it in a namespace whose
client identities are deliberately given *different* privileges from one
another. The cross-namespace and admin-impersonation vectors are closed; the
in-namespace lateral capability is the delegation itself.

**List authorization** is namespace-level, but operation checks still matter.
The caller first needs the relevant `*:list` grant (or the implicit home
grant). Parameter list results include values, so each item also requires
`parameter:read`; secret list results are metadata-only and accept
`secret:list` or `secret:read`. Because rules have no key field, that per-item
predicate is constant across a namespace: it can include every matching key or
none, never carve out individual keys. The `key_prefix` accepted by `List*` is
a convenience browsing filter — a plain `LIKE 'prefix%'` on the opaque key
string — never a security boundary.

Every authorization **denial** is audited (`authz.denial`) with the attempted
operation and the target `env`/`app`(`/key` for display). Policy rules are
normalized and validated at write time (`policy.ValidateRules`): unknown
operations are rejected, an empty `env`/`app` normalizes to `"*"`, and
non-`"*"` labels are validated by `internal/keyutil`.

## Configuration release security boundary

A configuration release is an immutable set of exact references and
non-sensitive metadata, not a capability. Permission to create, read,
validate, activate, list, or watch a release does **not** grant access to any
referenced parameter or secret. Creation and validation independently
authorize resource reads, including every cross-namespace reference; each SDK
loader fetches pinned values through the existing parameter and secret RPCs and
their existing authorization and cryptographic checks.

Secret entries capture the exact version's resource identity, content type,
non-sensitive metadata, and the booleans `client_bound` and
`has_access_token`. Secret plaintext, token hashes, plaintext access tokens,
and client-bound key shares never enter release rows, digests, watch events,
validation errors, diffs, acknowledgement diagnostics, logs, or metrics. A
loader's token provider is local application code and is invoked only for a
protected secret; the credential is sent only on that secret's read RPC.

The deterministic release digest covers an alias-sorted protobuf projection of
schema and resource pins, captured metadata, and parameter digests. It excludes
values, tokens, timestamps, creator identity, activation revision, and movable
labels. Parameters are not secret, but resolved parameter values are still
excluded from default snapshot formatting to avoid dumping a complete runtime
configuration. Secret values retain the SDK's redacting `Secret` type and
require explicit value access.

Current and previous releases are protected against parameter deletion,
secret deletion, and secret-version destruction in the same storage
transaction as the destructive operation. A conflict returns
`FAILED_PRECONDITION`, identifies the release/version/alias, and is audited.
This is referential integrity, not irrevocability: disabling a secret version
remains an emergency revocation mechanism, and a later validation/loading
attempt rejects an unreadable pin rather than revealing it.

Lifecycle rows are per process instance and state. `received` means the loader
finished resolution and snapshot construction; it is distinct from ordinary
namespace-stream transport acknowledgement. `prepared`, `applied`, and
`rejected` describe application lifecycle. Rejection diagnostics supplied by
applications are stored only as `[redacted]`; operators diagnose from the
bounded category. Connection state is stored separately, so registration does
not fabricate a lifecycle acknowledgement.

## Namespace and key validation

Namespaces and keys are validated by `internal/keyutil` (the successor to the
old `internal/pathutil`) before any storage or authorization operation touches
them:

- **`env` / `app`** (`ValidateEnv` / `ValidateApp`): 1–64 characters of
  `[a-z0-9-]`, not starting or ending with `-`.
- **`key`** (`ValidateKey`): 1–256 characters total; `/`-separated segments of
  `[a-z0-9._-]`; no leading, trailing, empty, `.`, or `..` segment. The slash
  is validated only as a legal character — a key is an **opaque** string the
  server never splits, prefix-matches, or otherwise interprets. `db/port` and
  `metrics/port` are two unrelated keys; there is no key hierarchy, no
  `MatchKey`, and no key-pattern authorization anywhere.

Because env, app, and key are explicit, validated fields on the wire — never a
single path string the server parses — the traversal and injection surface
that a path parser would expose does not exist: a caller cannot smuggle `..` or
an extra namespace segment through a key.

## Audit guarantees

Every secret read, secret reveal, secret/parameter write, version
promotion, disable, destroy, policy change, namespace change,
authentication failure, authorization denial, KEK rotation, schema
registration, release create/validate/activate/rollback, CAS conflict,
release lifecycle acknowledgement, and blocked release-reference destruction
is audited
(`internal/core/*.go`, `Service.audit`/`auditOp`/`auditStrict`) into
`audit_events`. Audit records carry actor identity/kind, the resource's
`env`/`app`/`key`/version (denormalized as text with no foreign key, so the
history stays readable after a namespace is deleted — plan-namespaces.md §3),
decision (`allow`/`deny`/`error`), source IP, user agent, request ID, and an
opaque `metadata_json` blob for operation-specific context — **never** secret
plaintext, never a token.
The recorded source IP is resolved the same way as the rate-limit key
above (real TCP peer address, or `X-Forwarded-For` only if
`security.trust_proxy_headers` is enabled) — see
[above](#login-and-failed-authentication-rate-limiting).

**Secret reads fail closed on audit failure.** For `secret.read` and
`secret.reveal` specifically, the audit event is written with
`Service.auditStrict` *before* the plaintext is returned to the caller; if
the audit write fails, the already-decrypted plaintext is explicitly zeroed
(`crypto.Zero`) and the call returns `domain.ErrFailedPrecondition`
("audit unavailable") instead of the secret. Every other audit call site
(writes, denials, admin actions) uses the non-strict `Service.audit`, which
logs a failure loudly but does not block the underlying operation — the
plan's requirement that "all secret reads are audited" (§28.9) is enforced
by refusing to serve the read rather than by hoping the write succeeds.
Audit writes also run with `context.WithoutCancel` plus a 5s timeout, so a
client disconnecting mid-request cannot suppress the record of what it did.

## Redaction

Redaction is enforced by type, not by call-site discipline:

- The Go SDK's `Secret` and `SecretValue` types (`sdk/go/paramstore`) always
  print `"[REDACTED]"` from `String`, `GoString`, `Format` (every `fmt`
  verb), and `MarshalJSON` — plaintext is reachable only via `Value()`/
  `StringValue()`. This makes accidental logging of a secret a type error
  you'd have to work around, not a mistake you can make by passing the
  wrong variable to `%v`.
- Go `ReleaseSnapshot` formatting and JSON marshaling contain only release
  identity and entry metadata; Python `ReleaseSnapshot` formatting similarly
  reports counts rather than resolved maps. Their nested secret values remain
  redacting `Secret` objects. Release/status metric types deliberately omit
  aliases, paths, diagnostics, values, and token material.
- Server-side, the JSON HTTP API's `SecretMetadata` shape
  (`docs/http-api.md`) never includes a value field at all — the only
  endpoint that can return plaintext is the admin-only `POST
  /api/v1/secrets/reveal`, which is explicitly audited per call.
- The frontend never renders a secret value outside the explicit reveal
  flow, and client-bound secrets have no reveal affordance in the UI at
  all — the "Reveal secret" control is absent and the panel explains why,
  because the server itself cannot produce the plaintext.
- HTTP request logging (`internal/server/httpserver/server.go`) deliberately
  omits the query string from log lines, since resource identifiers (the
  `env`/`app`/`key` query parameters) travel there and must never be allowed to
  grow into carrying anything sensitive.

## What this does not protect against

Being explicit about the boundary, per plan §3 (non-goals) and §10.7.4:

- **A live, fully compromised KMS host.** If an attacker has code execution
  on the server process, they can read the unsealed KEK from memory,
  observe client-bound tokens as they arrive on live requests, and decrypt
  anything the server itself is currently able to decrypt. Client-bound
  mode raises the bar (a leaked token alone, or a stolen DB+key alone, is
  each insufficient) but does not create a boundary against the process
  that holds the key material and sees the traffic.
- **A malicious or compromised administrator.** Admin identities are the
  management plane: they bypass the per-namespace method gate and data-plane
  policy, and can reveal any
  non-client-bound secret; every action is audited, which gives detection,
  not prevention.
- **Loss of the master key**, which makes every secret version unrecoverable,
  or loss of a client-bound version's client token, which makes versions
  encrypted under that token unrecoverable. Both are by design (no escrow); see
  [`operations.md`](operations.md#disaster-recovery) for what this means
  operationally.
- **This is not a replacement for a cloud KMS, HSM, or enterprise
  key-management system** in high-compliance environments (plan §3.1); the
  local KEK provider is the only one implemented in v1.
- **Multi-tenant isolation beyond namespace authorization.** All tenants share
  one SQLite database and one master key; isolation is enforced by the
  namespace method gate and authorization policy, not by separate encryption
  domains per tenant.
- **Availability.** SQLite is embedded, single-writer, single-node storage;
  this is a security document, not an HA design, and the service makes no
  claims about surviving host failure without external backup (see
  [`operations.md`](operations.md)).
