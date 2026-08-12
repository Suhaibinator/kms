# TypeScript SDK stage-review ledger

This is the durable triage record for the adversarial stage gates required by
[`sdk-typescript-plan.md`](sdk-typescript-plan.md). It complements the
behavioral [`sdk-typescript-parity.md`](sdk-typescript-parity.md) matrix: the
matrix says what is supported, while this ledger records what review found,
how each finding was resolved, and where the regression evidence lives.

The implementation landed as a sequence of small commits rather than nine
tagged release branches. A stage below is therefore accepted from its
implementation commits, later concern-focused review passes, corrective
commits, and executable evidence. Commit hashes are repository-local and can
be inspected with `git show <hash>`. Compilation alone is not treated as
evidence, and this ledger does not claim qualification beyond the support
boundaries listed at the end.

## Gate method

Each stage was challenged from the review perspectives that apply to it:

- Go/protocol parity and observable correctness;
- races, cancellation, ordering, reconnects, and resource lifetime;
- bounded work, backpressure, connection sharing, and memory ownership;
- secret handling, authentication, transport security, and browser isolation;
- public API shape, declaration compatibility, and documentation; and
- separation between the framework-neutral core and optional adapters.

A finding is closed only when the ledger identifies a resolution commit and
behavioral, build, or declaration evidence. Documentation-only corrections are
identified as such. The final hermetic package gate is `npm run check` from
`sdk/typescript`; compatibility and browser gates that intentionally sit
outside it are listed under Stage 8.

In evidence columns, abbreviated `tests/` and `scripts/` paths are relative to
`sdk/typescript/`. Package-root file names in Stage 8 refer to that directory as
well.

## Stage 0: contract inventory and public API design

**Disposition:** accepted. Review emphasized Go surface inventory, accidental
exports, lifecycle ownership, compatibility claims, and core/adapter boundaries.

| Adversarial finding | Resolution and triage | Evidence |
|---|---|---|
| Generated protobuf and implementation-only transport types leaked into the proposed public surface. | Kept generated protocol types internal (`32a2db9`), hid transport implementation details (`0851e12`) and client helpers (`18c7d81`), and narrowed configstore exports (`769c5b6`). | `sdk/typescript/tests/package-boundaries.test.ts`, `tests/package/public-api.ts`, `tests/types/public-api.ts` |
| Framework and browser support language was broader than the executable matrix. | Claims were narrowed to qualified adapters (`3803c09`), compilers (`5193dd1`), browsers (`aecfe30`), and pre-1.0 stability (`1dbfa46`, `fa1d622`). This was a claims correction, not a code fix. | `docs/sdk-typescript-api.md`, `sdk/typescript/README.md`, compatibility rows in `docs/sdk-typescript-parity.md` |
| The stable export inventory was incomplete. | Added the exhaustive API inventory (`b200046`) and built-package API lock (`0b82435`). | `docs/sdk-typescript-api.md`, `sdk/typescript/tests/package/public-api.ts` |
| Core and framework responsibilities needed enforceable package boundaries. | Split root, `next/server`, `next/client`, `configstore`, and `configgen` entry points (`aea7c3b`, `722a449`, `34bdf9e`). | `sdk/typescript/package.json`, `tests/package-boundaries.test.ts`, `npm run test:next` |

No Stage 0 exception permits an undocumented export or untested support claim.
New exports or compatibility claims must update the API inventory, consumer
tests, parity matrix, and this ledger.

## Stage 1: transport and foundational client

**Disposition:** accepted. Review emphasized wire behavior, exact integers,
metadata, TLS/mTLS, cancellation, discovery retries, and error redaction.

