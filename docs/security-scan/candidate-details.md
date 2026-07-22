# Candidate Details

Per-candidate detail for the 14 distinct issues summarised in the triage table in
[`../security-review.md`](../security-review.md). Read that file first — it
carries the status banner, the evidence vocabulary, and the scope limits that
apply to everything here.

Nothing in this file is a confirmed vulnerability. Every item is an **unvalidated
candidate** from a scan that did not run to completion. Severities are reproduced
verbatim from each candidate's own attack-path receipt; where the scan recorded
none, the entry reads *not assigned*, which means **unrated, not low**. Each
static candidate's `proof_gap` and counterevidence are preserved as the scan
wrote them.

Section numbers match the triage table's `#` column (most-actionable first), not
candidate-ID order.

---

## 1. Go SDK selects insecure transport by default

`CAND-W08-001` · **high** · Traced · confidence 0.98

**Locations:** entrypoint `sdk/go/kmsclient/client.go:143` (`NewClient`); root
control `:167`; sink `:170`; credential source `:267` (`withAuth`); plaintext RPC
`:385`.

**Claim.** `NewClient` treats the **zero-value** `Config.TLS` as permission to
select `insecure.NewCredentials()` — no explicit insecure/development opt-in, no
loopback-only guard, no endpoint restriction. `withAuth` (`:264-275`) then
attaches the identity bearer token and the per-secret `x-kms-secret-token`
metadata to that same cleartext channel, and `GetSecret` (`:383-402`) /
`PutSecret` (`:456-466`) carry secret plaintext over it.

**Exposure.** Every RPC made by an affected client — `WhoAmI`, watches,
parameter reads/writes, secret reads/writes. An on-path attacker needs **no KMS
credential**, and stolen bearer tokens can be replayed independently afterward.

**Rubric.** TLS omission is the zero-value/default constructor path ✓ · the
default branch installs plaintext credentials ✓ · identity and per-secret
credentials travel on the same channel ✓ · secret plaintext is sent and received
on that channel ✓ · **an explicit insecure opt-in or loopback-only guard prevents
accidental network use ✗ (not met)**.

**Counterevidence (preserved).** `tls.go:46-69` is a correct, safe explicit TLS
builder enforcing a TLS 1.2 minimum — it is simply **not the default**. The
README and docs label nil TLS as development-only, but the scan's position is
that this is *guidance rather than a runtime opt-in*, and therefore does not
prevent accidental production use. `go test ./sdk/go/kmsclient` passed — that is
the repository's own baseline suite, not a reproduction of this behaviour.

**Proof gap.** A concrete deployment must actually omit `Config.TLS` **and** place
a network attacker on-path. Blindspot: final impact depends on endpoint
reachability and on any trusted external tunnel or service mesh not represented
in the client configuration — a mesh-encrypted deployment is unaffected.

## 2. Python SDK selects insecure transport by default

`CAND-W08-002` · **high** · Traced · confidence 0.98

The exact parity issue in `sdk/python/kms_paramstore/client.py`: `__init__`
(`:79-93`) defaults `tls=None` and selects `grpc.insecure_channel` at `:125-130`;
`:179-185` attaches bearer and secret tokens; `:431-448` and `:469-480` carry
secret plaintext. `config.py:24-40` and `tls.py:17-45` offer safe credentials
**only when explicitly selected**.

**Rubric.** Identical to the Go client, including the same unmet criterion: no
explicit insecure opt-in or loopback-only guard.

**Counterevidence (preserved).** Same as the Go SDK — docs label no-TLS
development-only. **The Python test runtime was unavailable** (`grpc` and
`pytest` are not installed), so unlike the Go candidate this one has **no
executed test at all**; the scan rests on the branch being direct and
deterministic.

## 3. `ListNamespaces` skips the namespace authentication-method gate

`CAND-W02-001` + `CAND-W03-001` + `CAND-W05-001` (same defect, never deduped) ·
**medium** · Demonstrated · confidence 0.92

**Locations:** entrypoints `internal/server/httpserver/handlers.go:151` and
`internal/server/grpcserver/admin.go:54`; root control
`internal/core/admin.go:128`; sinks `internal/core/admin.go:153`,
`internal/storage/namespaces.go:109`, `internal/server/grpcserver/convert.go:170`.

