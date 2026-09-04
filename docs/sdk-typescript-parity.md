# Go-to-TypeScript SDK parity matrix

This is the living behavioral parity ledger required by the TypeScript SDK
delivery plan. `complete` means the behavior has an automated TypeScript test;
`partial` is never advertised as full parity.

| Go capability | TypeScript API/module | Required behavior | Test location | Status |
|---|---|---|---|---|
| Secure construction and close | `KmsClient`, `tlsFromFiles`, `mtlsFromFiles` | Fail-closed transport, TLS/mTLS, idempotent bounded cleanup | `sdk/typescript/tests/client.test.ts`, `transport.test.ts`, `tls-integration.test.ts` | complete |
| Namespace discovery and display paths | `KmsClient.whoAmI`, ref helpers | Lazy retryable discovery, cached unbound result, interior key slashes | `sdk/typescript/tests/refs.test.ts`, `client.test.ts` | complete |
| Reads/writes and credentials | `get/put/list/delete` methods | Bearer identity plus independent access-token/binding-key request fields, no write-side token, deadlines, exact `bigint` values, and positive protocol coverage | `sdk/typescript/tests/client.test.ts`, `sdk/typescript/tests/grpc-integration.test.ts` | complete |
| Typed errors | `KmsError` | Bounded status mapping without plaintext | `sdk/typescript/tests/errors.test.ts` | complete |
| Secret redaction and copying | `Secret` | Explicit access only; string/JSON/inspect redaction; no shared buffers | `sdk/typescript/tests/secret.test.ts` | complete |
| Parameter TTL cache / no secret cache | internal `ReadCache`, `KmsClient` | Bounded parameter cache and invalidation; secret plaintext never enters the cache regardless of credentials | `sdk/typescript/tests/cache.test.ts`, `client.test.ts` | complete |
| Declarative values and nested resolution | `SecretValue`, `ParameterValue`, `resolve` | env/store/default order, strict fallback, callbacks, hot/static values, cycles | `sdk/typescript/tests/values.test.ts` | complete |
| Shared parameter watch | `watch`, `watchNamespace` | One bidi stream, union/restart, heartbeats, resume, revision fencing | `sdk/typescript/tests/watch.test.ts` | complete |
| Reconciliation | internal subscription manager | Five-minute full sync, page cap, no false deletes, tombstone fences | `sdk/typescript/tests/watch.test.ts` | complete |
| Watch/reconcile observability | `KmsClient.watchStatus` | Value-free immutable connection, reconnect, scope, revision, reconciliation-health, and lifecycle timestamps; callers format the bigint revision before JSON | `sdk/typescript/tests/watch.test.ts` | complete |
| Concurrency, leak, and backpressure stress | shared watches, callback dispatcher, policy publisher | Reconnect storms, resumable fences, bounded callback queue, distinct-scope release, tombstone compaction, scaled AbortSignal cleanup, coherent atomic reads | `sdk/typescript/tests/stress.test.ts` | complete |
| Immutable release snapshot | `ReleaseManifest`, `ReleaseSnapshot` | Redacted serialization and defensive accessors | `sdk/typescript/tests/releases/types.test.ts` | complete |
| Release exact resolution and digest | `ReleaseLoader` | Home-namespace pins; deterministic digest without protection flags; exact live secret metadata identity/version/state/expiry; independent token/binding resolution | `sdk/typescript/tests/releases/digest.test.ts`, `loader.test.ts`, `client-release.test.ts` | complete |
| Binding-key lifecycle | `bindSecret`, `unbindSecret`, `previewSecretBindingCohort`, `rotateSecretBindingKey`, `purgeSecretBindingCohort` | Frozen results, paired/sorted CAS guards, sanitized credential errors, and committed-cleanup-pending distinction | `sdk/typescript/tests/client.test.ts`, `grpc-integration.test.ts` | complete |
| Atomic release lifecycle | `ReleaseLoader.run` | Supersession, prepare/commit/abort, active fence, LKG, reliable acks | `sdk/typescript/tests/releases/loader.test.ts` | complete |
| Public projection | `definePublicProjection`, `createPolicyPublisher` | Allowlist-only JSON, one captured snapshot, decimal revisions, stale result | `sdk/typescript/tests/publishing.test.ts` | complete |
| Publication and recovery observability | publisher and Next observer hooks | Frozen value-free publication, stale rejection, validation, HTTP, refresh, and recovery events with correlation timestamps | `sdk/typescript/tests/publishing.test.ts`, `next-server.test.ts`, `next-client.test.tsx` | complete |
| Next.js adapter | `next/server`, `next/client` | Server-only ownership, safe Route Handler, refresh and stale recovery | `sdk/typescript/tests/next-server.test.ts`, `next-client.test.tsx`, `npm run test:next` | complete |
| Next.js/React peer majors | optional adapter peers | Next.js 14–16 and React 18–19 compatibility where the framework peer ranges intersect | Exact isolated Next.js 14/React 18, Next.js 15/React 18 and 19, and Next.js 16/React 18 and 19 builds in `.github/workflows/ci.yml` | complete |
| Browser hook compatibility | `next/client` | Native `bigint`, fetch/cancellation, focus/navigation refresh, stale recovery | `sdk/typescript/tests/next-client.test.tsx`, `npm run test:browser` (Chromium) | complete |
| Managed strict decoding | `configstore` | Duplicate/unknown/missing/type/range checks; transactional decoding | `sdk/typescript/tests/configstore-codecs.test.ts` | complete |
| Managed drift/restart policy | `startManagedConfig`, `ManagedConfigManager` | Non-fatal reported startup drift, runtime restore, whole-candidate restart rejection, and declaration `bindKey` extraction/removal | `sdk/typescript/tests/configstore-manager.test.ts`, `configgen-generated.test.ts` | complete |
| Applied notifications | `Callbacks.onApplied`, `AppliedReport`, `FieldChange`, `consoleCallbacks` | Per-generation report with redacted `changed()`/`groups()`, isolated synchronous callbacks, divergence flag and field count on applied acknowledgements only, `SlogCallbacks`-equivalent log records | `sdk/typescript/tests/configstore-manager.test.ts`, `configstore-runtime.test.ts`, `configstore-logging.test.ts`, `configgen-generated.test.ts`, `releases/loader.test.ts` | complete |
| Canonical parameter hashing | `canonicalParameterValue`, `parameterHash` | Byte-identical canonical JSON and SHA-256 with the Go SDK and server, driven by the shared `sdk/go/configstore/testdata/canonical_vectors.json` vectors | `sdk/typescript/tests/configstore-canonical.test.ts` | complete |
| Value-free defaults verification | `KmsClient.verifyReleaseDefaults`, `configstore.verifyDefaults`, generated `verifyReleaseDefaults`, `RateLimitedError` | Hash-only request, bounded verdict validation, alias echo and count consistency, rate-limit mapping, sorted value-free CI report | `sdk/typescript/tests/client-verify.test.ts`, `configstore-verify.test.ts`, `configgen-generated.test.ts` | complete |
| TypeScript-native generation | `configgen`, `kms-config-gen-ts` | Stable binding/schema/contract output, emitted store behavior (including `changed`/`groups` and the generated verify helper), and check mode | `sdk/typescript/tests/configgen.test.ts`, `configgen-generated.test.ts` | complete |
| Generated protobuf contract | `src/generated/kms.ts` | Complete service/messages, `uint64` as `bigint`, stale generation check | `npm run check:generated` | complete |
| Published entry points and examples | package root, `next/server`, `next/client`, `configstore`, `configgen` | Declaration build, built consumer, serverful Next build, browser-bundle inspection, and invalid client-import rejection | `sdk/typescript/tests/types`, `sdk/typescript/tests/package`, `npm run test:next`, `npm run test:package` | complete |
| Supported Node majors | package release gate | Typecheck, lint, tests, consumer types, framework boundary builds, and package checks on Node 22, 24, and 26 | `.github/workflows/ci.yml` | complete |
| TypeScript compiler compatibility | published declarations | Built-package consumption on TypeScript 5.2.2 and the pinned current compiler | `npm run test:typescript-min`, `npm run test:types`, `npm run test:package` | complete |
| Protocol-faithful server interoperability | core transport, watches, releases | Actual gRPC loopback with TLS/mTLS, auth metadata, unary methods, exact `bigint` values, bidi resume, releases, and applied acknowledgement | `sdk/typescript/tests/grpc-integration.test.ts`, `tls-integration.test.ts` | complete |

Intentional differences are documented in
[`sdk-typescript-api.md`](sdk-typescript-api.md). The SDK must not claim managed
configuration parity until all Stage 7 rows are complete.

The interoperability row uses hermetic protocol-faithful loopback gRPC
services and runtime-generated certificates. It exercises the real transport
and generated wire contract, but does not claim qualification against a
separately deployed production server.

Every release-visible row is qualified by executable behavior or build gates.
Adding a supported runtime, compiler, framework, or browser requires extending
this matrix before the public support claim changes.