| Adversarial finding | Resolution and triage | Evidence |
|---|---|---|
| A unary call could complete after cancellation won the race. | Fenced unary completion (`c0c97d6`) and propagated cancellation through list pagination (`94d99a9`). | `sdk/typescript/tests/transport.test.ts`, `tests/client.test.ts` |
| Public and wire integer inputs were not uniformly checked before encoding. | Added runtime `uint64`/`int64` checks (`ffbf087`) on top of the bigint design (`12650c9`, `a8d31f9`). | `tests/client.test.ts`, `tests/refs.test.ts`, `tests/secret.test.ts`, `tests/grpc-integration.test.ts` |
| Namespace discovery needed proof that failure is retryable and an unbound identity is cached. | Added both discovery cases in `4a56f78`. | `sdk/typescript/tests/client.test.ts` |
| The first caller's cancellation owned a coalesced namespace lookup, per-call deadlines did not bound discovery waits, and parameter access tokens were discarded/cacheable. | Made discovery client-owned with caller-local wait cancellation/deadlines, forwarded parameter tokens, and bypassed cache for protected reads (`46fbbad`). | `tests/client.test.ts`, `tests/grpc-integration.test.ts` |
| Stubs and negative tests did not prove all metadata-bearing reads and mutations over gRPC. | Added generated loopback interoperability (`bf159cd`), positive metadata/mutations (`2655897`), secret writes and one-time access, and deterministic deadlines (`81efbf9`). | `sdk/typescript/tests/grpc-integration.test.ts` |
| TLS helpers lacked positive evidence for both mutual authentication and server-authenticated TLS. | Added loopback mTLS qualification (`7b5eeab`), server-authenticated `tlsFromFiles` plus bearer qualification (`64ffa2d`), and documented the scope (`c4ab082`). | `sdk/typescript/tests/tls-integration.test.ts` |

The transport fixture is deliberately protocol-faithful and hermetic.
Qualification against a separately deployed production server is an explicit
Stage 8 limitation, not inferred from loopback tests.

## Stage 2: values, caching, and resolution

**Disposition:** accepted. Review emphasized fallback order, recursive
resolution, secret ownership, callback lifetime, bounds, and disposal races.

| Adversarial finding | Resolution and triage | Evidence |
|---|---|---|
| Resolution needed observable env/store/default ordering, strict fallback, nested discovery, and cycle handling. | Implemented in `2d19708` and aligned with bigint/callback semantics in `a8d31f9`. | `sdk/typescript/tests/values.test.ts` |
| A queued parameter update could outlive `ParameterValue.dispose()`. | Added disposal fencing (`1d427c3`) and strengthened queued registration fencing (`903df95`). | `sdk/typescript/tests/values.test.ts` |
| A bounded queue alone allowed unbounded async callback promises, and queued work could run after unsubscribe. | The dispatcher now serializes async callbacks and checks registration lifetime at delivery (`903df95`); the stalled-callback drop boundary is documented in `5663c25`. | `tests/stress.test.ts`, `tests/values.test.ts`, `tests/watch.test.ts` |
| Secret values or cache entries could share mutable bytes. | Secret construction/access and cache reads use defensive copies and redacted renderers (`12650c9`, `6805334`); the public helper gained direct coverage in `8c5285d`. | `tests/secret.test.ts`, `tests/cache.test.ts` |
| Snapshot and live-delete invalidation did not cover every ordinary cache entry. | Corrected parameter cache and first-tombstone invalidation in `97bd23a`. | `tests/cache.test.ts`, `tests/watch.test.ts` |

Callback failures are isolated from stream ownership. Delivery is bounded and
serialized; application callbacks should still remain short so they do not
fill the bounded queue.

## Stage 3: watches and reconciliation

**Disposition:** accepted after a second lifecycle and stress pass. Review
emphasized shared-stream ownership, scope changes, full snapshots, revision
fences, backoff, acknowledgements, and leaks.

| Adversarial finding | Resolution and triage | Evidence |
|---|---|---|
| Initial registration caused an unnecessary stream restart. | Fixed in `882ce0b`. | `sdk/typescript/tests/watch.test.ts` |
| Reconciliation could mishandle missing values and tombstones. | Corrected tombstones (`6bbe6e0`) and added dropped-stream, paging, and reconciliation faults (`77964ea`, `e61dd33`). | `sdk/typescript/tests/watch.test.ts` |
| Subscriptions and AbortSignal listeners could remain attached after disposal or failure. | Released resources (`dd0786d`), corrected failed-registration ordering (`71cff00`), and added scaled lifecycle stress (`9697d3e`). | `tests/watch.test.ts`, `tests/stress.test.ts` |
| Growing a namespace union could expose a scope before its full snapshot arrived. | Added expanded-scope snapshots (`3f0a2dd`) and required snapshot delivery before resume (`3379bda`); scope growth also interrupts backoff (`903df95`). | `sdk/typescript/tests/watch.test.ts` |
| Full snapshots omitted resources without invalidating the ordinary parameter cache, and a first unknown delete could be ignored. | Corrected both behaviors (`97bd23a`) and added a protocol-faithful full-snapshot fixture (`d70bbc0`). | `tests/cache.test.ts`, `tests/watch.test.ts`, `tests/grpc-integration.test.ts` |
| Reconnect storms, duplicates, out-of-order revisions, failures, and callback pressure needed deterministic evidence. | Added fault and stress coverage (`77964ea`, `e61dd33`, `9697d3e`, `903df95`). | `tests/watch.test.ts`, `tests/stress.test.ts` |
| Namespace scope, background reconciliation, and deleted-path history were append-only after local unsubscribe or unique-key churn; an already in-flight reconcile page could also repopulate a released scope. | Added namespace reference ownership, idle stream/reconcile suspension, tombstone compaction, and a global stale-reconcile fence (`1b90753`), then fenced each awaited page by captured scope generation and current ownership (`0a4bd60`). | `tests/stress.test.ts`, `tests/watch.test.ts` |
| Operators lacked a value-free watch/reconciliation health surface. | Added immutable watch status (`4280d24`); docs require formatting its bigint revision before JSON. | `tests/watch.test.ts`, `docs/sdk-typescript-api.md` |