**Claim.** `Service.ListNamespaces` fetches every namespace and filters
visibility with `policy.Authorize` alone. Unlike ordinary namespaced operations,
it never calls `namespaceMethodGate` (`internal/core/service.go:347`) per
returned namespace. A **token**-authenticated client whose home namespace is
`mtls`-only therefore still receives that namespace's metadata via the implicit
home grant at `internal/core/admin.go:152`.

**Demonstrated.** `TestW02TokenCanListMTLSOnlyHomeNamespace` authenticates a token
client bound to a default mTLS-only namespace and receives **HTTP 200** with that
namespace in the response. Run as
`go test -overlay=... ./internal/server/httpserver -run '^TestW02'`.

**Disclosed fields.** Namespace name, description, creator, allowed auth methods,
and parameter/secret **counts**. No secret values.

**Rubric (all met).** Authenticated non-admin route reaches `ListNamespaces` ✓ ·
disallowed auth method remains represented in `Principal.Method` ✓ · a policy or
implicit home grant makes the namespace visible ✓ · no per-namespace method gate
runs before return ✓ · returned fields contain protected namespace metadata ✓.

**Counterevidence (preserved).** The caller still needs namespace read/list
authorization, so this is *not* cross-policy access — it does not reach
namespaces the caller was never granted. That limits impact but does not restore
the explicit method boundary. Blindspot: the sensitivity of operator-authored
descriptions varies by deployment. Admin-kind bypass is intentional and out of
scope; the reproduced caller is client-kind.

**Proof gap.** No dedicated exploit test was added to the immutable target; the
overlay test ran outside it, and the branch is deterministic from the source.

