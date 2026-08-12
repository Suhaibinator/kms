# Go-to-TypeScript SDK parity matrix

This is the living behavioral parity ledger required by the TypeScript SDK
delivery plan. `complete` means the behavior has an automated TypeScript test;
`partial` is never advertised as full parity.

| Go capability | TypeScript API/module | Required behavior | Test location | Status |
|---|---|---|---|---|
| Secure construction and close | `KmsClient`, `tlsFromFiles`, `mtlsFromFiles` | Fail-closed transport, TLS/mTLS, idempotent bounded cleanup | `sdk/typescript/tests/client.test.ts`, `transport.test.ts`, `tls-integration.test.ts` | complete |
| Namespace discovery and display paths | `KmsClient.whoAmI`, ref helpers | Lazy retryable discovery, cached unbound result, interior key slashes | `sdk/typescript/tests/refs.test.ts`, `client.test.ts` | complete |
| Reads/writes and auth metadata | `get/put/list/delete` methods | Bearer and per-secret metadata, deadlines, exact `bigint` values | `sdk/typescript/tests/client.test.ts` | complete |
| Typed errors | `KmsError` | Bounded status mapping without plaintext | `sdk/typescript/tests/errors.test.ts` | complete |
| Secret redaction and copying | `Secret` | Explicit access only; string/JSON/inspect redaction; no shared buffers | `sdk/typescript/tests/secret.test.ts` | complete |
| Bounded TTL cache | internal `ReadCache` | `(path,version,label)` keys, 4096 caps, invalidation, secret clones, token bypass | `sdk/typescript/tests/cache.test.ts` | complete |
| Declarative values and nested resolution | `SecretValue`, `ParameterValue`, `resolve` | env/store/default order, strict fallback, callbacks, hot/static values, cycles | `sdk/typescript/tests/values.test.ts` | complete |
| Shared parameter watch | `watch`, `watchNamespace` | One bidi stream, union/restart, heartbeats, resume, revision fencing | `sdk/typescript/tests/watch.test.ts` | complete |
| Reconciliation | internal subscription manager | Five-minute full sync, page cap, no false deletes, tombstone fences | `sdk/typescript/tests/watch.test.ts` | complete |
| Immutable release snapshot | `ReleaseManifest`, `ReleaseSnapshot` | Redacted serialization and defensive accessors | `sdk/typescript/tests/releases/types.test.ts` | complete |
| Release exact resolution and digest | `ReleaseLoader` | Deterministic protobuf digest; ref/version/content/digest verification | `sdk/typescript/tests/releases/digest.test.ts`, `loader.test.ts` | complete |
| Atomic release lifecycle | `ReleaseLoader.run` | Supersession, prepare/commit/abort, active fence, LKG, reliable acks | `sdk/typescript/tests/releases/loader.test.ts` | complete |
| Public projection | `definePublicProjection`, `createPolicyPublisher` | Allowlist-only JSON, one captured snapshot, decimal revisions, stale result | `sdk/typescript/tests/publishing.test.ts` | complete |
| Next.js adapter | `next/server`, `next/client` | Server-only ownership, safe Route Handler, refresh and stale recovery | `sdk/typescript/tests/next-server.test.ts`, `next-client.test.tsx`, `npm run test:next` | complete |
| Next.js/React peer majors | optional adapter peers | Intended Next.js 14–16 and React 18–19 compatibility | Next.js 16 / React 19 in `npm run test` | partial — older accepted peer majors need a compatibility matrix |
| Browser hook compatibility | `next/client` | Native `bigint`, fetch/cancellation, focus/navigation refresh, stale recovery | `sdk/typescript/tests/next-client.test.tsx` (simulated DOM) | partial — real-browser matrix pending |
| Managed strict decoding | `configstore` | Duplicate/unknown/missing/type/range checks; transactional decoding | `sdk/typescript/tests/configstore-codecs.test.ts` | complete |
| Managed drift/restart policy | `startManagedConfig`, `ManagedConfigManager` | Startup drift, runtime restore, whole-candidate restart rejection | `sdk/typescript/tests/configstore-manager.test.ts` | complete |
| TypeScript-native generation | `configgen`, `kms-config-gen-ts` | Stable binding/schema/contract output, emitted store behavior, and check mode | `sdk/typescript/tests/configgen.test.ts`, `configgen-generated.test.ts` | complete |
| Generated protobuf contract | `src/generated/kms.ts` | Complete service/messages, `uint64` as `bigint`, stale generation check | `npm run check:generated` | complete |
| Published entry points and examples | package root, `next/server`, `next/client`, `configstore`, `configgen` | Declaration build, built consumer, serverful Next build, browser-bundle inspection, and invalid client-import rejection | `sdk/typescript/tests/types`, `sdk/typescript/tests/package`, `npm run test:next`, `npm run test:package` | complete |
| Supported Node majors | package release gate | Typecheck, lint, tests, consumer types, framework boundary builds, and package checks on Node 22, 24, and 26 | `.github/workflows/ci.yml` | complete |
| TypeScript compiler compatibility | published declarations | TypeScript 5.2+ syntax; pinned compiler release gate | `npm run test:types`, `npm run test:package` | partial — minimum compiler matrix pending |
| Protocol-faithful server interoperability | core transport, watches, releases | Actual gRPC loopback with TLS/mTLS, auth metadata, unary methods, exact `bigint` values, bidi resume, releases, and applied acknowledgement | `sdk/typescript/tests/grpc-integration.test.ts`, `tls-integration.test.ts` | complete |

Intentional differences are documented in
[`sdk-typescript-api.md`](sdk-typescript-api.md). The SDK must not claim managed
configuration parity until all Stage 7 rows are complete.

The interoperability row uses hermetic protocol-faithful loopback gRPC
services and runtime-generated certificates. It exercises the real transport
and generated wire contract, but does not claim qualification against a
separately deployed production server.

The remaining `partial` and `planned` rows are release-visible qualification
gaps. Unit coverage and a declaration build do not substitute for the required
stress, browser, compiler, and peer-major compatibility scenarios.
Until those rows complete, the package may be published as a prerelease
candidate but must not advertise literal full Go parity or a generally
available stable release.