The shared stream, callback queue, reconnect work, and cleanup are bounded.
Revision safety relies on server full-snapshot and resume semantics, exercised
by the generated bidi integration fixture.

## Stage 4: atomic releases

**Disposition:** accepted after fault-path and constructor review. Review
emphasized exact resolution, digests, supersession, prepare/commit/abort,
terminal acknowledgements, LKG, and sensitive failure text.

| Adversarial finding | Resolution and triage | Evidence |
|---|---|---|
| A rejected startup candidate could finish before its terminal acknowledgement was flushed. | Added bounded terminal-ACK flush and replay behavior (`11b53be`). | `sdk/typescript/tests/releases/loader.test.ts` |
| Bad digests were covered at helper level but not through loader rejection, ACK, and LKG. | Added the complete loader path in `d381c58`. | `tests/releases/digest.test.ts`, `tests/releases/loader.test.ts` |
| Commit throws, interruption between prepare and commit, and abort-contract failure lacked fatal-path evidence. | Added redacted fail-closed, abort-once, no-unsafe-abort-after-commit, and fatal-contract tests (`d381c58`). | `sdk/typescript/tests/releases/loader.test.ts` |
| Caller cancellation could let `run()` return and clear exclusivity while a preparation that ignored abort was still pending. | Track and drain every owned candidate task, prevent successor starts after abort, and retain the running guard through settlement (`912fdf6`). | `sdk/typescript/tests/releases/loader.test.ts` |
| Public release identity constructors accepted invalid runtime values despite TypeScript annotations. | Validated entry/version/revision/schema bigint fields from zero through `UINT64_MAX` (`839c069`, `70ea91a`); added equivalent configstore checks (`421ed78`). | `tests/releases/types.test.ts`, `tests/configstore-runtime.test.ts` |
| Exported typed-runner and secret helpers lacked direct runtime tests. | Added `runTypedRelease` sequencing (`d381c58`) and `newSecret` defensive-copy coverage (`8c5285d`). | `tests/releases/loader.test.ts`, `tests/secret.test.ts` |

`PreparedRelease.commit()` and `abort()` are contractually infallible. If one
throws, the loader surfaces a redacted fatal contract error instead of guessing
whether partial application state can be rolled back. Concurrent `run` calls
are rejected; sequential reuse is supported and documented in `5663c25`.

## Stage 5: framework-neutral publishing primitives

**Disposition:** accepted. Review emphasized allowlists, one-snapshot
atomicity, stale recovery, exact revisions, immutable events, and observer
isolation.

| Adversarial finding | Resolution and triage | Evidence |
|---|---|---|
| Projection output needed to reject secret-bearing, cyclic, accessor-backed, or unsafe public JSON. | Hardened public-policy boundaries (`59992ac`) and package trust boundaries (`ee2d0de`). | `sdk/typescript/tests/publishing.test.ts`, `tests/package-boundaries.test.ts` |
| A stale request could be accepted when its revision differed from the authoritative snapshot. | Added structured current-policy rejection (`223721f`). | `sdk/typescript/tests/publishing.test.ts` |
| Publication and validation lacked a value-free observation contract. | Added safe publisher events (`cf926a2`), branded revisions (`093b134`), and observed Next recovery (`b8361bf`). | `tests/publishing.test.ts`, `tests/next-server.test.ts`, `tests/next-client.test.tsx` |
| An observer could see an outcome before later result construction threw, or observer failure could perturb the result. | Corrected outcome construction order and observer isolation (`b466765`). | `sdk/typescript/tests/publishing.test.ts` |