> This directly contradicts [`security.md:337-344`](../security.md#per-namespace-authentication-methods),
> which states the gate runs "on every namespaced operation."

## 4. Partial audit filter skips the method gate

`CAND-W02-002` + `CAND-W03-002` + `CAND-W05-002` (same defect) · **medium** ·
Demonstrated · confidence 0.95

**Locations:** entrypoints `internal/server/httpserver/handlers.go:623` and
`internal/server/grpcserver/admin.go:251`; root controls
`internal/core/admin.go:37` and `:533`; sinks `internal/storage/audit.go:35-50`,
`internal/server/httpserver/handlers.go:653`.

**Claim.** `requireAdminOrOp` calls `namespaceMethodGate` **only when both `env`
and `app` are non-empty** (`internal/core/admin.go:37`). A caller who simply
omits `app` is authorized against a ref of `env=stage, app=""` — which a
wildcard `admin:audit:read` grant matches, because `policy.go:109` makes `"*"`
match the empty component. `storage/audit.go:45-50` then adds namespace
predicates only for the non-empty fields, so rows are returned from every
matching namespace, including mTLS-only ones, with no per-row gate.

**Demonstrated.** `TestW02PartialAuditScopeSkipsMTLSMethodGate` authenticates the
stock token client, grants `admin:audit:read` for `env=stage/app=*`, queries
`/api/v1/audit?env=stage`, and receives **HTTP 200** with audit rows from a
default mTLS-only namespace. `admin_test.go:333-348` independently confirms a
wildcard delegated grant with an empty filter is accepted.

**Disclosed fields.** Actor identity and kind, resource `env`/`app`/`key`/version,
decision, source IP, user agent, request ID, and metadata. No secret plaintext.

**Rubric (all met).** Remote client controls empty/partial filter ✓ · delegated
policy can authorize it ✓ · method gate skipped for partial refs ✓ · storage
expands omitted fields across namespaces ✓ · records contain meaningful
cross-boundary metadata ✓.

**Counterevidence (preserved).** A wildcard policy *intentionally* grants broad
audit scope — this is a delegated management credential, more privileged than an
ordinary reader, and policy deny precedence remains active. A **fully specified**
namespace would be method-gated correctly. The scan's judgment is that
`requireAdminOrOp` documents that delegated management must *also* obey each
namespace's auth-method boundary, so the broad policy grant does not make the
missing gate acceptable. Blindspot: no row-by-row method filter exists, and
historical audit rows may reference deleted namespaces whose current method set
is no longer available to check against.

## 5. Private-key output follows symlinks and does not use `O_EXCL`

`CAND-W04-003` · severity *not assigned* · Demonstrated

`internal/cli/admincmds.go:299` (`identity create`) and `:338` (`issue-cert`)
pass a one-time private key to `writeCertBundle`; the `os.WriteFile(keyPath,
bundle.KeyPem, 0o600)` at `:525` neither uses `O_EXCL` nor rejects symlinks or
pre-existing files.

**Demonstrated.** Under `TestW04SecurityProbe`, a pre-created `svc.key` at `0644`
**remained `0644`** after `writeCertBundle` wrote the private key into it —
`0600` is only a *create* mode and does not alter an existing file. Separately, a
`svc.key` **symlink** redirected the private-key bytes into its attacker-readable
target.

**Impact.** Disclosure of a newly minted client or admin private key, and
possible overwrite of any other file writable by the CLI user.

**Severity facts (preserved).** Requires local access to a shared output
directory **and** predicting the identity name (the filename is `NAME.key`). The
leaked credential can confer namespace or admin authority — note the interaction
with `admin:identity:cert` delegation discussed in
[`security.md`](../security.md#authorization-namespace-native-rbac-with-deny-precedence).

## 6. Non-transactional read pairs a stale token hash with a new version

`CAND-W03-005` · **medium** · Traced · confidence 0.82 (the lowest recorded)

**Locations:** entrypoint `internal/core/secrets.go:44`; root controls
`internal/storage/secrets.go:214`, `internal/core/secrets.go:85`; sink
`internal/core/secrets.go:94`.

**Claim.** `GetSecretVersion` performs separate autocommit reads with **no
enclosing transaction** (`storage/secrets.go:215-243`): it loads the
secret-level access-token hash, then resolves the label/version. Under WAL, a
concurrent standard-secret token rotation can commit between the two, so the
method returns the **stale old hash alongside the newly created token-protected
version**. `GetSecret` at `core/secrets.go:85` compares the supplied old token to
that stale hash, passes, and decrypts the new plaintext — because standard
(non-client-bound) wrapping has no client-token cryptographic check
(`crypto/envelope.go:149-152`), the server KEK alone suffices once the gate is
passed.

**Impact.** Direct secret **plaintext** disclosure for one standard secret
version — the only candidate in this scan claiming plaintext disclosure. It
defeats the guarantee in [`configuration-releases.md:165-167`](../configuration-releases.md)
that explicit rotation replaces the credential for every standard token-protected
version.

**Rubric (all met).** Hash and version read in separate statements ✓ · writer can
commit between them under WAL/autocommit ✓ · new version retains
`HasAccessToken=true` ✓ · core checks the stale secret hash rather than a
version-bound credential ✓ · standard wrapping lets any gate-approved caller
decrypt with the server KEK ✓.

**Counterevidence (preserved).** A request that completes **entirely before or
entirely after** the rotation is handled correctly, and **the window is narrow**.
Namespace/policy authorization, `HasAccessToken`, and GCM all remain active — the
broken control is solely the inconsistent hash/version snapshot. That reduces
likelihood but does not prevent a request authorized under `H(T0)` from fetching
a post-rotation version protected by `H(T1)`. The attacker must also hold
`secret:read` and, for the exact-version variant, guess the monotonic next
version number. Confidence is recorded as *medium-high*, not high.

**Proof gap.** Runtime scheduling was not forced; no measured exploit success
rate under production load.

## 7. TOCTOU on concurrent client-bound secret creation

`CAND-W03-003` · **medium** · Traced · confidence 0.90

**Locations:** entrypoints `internal/server/httpserver/handlers.go:308`,
`internal/server/grpcserver/secrets.go:34`; root controls
`internal/core/secrets.go:197`, `internal/storage/secrets.go:69`; sinks
`internal/storage/secrets.go:158`, `:166`.

**Claim.** Two concurrent client-bound creates can both observe `ErrNotFound` at
`core/secrets.go:197`, **before** either storage transaction opens. Once one
request creates the secret, storage treats the stale second request as an
*update*: it checks only the wrap mode, accepts that request's independently
minted token hash **without proof of the now-current token**, appends a version,
and moves `current` to it. `BEGIN IMMEDIATE` serializes the two write
transactions but cannot serialize the earlier core preflight.

**Impact.** The racer cannot decrypt the winner's first version, but seizes
current-version control and installs its own token as the secret's current token.

**Rubric (all met).** Two remote writes pass authorization concurrently ✓ · both
observe nonexistence before either transaction ✓ · second transaction
reclassifies stale create as existing update ✓ · storage lacks an
expected-nonexistence / current-token predicate ✓ · second request becomes
current and replaces the token hash ✓.

**Counterevidence (preserved).** The attacker **already holds `secret:write`** on
the namespace, which limits the privilege delta considerably. The scan's argument
is narrower than "unauthorized write": ordinary updates to an existing
client-bound secret explicitly require its independent token, and this race
bypasses that intended *second factor*. Race reliability depends on deployment
latency and attacker timing, and the attacker must predict the new key name and
race its very first creation.

**Proof gap.** No deterministic scheduling harness was built; the two-transaction
ordering is argued from the source. Baseline tests pass and contain no concurrent
client-bound creation case.

## 8. TOCTOU defeats client-bound token rotation

`CAND-W03-004` · **medium** · Traced · confidence 0.93

**Locations:** root controls `internal/core/secrets.go:218`,
`internal/storage/secrets.go:97`; sinks `internal/core/secrets.go:268`,
`internal/storage/secrets.go:166`.

**Claim.** A non-rotating update can validate old token `T0` **outside** the SQL
transaction, before a concurrent legitimate rotation `T0 -> T1` commits. The
stale request then runs after the rotation: storage preserves hash `H(T1)` (it
never compares it against the presented token), while the captured `Encrypt`
closure encrypts the **new current version under `T0`**. The result is a durable
disagreement between the secret row's token hash and the ciphertext's actual key
— `T0` can read the new current version and `T1` cannot decrypt it.

`crypto/envelope.go:108-128` and `:149-166` confirm the version's inner DEK is
bound to the captured token, not to the secret row's hash.

**Rubric (all met).** Old token checked outside the transaction ✓ · legitimate
rotation can commit a new hash before the stale write starts ✓ · storage does not
compare expected vs. current hash ✓ · stale `Encrypt` closure uses the old token ✓ ·
resulting current version is decryptable only with the revoked-away token ✓.

**Counterevidence (preserved).** Old tokens **intentionally** continue to decrypt
their own historical versions — that is documented, per-version behavior and not
a bug. The scan grounds the finding on a narrower property: `gaps_test.go:48-52`
establishes that the old token must **not** decrypt the *new current* version
after rotation, and this race violates exactly that. Mode checks, SQL write
serialization, and crypto binding each work individually; they are simply not
joined into one atomic expected-token predicate. Timing reliability varies,
though an attacker can retry during a known rotation workflow.

**Proof gap.** No runtime scheduling harness was built; the interleaving is
statically derived.

## 9. Database file created at mode `0644`

`CAND-W04-001` · severity *not assigned* · Demonstrated

`internal/cli/admin.go:37` (`cmdInit`) → `internal/storage/store.go:148-152`.
`OpenWithOptions` supplies no restrictive create mode and performs no post-create
`chmod`, so SQLite creates the database under the process umask.

**Demonstrated.** `TestW04SecurityProbe/new_database_and_backup_permissions`
forced `umask 022` and observed a newly created KMS database at mode **`0644`**.

**Impact.** Other local users can read the KMS database — plaintext parameters,
identities, policy and audit metadata, and encrypted secret records.

**Severity facts (preserved).** Local, multi-user deployment precondition; **no
remote path**. The database directory must be traversable by another local
account. Confidentiality impact covers security-sensitive database contents, but
the KEK is not in the database, so secret *plaintext* is not directly exposed.

## 10. `VACUUM INTO` backup created at mode `0644`

`CAND-W04-002` · severity *not assigned* · Demonstrated

`internal/cli/admin.go:166` (`cmdBackup`) → `internal/storage/store.go:255-267`.
`cmdBackup` delegates output creation to `VACUUM INTO` without setting a
restrictive output mode.

**Demonstrated.** After chmodding the source database to `0600` and forcing
`umask 022`, `TestW04SecurityProbe` observed the backup at mode **`0644`** — the
backup silently widens permissions relative to a correctly locked-down source.

**Severity facts (preserved).** Local, multi-user precondition. Existence checks
prevent overwrite but nothing controls permissions. **The backup excludes the
KEK, limiting plaintext-secret impact**; what leaks is ciphertext plus namespace,
identity, policy, audit, and schema metadata.

## 11. `restore` replaces a concurrently created destination without `--force`

`CAND-W04-004` · severity *not assigned* · Demonstrated

`internal/cli/admin.go:187` → `internal/cli/restore.go:37` (check) and `:99`
(rename). `restore` checks destination existence before copying, but the later
`os.Rename` unconditionally replaces a destination created *after* that check.

**Demonstrated.** `TestW04SecurityProbe` confirmed `copyFileAtomic` replaces an
existing destination. The exploitable interleaving is exactly:
`fileExists(dst) == false` → concurrent creation of `dst` → `os.Rename(tmp, dst)`.

**Severity facts (preserved).** Race window plus an exact-path precondition.
Impact is bounded integrity/availability loss — destruction of a concurrently
created file despite no `--force`. Importantly, **`rename` replaces a symlink
rather than following it, so this is *not* an arbitrary-target overwrite.** The
temp file itself is created securely.

## 12. Release connection key omits authenticated identity

`CAND-W05-003` · severity *not assigned* · Traced · confidence high

`internal/server/grpcserver/releases.go:184-215` and `:32-37` →
`internal/storage/releases.go:599-619`.

**Claim.** Release registration accepts attacker-chosen `client_name` and
`instance_id`. `releaseConnectionKey` is `(namespace, name, clientName,
instanceID)` and the storage `OnConflict` target uses those same four columns —
neither includes the authenticated identity. Registration authorization proves
*watch access* but never proves *ownership of the subscriber identifiers*, so
two distinct principals collide on one liveness record and the later one
overwrites the former's identity and connection state.

**Impact.** Cross-principal corruption of the persisted subscriber
identity/liveness data shown in the admin subscriber view.

**Facts (preserved).** Precondition: the attacker must already hold
`configuration-release:watch` **for the same release**. Persistence: the
connection row remains until overwritten, disconnected, or reset.

## 13. Release acknowledgement overwrite across principals

`CAND-W05-004` · severity *not assigned* · Traced · confidence high

`internal/server/grpcserver/releases.go:258-278` →
`internal/core/releases.go:357-408` → `internal/storage/releases.go:477-487`.

**Claim.** Acknowledgements are matched to the stream's attacker-chosen
namespace/name/client/instance tuple but not to an authenticated owner; the
equality check at `releases.go:270` merely repeats the registration identifiers.
Core does overwrite `ack.Identity` with the current principal, but
`UpsertReleaseAcknowledgement` conflicts on `(namespace, release, client,
instance, state)` and updates identity and state data — and an
**attacker-supplied `client_timestamp` wins same-revision conflicts and can be
set arbitrarily far in the future**.

**Impact.** Persistent cross-principal overwrite or falsification of `received`,
`prepared`, `applied`, or `rejected` lifecycle evidence — potentially misleading
rollout decisions that operators make from that data.

**Facts (preserved).** Precondition: watch access to the same release plus
knowledge or a correct guess of the subscriber identifiers. Constraint: the
version/revision must identify a **real activation**, so arbitrary fabrication is
not possible.

## 14. mTLS certificate enrollment is not bound to the stored fingerprint or issuer

`CAND-W01-001` · severity *not assigned* · Traced · disposition
`validated_candidate`

**Locations**

| Role | Location |
|---|---|
| Implementation | `internal/ca/ca.go:210` — `IssueClientCert` computes SHA-256 of the exact leaf DER and returns it for persistence |
| Wrapper | `internal/cli/serve.go:281` — the gRPC client trust pool clones operator-supplied client CA roots and adds the built-in CA |
| Entrypoint | `internal/server/grpcserver/interceptors.go:117` — any TLS-verified leaf is forwarded to `VerifyClientCert` |
| Root control | `internal/core/service.go:301` — lookup is by presented serial plus SAN name, revocation, and expiry, omitting fingerprint and issuer |
| Sink | `internal/core/service.go:315` — target identity returned as authenticated |

**Claim.** Certificate serial numbers are *issuer-scoped*, but the KMS registry
lookup is global and the exact stored fingerprint is never compared. If an
operator has configured an **additional client CA**, a leaf signed by that other
trusted root carrying the target's `kms://identity/<name>` URI SAN and the exact
serial of a registered built-in-CA certificate satisfies the registry tuple and
authenticates as the target identity. If the target is admin-kind, the resulting
principal holds management-plane authority.

The scan checked whether a later control repairs this and found none:
`internal/core/service.go:500-506` repeats the same serial/name/revocation/expiry
checks for long-lived stream reauthorization.

**Counterevidence (from the ledger, preserved).** Accidental collision with the
built-in CA's random 128-bit serial is negligible. Exploitation requires **all
of**: an additional configured CA, and the ability to obtain a leaf under it with
both the target SAN and the exact target serial. **Absent an additional CA,
built-in-CA signature unforgeability closes this path entirely.** The scan's own
framing: the deployment precondition limits confidence and reachability but does
not repair the issuer-agnostic check when the documented additional-CA mode is
enabled.

**Severity-relevant facts.** Remote entrypoint: yes. Requires built-in CA key:
no. Requires target private key: no. Requires configured additional CA: yes.
Requires exact target serial and SAN: yes. Admin target possible: yes.

> Note the interaction with [`security.md`](../security.md#proof-of-identity-the-built-in-ca-and-mtls),
> which describes serial-based revocation as deliberate ("Revocation is a
> database check, not CRL/OCSP") and is silent on issuer scoping. The single-CA
> deployment the document describes is not affected by this candidate.

---

## Severity calibration used by the scan

Reproduced in full in [`threat-model.md`](threat-model.md):

- **Critical** — unauthenticated or low-privilege recovery of broad secret plaintext, RCE in the server, compromise of KEK or CA private keys via a normal network path, or systemic bypass of tenant/admin boundaries.
- **High** — exposes a meaningful set of secrets or privileged operations across a strong boundary, permits persistent admin takeover, defeats client-bound protection under its stated database-plus-KEK attacker model, or allows remote integrity compromise of active configuration. *Cited examples include "certificate mapping that authenticates the wrong identity."*
- **Medium** — requires a valid but limited credential, narrow deployment conditions, or produces bounded disclosure/integrity/availability impact.
- **Low** — minor or highly constrained impact, requires trusted operator/developer control, or is a defense-in-depth gap without a demonstrated boundary crossing.

The calibration also states that findings which merely restate documented
plaintext development defaults, require a malicious full administrator, or assume
arbitrary server-host code execution "should normally be informational or
suppressed unless they break an independent stated guarantee." Candidates 1 and 2
(the SDK insecure defaults) sit directly on that line — the scan resolved it
toward `high` on the reasoning that a *silently selected* insecure default is not
the same as a documented one. That judgment call is worth an owner's independent
review.

## What the scan reviewed and did not flag

The discovery work ledger records `suppressed` (reviewed, nothing reportable) for
a substantial set of security-critical files, which is useful negative evidence:
`internal/policy/policy.go` (deny precedence, home binding, operation matching
"are correct"), `internal/core/helpers.go`, `internal/storage/store.go`
(parameterization, `LIKE` escaping, `VACUUM` quoting), `internal/storage/keymeta.go`
(transactional rewrap of all live secrets and CA keys), `internal/storage/identities.go`,
`internal/storage/policies.go`, `internal/storage/ca.go`,
`internal/server/grpcserver/interceptors.go`, `.../secrets.go`, `.../watch.go`,
`internal/cli/serve.go`, and `internal/cli/importcmd.go`.

Repository-wide frontier searches for **command/template execution (RCE)**,
**SSRF**, and **archive/upload extraction** each surfaced no runtime sink —
though those rows are also still `open_frontier` and were never formally closed,
so they are not sealed negative results.