Publishing remains framework-neutral. It emits only an explicitly selected
public projection from one immutable snapshot; browser validation is never
authoritative.

## Stage 6: Next.js adapter

**Disposition:** accepted for the documented peer matrix. Review emphasized
server-only ownership, lifecycle races, App Router integration, refresh
cancellation, stale recovery, and bundle exclusion.

| Adversarial finding | Resolution and triage | Evidence |
|---|---|---|
| A failed shared initialization attempt permanently poisoned later starts. | Made startup retryable (`17a0267`) and coalesced concurrent shutdown (`7842a8a`). | `sdk/typescript/tests/next-server.test.ts` |
| Signal hooks attempted to preserve termination by re-signalling, unreliable when other listeners remain; the first cleanup-only example then had no termination owner. | Replaced the initial attempt (`488f4f8`) with validation/rollback-safe cleanup (`b466765`), added an explicit post-cleanup application callback and child-process exit gate (`715ce1a`), and corrected the public lifecycle contract (`5663c25`). | `tests/next-server.test.ts`, `scripts/test-next-boundaries.mjs`, `examples/next-serverful/instrumentation.ts`, `docs/sdk-typescript-api.md` |
| Browser policy could remain stale across navigation/focus or after `policy_changed`. | Added navigation refresh (`f98ff02`), structured stale recovery (`223721f`), and recovery observations (`b8361bf`). | `sdk/typescript/tests/next-client.test.tsx` |
| Unit mocks did not prove server-only imports, instrumentation, or a real App Router build. | Added real Next boundaries (`5593138`), isolated fixtures (`2c37976`), and instrumentation lifecycle (`bd89250`). | `npm run test:next`, `tests/fixtures/next-boundary` |
| Peer claims and browser behavior were not tested from isolated installed packages. | Added exact Next/React and Chromium gates (`f5672db`) and hardened them to packed, non-symlinked installs plus the minimum compiler (`2634141`). | `.github/workflows/ci.yml`, `npm run test:browser`, `scripts/test-next-peer.mjs`, `scripts/test-typescript-min.mjs` |

The root entry point remains Node-only. The sole browser entry point is
`@suhaibinator/kms/next/client`; it receives public HTTP policy and cannot
contain the KMS transport or credentials.

## Stage 7: optional managed configstore parity

**Disposition:** included and accepted. Review emphasized strict transactional
decoding, immutable views, whole-candidate policy, artifact determinism, type
soundness, and parity-sensitive numeric encodings.

| Adversarial finding | Resolution and triage | Evidence |
|---|---|---|
| Initial codecs did not cover the full duplicate/unknown/missing/type/range gauntlet. | Added strict codecs (`6a7bf61`), hardened them (`e255266`), and made malformed candidates abort transactionally (`152d0f3`). | `sdk/typescript/tests/configstore-codecs.test.ts`, `tests/configstore-manager.test.ts` |
| Composite configuration could expose mutable source arrays. | Added safe composite cloning (`5918e6a`). | `sdk/typescript/tests/configstore-runtime.test.ts` |
| Managed startup could bypass the authenticated client or apply partial drift/restart policy. | Added the managed bridge (`0fede0a`) and routed startup through `KmsClient` (`bf7b8c9`). | `sdk/typescript/tests/configstore-manager.test.ts` |
| Native generation needed deterministic bindings, schema/contract output, and stale checking. | Added generator and entry point (`44fb43a`, `34bdf9e`) and canonical record encoding (`8f531f6`). | `tests/configgen.test.ts`, `tests/configgen-generated.test.ts`, `tests/fixtures/configgen` |
| Output aliasing and generated types could create unsafe or uncompilable artifacts. | Guarded output identities (`40b0f23`), corrected emitted types (`97b46ef`), and kept resolver hooks private (`86e95bc`). | `tests/configgen.test.ts`, `tests/types/configgen-soundness.ts`, `tests/configgen-generated.test.ts` |
| Numeric edge cases diverged from canonical behavior. | Canonicalized signed zero (`4c0c2ab`), made bigint diagnostics safe (`1c208e1`), and completed identity validation (`421ed78`). | `tests/configstore-codecs.test.ts`, `tests/configstore-runtime.test.ts` |

JavaScript immutable views make defensive copies for some composite accessors;
this intentionally differs from allocation-free Go generated views while
preserving the ownership guarantee documented in the API guide.

## Stage 8: release readiness

**Disposition:** accepted as a pre-1.0 release candidate, subject to the
documented qualification boundaries below. Review emphasized package contents,
declarations, generated drift, compatibility matrices, examples, notices, and
hermetic CI.

| Adversarial finding | Resolution and triage | Evidence |
|---|---|---|
| Examples and package entry points could drift from emitted declarations. | Published API/examples (`6672dfe`), added declaration consumers (`88a17f3`), packaging gates (`d173cc8`), and the built API lock (`0b82435`). | `npm run test:types`, `npm run build`, `npm run test:package` |
| Published links and notices were incomplete or repository-relative. | Corrected links (`77f7fd2`, `2af8a32`), included notices (`23ce596`), and clarified security reporting (`44b13d9`). | package `README.md`, `LICENSE`, `SECURITY.md`, `CHANGELOG.md`, `package.json` |
| Generated checking reused committed output and could miss non-hermetic drift. | Generation now runs in isolation and byte-compares every generated file (`04c64e3`). | `npm run check:generated` |
| Runtime, compiler, peer, and browser claims lacked a complete matrix. | Added Node qualification (`73b3efe`), TypeScript/Next/React/Chromium gates (`f5672db`), and package-isolated compatibility (`2634141`). | `.github/workflows/ci.yml`, `npm run test:typescript-min`, `scripts/test-next-peer.mjs`, `npm run test:browser` |
| Protocol qualification needed positive TLS, auth, mutations, watches, releases, exact integers, secret writes, and deadlines. | Added coverage in `7b5eeab`, `bf159cd`, `2655897`, `d70bbc0`, and `81efbf9`. | `tests/tls-integration.test.ts`, `tests/grpc-integration.test.ts` |
| Concurrency confidence needed more than ordinary unit tests. | Added stress scenarios (`9697d3e`) and strengthened callback/lifecycle bounds (`903df95`). | `sdk/typescript/tests/stress.test.ts` |

The release gates are:

```bash
cd sdk/typescript
npm ci
npm run check
npm run test:typescript-min
npm run test:browser
```

CI additionally builds every documented Next.js/React peer tuple using the
packed SDK and runs `npm run check` on Node 22, 24, and 26. The repository-wide
Go suite remains a separate compatibility guard for the server and Go SDK.

## Intentional, user-visible limitations

These are accepted support boundaries, not unresolved hidden findings:

- **Node-only transport.** The package root uses Node gRPC and TLS. Browsers may
  import only `@suhaibinator/kms/next/client`; they do not call KMS directly.
- **Hermetic server qualification.** Integration tests use the generated wire
  contract over loopback TLS/mTLS but do not claim qualification against a
  separately deployed production KMS server.
- **Explicit compatibility matrix.** Supported Node majors are 22, 24, and 26;
  declarations are checked with TypeScript 5.2.2 and the pinned compiler;
  Next.js 14–16 and React 18–19 are supported only where tested peer ranges
  intersect.
- **Browser matrix.** Chromium is qualified. Other modern browsers with native
  `bigint`, `fetch`, `AbortController`, and focus/navigation events are expected
  to work but are not advertised as matrix-qualified.
- **Application-owned signal termination.** Next process hooks request
  cooperative cleanup for SIGINT/SIGTERM. Application code or a supervisor
  owns termination through the post-cleanup callback; callbacks that ignore
  cancellation can delay that handoff, so deployments may layer a process
  supervisor timeout.
- **Pre-1.0 API stability.** The package is a `0.x` release. The changelog and
  API guide define its compatibility policy; qualification does not imply a
  `1.0` stability promise.
- **Generator replacement boundary.** Configgen stages, fsyncs, and replaces
  three artifacts, but the renames are not one filesystem transaction.
  Concurrent writers are unsupported.
- **JavaScript ownership trade-off.** Managed composite getters return
  defensive immutable copies rather than allocation-free Go views.

Changing one of these boundaries requires public documentation, an updated
parity row, and executable qualification before the limitation can be removed.
